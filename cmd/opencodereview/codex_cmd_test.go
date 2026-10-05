// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/llm"
	"github.com/alibaba/open-code-review/internal/llm/codex"
)

func TestSelectCodexProvider(t *testing.T) {
	for _, active := range []string{"", "anthropic", "openai-codex"} {
		t.Run(active, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			cfg := &Config{Provider: active, Model: "existing", Providers: map[string]ProviderEntry{"openai-codex": {Model: "custom"}}}
			if err := saveConfig(path, cfg); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := selectCodexProvider(&out, path); err != nil {
				t.Fatal(err)
			}
			got, err := loadOrCreateConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			want := active
			if want == "" {
				want = "openai-codex"
			}
			if got.Provider != want || got.Providers["openai-codex"].Model != "custom" {
				t.Fatalf("config=%+v", got)
			}
			if active != "" && got.Model != "existing" {
				t.Fatal("changed existing model")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := selectCodexProvider(io.Discard, path); err != nil {
		t.Fatal(err)
	}
	cfg, _ := loadOrCreateConfig(path)
	if cfg.Providers["openai-codex"].Model != "gpt-6.1-sol" {
		t.Fatalf("model=%v", cfg.Providers)
	}
	p, _ := llm.LookupProvider("openai-codex")
	if err := checkAPIKeyRequirement(p.Name, "", "", p, true); err != nil {
		t.Fatal(err)
	}
}

func TestCodexCommands(t *testing.T) {
	setTestHome(t, t.TempDir())
	old := codexLogin
	defer func() { codexLogin = old }()
	codexLogin = func(ctx context.Context, in io.Reader, out, stderr io.Writer, home string) error {
		return os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"secret","account_id":"account"}}`), 0600)
	}
	var out bytes.Buffer
	for _, name := range []string{"login", "status", "logout", "logout"} {
		cmd, _, err := codexCmd.Find([]string{name})
		if err != nil {
			t.Fatal(err)
		}
		cmd.SetContext(context.Background())
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if strings.Contains(out.String(), "secret") {
		t.Fatal("printed token")
	}
	cmd, _, _ := codexCmd.Find([]string{"status"})
	if err := cmd.RunE(cmd, nil); err == nil {
		t.Fatal("accepted missing login")
	}
	codexLogin = func(context.Context, io.Reader, io.Writer, io.Writer, string) error {
		return fmt.Errorf("login failed")
	}
	cmd, _, _ = codexCmd.Find([]string{"login"})
	if err := cmd.RunE(cmd, nil); err == nil {
		t.Fatal("ignored login failure")
	}
	home, _ := codex.Home()
	if _, err := os.Stat(filepath.Join(home, "auth.json")); !os.IsNotExist(err) {
		t.Fatal("failed login persisted credential")
	}
}

func TestCodexLoginPreservesActiveModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := saveConfig(path, &Config{Provider: "openai-codex", Model: "account-model"}); err != nil {
		t.Fatal(err)
	}
	if err := selectCodexProvider(io.Discard, path); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadOrCreateConfig(path)
	if err != nil || cfg.Providers["openai-codex"].Model != "account-model" {
		t.Fatalf("model=%+v err=%v", cfg, err)
	}
}

func TestCodexLoginLock(t *testing.T) {
	home := t.TempDir()
	unlock, err := lockCodexLogin(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockCodexLogin(home); err == nil {
		t.Fatal("accepted concurrent login")
	}
	unlock()
	unlock, err = lockCodexLogin(home)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestCodexLiveLLMCommand(t *testing.T) {
	path := os.Getenv("OCR_CODEX_LIVE_AUTH")
	if path == "" {
		t.Skip("set OCR_CODEX_LIVE_AUTH to opt in to a real subscription test")
	}
	c, err := codex.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c.Tokens.RefreshToken = ""
	setTestHome(t, t.TempDir())
	home, err := codex.Home()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	configPath, err := defaultConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := selectCodexProvider(io.Discard, configPath); err != nil {
		t.Fatal(err)
	}
	if err := runLLMTestWithConfigPath(configPath); err != nil {
		t.Fatal(err)
	}
}
