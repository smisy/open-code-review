// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func setTestHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	return dir
}

// fakeDeviceFlow answers the device-code request and then replays polls in order.
func fakeDeviceFlow(t *testing.T, deviceCode string, polls ...string) {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("client_id") != ClientID {
			t.Errorf("bad form: %v %v", r.Form, err)
		}
		switch r.URL.Path {
		case "/login/device/code":
			_, _ = io.WriteString(w, deviceCode)
		case "/login/oauth/access_token":
			if n >= len(polls) {
				t.Fatalf("unexpected poll %d", n)
			}
			if polls[n] == "500" {
				n++
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			_, _ = io.WriteString(w, polls[n])
			n++
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := githubURL
	githubURL = srv.URL
	t.Cleanup(func() { githubURL = old })
}

func noSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	old := sleep
	sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}
	t.Cleanup(func() { sleep = old })
	return &waits
}

const validDeviceCode = `{"device_code":"dc","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`

func TestDeviceFlowSuccess(t *testing.T) {
	waits := noSleep(t)
	fakeDeviceFlow(t, validDeviceCode,
		`{"error":"authorization_pending"}`,
		`{"error":"slow_down","interval":12}`,
		`{"access_token":"gho_new"}`,
	)
	dc, err := RequestDeviceCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dc.UserCode != "ABCD-1234" {
		t.Fatalf("dc = %+v", dc)
	}
	token, err := PollAccessToken(context.Background(), dc)
	if err != nil || token != "gho_new" {
		t.Fatalf("token=%q err=%v", token, err)
	}
	want := []time.Duration{5 * time.Second, 5 * time.Second, 12 * time.Second}
	if len(*waits) != 3 || (*waits)[2] != want[2] {
		t.Fatalf("waits = %v, want %v", *waits, want)
	}
}

func TestPollAccessTokenFailures(t *testing.T) {
	tests := []struct {
		name string
		poll string
		want string
	}{
		{"expired", `{"error":"expired_token"}`, "expired"},
		{"denied", `{"error":"access_denied"}`, "cancelled"},
		{"unknown", `{"error":"unsupported_grant_type"}`, "unsupported_grant_type"},
		{"http error", "500", "HTTP 500"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			noSleep(t)
			fakeDeviceFlow(t, validDeviceCode, tt.poll)
			_, err := PollAccessToken(context.Background(), DeviceCode{DeviceCode: "dc", ExpiresIn: 60, Interval: 1})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestPollAccessTokenDeadlineAndCancel(t *testing.T) {
	noSleep(t)
	start := time.Unix(1_800_000_000, 0)
	clock := setNow(t, start)
	old := sleep
	sleep = func(ctx context.Context, d time.Duration) error {
		*clock = clock.Add(d)
		return nil
	}
	t.Cleanup(func() { sleep = old })
	fakeDeviceFlow(t, validDeviceCode, `{"error":"authorization_pending"}`, `{"error":"authorization_pending"}`)
	_, err := PollAccessToken(context.Background(), DeviceCode{DeviceCode: "dc", ExpiresIn: 10, Interval: 5})
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("err = %v", err)
	}

	sleep = old
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PollAccessToken(ctx, DeviceCode{DeviceCode: "dc", ExpiresIn: 10, Interval: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestSleepWaits(t *testing.T) {
	if err := sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestRequestDeviceCodeErrors(t *testing.T) {
	fakeDeviceFlow(t, `{"device_code":"dc"}`)
	if _, err := RequestDeviceCode(context.Background()); err == nil || !strings.Contains(err.Error(), "missing fields") {
		t.Fatalf("err = %v", err)
	}
	githubURL = "http://127.0.0.1:1"
	if _, err := RequestDeviceCode(context.Background()); err == nil {
		t.Fatal("want transport error")
	}
	githubURL = "://bad"
	if _, err := RequestDeviceCode(context.Background()); err == nil {
		t.Fatal("want URL error")
	}
}

func TestCredentialStorage(t *testing.T) {
	home := setTestHome(t)

	if tok, err := LoadGitHubToken(); err != nil || tok != "" {
		t.Fatalf("empty store: tok=%q err=%v", tok, err)
	}
	if removed, err := DeleteGitHubToken(); err != nil || removed {
		t.Fatalf("delete missing: removed=%v err=%v", removed, err)
	}

	if err := SaveGitHubToken("gho_saved"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".opencodereview", "github-copilot.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
	var stored storedCredentials
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &stored); err != nil || stored.GitHubToken != "gho_saved" {
		t.Fatalf("stored = %s", data)
	}
	if tok, err := LoadGitHubToken(); err != nil || tok != "gho_saved" {
		t.Fatalf("tok=%q err=%v", tok, err)
	}

	if removed, err := DeleteGitHubToken(); err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
}

func TestLoadGitHubTokenErrors(t *testing.T) {
	home := setTestHome(t)
	dir := filepath.Join(home, ".opencodereview")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "github-copilot.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGitHubToken(); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("err = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGitHubToken(); err == nil {
		t.Fatal("want read error for a directory")
	}
}

func TestStorageWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("home resolution differs")
	}
	if _, err := CredentialsPath(); err == nil {
		t.Fatal("want error without HOME")
	}
	if err := SaveGitHubToken("x"); err == nil {
		t.Error("save: want error")
	}
	if _, err := LoadGitHubToken(); err == nil {
		t.Error("load: want error")
	}
	if _, err := DeleteGitHubToken(); err == nil {
		t.Error("delete: want error")
	}
}

func TestSaveGitHubTokenUnwritableDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not apply")
	}
	home := setTestHome(t)
	if err := os.WriteFile(filepath.Join(home, ".opencodereview"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveGitHubToken("x"); err == nil {
		t.Fatal("want error when the config dir is a file")
	}
}
