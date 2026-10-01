// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/llm/copilot"
)

func copilotConfig(model string, entry map[string]any) map[string]any {
	return map[string]any{
		"provider":  "github-copilot",
		"model":     model,
		"providers": map[string]any{"github-copilot": entry},
	}
}

func TestCopilotProviderIsRegistered(t *testing.T) {
	p, ok := LookupProvider("github-copilot")
	if !ok {
		t.Fatal("github-copilot preset not found")
	}
	if !p.CopilotAuth || p.AmbientAuth {
		t.Errorf("CopilotAuth=%v AmbientAuth=%v", p.CopilotAuth, p.AmbientAuth)
	}
	if p.EnvVar != copilot.EnvToken || p.BaseURL != copilot.DefaultBaseURL {
		t.Errorf("EnvVar = %q, BaseURL = %q; keep them in sync with package copilot", p.EnvVar, p.BaseURL)
	}
}

func TestResolveCopilotRoutesProtocolByModel(t *testing.T) {
	setTestHome(t, t.TempDir())
	t.Setenv(copilot.EnvToken, "")
	if err := copilot.SaveGitHubToken("gho_stored"); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		model, protocol, urlSuffix string
	}{
		{"claude-sonnet-5", ProtocolAnthropic, "/v1/messages"},
		{"gemini-3.8-flash", ProtocolOpenAIChatCompletions, ".githubcopilot.com"},
		{"gpt-5-mini", ProtocolOpenAIResponses, ".githubcopilot.com"},
		{"some-model-not-in-the-preset-list", ProtocolOpenAIResponses, ".githubcopilot.com"},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			ep, err := ResolveEndpoint(writeConfig(t, copilotConfig(tt.model, map[string]any{})))
			if err != nil {
				t.Fatal(err)
			}
			if ep.Protocol != tt.protocol || !strings.HasSuffix(ep.URL, tt.urlSuffix) {
				t.Errorf("protocol=%q url=%q", ep.Protocol, ep.URL)
			}
			if ep.Token != "gho_stored" || ep.Copilot == nil || !ep.Copilot.RewriteHost || ep.Copilot.Source == nil {
				t.Errorf("token=%q copilot=%+v", ep.Token, ep.Copilot)
			}
			if ep.Protocol == ProtocolAnthropic && ep.AuthHeader != "authorization" {
				t.Errorf("AuthHeader = %q", ep.AuthHeader)
			}
		})
	}
}

func TestResolveCopilotCredentialPrecedence(t *testing.T) {
	setTestHome(t, t.TempDir())
	if err := copilot.SaveGitHubToken("gho_stored"); err != nil {
		t.Fatal(err)
	}

	t.Setenv(copilot.EnvToken, "gho_env")
	ep, err := ResolveEndpoint(writeConfig(t, copilotConfig("claude-sonnet-5", map[string]any{})))
	if err != nil || ep.Token != "gho_env" {
		t.Fatalf("env: token=%q err=%v", ep.Token, err)
	}

	ep, err = ResolveEndpoint(writeConfig(t, copilotConfig("claude-sonnet-5", map[string]any{"api_key": "gho_config"})))
	if err != nil || ep.Token != "gho_config" {
		t.Fatalf("api_key: token=%q err=%v", ep.Token, err)
	}
}

