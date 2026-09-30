// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DeviceCode is the pending authorization the user completes in a browser.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

var sleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// RequestDeviceCode starts the GitHub device flow.
func RequestDeviceCode(ctx context.Context) (DeviceCode, error) {
	var dc DeviceCode
	err := postForm(ctx, githubURL+"/login/device/code", url.Values{
		"client_id": {ClientID},
		"scope":     {"read:user"},
	}, &dc)
	if err != nil {
		return DeviceCode{}, fmt.Errorf("request device code: %w", err)
	}
	if dc.DeviceCode == "" || dc.UserCode == "" || dc.VerificationURI == "" || dc.ExpiresIn <= 0 {
		return DeviceCode{}, errors.New("request device code: response is missing fields")
	}
	return dc, nil
}

// PollAccessToken waits until the user approves dc and returns the GitHub
// OAuth token.
func PollAccessToken(ctx context.Context, dc DeviceCode) (string, error) {
	interval := time.Duration(max(dc.Interval, 1)) * time.Second
	deadline := now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	form := url.Values{
		"client_id":   {ClientID},
		"device_code": {dc.DeviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	for now().Before(deadline) {
		if err := sleep(ctx, interval); err != nil {
			return "", err
		}
		var res struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
			Interval    int    `json:"interval"`
		}
		if err := postForm(ctx, githubURL+"/login/oauth/access_token", form, &res); err != nil {
			return "", fmt.Errorf("poll access token: %w", err)
		}
		switch {
		case res.AccessToken != "":
			return res.AccessToken, nil
		case res.Error == "authorization_pending":
		case res.Error == "slow_down":
			// RFC 8628: every slow_down permanently adds five seconds.
			interval = max(interval+5*time.Second, time.Duration(res.Interval)*time.Second)
		case res.Error == "expired_token":
			return "", errors.New("the device code expired; run 'ocr copilot login' again")
		case res.Error == "access_denied":
			return "", errors.New("the login was cancelled in the browser")
		default:
			return "", fmt.Errorf("poll access token: %s", res.Error)
		}
	}
	return "", errors.New("the device code expired; run 'ocr copilot login' again")
}

func postForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, readSnippet(resp.Body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type storedCredentials struct {
	GitHubToken string `json:"github_token"`
}

// CredentialsPath is where 'ocr copilot login' keeps the GitHub OAuth token,
// next to config.json.
func CredentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".opencodereview", "github-copilot.json"), nil
}

// SaveGitHubToken stores token readable by the current user only.
func SaveGitHubToken(token string) error {
	path, err := CredentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(storedCredentials{GitHubToken: token})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".github-copilot-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// LoadGitHubToken returns the stored token, or "" when there is no login.
func LoadGitHubToken() (string, error) {
	path, err := CredentialsPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var creds storedCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	return strings.TrimSpace(creds.GitHubToken), nil
}

// DeleteGitHubToken removes the stored login and reports whether one existed.
func DeleteGitHubToken() (bool, error) {
	path, err := CredentialsPath()
	if err != nil {
		return false, err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
