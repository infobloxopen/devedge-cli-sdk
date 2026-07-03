package oidc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/infobloxopen/devedge-cli-sdk/clikit/auth"
)

// mockIdP is an httptest handler implementing the RFC 8628 device flow plus a
// refresh-token grant, with configurable "pending" polls before success.
type mockIdP struct {
	mu             sync.Mutex
	pendingPolls   int // remaining authorization_pending responses before success
	deviceRequests int
	tokenRequests  int
	refreshGrants  int
	issueURL       string // base URL, filled after the server starts
}

func (m *mockIdP) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                        m.issueURL,
			"token_endpoint":                m.issueURL + "/token",
			"device_authorization_endpoint": m.issueURL + "/device",
			"authorization_endpoint":        m.issueURL + "/authorize",
		})
	})
	mux.HandleFunc("/device", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		m.deviceRequests++
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":      "dev-code-123",
			"user_code":        "WXYZ-1234",
			"verification_uri": m.issueURL + "/activate",
			"expires_in":       600,
			"interval":         1,
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		grant := r.Form.Get("grant_type")
		m.mu.Lock()
		defer m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")

		if grant == "refresh_token" {
			m.refreshGrants++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "refreshed-access-token",
				"refresh_token": "refresh-token-2",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
			return
		}

		// device_code grant
		m.tokenRequests++
		if m.pendingPolls > 0 {
			m.pendingPolls--
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "device-access-token",
			"refresh_token": "refresh-token-1",
			"id_token":      testIDToken(),
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	})
	return mux
}

// testIDToken is a header.payload.signature JWT whose payload carries sub+email.
func testIDToken() string {
	payload := base64URL(`{"sub":"user-42","email":"u@example.com"}`)
	return "eyJhbGciOiJub25lIn0." + payload + ".sig"
}

func base64URL(s string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	src := []byte(s)
	var out strings.Builder
	for i := 0; i < len(src); i += 3 {
		var b [3]byte
		n := copy(b[:], src[i:])
		out.WriteByte(alphabet[b[0]>>2])
		out.WriteByte(alphabet[(b[0]&0x03)<<4|b[1]>>4])
		if n > 1 {
			out.WriteByte(alphabet[(b[1]&0x0f)<<2|b[2]>>6])
		}
		if n > 2 {
			out.WriteByte(alphabet[b[2]&0x3f])
		}
	}
	return out.String()
}

func TestDeviceGrantPendingThenSuccessThenRefresh(t *testing.T) {
	idp := &mockIdP{pendingPolls: 1}
	srv := httptest.NewServer(idp.handler())
	defer srv.Close()
	idp.issueURL = srv.URL

	clock := time.Now()
	var prompt bytes.Buffer
	p := New(Config{
		Issuer:   srv.URL, // exercise .well-known discovery
		ClientID: "test-client",
		Store:    &auth.MemoryStore{},
		Prompt:   &prompt,
		Now:      func() time.Time { return clock },
	})

	// First Token(): no cache → device flow → one pending poll → success.
	tok, err := p.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "device-access-token" {
		t.Fatalf("got token %q", tok)
	}
	if !strings.Contains(prompt.String(), "WXYZ-1234") {
		t.Fatalf("prompt did not show user_code:\n%s", prompt.String())
	}
	if idp.tokenRequests < 2 {
		t.Fatalf("expected >=2 token polls (pending then success), got %d", idp.tokenRequests)
	}

	// Claims decode from the id_token.
	claims, err := p.Claims(context.Background())
	if err != nil {
		t.Fatalf("Claims: %v", err)
	}
	if claims["sub"] != "user-42" {
		t.Fatalf("claims sub = %v", claims["sub"])
	}

	// Cached + fresh → no new network call.
	before := idp.tokenRequests
	if _, err := p.Token(context.Background()); err != nil {
		t.Fatalf("cached Token: %v", err)
	}
	if idp.tokenRequests != before {
		t.Fatalf("fresh token should not re-poll; polls %d -> %d", before, idp.tokenRequests)
	}

	// Advance past expiry → silent refresh-token grant (no new device flow).
	clock = clock.Add(2 * time.Hour)
	devBefore := idp.deviceRequests
	tok, err = p.Token(context.Background())
	if err != nil {
		t.Fatalf("refresh Token: %v", err)
	}
	if tok != "refreshed-access-token" {
		t.Fatalf("expected refreshed token, got %q", tok)
	}
	if idp.refreshGrants != 1 {
		t.Fatalf("expected 1 refresh grant, got %d", idp.refreshGrants)
	}
	if idp.deviceRequests != devBefore {
		t.Fatalf("refresh must not trigger a new device flow")
	}
}

func TestLogoutClearsStore(t *testing.T) {
	store := &auth.MemoryStore{}
	_ = store.Save(auth.TokenSet{AccessToken: "x", Expiry: time.Now().Add(time.Hour)})
	p := New(Config{TokenURL: "http://x/token", DeviceAuthURL: "http://x/device", ClientID: "c", Store: store})
	if err := p.Logout(context.Background()); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	ts, _ := store.Load()
	if ts.AccessToken != "" {
		t.Fatalf("store not cleared: %+v", ts)
	}
}
