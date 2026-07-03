package clikit

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/infobloxopen/devedge-cli-sdk/clikit/auth"
)

// countingSession is a fake auth.Session that counts Login calls and hands back
// a fixed token.
type countingSession struct {
	token  string
	logins int
}

func (s *countingSession) Token(context.Context) (string, error)          { return s.token, nil }
func (s *countingSession) Claims(context.Context) (map[string]any, error) { return nil, nil }
func (s *countingSession) Login(context.Context) error                    { s.logins++; return nil }
func (s *countingSession) Logout(context.Context) error                   { return nil }
func (s *countingSession) Subscribe(func(auth.Event)) func()              { return func() {} }

func TestAuthTransport401RetriesOnce(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing bearer on call %d", calls)
		}
		if calls == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	sess := &countingSession{token: "tok"}
	client := NewAuthClient(sess)
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls (401 then retry), got %d", calls)
	}
	if sess.logins != 1 {
		t.Fatalf("expected 1 Login on 401, got %d", sess.logins)
	}
}

func TestPrinterFormats(t *testing.T) {
	type row struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	rows := []row{{"a", "Alpha"}, {"b", "Beta"}}

	var buf bytes.Buffer
	(&Printer{Out: &buf, Format: FormatTable}).Print(rows)
	if !strings.Contains(buf.String(), "ID") || !strings.Contains(buf.String(), "Alpha") {
		t.Fatalf("table missing content:\n%s", buf.String())
	}

	buf.Reset()
	(&Printer{Out: &buf, Format: FormatJSON}).Print(rows)
	var back []row
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil || len(back) != 2 {
		t.Fatalf("json roundtrip failed: %v\n%s", err, buf.String())
	}

	buf.Reset()
	(&Printer{Out: &buf, Format: FormatYAML}).Print(rows[0])
	if !strings.Contains(buf.String(), "Alpha") {
		t.Fatalf("yaml missing content:\n%s", buf.String())
	}
}

func TestParseResourceName(t *testing.T) {
	if got := ResourceName("widgets", "abc"); got != "widgets/abc" {
		t.Fatalf("ResourceName = %q", got)
	}
	coll, id, err := ParseResourceName("widgets/abc")
	if err != nil || coll != "widgets" || id != "abc" {
		t.Fatalf("ParseResourceName = %q,%q,%v", coll, id, err)
	}
	if got := ResourceID("abc"); got != "abc" {
		t.Fatalf("ResourceID bare = %q", got)
	}
	if got := ResourceID("widgets/xyz"); got != "xyz" {
		t.Fatalf("ResourceID name = %q", got)
	}
}

func TestConfigLoadAndResolve(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`current_profile: dev
profiles:
  dev:
    server: https://dev.example.com
    auth:
      type: oidc
      oidc:
        issuer: https://issuer.example.com
        client_id: cli
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig("ib", path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	p, err := cfg.ResolveProfile("ib", "", "")
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if p.Name != "dev" || p.Server != "https://dev.example.com" || p.Auth.OIDC.ClientID != "cli" {
		t.Fatalf("resolved profile = %+v", p)
	}

	// flag override beats config
	p, _ = cfg.ResolveProfile("ib", "", "https://override.example.com")
	if p.Server != "https://override.example.com" {
		t.Fatalf("server override failed: %s", p.Server)
	}

	// env override beats config, below flag
	t.Setenv("IB_SERVER", "https://env.example.com")
	p, _ = cfg.ResolveProfile("ib", "", "")
	if p.Server != "https://env.example.com" {
		t.Fatalf("env override failed: %s", p.Server)
	}
}

func TestLookupPluginLongestPrefix(t *testing.T) {
	dir := t.TempDir()
	writeExec(t, filepath.Join(dir, "ib-foo"))
	writeExec(t, filepath.Join(dir, "ib-foo-bar"))
	t.Setenv("PATH", dir)

	path, args, ok := LookupPlugin("ib", []string{"foo", "bar", "baz"})
	if !ok || filepath.Base(path) != "ib-foo-bar" {
		t.Fatalf("longest-prefix match failed: %q %v %v", path, args, ok)
	}
	if len(args) != 1 || args[0] != "baz" {
		t.Fatalf("remaining args = %v", args)
	}

	path, _, ok = LookupPlugin("ib", []string{"foo"})
	if !ok || filepath.Base(path) != "ib-foo" {
		t.Fatalf("single match failed: %q %v", path, ok)
	}

	if _, _, ok := LookupPlugin("ib", []string{"nope"}); ok {
		t.Fatalf("unexpected match for nope")
	}
}

func TestPollOperation(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		done := calls >= 2
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "operations/1", "done": done})
	}))
	defer srv.Close()

	op, err := PollOperation(context.Background(), http.DefaultClient, srv.URL, WithInterval(time.Millisecond))
	if err != nil {
		t.Fatalf("PollOperation: %v", err)
	}
	if !op.Done || op.Name != "operations/1" {
		t.Fatalf("op = %+v", op)
	}
	if calls < 2 {
		t.Fatalf("expected >=2 polls, got %d", calls)
	}
}

func writeExec(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}
