// Package hostclient is the HTTP transport auth/ deliberately has none of
// (auth/bridgestore.go's Client.Rotate doc comment: "In production this is
// an HTTP call") — ADR 0017. It calls a Network Host's already-shipped,
// already-tested enrollment API (host/hostapi, ADR 0016) to enroll a Bridge
// identity and to satisfy the exact func signature auth.Client.Rotate
// expects.
package hostclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// DefaultTimeout bounds a single call when the caller doesn't set one.
const DefaultTimeout = 30 * time.Second

// ErrInvitationInvalid mirrors host/directory.ErrInvitationInvalid — the
// Host deliberately doesn't distinguish not-found/expired/exhausted/revoked
// over the wire (host/hostapi's own comment: so a Bridge can't use the
// response to probe why), so neither does this client.
var ErrInvitationInvalid = errors.New("hostclient: invitation is invalid, expired, or already used")

// Client calls one Network Host's enrollment API.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
	Timeout    time.Duration
}

// New returns a Client for baseURL, with DefaultTimeout and http.DefaultClient.
func New(baseURL string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/")}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

// EnrollResult is what a successful enrollment returns.
type EnrollResult struct {
	BridgeID protocol.BridgeID
	Refresh  auth.Token
}

type enrollRequest struct {
	Code      string `json:"code"`
	PublicKey string `json:"public_key"`
}

type enrollResponse struct {
	BridgeID     string `json:"bridge_id"`
	RefreshToken string `json:"refresh_token"`
}

// Enroll redeems code against the Host, presenting publicKey as this
// Bridge's identity. Matches host/hostapi.handleEnrollBridge's exact
// contract: POST /api/bridges/enroll, {code, public_key (base64)} in,
// {bridge_id, refresh_token} out.
func (c *Client) Enroll(ctx context.Context, code string, publicKey ed25519.PublicKey) (EnrollResult, error) {
	body, err := json.Marshal(enrollRequest{
		Code:      code,
		PublicKey: base64.StdEncoding.EncodeToString(publicKey),
	})
	if err != nil {
		return EnrollResult{}, fmt.Errorf("hostclient: encoding enroll request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/bridges/enroll", bytes.NewReader(body))
	if err != nil {
		return EnrollResult{}, fmt.Errorf("hostclient: building enroll request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return EnrollResult{}, fmt.Errorf("hostclient: calling enroll: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusCreated:
		var out enrollResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return EnrollResult{}, fmt.Errorf("hostclient: decoding enroll response: %w", err)
		}
		return EnrollResult{BridgeID: protocol.BridgeID(out.BridgeID), Refresh: auth.Token(out.RefreshToken)}, nil
	case http.StatusUnprocessableEntity:
		return EnrollResult{}, ErrInvitationInvalid
	default:
		return EnrollResult{}, fmt.Errorf("hostclient: enroll failed: %s", responseErrorMessage(resp))
	}
}

type rotateRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type rotateResponse struct {
	RefreshToken    string    `json:"refresh_token"`
	AccessExpiresAt time.Time `json:"access_expires_at"`
	Outcome         string    `json:"outcome"`
}

// RotateFunc returns exactly the func(protocol.BridgeID, auth.Token)
// (auth.Result, error) shape auth.Client.Rotate expects. store is read
// once per call to recover the pre-rotation Generation — the wire response
// carries no generation field at all, but auth.Verifier.Rotate always
// increments by exactly 1 on every successful path (normal or
// grace-recovery), so previous+1 is exact, not a guess. Generation is
// diagnostic-only; nothing in auth/ validates a Bridge-presented value.
func (c *Client) RotateFunc(store *auth.FileStore) func(protocol.BridgeID, auth.Token) (auth.Result, error) {
	return func(bridge protocol.BridgeID, presented auth.Token) (auth.Result, error) {
		prev, err := store.Load()
		if err != nil {
			return auth.Result{}, fmt.Errorf("hostclient: loading the stored credential before rotating: %w", err)
		}

		body, err := json.Marshal(rotateRequest{RefreshToken: string(presented)})
		if err != nil {
			return auth.Result{}, fmt.Errorf("hostclient: encoding rotate request: %w", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), c.timeout())
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/bridges/"+string(bridge)+"/rotate", bytes.NewReader(body))
		if err != nil {
			return auth.Result{}, fmt.Errorf("hostclient: building rotate request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient().Do(req)
		if err != nil {
			return auth.Result{}, fmt.Errorf("hostclient: calling rotate: %w", err)
		}
		defer resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK:
			var out rotateResponse
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				return auth.Result{}, fmt.Errorf("hostclient: decoding rotate response: %w", err)
			}
			outcome := auth.OutcomeRotated
			if out.Outcome == string(auth.OutcomeRecovered) {
				outcome = auth.OutcomeRecovered
			}
			return auth.Result{
				Outcome:         outcome,
				Refresh:         auth.Token(out.RefreshToken),
				AccessExpiresAt: out.AccessExpiresAt,
				Generation:      prev.Generation + 1,
			}, nil
		case http.StatusUnauthorized:
			// Matches host/hostapi.writeCredentialError's mapping, so
			// errors.Is checks behave identically whether auth.Verifier
			// ran in-process (as in the Host's own tests) or, as here,
			// over HTTP.
			return auth.Result{}, auth.ErrUnknownToken
		case http.StatusForbidden:
			return auth.Result{}, auth.ErrFamilyRevoked
		default:
			return auth.Result{}, fmt.Errorf("hostclient: rotate failed: %s", responseErrorMessage(resp))
		}
	}
}

type errorBody struct {
	Error string `json:"error"`
}

// responseErrorMessage reads host/hostapi's {"error": "..."} shape, falling
// back to the raw status and body when the response isn't shaped that way.
func responseErrorMessage(resp *http.Response) string {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return resp.Status
	}
	var body errorBody
	if json.Unmarshal(raw, &body) == nil && body.Error != "" {
		return fmt.Sprintf("%s: %s", resp.Status, body.Error)
	}
	return fmt.Sprintf("%s: %s", resp.Status, strings.TrimSpace(string(raw)))
}
