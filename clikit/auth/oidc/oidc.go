// Package oidc is a generic OIDC binding for the clikit/auth [auth.Session]
// seam, using the RFC 8628 Device Authorization Grant.
//
// It is provider-agnostic: the issuer is generic (Dex in dev, any OIDC provider
// in prod) and no identity provider is named or hardwired here. A
// provider-specific binding (Okta, PDS, Auth0, ...) that merely supplies the
// issuer/client/audience is a separate private package, the same way a private
// authorizer binds to authz.Authorizer in devedge-sdk. This mirrors
// devedge-ufe-sdk's OidcSessionProvider: freshness with a refresh skew,
// single-in-flight refresh, and an interactive fallback.
package oidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/infobloxopen/devedge-cli-sdk/clikit/auth"
)

// refreshSkew is how near expiry (mirroring the ufe REFRESH_SKEW_SECONDS=30) a
// token is treated as stale so it is refreshed before it actually expires.
const refreshSkew = 30 * time.Second

// Config is a generic OIDC configuration. It carries no provider-specific
// fields. When only Issuer is set, the token and device-authorization endpoints
// are discovered via the issuer's .well-known/openid-configuration document.
type Config struct {
	Issuer        string   // OIDC issuer; enables discovery when endpoints are unset
	AuthorizeURL  string   // optional explicit authorization endpoint
	TokenURL      string   // token endpoint (discovered from Issuer if empty)
	DeviceAuthURL string   // device-authorization endpoint (discovered if empty)
	ClientID      string   // OAuth client id
	Scopes        []string // requested scopes; defaults applied when empty
	Audience      string   // optional audience passed to the token endpoint

	// HTTPClient is used for all IdP calls; defaults to http.DefaultClient.
	HTTPClient *http.Client
	// Store persists tokens between invocations; defaults to an in-memory store.
	Store auth.TokenStore
	// Prompt is where the user_code + verification_uri are written during an
	// interactive device flow; defaults to os.Stderr.
	Prompt io.Writer
	// Now is the clock, injectable for tests; defaults to time.Now.
	Now func() time.Time
}

var defaultScopes = []string{"openid", "profile", "email", "offline_access"}

// Provider is an [auth.Session] backed by the OIDC device-authorization grant.
type Provider struct {
	cfg Config
	bus auth.Bus

	mu         sync.Mutex
	refreshing chan struct{} // non-nil while one refresh/login is in flight

	discovered    bool
	tokenURL      string
	deviceAuthURL string
}

// New returns a device-grant [Provider]. It applies defaults but performs no
// network calls until a token is first needed.
func New(cfg Config) *Provider {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.Store == nil {
		cfg.Store = &auth.MemoryStore{}
	}
	if cfg.Prompt == nil {
		cfg.Prompt = os.Stderr
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = defaultScopes
	}
	return &Provider{cfg: cfg, tokenURL: cfg.TokenURL, deviceAuthURL: cfg.DeviceAuthURL}
}

// Token returns a currently-valid access token, refreshing when near or past
// expiry: a refresh-token grant first, falling back to an interactive device
// login. Concurrent callers share ONE refresh, mirroring the ufe getToken.
func (p *Provider) Token(ctx context.Context) (string, error) {
	ts, err := p.cfg.Store.Load()
	if err != nil {
		return "", err
	}
	if p.fresh(ts) {
		return ts.AccessToken, nil
	}
	if err := p.ensure(ctx, ts.RefreshToken); err != nil {
		return "", err
	}
	ts, err = p.cfg.Store.Load()
	if err != nil {
		return "", err
	}
	if ts.AccessToken == "" {
		return "", errors.New("oidc: no access token after refresh")
	}
	return ts.AccessToken, nil
}

// Login forces an interactive device-authorization login, ignoring any cached
// refresh token.
func (p *Provider) Login(ctx context.Context) error { return p.runOnce(ctx, "") }

