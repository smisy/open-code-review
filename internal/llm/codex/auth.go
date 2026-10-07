// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package codex

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const BaseURL = "https://chatgpt.com/backend-api/codex"

// Home keeps OCR's refresh tokens separate from the user's Codex CLI session.
func Home() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".opencodereview", "codex"), nil
}

type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	AccountID    string `json:"account_id"`
}

type Credentials struct {
	AuthMode    string    `json:"auth_mode"`
	Tokens      Tokens    `json:"tokens"`
	LastRefresh time.Time `json:"last_refresh"`
}

func Load(path string) (Credentials, error) {
	var c Credentials
	data, err := os.ReadFile(path)
	if err != nil {
		return c, fmt.Errorf("read Codex login: run 'ocr codex login': %w", err)
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("parse Codex login: %w", err)
	}
	if c.AuthMode != "chatgpt" || strings.TrimSpace(c.Tokens.AccessToken) == "" || strings.TrimSpace(c.Tokens.AccountID) == "" {
		return c, fmt.Errorf("ChatGPT subscription login required; run 'ocr codex login'")
	}
	return c, nil
}

// Auth reloads credentials on every attempt so long reviews see renewed logins.
type Auth struct {
	Path            string
	Client          *http.Client
	RefreshURL      string
	ReasoningEffort string
}

func (a *Auth) token(ctx context.Context, force bool) (Tokens, error) {
	return a.tokenAfterRejection(ctx, force, "")
}

func (a *Auth) tokenAfterRejection(ctx context.Context, force bool, rejected string) (Tokens, error) {
	// A directory lock also serializes refresh-token rotation across OCR processes.
	lock := a.Path + ".lock"
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for {
		err := os.Mkdir(lock, 0700)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return Tokens{}, fmt.Errorf("lock Codex login: %w", err)
		}
		select {
		case <-ctx.Done():
			return Tokens{}, ctx.Err()
		case <-deadline.C:
			return Tokens{}, fmt.Errorf("Codex login lock timed out; if no OCR process is running, remove %s", lock)
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer os.Remove(lock)
	c, err := Load(a.Path)
	if err != nil {
		return Tokens{}, err
	}
	if rejected != "" && c.Tokens.AccessToken != rejected {
		return c.Tokens, nil
	}
	if !force && !expiring(c.Tokens.AccessToken) {
		return c.Tokens, nil
	}
	if c.Tokens.RefreshToken == "" {
		return Tokens{}, fmt.Errorf("Codex login expired; run 'ocr codex login'")
	}
	body, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "client_id": "app_EMoamEEZ73f0CkXaXp7hrann", "refresh_token": c.Tokens.RefreshToken})
	endpoint := a.RefreshURL
	if endpoint == "" {
		endpoint = "https://auth.openai.com/oauth/token"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Tokens{}, fmt.Errorf("refresh Codex login: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Tokens{}, fmt.Errorf("refresh Codex login failed (HTTP %d); run 'ocr codex login'", resp.StatusCode)
	}
	var renewed Tokens
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&renewed); err != nil {
		return Tokens{}, fmt.Errorf("decode refreshed Codex login: %w", err)
	}
	if renewed.AccessToken == "" {
		return Tokens{}, fmt.Errorf("refresh Codex login returned no access token")
	}
	c.Tokens.AccessToken = renewed.AccessToken
	if renewed.RefreshToken != "" {
		c.Tokens.RefreshToken = renewed.RefreshToken
	}
	if renewed.IDToken != "" {
		c.Tokens.IDToken = renewed.IDToken
	}
	c.LastRefresh = time.Now().UTC()
	if err := save(a.Path, c); err != nil {
		return Tokens{}, err
	}
	return c.Tokens, nil
}

func expiring(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return false
	}
	return time.Unix(claims.Exp, 0).Before(time.Now().Add(5 * time.Minute))
}

func save(path string, c Credentials) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".auth-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
