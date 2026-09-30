// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/llm"
	"github.com/alibaba/open-code-review/internal/llm/copilot"
)

// stubCopilot replaces every network call the copilot commands make.
func stubCopilot(t *testing.T, checkErr error) {
	t.Helper()
	oldReq, oldPoll, oldCheck, oldList := copilotRequestDeviceCode, copilotPollAccessToken, copilotCheckToken, copilotListModels
	t.Cleanup(func() {
		copilotRequestDeviceCode, copilotPollAccessToken, copilotCheckToken, copilotListModels = oldReq, oldPoll, oldCheck, oldList
	})
	copilotRequestDeviceCode = func(context.Context) (copilot.DeviceCode, error) {
		return copilot.DeviceCode{DeviceCode: "dc", UserCode: "ABCD-1234", VerificationURI: "https://github.com/login/device", ExpiresIn: 900}, nil
	}
	copilotPollAccessToken = func(context.Context, copilot.DeviceCode) (string, error) { return "gho_new", nil }
	copilotCheckToken = func(_ context.Context, token string) (string, error) {
		if checkErr != nil {
			return "", checkErr
		}
		return "https://api.enterprise.githubcopilot.com", nil
	}
	copilotListModels = func(context.Context, string) ([]copilot.Model, error) {
		return []copilot.Model{
			{ID: "claude-sonnet-5", Vendor: "Anthropic", Endpoints: []string{"/v1/messages"}, ToolCalls: true},
			{ID: "gpt-5-mini", Vendor: "OpenAI"},
		}, nil
	}
}

