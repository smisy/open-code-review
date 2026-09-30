// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

// Package copilot lets a GitHub Copilot subscription serve as an LLM provider.
//
// A long-lived GitHub OAuth token (from the device login, config, or
// COPILOT_GITHUB_TOKEN) is exchanged for a short-lived Copilot API token, which
// is refreshed transparently while a run is in flight. Requests carry the
// editor identity headers the Copilot API expects from its IDE clients.
package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// ClientID is the OAuth app the Copilot editor integrations use for the
	// GitHub device flow; the token endpoint only accepts tokens it minted.
	ClientID = "Iv1.b507a08c87ecfe98"

	// EnvToken is checked for a GitHub OAuth token before the stored login.
	EnvToken = "COPILOT_GITHUB_TOKEN"

	// DefaultBaseURL serves Copilot Individual; business and enterprise seats
	// are pointed elsewhere by the proxy-ep hint inside the Copilot token.
	DefaultBaseURL = "https://api.individual.githubcopilot.com"

	integrationID       = "vscode-chat"
	editorVersion       = "vscode/1.107.0"
	editorPluginVersion = "copilot-chat/0.35.0"
	userAgent           = "GitHubCopilotChat/0.35.0"
	githubAPIVersion    = "2025-04-01"

	// refreshMargin keeps a token from expiring between the check and the
	// response of a long completion.
	refreshMargin = 5 * time.Minute
)

var (
	githubURL    = "https://github.com"
	githubAPIURL = "https://api.github.com"
	httpClient   = &http.Client{Timeout: 30 * time.Second}
	now          = time.Now
)

// ErrUnauthorized means GitHub rejected the OAuth token, so a new login is needed.
var ErrUnauthorized = errors.New("GitHub rejected the Copilot credential; run 'ocr copilot login'")

// setIDEHeaders adds the editor identity every Copilot endpoint checks.
func setIDEHeaders(h http.Header) {
	h.Set("Copilot-Integration-Id", integrationID)
	h.Set("Editor-Version", editorVersion)
	h.Set("Editor-Plugin-Version", editorPluginVersion)
	h.Set("User-Agent", userAgent)
}

// TokenSource hands out Copilot API tokens for one GitHub OAuth token,
// exchanging again shortly before the current token expires.
type TokenSource struct {
	githubToken string

	mu        sync.Mutex
	token     string
	baseURL   string
	expiresAt time.Time
}

// NewTokenSource returns a source for githubToken. It makes no request until
// the first Token call.
func NewTokenSource(githubToken string) *TokenSource {
	return &TokenSource{githubToken: strings.TrimSpace(githubToken)}
}

// Token returns a valid Copilot API token and the API base URL it is issued for.
func (s *TokenSource) Token(ctx context.Context) (token, baseURL string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && now().Add(refreshMargin).Before(s.expiresAt) {
		return s.token, s.baseURL, nil
	}
	tok, exp, err := exchange(ctx, s.githubToken)
	if err != nil {
		return "", "", err
	}
	s.token, s.expiresAt, s.baseURL = tok, exp, BaseURLFromToken(tok)
	return s.token, s.baseURL, nil
}

// Invalidate forces the next Token call to exchange again.
func (s *TokenSource) Invalidate() {
	s.mu.Lock()
	s.token = ""
	s.mu.Unlock()
}

func exchange(ctx context.Context, githubToken string) (string, time.Time, error) {
	if githubToken == "" {
		return "", time.Time{}, ErrUnauthorized
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIURL+"/copilot_internal/v2/token", nil)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+githubToken)
	req.Header.Set("X-Github-Api-Version", githubAPIVersion)
	setIDEHeaders(req.Header)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("copilot token exchange: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return "", time.Time{}, ErrUnauthorized
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound:
		return "", time.Time{}, fmt.Errorf("copilot token exchange: HTTP %d: this GitHub account has no Copilot access (or the token was not issued by 'ocr copilot login')", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return "", time.Time{}, fmt.Errorf("copilot token exchange: HTTP %d: %s", resp.StatusCode, readSnippet(resp.Body))
	}
	var body struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", time.Time{}, fmt.Errorf("copilot token exchange: decode response: %w", err)
	}
	if body.Token == "" || body.ExpiresAt <= 0 {
		return "", time.Time{}, errors.New("copilot token exchange: response is missing token or expires_at")
	}
	return body.Token, time.Unix(body.ExpiresAt, 0), nil
}

var proxyEndpointRe = regexp.MustCompile(`(?:^|;)\s*proxy-ep=([^;\s]+)`)

// BaseURLFromToken derives the account's API base URL from the proxy-ep hint
// in a Copilot token. The hint is credential data, so only GitHub-owned
// Copilot hosts are accepted; anything else falls back to DefaultBaseURL
// rather than sending the token to an arbitrary host.
func BaseURLFromToken(token string) string {
	m := proxyEndpointRe.FindStringSubmatch(token)
	if m == nil {
		return DefaultBaseURL
	}
	raw := m[1]
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return DefaultBaseURL
	}
	host := strings.ToLower(u.Hostname())
	if rest, ok := strings.CutPrefix(host, "proxy."); ok {
		host = "api." + rest
	}
	if !strings.HasSuffix(host, ".githubcopilot.com") {
		return DefaultBaseURL
	}
	return "https://" + host
}

func readSnippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 300))
	return strings.TrimSpace(string(b))
}
