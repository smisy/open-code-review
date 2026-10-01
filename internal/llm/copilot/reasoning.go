// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EffortOff disables the reasoning parameters entirely.
const EffortOff = "off"

// DefaultReasoningEffort is used when the user configures none. Copilot serves
// Claude models with thinking off unless the request opts in, and those models
// then answer a code review from the diff alone, so the provider opts in.
const DefaultReasoningEffort = "high"

// effortRank orders every effort level any Copilot model advertises.
var effortRank = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// ValidReasoningEffort reports whether value is an effort this provider
// accepts in configuration or on the command line.
func ValidReasoningEffort(value string) bool {
	return value == EffortOff || slices.Contains(effortRank, value)
}

// ReasoningEffortLevels lists every accepted value, for help and completion.
func ReasoningEffortLevels() []string {
	return append([]string{EffortOff}, effortRank...)
}

// ReasoningEfforts lists the accepted values for error messages.
func ReasoningEfforts() string {
	return strings.Join(ReasoningEffortLevels(), ", ")
}

// clampEffort maps the requested effort onto the highest level the model
// supports that is not above the request. It never raises the effort, so a
// request below every supported level sends none.
func clampEffort(requested string, supported []string) string {
	want := slices.Index(effortRank, requested)
	best, bestRank := "", -1
	for _, level := range supported {
		rank := slices.Index(effortRank, level)
		if rank >= 0 && rank <= want && rank > bestRank {
			best, bestRank = level, rank
		}
	}
	return best
}

// thinkingThreshold is the lowest effort that turns adaptive thinking on.
var thinkingThreshold = slices.Index(effortRank, "low")

// maxCatalogAttempts bounds retries of a failing catalog fetch, so a broken
// /models endpoint costs a few requests, not one per completion.
const maxCatalogAttempts = 3

// catalogFetchTimeout bounds one catalog fetch, token exchange included, so
// requests waiting on the catalog lock are not held for long.
const catalogFetchTimeout = 30 * time.Second

// catalogCache holds the account's model capabilities, fetched once per run.
type catalogCache struct {
	mu       sync.Mutex
	models   map[string]Model
	attempts int
}

// capabilities looks the model up in the cached catalog, fetching it on first
// use. The fetch is detached from the calling request's cancellation, since
// one review task timing out must not leave the whole run without
// capabilities; a failed fetch is retried on later requests up to
// maxCatalogAttempts times.
func (a *Auth) capabilities(req *http.Request, model string) (Model, bool) {
	a.catalog.mu.Lock()
	defer a.catalog.mu.Unlock()
	if a.catalog.models == nil && a.catalog.attempts < maxCatalogAttempts {
		a.catalog.attempts++
		ctx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), catalogFetchTimeout)
		models, err := ListModels(ctx, a.Source)
		cancel()
		if err == nil {
			a.catalog.models = make(map[string]Model, len(models))
			for _, m := range models {
				a.catalog.models[m.ID] = m
			}
		}
	}
	m, ok := a.catalog.models[model]
	return m, ok
}

// applyReasoning adds the reasoning parameters the model supports to the
// request body, in the shape each Copilot endpoint expects, and keeps a
// non-streaming Messages request within the model's output limit. Fields the
// caller already set (for example through extra_body) are left alone.
func (a *Auth) applyReasoning(req *http.Request) error {
	raw := peekBody(req)
	if len(raw) == 0 {
		return nil
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		return nil
	}
	var model string
	if json.Unmarshal(body["model"], &model) != nil || model == "" {
		return nil
	}
	caps, ok := a.capabilities(req, model)
	if !ok {
		return nil
	}
	effort, adaptive := "", false
	if a.ReasoningEffort != "" && a.ReasoningEffort != EffortOff {
		effort = clampEffort(a.ReasoningEffort, caps.ReasoningEfforts)
		// Anthropic rejects thinking together with a forced tool choice, the
		// same guard the Anthropic client applies to extra_body.thinking.
		adaptive = caps.AdaptiveThinking &&
			slices.Index(effortRank, a.ReasoningEffort) >= thinkingThreshold &&
			!forcedToolChoice(body["tool_choice"])
	}
	changed := false
	set := func(key string, value any) {
		if _, exists := body[key]; exists {
			return
		}
		encoded, _ := json.Marshal(value)
		body[key] = encoded
		changed = true
	}
	switch path := req.URL.Path; {
	case strings.HasSuffix(path, "/v1/messages"):
		if adaptive {
			set("thinking", map[string]string{"type": "adaptive"})
		}
		if effort != "" {
			set("output_config", map[string]string{"effort": effort})
		}
		changed = clampMaxTokens(body, caps.MaxNonStreamingOutput) || changed
	case strings.HasSuffix(path, "/responses"):
		if effort != "" {
			set("reasoning", map[string]string{"effort": effort})
		}
	case strings.HasSuffix(path, "/chat/completions"):
		if effort != "" {
			set("reasoning_effort", effort)
		}
	}
	if !changed {
		return nil
	}
	rewritten, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("copilot reasoning: encode request: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(rewritten))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(rewritten)), nil }
	req.ContentLength = int64(len(rewritten))
	req.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
	return nil
}

// forcedToolChoice reports whether a Messages request forces a tool call.
func forcedToolChoice(raw json.RawMessage) bool {
	var choice struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &choice) != nil {
		return false
	}
	return choice.Type == "any" || choice.Type == "tool"
}

// clampMaxTokens keeps a non-streaming Messages request within the model's
// non-streaming output limit, which is lower than its streaming limit.
func clampMaxTokens(body map[string]json.RawMessage, limit int) bool {
	if limit <= 0 {
		return false
	}
	var stream bool
	if json.Unmarshal(body["stream"], &stream) == nil && stream {
		return false
	}
	var maxTokens int
	if json.Unmarshal(body["max_tokens"], &maxTokens) != nil || maxTokens <= limit {
		return false
	}
	body["max_tokens"] = json.RawMessage(strconv.Itoa(limit))
	return true
}