func TestResolveCopilotWithoutLogin(t *testing.T) {
	setTestHome(t, t.TempDir())
	t.Setenv(copilot.EnvToken, "")
	_, err := ResolveEndpoint(writeConfig(t, copilotConfig("claude-sonnet-5", map[string]any{})))
	if err == nil || !strings.Contains(err.Error(), "ocr copilot login") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveCopilotCorruptLogin(t *testing.T) {
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
	_, err := ResolveEndpoint(writeConfig(t, copilotConfig("claude-sonnet-5", map[string]any{})))
	if err == nil || !strings.Contains(err.Error(), "read Copilot login") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveCopilotHonorsPinnedURLAndProtocol(t *testing.T) {
	setTestHome(t, t.TempDir())
	t.Setenv(copilot.EnvToken, "gho_env")
	ep, err := ResolveEndpoint(writeConfig(t, copilotConfig("claude-sonnet-5", map[string]any{
		"url":      "https://api.business.githubcopilot.com",
		"protocol": "openai",
	})))
	if err != nil {
		t.Fatal(err)
	}
	if ep.Protocol != ProtocolOpenAIChatCompletions || ep.URL != "https://api.business.githubcopilot.com" || ep.Copilot.RewriteHost {
		t.Fatalf("protocol=%q url=%q rewrite=%v", ep.Protocol, ep.URL, ep.Copilot.RewriteHost)
	}
}

func TestResolveCopilotModelOverrideIsNotGated(t *testing.T) {
	setTestHome(t, t.TempDir())
	t.Setenv(copilot.EnvToken, "gho_env")
	ep, err := ResolveEndpointWithModelOverride(writeConfig(t, copilotConfig("claude-sonnet-5", map[string]any{})), "grok-4.7")
	if err != nil || ep.Model != "grok-4.7" || ep.Protocol != ProtocolOpenAIResponses {
		t.Fatalf("model=%q protocol=%q err=%v", ep.Model, ep.Protocol, err)
	}
}

func TestNewLLMClientMountsCopilotAuth(t *testing.T) {
	auth := &copilot.Auth{Source: copilot.NewTokenSource("gho"), RewriteHost: true}
	for _, protocol := range []string{ProtocolAnthropic, ProtocolOpenAIChatCompletions, ProtocolOpenAIResponses} {
		c := NewLLMClient(ResolvedEndpoint{URL: copilot.DefaultBaseURL, Token: "gho", Model: "m", Protocol: protocol, Copilot: auth}, nil, nil)
		if c == nil {
			t.Fatalf("%s: nil client", protocol)
		}
	}
}

func TestResolveCopilotCredentialMatchesResolver(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	t.Setenv(copilot.EnvToken, "")

	missing := filepath.Join(home, "absent.json")
	if _, _, err := ResolveCopilotCredential(missing); err == nil || !strings.Contains(err.Error(), "ocr copilot login") {
		t.Fatalf("no credential: err = %v", err)
	}
	if err := copilot.SaveGitHubToken("gho_stored"); err != nil {
		t.Fatal(err)
	}
	if tok, src, err := ResolveCopilotCredential(missing); err != nil || tok != "gho_stored" || !strings.HasSuffix(src, "github-copilot.json") {
		t.Fatalf("stored: tok=%q src=%q err=%v", tok, src, err)
	}

	tests := []struct {
		name   string
		entry  map[string]any
		env    string
		token  string
		source string
	}{
		{"env over stored", map[string]any{}, "gho_env", "gho_env", "$" + copilot.EnvToken},
		{"api_key_cmd over env", map[string]any{"api_key_cmd": "echo gho_cmd"}, "gho_env", "gho_cmd", "providers.github-copilot.api_key_cmd"},
		{"api_key over all", map[string]any{"api_key": "gho_key", "api_key_cmd": "echo gho_cmd"}, "gho_env", "gho_key", "providers.github-copilot.api_key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(copilot.EnvToken, tt.env)
			path := writeConfig(t, copilotConfig("claude-sonnet-5", tt.entry))
			tok, src, err := ResolveCopilotCredential(path)
			if err != nil || tok != tt.token || src != tt.source {
				t.Fatalf("tok=%q src=%q err=%v", tok, src, err)
			}
			ep, err := ResolveEndpoint(path)
			if err != nil || ep.Token != tok {
				t.Fatalf("resolver used %q, credential helper %q (err=%v)", ep.Token, tok, err)
			}
		})
	}
}

func TestResolveCopilotCredentialConfigErrors(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(bad, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveCopilotCredential(bad); err == nil || !strings.Contains(err.Error(), "parse config") {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := ResolveCopilotCredential(t.TempDir()); err == nil {
		t.Fatal("want read error for a directory")
	}
	failing := writeConfig(t, copilotConfig("claude-sonnet-5", map[string]any{"api_key_cmd": "exit 3"}))
	if _, err := ResolveEndpoint(failing); err == nil {
		t.Fatal("want api_key_cmd failure")
	}
}

func TestResolveCopilotRejectsBedrock(t *testing.T) {
	setTestHome(t, t.TempDir())
	t.Setenv(copilot.EnvToken, "gho_env")
	_, err := ResolveEndpoint(writeConfig(t, copilotConfig("claude-sonnet-5", map[string]any{"protocol": "anthropic-bedrock"})))
	if err == nil || !strings.Contains(err.Error(), "does not support protocol") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveCopilotRejectsAuthHeader(t *testing.T) {
	setTestHome(t, t.TempDir())
	t.Setenv(copilot.EnvToken, "gho_env")
	_, err := ResolveEndpoint(writeConfig(t, copilotConfig("claude-sonnet-5", map[string]any{"auth_header": "x-custom-auth"})))
	if err == nil || !strings.Contains(err.Error(), "does not support auth_header") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveCopilotReasoningEffort(t *testing.T) {
	setTestHome(t, t.TempDir())
	t.Setenv(copilot.EnvToken, "gho_env")
	resolve := func(entry map[string]any, override string) (ResolvedEndpoint, error) {
		return ResolveEndpointWithOptions(writeConfig(t, copilotConfig("claude-opus-5.5", entry)), ResolveOptions{ReasoningEffort: override})
	}
	tests := []struct {
		name     string
		entry    map[string]any
		override string
		want     string
	}{
		{"default", map[string]any{}, "", copilot.DefaultReasoningEffort},
		{"configured", map[string]any{"reasoning_effort": "xhigh"}, "", "xhigh"},
		{"override wins", map[string]any{"reasoning_effort": "low"}, "max", "max"},
		{"override is normalized", map[string]any{}, " MAX ", "max"},
		{"off", map[string]any{"reasoning_effort": "off"}, "", copilot.EffortOff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep, err := resolve(tt.entry, tt.override)
			if err != nil || ep.Copilot.ReasoningEffort != tt.want {
				t.Fatalf("effort=%q err=%v, want %q", ep.Copilot.ReasoningEffort, err, tt.want)
			}
		})
	}
	if _, err := resolve(map[string]any{"reasoning_effort": "turbo"}, ""); err == nil || !strings.Contains(err.Error(), "invalid reasoning effort") {
		t.Fatalf("configured invalid: err = %v", err)
	}
	if _, err := resolve(map[string]any{}, "ultra"); err == nil || !strings.Contains(err.Error(), "invalid reasoning effort") {
		t.Fatalf("override invalid: err = %v", err)
	}
}

func TestReasoningEffortOverrideRejectedForOtherProviders(t *testing.T) {
	setTestHome(t, t.TempDir())
	path := writeConfig(t, map[string]any{
		"provider":  "anthropic",
		"model":     "claude-opus-5",
		"providers": map[string]any{"anthropic": map[string]any{"api_key": "sk-test"}},
	})
	if _, err := ResolveEndpointWithOptions(path, ResolveOptions{ReasoningEffort: "max"}); err == nil || !strings.Contains(err.Error(), "only supported by the github-copilot provider") {
		t.Fatalf("config path: err = %v", err)
	}
	if _, err := ResolveEndpointWithOptions(path, ResolveOptions{Provider: "anthropic", ReasoningEffort: "max"}); err == nil {
		t.Fatal("explicit provider path: want error")
	}
	if _, err := ResolveEndpointWithOptions(path, ResolveOptions{}); err != nil {
		t.Fatalf("no override: %v", err)
	}
}

func TestReasoningEffortChecksRunBeforeApiKeyCmd(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	sentinel := filepath.Join(home, "ran")
	other := writeConfig(t, map[string]any{
		"provider":  "anthropic",
		"model":     "claude-opus-5",
		"providers": map[string]any{"anthropic": map[string]any{"api_key_cmd": "touch '" + sentinel + "'; echo sk"}},
	})
	if _, err := ResolveEndpointWithOptions(other, ResolveOptions{ReasoningEffort: "max"}); err == nil {
		t.Fatal("want error for a non-Copilot provider")
	}
	typo := writeConfig(t, copilotConfig("claude-opus-5.5", map[string]any{"api_key_cmd": "touch '" + sentinel + "'; echo gho", "reasoning_effort": "turbo"}))
	if _, err := ResolveEndpoint(typo); err == nil {
		t.Fatal("want error for an invalid effort")
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("api_key_cmd ran before the reasoning effort was rejected")
	}
}
