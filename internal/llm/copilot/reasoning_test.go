// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package copilot

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClampEffort(t *testing.T) {
	claude := []string{"low", "medium", "high", "xhigh", "max"}
	gpt := []string{"none", "low", "medium", "high", "xhigh"}
	tests := []struct {
		requested string
		supported []string
		want      string
	}{
		{"max", claude, "max"},
		{"max", gpt, "xhigh"},
		{"high", gpt, "high"},
		{"minimal", claude, ""},
		{"none", gpt, "none"},
		{"high", nil, ""},
		{"high", []string{"turbo"}, ""},
	}
	for _, tt := range tests {
		if got := clampEffort(tt.requested, tt.supported); got != tt.want {
			t.Errorf("clampEffort(%q, %v) = %q, want %q", tt.requested, tt.supported, got, tt.want)
		}
	}
}

func TestValidReasoningEffort(t *testing.T) {
	for _, v := range []string{"off", "none", "minimal", "low", "medium", "high", "xhigh", "max"} {
		if !ValidReasoningEffort(v) {
			t.Errorf("%q should be valid", v)
		}
	}
	for _, v := range []string{"", "ultra", "MAX"} {
		if ValidReasoningEffort(v) {
			t.Errorf("%q should be invalid", v)
		}
	}
	if !strings.Contains(ReasoningEfforts(), "off, none") {
		t.Errorf("ReasoningEfforts() = %q", ReasoningEfforts())
	}
}

// catalogServer serves /models with one Claude-style and one GPT-style model
// and counts the catalog fetches.
func catalogServer(t *testing.T, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"data":[
			{"id":"claude-opus-5.5","model_picker_enabled":true,"capabilities":{"type":"chat",
			 "supports":{"tool_calls":true,"adaptive_thinking":true,"reasoning_effort":["low","medium","high","xhigh","max"]},
			 "limits":{"max_non_streaming_output_tokens":16000}}},
			{"id":"gpt-5.5","model_picker_enabled":true,"capabilities":{"type":"chat",
			 "supports":{"tool_calls":true,"reasoning_effort":["none","low","medium","high","xhigh"]}}},
			{"id":"plain","model_picker_enabled":true,"capabilities":{"type":"chat","supports":{}}}
		]}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// send runs one request through the middleware and returns the body the