// Logout clears the cached token set and publishes a signed_out event.
func (p *Provider) Logout(context.Context) error {
	err := p.cfg.Store.Clear()
	p.bus.Publish(auth.Event{Type: auth.EventSignedOut})
	return err
}

// Claims decodes the identity claims from the cached ID token, or returns
// (nil, nil) when none is available.
func (p *Provider) Claims(context.Context) (map[string]any, error) {
	ts, err := p.cfg.Store.Load()
	if err != nil || ts.IDToken == "" {
		return nil, err
	}
	return decodeJWTClaims(ts.IDToken), nil
}

// Subscribe registers fn for session events.
func (p *Provider) Subscribe(fn func(auth.Event)) func() { return p.bus.Subscribe(fn) }

// fresh reports whether ts is usable, using the injectable clock so tests can
// advance time to force a refresh.
func (p *Provider) fresh(ts auth.TokenSet) bool {
	if ts.AccessToken == "" {
		return false
	}
	if ts.Expiry.IsZero() {
		return true
	}
	return p.cfg.Now().Add(refreshSkew).Before(ts.Expiry)
}

// ensure runs one refresh/login shared across concurrent callers. The first
// caller starts it; the rest await the same completion, then re-read the store.
func (p *Provider) ensure(ctx context.Context, refreshToken string) error {
	p.mu.Lock()
	if ch := p.refreshing; ch != nil {
		p.mu.Unlock()
		select {
		case <-ch:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ch := make(chan struct{})
	p.refreshing = ch
	p.mu.Unlock()

	err := p.refreshOrLogin(ctx, refreshToken)

	p.mu.Lock()
	p.refreshing = nil
	close(ch)
	p.mu.Unlock()
	return err
}

// refreshOrLogin tries a silent refresh-token grant first, then falls back to
// the interactive device flow — the device-grant analog of ufe's
// "signinSilent then signinRedirect".
func (p *Provider) refreshOrLogin(ctx context.Context, refreshToken string) error {
	if refreshToken != "" {
		if err := p.refresh(ctx, refreshToken); err == nil {
			return nil
		}
	}
	return p.runOnce(ctx, refreshToken)
}

func (p *Provider) refresh(ctx context.Context, refreshToken string) error {
	if err := p.discover(ctx); err != nil {
		return err
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {p.cfg.ClientID},
	}
	tr, err := p.postToken(ctx, form)
	if err != nil {
		return err
	}
	return p.save(tr, refreshToken)
}

// deviceAuthResponse is the RFC 8628 device-authorization response.
type deviceAuthResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// runOnce performs the full RFC 8628 device-authorization grant: request a
// device code, prompt the user, then poll the token endpoint honoring
// slow_down / authorization_pending / interval until success or expiry.
func (p *Provider) runOnce(ctx context.Context, prevRefresh string) error {
	if err := p.discover(ctx); err != nil {
		return err
	}
	da, err := p.requestDeviceCode(ctx)
	if err != nil {
		return err
	}
	uri := da.VerificationURI
	if da.VerificationURIComplete != "" {
		uri = da.VerificationURIComplete
	}
	fmt.Fprintf(p.cfg.Prompt, "\nTo sign in, visit %s and enter code: %s\n\n", uri, da.UserCode)

	interval := time.Duration(da.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := p.cfg.Now().Add(time.Duration(max(da.ExpiresIn, 300)) * time.Second)
	for {
		if p.cfg.Now().After(deadline) {
			return errors.New("oidc: device authorization expired before approval")
		}
		form := url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {da.DeviceCode},
			"client_id":   {p.cfg.ClientID},
		}
		tr, err := p.postToken(ctx, form)
		if err == nil {
			return p.save(tr, prevRefresh)
		}
		// Honor the RFC 8628 polling errors; anything else is fatal.
		var oe *oauthError
		if !errors.As(err, &oe) {
			return err
		}
		switch oe.Code {
		case "authorization_pending":
			// keep polling
		case "slow_down":
			interval += 5 * time.Second
		default:
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (p *Provider) requestDeviceCode(ctx context.Context) (*deviceAuthResponse, error) {
	form := url.Values{"client_id": {p.cfg.ClientID}, "scope": {strings.Join(p.cfg.Scopes, " ")}}
	if p.cfg.Audience != "" {
		form.Set("audience", p.cfg.Audience)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.deviceAuthURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc: device authorization failed (%s): %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var da deviceAuthResponse
	if err := json.Unmarshal(body, &da); err != nil {
		return nil, fmt.Errorf("oidc: decode device authorization: %w", err)
	}
	if da.DeviceCode == "" {
		return nil, errors.New("oidc: device authorization returned no device_code")
	}
	return &da, nil
}

// tokenResponse is the OAuth token-endpoint success payload.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

// oauthError is an OAuth error-response payload (RFC 6749 §5.2 / RFC 8628 §3.5).
type oauthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *oauthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("oidc: %s: %s", e.Code, e.Description)
	}
	return "oidc: " + e.Code
}

func (p *Provider) postToken(ctx context.Context, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		var oe oauthError
		if json.Unmarshal(body, &oe) == nil && oe.Code != "" {
			return nil, &oe
		}
		return nil, fmt.Errorf("oidc: token endpoint failed (%s): %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("oidc: decode token response: %w", err)
	}
	return &tr, nil
}

func (p *Provider) save(tr *tokenResponse, prevRefresh string) error {
	refresh := tr.RefreshToken
	if refresh == "" {
		refresh = prevRefresh // token endpoints may omit an unchanged refresh token
	}
	ts := auth.TokenSet{
		AccessToken:  tr.AccessToken,
		RefreshToken: refresh,
		IDToken:      tr.IDToken,
		TokenType:    tr.TokenType,
	}
	if tr.ExpiresIn > 0 {
		ts.Expiry = p.cfg.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	if err := p.cfg.Store.Save(ts); err != nil {
		return err
	}
	p.bus.Publish(auth.Event{Type: auth.EventTokenAcquired, Token: ts.AccessToken, ExpiresAt: ts.Expiry.Unix()})
	return nil
}

// discoveryDoc is the subset of the OIDC discovery document we consume.
type discoveryDoc struct {
	TokenEndpoint               string `json:"token_endpoint"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
	AuthorizationEndpoint       string `json:"authorization_endpoint"`
}

// discover resolves token/device endpoints from the issuer's well-known
// document when they were not supplied explicitly. It runs at most once.
func (p *Provider) discover(ctx context.Context) error {
	if p.discovered || (p.tokenURL != "" && p.deviceAuthURL != "") {
		p.discovered = true
		return nil
	}
	if p.cfg.Issuer == "" {
		return errors.New("oidc: TokenURL and DeviceAuthURL required when Issuer is unset")
	}
	well := strings.TrimSuffix(p.cfg.Issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, well, nil)
	if err != nil {
		return err
	}
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oidc: discovery failed (%s)", resp.Status)
	}
	var doc discoveryDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fmt.Errorf("oidc: decode discovery: %w", err)
	}
	if p.tokenURL == "" {
		p.tokenURL = doc.TokenEndpoint
	}
	if p.deviceAuthURL == "" {
		p.deviceAuthURL = doc.DeviceAuthorizationEndpoint
	}
	if p.tokenURL == "" || p.deviceAuthURL == "" {
		return errors.New("oidc: issuer discovery missing token or device_authorization endpoint")
	}
	p.discovered = true
	return nil
}

// decodeJWTClaims decodes the payload segment of a JWT without verifying its
// signature (claims are advisory here; the resource server verifies the token).
func decodeJWTClaims(jwt string) map[string]any {
	parts := strings.Split(jwt, ".")
	if len(parts) < 2 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(raw, &claims) != nil {
		return nil
	}
	return claims
}
