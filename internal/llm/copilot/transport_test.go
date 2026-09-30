// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package copilot

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// primedSource returns a TokenSource that already holds a valid token, so
// tests of the middleware make no exchange request.
func primedSource(token, baseURL string) *TokenSource {
	return &TokenSource{githubToken: "gho_test", token: token, baseURL: baseURL, expiresAt: now().Add(time.Hour)}
}

func newRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://api.individual.githubcopilot.com/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Api-Key", "leak")
	req.Header.Set("Authorization", "Bearer gho_test")
	return req
}

func TestMiddlewareRewritesAndAuthenticates(t *testing.T) {
	auth := &Auth{Source: primedSource("cop_tok", "https://api.enterprise.githubcopilot.com"), RewriteHost: true}
	var seen *http.Request
	resp, err := auth.Middleware(newRequest(t, `{"messages":[{"role":"user","content":"hi"}]}`), func(r *http.Request) (*http.Response, error) {
		seen = r
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	if seen.URL.String() != "https://api.enterprise.githubcopilot.com/v1/messages" || seen.Host != "api.enterprise.githubcopilot.com" {
		t.Errorf("url = %s host = %s", seen.URL, seen.Host)
	}
	checks := map[string]string{
		"Authorization":          "Bearer cop_tok",
		"X-Api-Key":              "",
		"Copilot-Integration-Id": integrationID,
		"Editor-Version":         editorVersion,
		"User-Agent":             userAgent,
		"X-Initiator":            "user",
	}
	for k, want := range checks {
		if got := seen.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestMiddlewareKeepsPinnedHost(t *testing.T) {
	auth := &Auth{Source: primedSource("cop_tok", "https://api.enterprise.githubcopilot.com")}
	_, err := auth.Middleware(newRequest(t, ""), func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.individual.githubcopilot.com" {
			t.Errorf("host rewritten to %s", r.URL.Host)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMiddlewareRetriesOnceAfterUnauthorized(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	setNow(t, start)
	gh := &fakeGitHub{status: http.StatusOK, body: tokenBody("fresh", start.Add(30*time.Minute))}
	gh.start(t)

	auth := &Auth{Source: primedSource("stale", DefaultBaseURL), RewriteHost: true}
	var tokens []string
	var bodies []string
	resp, err := auth.Middleware(newRequest(t, `{"x":1}`), func(r *http.Request) (*http.Response, error) {
		tokens = append(tokens, r.Header.Get("Authorization"))
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if len(tokens) == 1 {
			return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("expired"))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	if len(tokens) != 2 || tokens[0] != "Bearer stale" || tokens[1] != "Bearer fresh" {
		t.Fatalf("tokens = %v", tokens)
	}
	if bodies[1] != `{"x":1}` {
		t.Fatalf("retry body = %q", bodies[1])
	}
}

func TestMiddlewarePassesThroughWithoutReplayableBody(t *testing.T) {
	auth := &Auth{Source: primedSource("cop_tok", DefaultBaseURL)}
	req := newRequest(t, "")
	req.Body, req.GetBody = nil, nil
	calls := 0
	resp, err := auth.Middleware(req, func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: http.NoBody}, nil
	})
	if err != nil || resp.StatusCode != http.StatusUnauthorized || calls != 1 {
		t.Fatalf("status=%d calls=%d err=%v", resp.StatusCode, calls, err)
	}
}

func TestMiddlewareErrors(t *testing.T) {
	next := func(*http.Request) (*http.Response, error) {
		t.Fatal("next must not run")
		return nil, nil
	}
	if _, err := (&Auth{Source: NewTokenSource("")}).Middleware(newRequest(t, ""), next); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v", err)
	}
	bad := &Auth{Source: primedSource("t", "://bad"), RewriteHost: true}
	if _, err := bad.Middleware(newRequest(t, ""), next); err == nil {
		t.Error("want base URL error")
	}
}

func TestMiddlewareRetryFailures(t *testing.T) {
	unauthorized := func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: http.NoBody}, nil
	}

	req := newRequest(t, "{}")
	req.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("no replay") }
	if _, err := (&Auth{Source: primedSource("t", DefaultBaseURL)}).Middleware(req, unauthorized); err == nil {
		t.Error("want GetBody error")
	}

	gh := &fakeGitHub{status: http.StatusUnauthorized}
	gh.start(t)
	if _, err := (&Auth{Source: primedSource("t", DefaultBaseURL)}).Middleware(newRequest(t, "{}"), unauthorized); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v", err)
	}
}

func TestInitiator(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"empty", "", "user"},
		{"not json", "{", "user"},
		{"no messages", `{"model":"x"}`, "user"},
		{"chat user", `{"messages":[{"role":"system","content":"s"},{"role":"user","content":"diff"}]}`, "user"},
		{"chat tool", `{"messages":[{"role":"user","content":"diff"},{"role":"assistant"},{"role":"tool","content":"r"}]}`, "agent"},
		{"anthropic tool result", `{"messages":[{"role":"user","content":[{"type":"tool_result","content":"r"},{"type":"text","text":"more"}]}]}`, "agent"},
		{"anthropic text", `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, "user"},
		{"assistant last", `{"messages":[{"role":"assistant","content":"x"}]}`, "agent"},
		{"responses output", `{"input":[{"role":"user","content":"x"},{"type":"function_call_output","output":"r"}]}`, "agent"},
		{"responses string input", `{"input":"hello"}`, "user"},
		{"bad last item", `{"messages":[1]}`, "user"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, "https://x", strings.NewReader(tt.body))
			if got := initiator(req); got != tt.want {
				t.Errorf("initiator = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPeekBodyWithoutGetBody(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	req.Body = io.NopCloser(bytes.NewReader([]byte(`{"messages":[{"role":"tool"}]}`)))
	if got := initiator(req); got != "agent" {
		t.Fatalf("initiator = %q", got)
	}
	b, _ := io.ReadAll(req.Body)
	if string(b) != `{"messages":[{"role":"tool"}]}` {
		t.Fatalf("body not restored: %q", b)
	}
	if req.GetBody == nil {
		t.Fatal("GetBody not installed")
	}

	failing, _ := http.NewRequest(http.MethodPost, "https://x", strings.NewReader("{}"))
	failing.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("boom") }
	if got := initiator(failing); got != "user" {
		t.Fatalf("initiator = %q", got)
	}

	broken, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	broken.Body = io.NopCloser(io.MultiReader(strings.NewReader(`{"mess`), errReader{}))
	if got := peekBody(broken); got != nil {
		t.Fatalf("peekBody = %q", got)
	}
	if broken.GetBody != nil {
		t.Fatal("GetBody installed for a body that failed to read")
	}
	if b, err := io.ReadAll(broken.Body); err == nil || string(b) != `{"mess` {
		t.Fatalf("read failure not replayed: body=%q err=%v", b, err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer cop_tok" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"data":[
			{"id":"claude-sonnet-5","vendor":"Anthropic","model_picker_enabled":true,"supported_endpoints":["/v1/messages"],"capabilities":{"type":"chat","supports":{"tool_calls":true}}},
			{"id":"hidden","model_picker_enabled":false,"capabilities":{"type":"chat"}},
			{"id":"text-embedding-3-small","capabilities":{"type":"embeddings"}},
			{"id":"gpt-5-mini","capabilities":{"type":"chat"}}
		]}`)
	}))
	defer srv.Close()

	models, err := ListModels(context.Background(), primedSource("cop_tok", srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "claude-sonnet-5" || !models[0].ToolCalls || models[1].ID != "gpt-5-mini" {
		t.Fatalf("models = %+v", models)
	}

	if _, err := ListModels(context.Background(), primedSource("wrong", srv.URL)); err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("err = %v", err)
	}
}

func TestListModelsErrors(t *testing.T) {
	if _, err := ListModels(context.Background(), NewTokenSource("")); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v", err)
	}
	if _, err := ListModels(context.Background(), primedSource("t", "://bad")); err == nil {
		t.Error("want URL error")
	}
	if _, err := ListModels(context.Background(), primedSource("t", "http://127.0.0.1:1")); err == nil {
		t.Error("want transport error")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "{")
	}))
	defer srv.Close()
	if _, err := ListModels(context.Background(), primedSource("t", srv.URL)); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Errorf("err = %v", err)
	}
}
