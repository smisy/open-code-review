// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package codex

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMiddleware(t *testing.T) {
	path := credentialFile(t, "access")
	renew := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"access_token":"renewed"}`) }))
	defer renew.Close()
	a := &Auth{Path: path, RefreshURL: renew.URL}
	req, _ := http.NewRequest(http.MethodPost, BaseURL+"/responses", strings.NewReader(`{"model":"model","max_output_tokens":10,"temperature":1,"input":[{"type":"function_call_output","call_id":"call","output":"result"}]}`))
	attempts := 0
	resp, err := a.Middleware(req, func(r *http.Request) (*http.Response, error) {
		attempts++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["stream"] != true || body["store"] != false || body["instructions"] != "" || body["max_output_tokens"] != nil || body["temperature"] != nil || body["input"] == nil {
			t.Fatalf("body=%v", body)
		}
		if r.Header.Get("ChatGPT-Account-Id") != "account" {
			t.Fatalf("headers=%v", r.Header)
		}
		expected := "access"
		if attempts > 1 {
			expected = "renewed"
		}
		if r.Header.Get("Authorization") != "Bearer "+expected {
			t.Fatal("incorrect bearer token")
		}
		replay, err := r.GetBody()
		if err != nil {
			t.Fatal(err)
		}
		replay.Close()
		if attempts == 1 {
			return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"call_id\":\"call\"}]}}\n\n"))}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if attempts != 2 || resp.Header.Get("Content-Type") != "application/json" || !strings.Contains(string(data), "function_call") {
		t.Fatalf("attempts=%d response=%s", attempts, data)
	}
}

func TestMiddlewareReasoningEffort(t *testing.T) {
	for _, tc := range []struct{ effort, body, want string }{
		{"xhigh", `{}`, "xhigh"},
		{"none", `{"reasoning":{"effort":"high","summary":"auto"}}`, "none"},
		{"", `{"reasoning":{"effort":"low","summary":"auto"}}`, "low"},
		{"", `{}`, ""},
	} {
		a := &Auth{Path: credentialFile(t, "access"), ReasoningEffort: tc.effort}
		req, _ := http.NewRequest(http.MethodPost, BaseURL+"/responses", strings.NewReader(tc.body))
		resp, err := a.Middleware(req, func(r *http.Request) (*http.Response, error) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			reasoning, _ := body["reasoning"].(map[string]any)
			if tc.want == "" {
				if body["reasoning"] != nil {
					t.Errorf("default introduced reasoning: %v", reasoning)
				}
			} else if reasoning["effort"] != tc.want {
				t.Errorf("effort=%v want=%q", reasoning["effort"], tc.want)
			}
			if strings.Contains(tc.body, "summary") && reasoning["summary"] != "auto" {
				t.Error("lost reasoning summary")
			}
			return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(""))}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	a := &Auth{Path: "missing", ReasoningEffort: "invalid"}
	req, _ := http.NewRequest(http.MethodPost, BaseURL+"/responses", strings.NewReader(`{}`))
	if _, err := a.Middleware(req, func(*http.Request) (*http.Response, error) {
		t.Fatal("sent invalid effort")
		return nil, nil
	}); err == nil || !strings.Contains(err.Error(), "invalid reasoning effort") {
		t.Fatalf("invalid effort reached credentials: %v", err)
	}
}

func TestStream(t *testing.T) {
	for _, tt := range []struct {
		input string
		ok    bool
	}{
		{"event: response.completed\r\ndata: {\"type\":\"response.completed\",\ndata: \"response\":{\"status\":\"completed\"}}\n\n", true},
		{"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}", true},
		{"data: {\"type\":\"response.created\"}\n\ndata: [DONE]\n\n", false},
		{"data: {\"type\":\"response.completed\"}\n\n", false},
		{"data: {\"type\":\"error\"}\n\n", false},
		{"data: broken\n\n", false},
		{"", false},
	} {
		_, err := completedResponse(strings.NewReader(tt.input))
		if (err == nil) != tt.ok {
			t.Errorf("stream %q: %v", tt.input, err)
		}
	}
}

func TestMiddlewareRejectsOtherHosts(t *testing.T) {
	a := &Auth{}
	for _, endpoint := range []string{"https://evil.example/responses", "http://chatgpt.com/backend-api/codex/responses", BaseURL + "/models"} {
		req, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{}`))
		_, err := a.Middleware(req, func(*http.Request) (*http.Response, error) {
			t.Fatal("sent credential to invalid endpoint")
			return nil, nil
		})
		if err == nil {
			t.Fatal("accepted invalid endpoint")
		}
	}
}

func TestMiddlewareProviderErrors(t *testing.T) {
	for _, tt := range []struct {
		status  int
		body    string
		wantErr bool
	}{
		{429, `{"error":"rate limited"}`, false},
		{200, "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\"}}\n\n", true},
		{200, "data: {\"type\":\"error\"}\n\n", true},
		{200, "", true},
	} {
		t.Run(fmt.Sprint(tt.status, tt.body), func(t *testing.T) {
			a := &Auth{Path: credentialFile(t, "access")}
			req, _ := http.NewRequest(http.MethodPost, BaseURL+"/responses", strings.NewReader(`{}`))
			resp, err := a.Middleware(req, func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tt.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tt.body))}, nil
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("response=%v err=%v", resp, err)
			}
			if resp != nil {
				resp.Body.Close()
			}
		})
	}
}

func TestStreamCollectsOutputItems(t *testing.T) {
	stream := "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"name\":\"verify_connection\",\"call_id\":\"call_1\",\"arguments\":\"{}\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"
	result, err := completedResponse(strings.NewReader(stream))
	if err != nil || !strings.Contains(string(result), "verify_connection") {
		t.Fatalf("output items lost: %s, %v", result, err)
	}
}
