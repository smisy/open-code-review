// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alibaba/open-code-review/internal/llm/codex"
)

func TestCodexResolver(t *testing.T) {
	setTestHome(t, t.TempDir())
	home, err := codex.Home()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"access","account_id":"account"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := configFile{Provider: "openai-codex", Model: "account-model", Providers: map[string]providerEntryConfig{"openai-codex": {}}}
	ep, ok, err := tryProviderConfig(cfg, "new-account-model", "")
	if err != nil || !ok || ep.Codex == nil || ep.Token != "" || ep.Model != "new-account-model" || ep.Protocol != ProtocolOpenAIResponses {
		t.Fatalf("endpoint=%+v ok=%v err=%v", ep, ok, err)
	}
	if _, ok := NewLLMClient(ep, nil, nil).(*OpenAIResponsesClient); !ok {
		t.Fatal("wrong client")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	ep, err = ResolveEndpointWithOptions(path, ResolveOptions{Provider: "openai-codex"})
	if err != nil || ep.Codex == nil {
		t.Fatalf("resolve=%+v %v", ep, err)
	}
	for _, entry := range []providerEntryConfig{{URL: "https://evil.example"}, {Protocol: ProtocolOpenAIChatCompletions}, {APIKey: "key"}, {APIKeyCmd: "printf key"}, {AuthHeader: "x-api-key"}} {
		cfg.Providers = map[string]providerEntryConfig{"openai-codex": entry}
		if _, _, err := tryProviderConfig(cfg, "", ""); err == nil {
			t.Errorf("accepted %+v", entry)
		}
	}
	cfg.Providers = map[string]providerEntryConfig{"openai-codex": {}}
	cfg.Model = ""
	if _, _, err := tryProviderConfig(cfg, "", ""); err == nil {
		t.Fatal("accepted missing model")
	}
	cfg.Model = "model"
	if err := os.Remove(filepath.Join(home, "auth.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tryProviderConfig(cfg, "", ""); err == nil {
		t.Fatal("accepted missing login")
	}
}

func TestCodexResponsesToolRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"access","account_id":"account"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	round := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["stream"] != true || body["store"] != false || body["model"] != "gpt-6.1-sol" {
			t.Errorf("request=%v", body)
		}
		if r.Header.Get("Authorization") != "Bearer access" || r.Header.Get("ChatGPT-Account-Id") != "account" {
			t.Error("missing auth headers")
		}
		round++
		output := `[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"inspect","arguments":"{}","status":"completed"}]`
		if round == 2 {
			input, _ := json.Marshal(body["input"])
			if !strings.Contains(string(input), "function_call_output") || !strings.Contains(string(input), "call_1") {
				t.Errorf("missing tool history: %s", input)
			}
			output = `[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Done","annotations":[]}]}]`
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-6.1-sol\",\"output\":%s}}\n\n", output)
	}))
	defer server.Close()
	old := httpClientWithHeaderTimeout
	defer func() { httpClientWithHeaderTimeout = old }()
	httpClientWithHeaderTimeout = func(time.Duration) *http.Client {
		return &http.Client{Transport: codexTestTransport{server: server.URL, base: http.DefaultTransport}}
	}
	client := NewLLMClient(ResolvedEndpoint{URL: codex.BaseURL, Model: "gpt-6.1-sol", Protocol: ProtocolOpenAIResponses, Codex: &codex.Auth{Path: path}}, nil, nil)
	req := ChatRequest{Messages: []Message{{Role: "system", Content: "Review code"}, {Role: "user", Content: "Inspect this change"}}}
	first, err := client.CompletionsWithCtx(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Choices) != 1 || len(first.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("response=%+v", first)
	}
	req.Messages = append(req.Messages, Message{Role: "assistant", ToolCalls: first.ToolCalls(), Native: first.Native()}, Message{Role: "tool", ToolCallID: "call_1", Content: "No defects"})
	second, err := client.CompletionsWithCtx(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if second.Content() != "Done" || round != 2 {
		t.Fatalf("response=%+v rounds=%d", second, round)
	}
}

type codexTestTransport struct {
	server string
	base   http.RoundTripper
}

func (tr codexTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	target, _ := url.Parse(tr.server)
	copy.URL.Scheme, copy.URL.Host = target.Scheme, target.Host
	return tr.base.RoundTrip(copy)
}
