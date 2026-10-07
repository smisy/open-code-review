// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func credentialFile(t *testing.T, access string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := save(path, Credentials{AuthMode: "chatgpt", Tokens: Tokens{AccessToken: access, RefreshToken: "refresh", AccountID: "account"}}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	path := credentialFile(t, "access")
	c, err := Load(path)
	if err != nil || c.Tokens.AccountID != "account" {
		t.Fatalf("load: %+v, %v", c, err)
	}
	for _, data := range []string{`{`, `{}`, `{"auth_mode":"apikey","tokens":{"access_token":"key","account_id":"account"}}`, `{"auth_mode":"chatgpt","tokens":{"access_token":"access"}}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
	if _, err := Load(path + "missing"); err == nil {
		t.Fatal("accepted missing login")
	}
}

func TestExpiry(t *testing.T) {
	for _, tt := range []struct {
		token string
		want  bool
	}{
		{"opaque", false}, {"a.invalid.c", false}, {"a." + base64.RawURLEncoding.EncodeToString([]byte(`{}`)) + ".c", false},
		{"a." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(-time.Minute).Unix()))) + ".c", true},
		{"a." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(time.Hour).Unix()))) + ".c", false},
	} {
		if got := expiring(tt.token); got != tt.want {
			t.Errorf("expiring=%t want %t", got, tt.want)
		}
	}
}

func TestTokenRefreshAndPersistence(t *testing.T) {
	path := credentialFile(t, "access")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["refresh_token"] != "refresh" || body["grant_type"] != "refresh_token" || body["client_id"] == "" {
			t.Errorf("body=%v", body)
		}
		fmt.Fprint(w, `{"access_token":"renewed","refresh_token":"rotated","id_token":"id"}`)
	}))
	defer srv.Close()
	a := &Auth{Path: path, RefreshURL: srv.URL}
	tokens, err := a.token(context.Background(), true)
	if err != nil || tokens.AccessToken != "renewed" {
		t.Fatalf("refresh=%+v %v", tokens, err)
	}
	c, err := Load(path)
	if err != nil || c.Tokens.RefreshToken != "rotated" || c.Tokens.AccountID != "account" || c.LastRefresh.IsZero() {
		t.Fatalf("saved=%+v %v", c, err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Errorf("permissions=%v", info.Mode())
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := a.token(context.Background(), false)
			if err != nil || tok.AccessToken != "renewed" {
				t.Errorf("reload=%v %v", tok, err)
			}
		}()
	}
	wg.Wait()
}

func TestTokenFailures(t *testing.T) {
	for _, tt := range []struct {
		status int
		body   string
	}{{401, `secret`}, {200, `{`}, {200, `{}`}} {
		t.Run(fmt.Sprint(tt.status, tt.body), func(t *testing.T) {
			path := credentialFile(t, "access")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tt.status); fmt.Fprint(w, tt.body) }))
			defer srv.Close()
			_, err := (&Auth{Path: path, RefreshURL: srv.URL}).token(context.Background(), true)
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("error=%v", err)
			}
		})
	}
	path := credentialFile(t, "access")
	if err := os.Mkdir(path+".lock", 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Auth{Path: path}).token(ctx, false); err == nil {
		t.Fatal("ignored cancellation")
	}
	if err := os.Remove(path + ".lock"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Auth{Path: path}).token(context.Background(), false); err == nil {
		t.Fatal("accepted missing login")
	}
}

func TestConcurrentUnauthorizedUsesRenewedToken(t *testing.T) {
	path := credentialFile(t, "renewed")
	a := &Auth{Path: path, RefreshURL: "invalid"}
	tokens, err := a.tokenAfterRejection(context.Background(), true, "rejected")
	if err != nil || tokens.AccessToken != "renewed" {
		t.Fatalf("token=%+v err=%v", tokens, err)
	}
}