// next handler saw.
func send(t *testing.T, auth *Auth, path, body string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://api.individual.githubcopilot.com"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var seen map[string]any
	_, err = auth.Middleware(req, func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		if r.ContentLength != int64(len(raw)) {
			t.Errorf("ContentLength = %d, body is %d bytes", r.ContentLength, len(raw))
		}
		if r.GetBody != nil {
			replay, _ := r.GetBody()
			again, _ := io.ReadAll(replay)
			if string(again) != string(raw) {
				t.Errorf("GetBody replays %q, sent %q", again, raw)
			}
		}
		if err := json.Unmarshal(raw, &seen); err != nil {
			t.Fatalf("body is not JSON: %q", raw)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return seen
}

func TestApplyReasoningPerEndpoint(t *testing.T) {
	srv, calls := catalogServer(t, http.StatusOK)
	auth := &Auth{Source: primedSource("cop_tok", srv.URL), ReasoningEffort: "max"}

	body := send(t, auth, "/v1/messages", `{"model":"claude-opus-5.5","max_tokens":16384,"messages":[]}`)
	if body["thinking"].(map[string]any)["type"] != "adaptive" {
		t.Errorf("thinking = %v", body["thinking"])
	}
	if body["output_config"].(map[string]any)["effort"] != "max" {
		t.Errorf("output_config = %v", body["output_config"])
	}
	if body["max_tokens"].(float64) != 16000 {
		t.Errorf("max_tokens = %v, want the 16000 non-streaming limit", body["max_tokens"])
	}

	body = send(t, auth, "/responses", `{"model":"gpt-5.5","input":[]}`)
	if body["reasoning"].(map[string]any)["effort"] != "xhigh" {
		t.Errorf("reasoning = %v, want max clamped to xhigh", body["reasoning"])
	}
	if _, ok := body["thinking"]; ok {
		t.Error("thinking sent to a model without adaptive thinking")
	}

	body = send(t, auth, "/chat/completions", `{"model":"gpt-5.5","messages":[]}`)
	if body["reasoning_effort"] != "xhigh" {
		t.Errorf("reasoning_effort = %v", body["reasoning_effort"])
	}

	body = send(t, auth, "/chat/completions", `{"model":"plain","messages":[]}`)
	if _, ok := body["reasoning_effort"]; ok {
		t.Error("effort sent to a model that advertises none")
	}

	if calls.Load() != 1 {
		t.Errorf("catalog fetched %d times, want once", calls.Load())
	}
}

func TestApplyReasoningKeepsCallerFields(t *testing.T) {
	srv, _ := catalogServer(t, http.StatusOK)
	auth := &Auth{Source: primedSource("cop_tok", srv.URL), ReasoningEffort: "max"}
	body := send(t, auth, "/v1/messages",
		`{"model":"claude-opus-5.5","max_tokens":8000,"thinking":{"type":"enabled","budget_tokens":2000},"output_config":{"effort":"low"}}`)
	if body["thinking"].(map[string]any)["type"] != "enabled" || body["output_config"].(map[string]any)["effort"] != "low" {
		t.Errorf("caller fields overwritten: %v", body)
	}
	if body["max_tokens"].(float64) != 8000 {
		t.Errorf("max_tokens changed below the limit: %v", body["max_tokens"])
	}
}

func TestApplyReasoningOffStillClampsOutput(t *testing.T) {
	srv, _ := catalogServer(t, http.StatusOK)
	for _, effort := range []string{"", EffortOff} {
		auth := &Auth{Source: primedSource("cop_tok", srv.URL), ReasoningEffort: effort}
		body := send(t, auth, "/v1/messages", `{"model":"claude-opus-5.5","max_tokens":20000}`)
		if _, ok := body["thinking"]; ok {
			t.Errorf("effort %q: thinking sent", effort)
		}
		if body["max_tokens"].(float64) != 16000 {
			t.Errorf("effort %q: max_tokens = %v", effort, body["max_tokens"])
		}
		body = send(t, auth, "/v1/messages", `{"model":"claude-opus-5.5","max_tokens":20000,"stream":true}`)
		if body["max_tokens"].(float64) != 20000 {
			t.Errorf("effort %q: streaming max_tokens clamped to %v", effort, body["max_tokens"])
		}
	}
}

func TestApplyReasoningLeavesUnknownInputsAlone(t *testing.T) {
	srv, _ := catalogServer(t, http.StatusOK)
	auth := &Auth{Source: primedSource("cop_tok", srv.URL), ReasoningEffort: "high"}
	for _, body := range []string{
		`{"model":"unknown-model","max_tokens":20000}`,
		`{"max_tokens":20000}`,
		`{"model":42}`,
	} {
		got := send(t, auth, "/v1/messages", body)
		var want map[string]any
		_ = json.Unmarshal([]byte(body), &want)
		if len(got) != len(want) {
			t.Errorf("body %s was rewritten to %v", body, got)
		}
	}

	odd := send(t, auth, "/v1/messages", `{"model":"claude-opus-5.5","max_tokens":"lots"}`)
	if odd["max_tokens"] != "lots" {
		t.Errorf("unparseable max_tokens rewritten to %v", odd["max_tokens"])
	}

	req, _ := http.NewRequest(http.MethodPost, "https://x/v1/messages", strings.NewReader("not json"))
	if err := auth.applyReasoning(req); err != nil {
		t.Fatal(err)
	}
	empty, _ := http.NewRequest(http.MethodPost, "https://x/v1/messages", nil)
	if err := auth.applyReasoning(empty); err != nil {
		t.Fatal(err)
	}
}

func TestApplyReasoningSkipsWhenCatalogFails(t *testing.T) {
	srv, calls := catalogServer(t, http.StatusInternalServerError)
	auth := &Auth{Source: primedSource("cop_tok", srv.URL), ReasoningEffort: "max"}
	for range 2 {
		body := send(t, auth, "/v1/messages", `{"model":"claude-opus-5.5","max_tokens":20000}`)
		if _, ok := body["thinking"]; ok || body["max_tokens"].(float64) != 20000 {
			t.Errorf("body changed without a catalog: %v", body)
		}
	}
	if calls.Load() != 2 {
		t.Errorf("failed catalog fetched %d times, want one per request up to the retry limit", calls.Load())
	}
}

func TestApplyReasoningRespectsLowEffortAndForcedTools(t *testing.T) {
	srv, _ := catalogServer(t, http.StatusOK)
	low := &Auth{Source: primedSource("cop_tok", srv.URL), ReasoningEffort: "none"}
	body := send(t, low, "/v1/messages", `{"model":"claude-opus-5.5","max_tokens":1000}`)
	if _, ok := body["thinking"]; ok {
		t.Error("none turned thinking on")
	}
	if _, ok := body["output_config"]; ok {
		t.Error("none raised the effort to the model's lowest level")
	}

	auth := &Auth{Source: primedSource("cop_tok", srv.URL), ReasoningEffort: "max"}
	body = send(t, auth, "/v1/messages", `{"model":"claude-opus-5.5","max_tokens":1000,"tool_choice":{"type":"any"}}`)
	if _, ok := body["thinking"]; ok {
		t.Error("thinking sent with a forced tool choice")
	}
	if body["output_config"].(map[string]any)["effort"] != "max" {
		t.Errorf("output_config = %v", body["output_config"])
	}
	body = send(t, auth, "/v1/messages", `{"model":"claude-opus-5.5","max_tokens":1000,"tool_choice":{"type":"auto"}}`)
	if _, ok := body["thinking"]; !ok {
		t.Error("thinking dropped for an automatic tool choice")
	}
}

func TestCatalogFetchRetriesAfterFailure(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"claude-opus-5.5","model_picker_enabled":true,"capabilities":{"type":"chat","supports":{"adaptive_thinking":true,"reasoning_effort":["high","max"]}}}]}`)
	}))
	defer srv.Close()
	auth := &Auth{Source: primedSource("cop_tok", srv.URL), ReasoningEffort: "max"}
	first := send(t, auth, "/v1/messages", `{"model":"claude-opus-5.5","max_tokens":1000}`)
	if _, ok := first["thinking"]; ok {
		t.Error("thinking added without a catalog")
	}
	second := send(t, auth, "/v1/messages", `{"model":"claude-opus-5.5","max_tokens":1000}`)
	if _, ok := second["thinking"]; !ok {
		t.Error("catalog was not refetched after the first failure")
	}
	send(t, auth, "/v1/messages", `{"model":"claude-opus-5.5","max_tokens":1000}`)
	if calls != 2 {
		t.Errorf("catalog fetched %d times, want 2", calls)
	}
}

func TestAdaptiveThinkingFollowsTheClampedEffort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[
			{"id":"high-only","model_picker_enabled":true,"capabilities":{"type":"chat","supports":{"adaptive_thinking":true,"reasoning_effort":["high","max"]}}},
			{"id":"no-levels","model_picker_enabled":true,"capabilities":{"type":"chat","supports":{"adaptive_thinking":true}}}
		]}`)
	}))
	defer srv.Close()
	low := &Auth{Source: primedSource("cop_tok", srv.URL), ReasoningEffort: "low"}
	body := send(t, low, "/v1/messages", `{"model":"high-only","max_tokens":1000}`)
	if _, ok := body["thinking"]; ok {
		t.Error("thinking enabled although low clamps to no supported level")
	}
	body = send(t, low, "/v1/messages", `{"model":"no-levels","max_tokens":1000}`)
	if _, ok := body["thinking"]; !ok {
		t.Error("thinking dropped for a model that advertises no effort levels")
	}
}

func TestReasoningOffSkipsCatalogOutsideMessages(t *testing.T) {
	srv, calls := catalogServer(t, http.StatusOK)
	auth := &Auth{Source: primedSource("cop_tok", srv.URL), ReasoningEffort: EffortOff}
	send(t, auth, "/responses", `{"model":"gpt-5.5","input":[]}`)
	send(t, auth, "/chat/completions", `{"model":"gpt-5.5","messages":[]}`)
	if calls.Load() != 0 {
		t.Fatalf("catalog fetched %d times with reasoning off on non-Messages endpoints", calls.Load())
	}
	send(t, auth, "/v1/messages", `{"model":"claude-opus-5.5","max_tokens":20000}`)
	if calls.Load() != 1 {
		t.Fatalf("Messages request did not fetch the catalog for the output clamp")
	}
}
