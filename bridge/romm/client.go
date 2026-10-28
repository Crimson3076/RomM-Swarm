package romm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AuthScheme is one way of presenting a RomM credential.
//
// Several are tried rather than one being assumed. RomM's Client API Token is
// presented differently across versions and deployments, and guessing wrong
// produces a 401 that looks identical to a bad token — which would send an
// operator hunting for a credential problem that does not exist. The probe
// reports which scheme actually worked, and the Bridge then uses that one.
type AuthScheme struct {
	ID    string
	Apply func(req *http.Request, token string)
}

// AuthSchemes are the credential presentations the probe tries, in order.
func AuthSchemes() []AuthScheme {
	return []AuthScheme{
		{
			ID: "bearer",
			Apply: func(req *http.Request, token string) {
				req.Header.Set("Authorization", "Bearer "+token)
			},
		},
		{
			ID: "x-api-key",
			Apply: func(req *http.Request, token string) {
				req.Header.Set("X-Api-Key", token)
			},
		},
		{
			ID: "authorization-raw",
			Apply: func(req *http.Request, token string) {
				req.Header.Set("Authorization", token)
			},
		},
	}
}

// LookupAuthScheme returns a scheme by id.
func LookupAuthScheme(id string) (AuthScheme, bool) {
	for _, s := range AuthSchemes() {
		if s.ID == id {
			return s, true
		}
	}
	return AuthScheme{}, false
}

// Client talks to one RomM server.
//
// The token is held here and nowhere else. Scope of Work §3: "RomM Client API
// Tokens stay on the local Bridge", and §7: no credential logging. Every error
// this package returns passes through Redact before it leaves.
type Client struct {
	BaseURL string
	Token   string
	Scheme  AuthScheme
	HTTP    *http.Client
}

// NewClient builds a client. The scheme may be empty, in which case the caller
// is expected to run DetectAuthScheme first.
func NewClient(baseURL, token string, scheme AuthScheme) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		Scheme:  scheme,
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Redact removes the token from a string, so that a URL, an error, or a server
// response body can be recorded or logged without leaking the credential.
//
// Applied at the boundary rather than at each call site: a rule that has to be
// remembered every time is a rule that will eventually be forgotten.
func (c *Client) Redact(s string) string {
	if c.Token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.Token, "[redacted]")
}

// Get performs an authenticated GET and returns the status and body.
//
// The body is capped: a probe must not be turned into a memory exhaustion by a
// server that streams indefinitely.
func (c *Client) Get(ctx context.Context, path string, query url.Values) (int, []byte, error) {
	return c.get(ctx, path, query, true)
}

// get performs a GET and optionally presents the configured credential.
// Authentication is a per-request decision so an unauthenticated specification
// fetch never mutates shared Client state or races with an authenticated call.
func (c *Client) get(ctx context.Context, path string, query url.Values, authenticate bool) (int, []byte, error) {
	const maxBody = 8 << 20

	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("romm: building request for %s: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")
	if authenticate && c.Token != "" && c.Scheme.Apply != nil {
		c.Scheme.Apply(req, c.Token)
	}

	httpClient := *c.HTTP
	configuredOrigin := req.URL
	previousRedirectCheck := httpClient.CheckRedirect
	httpClient.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if !sameOrigin(configuredOrigin, next.URL) {
			return fmt.Errorf("romm: refusing redirect from %s to a different origin", configuredOrigin.Redacted())
		}
		if previousRedirectCheck != nil {
			return previousRedirectCheck(next, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("romm: stopped after 10 redirects")
		}
		return nil
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("romm: requesting %s: %s", path, c.Redact(err.Error()))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("romm: reading %s: %s", path, c.Redact(err.Error()))
	}
	if len(body) > maxBody {
		return resp.StatusCode, nil, fmt.Errorf("romm: reading %s: response exceeds %d bytes", path, maxBody)
	}
	return resp.StatusCode, body, nil
}

// GetUnauthenticated performs a GET without presenting the credential. Used for
// the OpenAPI document, which is usually public and which must not be a reason
// to send a token to an endpoint that does not need one.
func (c *Client) GetUnauthenticated(ctx context.Context, path string) (int, []byte, error) {
	return c.get(ctx, path, nil, false)
}

// sameOrigin reports whether two URLs have the same scheme, host, and effective
// port. Credentials must never follow redirects away from the RomM origin the
// operator configured, including redirects that downgrade HTTPS to HTTP.
func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		effectivePort(a) == effectivePort(b)
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

// Stream performs an authenticated GET and returns the raw response body for
// the caller to read and close, without buffering it.
//
// Separate from Get on purpose: Get's response cap exists to stop a probe
// request being turned into a memory exhaustion, but that same cap would make
// Get unusable for downloading ROM content, which is routinely far larger than
// 8 MiB. The caller is responsible for closing the returned body and for
// applying whatever size limit is appropriate to what it's downloading.
func (c *Client) Stream(ctx context.Context, path string) (status int, body io.ReadCloser, contentLength int64, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return 0, nil, 0, fmt.Errorf("romm: building request for %s: %w", path, err)
	}
	if c.Token != "" && c.Scheme.Apply != nil {
		c.Scheme.Apply(req, c.Token)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, 0, fmt.Errorf("romm: requesting %s: %s", path, c.Redact(err.Error()))
	}
	return resp.StatusCode, resp.Body, resp.ContentLength, nil
}

// DetectAuthScheme finds which credential presentation the server accepts, by
// making an authenticated request to probePath under each scheme in turn.
//
// A 200 means the scheme works. A 401 or 403 means it does not. Anything else
// is reported as inconclusive rather than being read as success, because a
// server returning 500 to every request would otherwise "confirm" the first
// scheme tried.
func DetectAuthScheme(ctx context.Context, baseURL, token, probePath string, httpClient *http.Client) (AuthScheme, int, error) {
	var lastStatus int
	for _, scheme := range AuthSchemes() {
		c := NewClient(baseURL, token, scheme)
		if httpClient != nil {
			c.HTTP = httpClient
		}
		status, _, err := c.Get(ctx, probePath, nil)
		if err != nil {
			return AuthScheme{}, 0, err
		}
		lastStatus = status
		if status == http.StatusOK {
			return scheme, status, nil
		}
		if status != http.StatusUnauthorized && status != http.StatusForbidden {
			return AuthScheme{}, status, fmt.Errorf(
				"romm: %s returned status %d, which is neither success nor an authentication failure; the credential could not be confirmed",
				probePath, status)
		}
	}
	return AuthScheme{}, lastStatus, fmt.Errorf(
		"romm: no supported credential presentation was accepted by %s (last status %d)", probePath, lastStatus)
}
