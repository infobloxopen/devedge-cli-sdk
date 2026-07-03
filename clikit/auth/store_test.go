package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStoreRoundTripAnd0600(t *testing.T) {
	dir := t.TempDir()
	fs := &FileStore{Path: filepath.Join(dir, "sub", "token.json")}

	// Missing file is not an error.
	if ts, err := fs.Load(); err != nil || ts.AccessToken != "" {
		t.Fatalf("empty load = %+v, %v", ts, err)
	}

	want := TokenSet{AccessToken: "a", RefreshToken: "r", Expiry: time.Now().Add(time.Hour)}
	if err := fs.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(fs.Path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file perm = %o, want 600", fi.Mode().Perm())
	}
	got, err := fs.Load()
	if err != nil || got.AccessToken != "a" || got.RefreshToken != "r" {
		t.Fatalf("Load = %+v, %v", got, err)
	}

	if err := fs.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := os.Stat(fs.Path); !os.IsNotExist(err) {
		t.Fatalf("file should be gone: %v", err)
	}
	// Clearing an absent file is a no-op.
	if err := fs.Clear(); err != nil {
		t.Fatalf("Clear absent: %v", err)
	}
}

func TestTokenSetValid(t *testing.T) {
	if (TokenSet{}).Valid(0) {
		t.Fatal("empty token should be invalid")
	}
	if !(TokenSet{AccessToken: "x"}).Valid(time.Minute) {
		t.Fatal("no-expiry token should be valid")
	}
	if (TokenSet{AccessToken: "x", Expiry: time.Now().Add(10 * time.Second)}).Valid(time.Minute) {
		t.Fatal("token within skew of expiry should be invalid")
	}
	if !(TokenSet{AccessToken: "x", Expiry: time.Now().Add(time.Hour)}).Valid(time.Minute) {
		t.Fatal("token far from expiry should be valid")
	}
}
