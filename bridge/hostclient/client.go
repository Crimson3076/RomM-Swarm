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

// EnrollResult is what a successful enrollment returns. SwarmID and Alias
// are needed to publish inventory later (ADR 0019) — a Bridge cannot
// compute its own alias, since the Swarm's alias key never leaves the
// Host, so enrollment is the only point it can be handed over.
type EnrollResult struct {
	BridgeID protocol.BridgeID
	SwarmID  protocol.SwarmID
	Alias    protocol.BridgeAlias
	Refresh  auth.Token
}

type enrollRequest struct {
	Code      string `json:"code"`
	PublicKey string `json:"public_key"`
}

type enrollResponse struct {
	BridgeID     string `json:"bridge_id"`
	SwarmID      string `json:"swarm_id"`
	BridgeAlias  string `json:"bridge_alias"`
	RefreshToken string `json:"refresh_token"`
}

// Enroll redeems code against the Host, presenting publicKey as this
// Bridge's identity. Matches host/hostapi.handleEnrollBridge's exact
// contract: POST /api/bridges/enroll, {code, public_key (base64)} in,
// {bridge_id, swarm_id, bridge_alias, refresh_token} out.
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
		return EnrollResult{
			BridgeID: protocol.BridgeID(out.BridgeID),
			SwarmID:  protocol.SwarmID(out.SwarmID),
			Alias:    protocol.BridgeAlias(out.BridgeAlias),
			Refresh:  auth.Token(out.RefreshToken),
		}, nil
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

// publishInventoryTimeout replaces DefaultTimeout for PublishInventory: a
// full-catalogue manifest's body can be far larger than enroll/rotate's
// small, fixed-shape requests, so 30 seconds is too tight a default here.
const publishInventoryTimeout = 5 * time.Minute

// PublishInventoryResult is what a successful inventory publish returns.
type PublishInventoryResult struct {
	ItemCount     int
	Revision      protocol.Revision
	PublishedAt   time.Time
	DistinctFiles int
}

type publishInventoryRequest struct {
	RefreshToken string            `json:"refresh_token"`
	Manifest     protocol.Manifest `json:"manifest"`
}

type publishInventoryResponse struct {
	ItemCount     int       `json:"item_count"`
	Revision      uint64    `json:"revision"`
	PublishedAt   time.Time `json:"published_at"`
	DistinctFiles int       `json:"distinct_files"`
}

// PublishInventory sends manifest to the Host, presenting refresh as the
// Bridge's proof of identity. Matches host/hostapi.handlePublishInventory's
// exact contract: POST /api/bridges/{id}/inventory,
// {refresh_token, manifest} in, {item_count, revision, published_at,
// distinct_files} out.
//
// Only a 401 maps to a sentinel error (auth.ErrUnknownToken) — unlike
// RotateFunc, a 403 here has two distinct causes (a revoked credential
// family, or a Bridge no longer actively enrolled in the named Swarm) that
// this client cannot safely collapse into one sentinel without misleading
// the caller about which happened; the server's own message is preserved
// via responseErrorMessage instead.
func (c *Client) PublishInventory(ctx context.Context, bridge protocol.BridgeID, refresh auth.Token, manifest protocol.Manifest) (PublishInventoryResult, error) {
	body, err := json.Marshal(publishInventoryRequest{RefreshToken: string(refresh), Manifest: manifest})
	if err != nil {
		return PublishInventoryResult{}, fmt.Errorf("hostclient: encoding inventory publish request: %w", err)
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = publishInventoryTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/bridges/"+string(bridge)+"/inventory", bytes.NewReader(body))
	if err != nil {
		return PublishInventoryResult{}, fmt.Errorf("hostclient: building inventory publish request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return PublishInventoryResult{}, fmt.Errorf("hostclient: calling inventory publish: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var out publishInventoryResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return PublishInventoryResult{}, fmt.Errorf("hostclient: decoding inventory publish response: %w", err)
		}
		return PublishInventoryResult{
			ItemCount:     out.ItemCount,
			Revision:      protocol.Revision(out.Revision),
			PublishedAt:   out.PublishedAt,
			DistinctFiles: out.DistinctFiles,
		}, nil
	case http.StatusUnauthorized:
		return PublishInventoryResult{}, auth.ErrUnknownToken
	default:
		return PublishInventoryResult{}, fmt.Errorf("hostclient: inventory publish failed: %s", responseErrorMessage(resp))
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
