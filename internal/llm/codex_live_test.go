// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package llm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alibaba/open-code-review/internal/llm/codex"
)

func TestCodexLiveSubscription(t *testing.T) {
	path := os.Getenv("OCR_CODEX_LIVE_AUTH")
	if path == "" {
		t.Skip("set OCR_CODEX_LIVE_AUTH to opt in to a real subscription test")
	}
	c, err := codex.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c.Tokens.RefreshToken = ""
	isolated := filepath.Join(t.TempDir(), "auth.json")
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(isolated, data, 0600); err != nil {
		t.Fatal(err)
	}
	client := NewLLMClient(ResolvedEndpoint{URL: codex.BaseURL, Model: "gpt-6.1-sol", Protocol: ProtocolOpenAIResponses, Codex: &codex.Auth{Path: isolated}}, nil, nil)
	tools := []ToolDef{{Type: "function", Function: FunctionDef{Name: "verify_connection", Description: "Verify the tool connection", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}}}
	messages := []Message{{Role: "system", Content: "Call verify_connection exactly once, then reply CONNECTION_OK after receiving the tool result."}, {Role: "user", Content: "Verify the connection now."}}
	send := func() (*ChatResponse, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		return client.CompletionsWithCtx(ctx, ChatRequest{Messages: messages, Tools: tools, MaxTokens: 256})
	}
	first, err := send()
	if err != nil {
		t.Fatal(err)
	}
	calls := first.ToolCalls()
	if len(calls) != 1 || calls[0].Function.Name != "verify_connection" {
		t.Fatalf("expected verify_connection tool call; count=%d content=%q", len(calls), first.VisibleContent())
	}
	messages = append(messages, NewToolCallMessage(first.VisibleContent(), calls, first.Native(), first.ReasoningContent()), Message{Role: "tool", ToolCallID: calls[0].ID, Content: "CONNECTION_OK"})
	second, err := send()
	if err != nil {
		t.Fatal(err)
	}
	if second.VisibleContent() != "CONNECTION_OK" {
		t.Fatalf("unexpected completion: %q", second.VisibleContent())
	}
	t.Logf("Live subscription verified: model=%s, tool call and follow-up completion succeeded", second.Model)
}