func readTestConfig(t *testing.T, path string) Config {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestCopilotLoginSavesCredentialAndSelectsProvider(t *testing.T) {
	setTestHome(t, t.TempDir())
	stubCopilot(t, nil)
	cfgPath := filepath.Join(t.TempDir(), "config.json")

	var out bytes.Buffer
	if err := runCopilotLogin(context.Background(), &out, cfgPath); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ABCD-1234", "Signed in", "Provider set to github-copilot (model claude-sonnet-5)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	if tok, err := copilot.LoadGitHubToken(); err != nil || tok != "gho_new" {
		t.Fatalf("stored token=%q err=%v", tok, err)
	}
	cfg := readTestConfig(t, cfgPath)
	if cfg.Provider != "github-copilot" || cfg.Providers["github-copilot"].Model != "claude-sonnet-5" {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestCopilotLoginKeepsOtherProviderAndModel(t *testing.T) {
	setTestHome(t, t.TempDir())
	stubCopilot(t, nil)
	dir := t.TempDir()

	other := filepath.Join(dir, "other.json")
	if err := os.WriteFile(other, []byte(`{"provider":"anthropic"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runCopilotLogin(context.Background(), &out, other); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `active provider is "anthropic"`) || readTestConfig(t, other).Provider != "anthropic" {
		t.Fatalf("login overrode another provider:\n%s", out.String())
	}

	withModel := filepath.Join(dir, "model.json")
	if err := os.WriteFile(withModel, []byte(`{"model":"gpt-5.5"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runCopilotLogin(context.Background(), &out, withModel); err != nil {
		t.Fatal(err)
	}
	cfg := readTestConfig(t, withModel)
	if cfg.Providers["github-copilot"].Model != "" || !strings.Contains(out.String(), "model gpt-5.5") {
		t.Fatalf("login replaced the configured model: %+v\n%s", cfg, out.String())
	}
}

func TestCopilotLoginFailures(t *testing.T) {
	setTestHome(t, t.TempDir())
	cfgPath := filepath.Join(t.TempDir(), "config.json")

	stubCopilot(t, errors.New("no seat"))
	if err := runCopilotLogin(context.Background(), &bytes.Buffer{}, cfgPath); err == nil || !strings.Contains(err.Error(), "Copilot is not available") {
		t.Fatalf("err = %v", err)
	}
	if tok, _ := copilot.LoadGitHubToken(); tok != "" {
		t.Fatal("a token without Copilot access was saved")
	}

	stubCopilot(t, nil)
	copilotPollAccessToken = func(context.Context, copilot.DeviceCode) (string, error) { return "", errors.New("denied") }
	if err := runCopilotLogin(context.Background(), &bytes.Buffer{}, cfgPath); err == nil || err.Error() != "denied" {
		t.Fatalf("err = %v", err)
	}

	copilotRequestDeviceCode = func(context.Context) (copilot.DeviceCode, error) { return copilot.DeviceCode{}, errors.New("offline") }
	if err := runCopilotLogin(context.Background(), &bytes.Buffer{}, cfgPath); err == nil || err.Error() != "offline" {
		t.Fatalf("err = %v", err)
	}
}

func TestCopilotLoginBadConfig(t *testing.T) {
	setTestHome(t, t.TempDir())
	stubCopilot(t, nil)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runCopilotLogin(context.Background(), &bytes.Buffer{}, cfgPath); err == nil || !strings.Contains(err.Error(), "load config") {
		t.Fatalf("err = %v", err)
	}
}

func TestCopilotLogout(t *testing.T) {
	setTestHome(t, t.TempDir())
	t.Setenv(copilot.EnvToken, "")
	if err := copilot.SaveGitHubToken("gho"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runCopilotLogout(&out); err != nil || !strings.Contains(out.String(), "Removed") {
		t.Fatalf("out=%q err=%v", out.String(), err)
	}
	out.Reset()
	t.Setenv(copilot.EnvToken, "gho_env")
	if err := runCopilotLogout(&out); err != nil || !strings.Contains(out.String(), "No stored") || !strings.Contains(out.String(), "still set") {
		t.Fatalf("out=%q err=%v", out.String(), err)
	}
}

func TestCopilotStatusAndModels(t *testing.T) {
	setTestHome(t, t.TempDir())
	t.Setenv(copilot.EnvToken, "")
	stubCopilot(t, nil)

	if err := runCopilotStatus(context.Background(), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("err = %v", err)
	}
	if err := runCopilotModels(context.Background(), &bytes.Buffer{}); err == nil {
		t.Fatal("models without login: want error")
	}

	if err := copilot.SaveGitHubToken("gho"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runCopilotStatus(context.Background(), &out); err != nil || !strings.Contains(out.String(), "api.enterprise.githubcopilot.com") || !strings.Contains(out.String(), "github-copilot.json") {
		t.Fatalf("out=%q err=%v", out.String(), err)
	}

	t.Setenv(copilot.EnvToken, "gho_env")
	out.Reset()
	if err := runCopilotStatus(context.Background(), &out); err != nil || !strings.Contains(out.String(), "$"+copilot.EnvToken) {
		t.Fatalf("out=%q err=%v", out.String(), err)
	}

	out.Reset()
	if err := runCopilotModels(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"claude-sonnet-5", "anthropic", "/v1/messages", "gpt-5-mini", "openai-responses"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("models output missing %q:\n%s", want, out.String())
		}
	}

	stubCopilot(t, errors.New("revoked"))
	copilotListModels = func(context.Context, string) ([]copilot.Model, error) { return nil, errors.New("down") }
	if err := runCopilotStatus(context.Background(), &bytes.Buffer{}); err == nil {
		t.Error("status: want error")
	}
	if err := runCopilotModels(context.Background(), &bytes.Buffer{}); err == nil {
		t.Error("models: want error")
	}
}

func TestCopilotGitHubTokenCorruptLogin(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	t.Setenv(copilot.EnvToken, "")
	dir := filepath.Join(home, ".opencodereview")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "github-copilot.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := copilotGitHubToken(); err == nil {
		t.Fatal("want parse error")
	}
}

func TestCopilotCommandsRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"copilot", "login"})
	if err != nil || cmd != copilotLoginCmd {
		t.Fatalf("cmd=%v err=%v", cmd, err)
	}
	if err := copilotCmd.RunE(copilotCmd, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCheckAPIKeyRequirementAllowsCopilot(t *testing.T) {
	t.Setenv(copilot.EnvToken, "")
	p, _ := llm.LookupProvider("github-copilot")
	if err := checkAPIKeyRequirement("github-copilot", "", "", p, true); err != nil {
		t.Fatal(err)
	}
}
