package clikit

import (
	"bytes"
	"io"
	"net/http"

	"github.com/infobloxopen/devedge-cli-sdk/clikit/auth"
)

// authTransport attaches "Authorization: Bearer <token>" from a [auth.Session]
// and, on a 401, calls Session.Login then retries the request ONCE. It is the
// Go analog of the devedge-ufe-sdk createAuthedFetch.
type authTransport struct {
	sess auth.Session
	base http.RoundTripper
}

// NewAuthTransport wraps base (or http.DefaultTransport) so requests carry the
// session bearer token and a single 401→login→retry happens transparently.
func NewAuthTransport(sess auth.Session, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &authTransport{sess: sess, base: base}
}

// NewAuthClient returns an *http.Client using [NewAuthTransport].
func NewAuthClient(sess auth.Session) *http.Client {
	return &http.Client{Transport: NewAuthTransport(sess, nil)}
}

// headerTransport adds a fixed set of headers to every request before
// delegating to base. It is how the CLI attaches request metadata a target
// service reads directly — for example the account-id/groups/subject metadata a
// devedge-sdk dev authorizer reads, which a bearer token alone does not carry.
type headerTransport struct {
	headers http.Header
	base    http.RoundTripper
}

// NewHeaderTransport wraps base (or http.DefaultTransport) so every request
// carries the given headers. Existing headers with the same key are overwritten.
// It composes under [NewAuthTransport]: the bearer token and the extra headers
// are both attached.
func NewHeaderTransport(headers http.Header, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &headerTransport{headers: headers, base: base}
}

// RoundTrip implements http.RoundTripper.
func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(t.headers) == 0 {
		return t.base.RoundTrip(req)
	}
	clone := req.Clone(req.Context())
	for k, vs := range t.headers {
		clone.Header.Del(k)
		for _, v := range vs {
			clone.Header.Add(k, v)
		}
	}
	return t.base.RoundTrip(clone)
}

// NewAuthClientWithHeaders returns an *http.Client that attaches the session
// bearer token AND the given fixed headers on every request. Pass local-dev
// identity metadata here (e.g. account-id/groups) so a generated CLI can drive a
// service whose authorizer reads request metadata rather than the token.
func NewAuthClientWithHeaders(sess auth.Session, headers http.Header) *http.Client {
	return &http.Client{Transport: NewAuthTransport(sess, NewHeaderTransport(headers, nil))}
}

// RoundTrip implements http.RoundTripper.
func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Buffer the body so a 401 retry can resend it (a request body is single
	// use), mirroring how createAuthedFetch rebuilds the Request per send.
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = b
	}

	send := func() (*http.Response, error) {
		clone := req.Clone(req.Context())
		if body != nil {
			clone.Body = io.NopCloser(bytes.NewReader(body))
			clone.ContentLength = int64(len(body))
		}
		token, err := t.sess.Token(req.Context())
		if err != nil {
			return nil, err
		}
		if token != "" {
			clone.Header.Set("Authorization", "Bearer "+token)
		}
		return t.base.RoundTrip(clone)
	}

	resp, err := send()
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	// 401 → re-authenticate and retry exactly once.
	_ = resp.Body.Close()
	if err := t.sess.Login(req.Context()); err != nil {
		return nil, err
	}
	return send()
}
