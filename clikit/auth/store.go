package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// TokenSet is the persisted result of an OIDC exchange.
type TokenSet struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	IDToken      string    `json:"id_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
}

// Valid reports whether the access token is non-empty and not past skew of its
// expiry. A zero Expiry is treated as "no known expiry" and therefore valid.
func (t TokenSet) Valid(skew time.Duration) bool {
	if t.AccessToken == "" {
		return false
	}
	if t.Expiry.IsZero() {
		return true
	}
	return time.Now().Add(skew).Before(t.Expiry)
}

// TokenStore persists a [TokenSet] between CLI invocations. It is intentionally
// keychain-agnostic: the default is a 0600 file store, and an OS-keychain
// binding can implement the same interface without touching callers.
type TokenStore interface {
	// Load returns the stored token set. A missing store returns
	// (zero TokenSet, nil) so first-run is not an error.
	Load() (TokenSet, error)
	// Save persists the token set.
	Save(TokenSet) error
	// Clear removes any stored token set. Clearing an empty store is a no-op.
	Clear() error
}

// FileStore persists a [TokenSet] as JSON at Path with 0600 permissions.
type FileStore struct {
	Path string
}

// Load reads and decodes the token set. A missing file is not an error.
func (f *FileStore) Load() (TokenSet, error) {
	var ts TokenSet
	b, err := os.ReadFile(f.Path)
	if os.IsNotExist(err) {
		return ts, nil
	}
	if err != nil {
		return ts, err
	}
	if len(b) == 0 {
		return ts, nil
	}
	return ts, json.Unmarshal(b, &ts)
}

// Save writes the token set atomically with 0600 permissions.
func (f *FileStore) Save(ts TokenSet) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

// Clear removes the token file. A missing file is not an error.
func (f *FileStore) Clear() error {
	err := os.Remove(f.Path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// MemoryStore is an in-process [TokenStore] for tests.
type MemoryStore struct{ ts TokenSet }

// Load returns the in-memory token set.
func (m *MemoryStore) Load() (TokenSet, error) { return m.ts, nil }

// Save stores the token set in memory.
func (m *MemoryStore) Save(ts TokenSet) error { m.ts = ts; return nil }

// Clear zeroes the in-memory token set.
func (m *MemoryStore) Clear() error { m.ts = TokenSet{}; return nil }
