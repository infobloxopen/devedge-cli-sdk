// Package auth is the CLI session seam: the Go analog of the devedge-ufe-sdk
// SessionProvider.
//
// The seam is public; any product-specific implementation binds on top
// privately. This mirrors the governance principle of devedge-sdk, where the
// authorization seam is the public authz.Authorizer interface and a concrete
// decision point (e.g. an OPA-backed authorizer) binds to it from a separate
// private package. Here the seam is [Session]: a generic OIDC device-grant
// binding is public (see clikit/auth/oidc) because OIDC is a standard, while a
// provider-specific binding (Okta, PDS, ...) that merely supplies issuer and
// client details is a separate private package. No identity provider is named
// or hardwired here.
package auth

import (
	"context"
	"log"
)

// Session is the read-only session view a CLI uses to authenticate requests.
//
// A command reads a bearer [Session.Token], optionally reads [Session.Claims],
// subscribes to lifecycle events, and requests [Session.Login]/[Session.Logout]
// — but it never constructs a session or reaches the identity provider itself.
// That is the CLI mirror of the devedge-ufe-sdk rule "the shell owns the
// session; child surfaces consume the read-only view".
type Session interface {
	// Token returns a currently-valid bearer token, refreshing if needed.
	Token(ctx context.Context) (string, error)
	// Claims returns the decoded identity claims, or nil when unavailable.
	// Optional: implementations without an ID token may return (nil, nil).
	Claims(ctx context.Context) (map[string]any, error)
	// Login performs an interactive login (e.g. the device-authorization flow).
	Login(ctx context.Context) error
	// Logout clears any cached tokens for this session.
	Logout(ctx context.Context) error
	// Subscribe registers fn for session events and returns an unsubscribe func.
	Subscribe(fn func(Event)) func()
}

// EventType enumerates the session lifecycle transitions.
type EventType string

const (
	// EventTokenAcquired fires when a fresh token becomes available.
	EventTokenAcquired EventType = "token_acquired"
	// EventTokenExpired fires when the cached token has expired.
	EventTokenExpired EventType = "token_expired"
	// EventSignedOut fires on logout.
	EventSignedOut EventType = "signed_out"
)

// Event is broadcast as a session's token lifecycle advances. It mirrors the
// devedge-ufe-sdk SessionEvent union.
type Event struct {
	Type      EventType
	Token     string
	ExpiresAt int64 // unix seconds; 0 when unknown
}

// Bus is a minimal publish/subscribe bus for session [Event]s. It is safe for
// providers to embed to satisfy the Subscribe half of [Session].
type Bus struct {
	subs map[int]func(Event)
	next int
}

// Publish delivers e to every current subscriber.
func (b *Bus) Publish(e Event) {
	for _, fn := range b.subs {
		fn(e)
	}
}

// Subscribe registers fn and returns an unsubscribe func.
func (b *Bus) Subscribe(fn func(Event)) func() {
	if b.subs == nil {
		b.subs = map[int]func(Event){}
	}
	id := b.next
	b.next++
	b.subs[id] = fn
	return func() { delete(b.subs, id) }
}

// StubSession is a no-real-auth session for local development and tests. It
// returns a fixed token and no-ops on login/logout. It is the Go analog of the
// devedge-ufe-sdk StubSessionProvider.
//
// DEVELOPMENT ONLY. This performs NO authentication and must not be used in
// production. Constructing it logs a warning so an accidental production build
// surfaces the misuse loudly (it stays functional and does not panic).
type StubSession struct {
	token  string
	claims map[string]any
	bus    Bus
}

// NewStubSession returns a [StubSession] emitting the given token (or a default
// when empty).
func NewStubSession(token string) *StubSession {
	log.Println("[clikit] StubSession is for development only and must not be used in production")
	if token == "" {
		token = "dev-stub-token"
	}
	return &StubSession{token: token, claims: map[string]any{"sub": "dev-user"}}
}

// Token returns the fixed development token.
func (s *StubSession) Token(context.Context) (string, error) { return s.token, nil }

// Claims returns the fixed development claims.
func (s *StubSession) Claims(context.Context) (map[string]any, error) { return s.claims, nil }

// Login publishes a token_acquired event; it performs no real authentication.
func (s *StubSession) Login(context.Context) error {
	s.bus.Publish(Event{Type: EventTokenAcquired, Token: s.token})
	return nil
}

// Logout publishes a signed_out event.
func (s *StubSession) Logout(context.Context) error {
	s.bus.Publish(Event{Type: EventSignedOut})
	return nil
}

// Subscribe registers fn for session events.
func (s *StubSession) Subscribe(fn func(Event)) func() { return s.bus.Subscribe(fn) }
