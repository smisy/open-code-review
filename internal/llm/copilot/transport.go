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
	"net/url"
	"strings"
)

// Auth is the per-endpoint state the LLM clients need to talk to Copilot.
type Auth struct {
	Source *TokenSource
	// RewriteHost sends each request to the base URL named by the Copilot
	// token. It is off when the user pinned a URL explicitly.
	RewriteHost bool
}

// Middleware authenticates each HTTP attempt with a current Copilot token. Its
// signature matches both the OpenAI and Anthropic SDK middleware types. It must
// be registered first so it wraps the raw capture and retry observers, which
// then record the request as actually sent.
func (a *Auth) Middleware(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	if err := a.prepare(req); err != nil {
		return nil, err
	}
	resp, err := next(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized || req.GetBody == nil {
		return resp, err
	}
	// A token can be revoked before its expiry (plan change, seat removal);
	// one fresh exchange distinguishes that from a GitHub token that is dead.
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	a.Source.Invalidate()
	retry := req.Clone(req.Context())
	if retry.Body, err = req.GetBody(); err != nil {
		return nil, err
	}
	if err := a.prepare(retry); err != nil {
		return nil, err
	}
	return next(retry)
}

func (a *Auth) prepare(req *http.Request) error {
	token, baseURL, err := a.Source.Token(req.Context())
	if err != nil {
		return err
	}
	if a.RewriteHost {
		base, err := url.Parse(baseURL)
		if err != nil {
			return fmt.Errorf("copilot base URL %q: %w", baseURL, err)
		}
		req.URL.Scheme = base.Scheme
		req.URL.Host = base.Host
		req.Host = base.Host
	}
	req.Header.Del("X-Api-Key")
	req.Header.Set("Authorization", "Bearer "+token)
	setIDEHeaders(req.Header)
	req.Header.Set("X-Initiator", initiator(req))
	return nil
}

// initiator reports "agent" for turns that only carry tool results back to
// the model, and "user" otherwise, the same split the Copilot editor clients
// make.
func initiator(req *http.Request) string {
	body := peekBody(req)
	if len(body) == 0 {
		return "user"
	}
	var payload struct {
		Messages []json.RawMessage `json:"messages"`
		Input    json.RawMessage   `json:"input"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return "user"
	}
	items := payload.Messages
	if len(items) == 0 && len(payload.Input) > 0 && payload.Input[0] == '[' {
		_ = json.Unmarshal(payload.Input, &items)
	}
	if len(items) == 0 {
		return "user"
	}
	var last struct {
		Role    string          `json:"role"`
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(items[len(items)-1], &last) != nil {
		return "user"
	}
	switch {
	case last.Role == "tool", last.Role == "assistant", last.Type == "function_call_output":
		return "agent"
	case last.Role == "user" && hasToolResult(last.Content):
		return "agent"
	default:
		return "user"
	}
}

func hasToolResult(content json.RawMessage) bool {
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return true
		}
	}
	return false
}

func peekBody(req *http.Request) []byte {
	if req.GetBody != nil {
		rc, err := req.GetBody()
		if err != nil {
			return nil
		}
		defer rc.Close()
		b, _ := io.ReadAll(rc)
		return b
	}
	if req.Body == nil {
		return nil
	}
	b, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		// Replay the failure instead of a clean truncated body, which would
		// turn a client-side read fault into a malformed request.
		req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(b), failingReader{err}))
		return nil
	}
	req.Body = io.NopCloser(bytes.NewReader(b))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
	return b
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

// Model is one entry of the account's live Copilot catalog.
type Model struct {
	ID        string
	Vendor    string
	Endpoints []string
	ToolCalls bool
}

// ListModels returns the chat models the account can pick.
func ListModels(ctx context.Context, src *TokenSource) ([]Model, error) {
	token, baseURL, err := src.Token(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	setIDEHeaders(req.Header)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list copilot models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list copilot models: HTTP %d: %s", resp.StatusCode, readSnippet(resp.Body))
	}
	var body struct {
		Data []struct {
			ID                 string   `json:"id"`
			Vendor             string   `json:"vendor"`
			ModelPickerEnabled *bool    `json:"model_picker_enabled"`
			SupportedEndpoints []string `json:"supported_endpoints"`
			Capabilities       struct {
				Type     string `json:"type"`
				Supports struct {
					ToolCalls bool `json:"tool_calls"`
				} `json:"supports"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("list copilot models: decode response: %w", err)
	}
	var out []Model
	for _, m := range body.Data {
		if m.Capabilities.Type != "chat" || (m.ModelPickerEnabled != nil && !*m.ModelPickerEnabled) {
			continue
		}
		out = append(out, Model{
			ID:        m.ID,
			Vendor:    m.Vendor,
			Endpoints: m.SupportedEndpoints,
			ToolCalls: m.Capabilities.Supports.ToolCalls,
		})
	}
	return out, nil
}

// Protocol names returned by ProtocolForModel; they match the llm package's
// canonical protocol names.
const (
	ProtocolAnthropic       = "anthropic"
	ProtocolChatCompletions = "openai"
	ProtocolResponses       = "openai-responses"
)

// ProtocolForModel picks the wire protocol for a Copilot model from its
// family, the same routing the Copilot clients use: Claude speaks Anthropic
// Messages, Gemini and GPT-4-era models only Chat Completions, and the newer
// OpenAI-style models only the Responses API. Resolution stays offline this
// way; a model outside these families can be pinned with the provider's
// protocol setting.
func ProtocolForModel(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "claude"):
		return ProtocolAnthropic
	case strings.HasPrefix(m, "gemini"), strings.HasPrefix(m, "gpt-4"), strings.HasPrefix(m, "gpt-3"):
		return ProtocolChatCompletions
	default:
		return ProtocolResponses
	}
}
