// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

func (a *Auth) Middleware(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host != "chatgpt.com" || req.URL.Path != "/backend-api/codex/responses" {
		return nil, fmt.Errorf("Codex subscription credentials can only be sent to the Codex Responses endpoint")
	}
	data, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	body["stream"] = true
	body["store"] = false
	if _, ok := body["instructions"]; !ok {
		body["instructions"] = ""
	}
	// The subscription endpoint does not accept sampling or token-limit fields.
	for _, key := range []string{"max_output_tokens", "temperature", "top_p", "background"} {
		delete(body, key)
	}
	data, err = json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var rejected string
	for attempt := 0; attempt < 2; attempt++ {
		tokens, err := a.tokenAfterRejection(req.Context(), attempt > 0, rejected)
		if err != nil {
			return nil, err
		}
		r := req.Clone(req.Context())
		r.Body = io.NopCloser(bytes.NewReader(data))
		r.ContentLength = int64(len(data))
		r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
		r.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
		r.Header.Set("ChatGPT-Account-Id", tokens.AccountID)
		r.Header.Set("Accept", "text/event-stream")
		r.Header.Set("Originator", "codex_cli_rs")
		resp, err := next(r)
		if err != nil {
			return resp, err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			rejected = tokens.AccessToken
			resp.Body.Close()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return resp, nil
		}
		result, err := completedResponse(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		resp.Body = io.NopCloser(bytes.NewReader(result))
		resp.ContentLength = int64(len(result))
		resp.Header.Set("Content-Type", "application/json")
		resp.Header.Set("Content-Length", fmt.Sprint(len(result)))
		return resp, nil
	}
	return nil, fmt.Errorf("Codex authentication failed")
}

func completedResponse(r io.Reader) ([]byte, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 16<<20)
	var data []string
	items := map[int]json.RawMessage{}
	dispatch := func() ([]byte, error) {
		if len(data) == 0 {
			return nil, nil
		}
		payload := strings.Join(data, "\n")
		data = nil
		if payload == "[DONE]" {
			return nil, nil
		}
		var event struct {
			Type        string          `json:"type"`
			Response    json.RawMessage `json:"response"`
			Item        json.RawMessage `json:"item"`
			OutputIndex int             `json:"output_index"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return nil, fmt.Errorf("decode Codex stream: %w", err)
		}
		switch event.Type {
		case "response.output_item.done":
			if len(event.Item) > 0 {
				items[event.OutputIndex] = event.Item
			}
			return nil, nil
		case "response.incomplete":
			return nil, fmt.Errorf("Codex response is incomplete")
		case "response.completed", "response.failed":
			if len(event.Response) == 0 || string(event.Response) == "null" {
				return nil, fmt.Errorf("Codex stream has no terminal response")
			}
			// Codex sends output items separately and may leave terminal output empty.
			var response map[string]json.RawMessage
			if err := json.Unmarshal(event.Response, &response); err != nil {
				return nil, err
			}
			var output []json.RawMessage
			if err := json.Unmarshal(response["output"], &output); err != nil && len(response["output"]) > 0 {
				return nil, err
			}
			if len(output) == 0 && len(items) > 0 {
				indexes := make([]int, 0, len(items))
				for index := range items {
					indexes = append(indexes, index)
				}
				sort.Ints(indexes)
				for _, index := range indexes {
					output = append(output, items[index])
				}
				response["output"], _ = json.Marshal(output)
				return json.Marshal(response)
			}
			return event.Response, nil
		case "error":
			return nil, fmt.Errorf("Codex stream returned an error")
		}
		return nil, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			result, err := dispatch()
			if result != nil || err != nil {
				return result, err
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Codex stream: %w", err)
	}
	if result, err := dispatch(); result != nil || err != nil {
		return result, err
	}
	return nil, fmt.Errorf("Codex stream ended without a completed response")
}
