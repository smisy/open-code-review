// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeGitHub serves the token endpoint and records how often it was hit.
type fakeGitHub struct {
	calls  atomic.Int32
	status int
	body   string
}

func (f *fakeGitHub) start(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.URL.Path != "/copilot_internal/v2/token" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer gho_test" {
			t.Errorf("Authorization = %q", got)
		}
		if r.Header.Get("Editor-Version") == "" || r.Header.Get("X-Github-Api-Version") == "" {
			t.Errorf("missing IDE headers: %v", r.Header)
		}
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.body)
	}))
	t.Cleanup(srv.Close)
	old := githubAPIURL
	githubAPIURL = srv.URL
	t.Cleanup(func() { githubAPIURL = old })
}

func tokenBody(token string, expiresAt time.Time) string {
	b, _ := json.Marshal(map[string]any{"token": token, "expires_at": expiresAt.Unix()})
	return string(b)
}

func setNow(t *testing.T, at time.Time) *time.Time {
	t.Helper()
	cur := at
	old := now
	now = func() time.Time { return cur }
	t.Cleanup(func() { now = old })
	return &cur
}

func TestTokenSourceCachesAndRefreshes(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	clock := setNow(t, start)
	gh := &fakeGitHub{status: http.StatusOK, body: tokenBody("tid=1;proxy-ep=proxy.enterprise.githubcopilot.com;", start.Add(30*time.Minute))}
	gh.start(t)

	src := NewTokenSource(" gho_test\n")
	tok, base, err := src.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "tid=1") || base != "https://api.enterprise.githubcopilot.com" {
		t.Fatalf("got %q %q", tok, base)
	}
	if _, _, err := src.Token(context.Background()); err != nil || gh.calls.Load() != 1 {
		t.Fatalf("cached token not reused: calls=%d err=%v", gh.calls.Load(), err)
	}

	*clock = start.Add(26 * time.Minute)
	if _, _, err := src.Token(context.Background()); err != nil || gh.calls.Load() != 2 {
		t.Fatalf("token near expiry not refreshed: calls=%d err=%v", gh.calls.Load(), err)
	}

	src.Invalidate()
	if _, _, err := src.Token(context.Background()); err != nil || gh.calls.Load() != 3 {
		t.Fatalf("invalidated token not refreshed: calls=%d err=%v", gh.calls.Load(), err)
	}
}

func TestExchangeErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"unauthorized", http.StatusUnauthorized, "", "run 'ocr copilot login'"},
		{"no seat", http.StatusForbidden, "", "no Copilot access"},
		{"not found", http.StatusNotFound, "", "no Copilot access"},
		{"server error", http.StatusBadGateway, "upstream down", "HTTP 502: upstream down"},
		{"bad json", http.StatusOK, "{", "decode response"},
		{"missing fields", http.StatusOK, `{"token":""}`, "missing token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh := &fakeGitHub{status: tt.status, body: tt.body}
			gh.start(t)
			_, _, err := NewTokenSource("gho_test").Token(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestExchangeWithoutGitHubToken(t *testing.T) {
	_, _, err := NewTokenSource("  ").Token(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
}

func TestExchangeTransportError(t *testing.T) {
	old := githubAPIURL
	githubAPIURL = "http://127.0.0.1:1"
	t.Cleanup(func() { githubAPIURL = old })
	_, _, err := NewTokenSource("gho_test").Token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "copilot token exchange") {
		t.Fatalf("err = %v", err)
	}
}

func TestExchangeBadURL(t *testing.T) {
	old := githubAPIURL
	githubAPIURL = "://bad"
	t.Cleanup(func() { githubAPIURL = old })
	if _, _, err := NewTokenSource("gho_test").Token(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

func TestBaseURLFromToken(t *testing.T) {
	tests := []struct{ token, want string }{
		{"tid=1;exp=2", DefaultBaseURL},
		{"tid=1;proxy-ep=proxy.individual.githubcopilot.com;", "https://api.individual.githubcopilot.com"},
		{"proxy-ep=proxy.business.githubcopilot.com", "https://api.business.githubcopilot.com"},
		{"tid=1; proxy-ep=https://api.enterprise.githubcopilot.com/x;", "https://api.enterprise.githubcopilot.com"},
		{"proxy-ep=proxy.evil.example.com", DefaultBaseURL},
		{"proxy-ep=githubcopilot.com.evil.io", DefaultBaseURL},
		{"proxy-ep=https://", DefaultBaseURL},
		{"proxy-ep=%zz", DefaultBaseURL},
	}
	for _, tt := range tests {
		if got := BaseURLFromToken(tt.token); got != tt.want {
			t.Errorf("BaseURLFromToken(%q) = %q, want %q", tt.token, got, tt.want)
		}
	}
}

func TestProtocolForModel(t *testing.T) {
	tests := map[string]string{
		"claude-sonnet-5":  ProtocolAnthropic,
		"Claude-Haiku-4.5": ProtocolAnthropic,
		"gemini-3.8-flash": ProtocolChatCompletions,
		"gpt-4.1":          ProtocolChatCompletions,
		"gpt-5-mini":       ProtocolResponses,
		"gpt-5.3-codex":    ProtocolResponses,
		"grok-4.7":         ProtocolResponses,
	}
	for model, want := range tests {
		if got := ProtocolForModel(model); got != want {
			t.Errorf("ProtocolForModel(%q) = %q, want %q", model, got, want)
		}
	}
}
