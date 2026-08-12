package directory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/host/hoststore"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// ErrInvitationInvalid covers every reason an invitation redemption can
// fail — not found, expired, exhausted, or revoked — deliberately not
// distinguished in the returned error, so a Bridge presenting a guessed
// code can't use the response to learn which reason applies. The specific
// reason is not logged by this package either; a caller that wants an
// audit trail for invitation attempts should add that at the API layer.
var ErrInvitationInvalid = errors.New("directory: invitation is invalid, expired, or already used")

// RedeemInvitation validates code, derives the presenting Bridge's identity
// from publicKey, and enrolls it into the invitation's Swarm. Two
// concurrent redemptions of the same single-use invitation by two
// different keys are not covered by the per-BridgeID lock below (different
// keys mean different BridgeIDs) — closed instead by the invitation
// UPDATE's own row-level locking, a single atomic statement.
func (d *Directory) RedeemInvitation(ctx context.Context, code InvitationCode, publicKey []byte) (protocol.BridgeID, auth.Token, error) {
	now := d.now()

	var (
		invitationID protocol.InvitationID
		swarmID      protocol.SwarmID
	)
	err := d.DB.QueryRowContext(ctx, `
		UPDATE invitations
		SET use_count = use_count + 1
		WHERE code_hash = $1
		  AND revoked_at IS NULL
		  AND expires_at > $2
		  AND use_count < max_uses
		RETURNING id, swarm_id`,
		code.hash(), now,
	).Scan(&invitationID, &swarmID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrInvitationInvalid
	}
	if err != nil {
		return "", "", fmt.Errorf("directory: redeeming invitation: %w", err)
	}

	bridgeID := protocol.BridgeIDFromPublicKey(publicKey)
	unlock := d.locks.lock(bridgeID)
	defer unlock()

	if err := hoststore.EnsureBridge(ctx, d.DB, bridgeID, publicKey); err != nil {
		return "", "", err
	}
	_, err = d.DB.ExecContext(ctx, `
		INSERT INTO bridge_swarm_memberships (bridge_id, swarm_id, enrolled_via_invitation, joined_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (bridge_id, swarm_id) DO NOTHING`,
		string(bridgeID), string(swarmID), string(invitationID), now)
	if err != nil {
		return "", "", fmt.Errorf("directory: recording Swarm membership: %w", err)
	}

	token, err := d.verifier.Enroll(bridgeID)
	if err != nil {
		return "", "", fmt.Errorf("directory: enrolling Bridge credential: %w", err)
	}

	_ = d.events.Record(ctx, protocol.Event{
		Kind: protocol.EventBridgeEnrolled, At: now, SwarmID: swarmID, ActorBridge: bridgeID,
	})
	return bridgeID, token, nil
}

// RotateBridgeCredential exchanges a Bridge's refresh credential for a new
// one. See auth.Verifier.Rotate for the full protocol; this just adds the
// per-BridgeID serialization and event recording.
func (d *Directory) RotateBridgeCredential(ctx context.Context, bridge protocol.BridgeID, presented auth.Token) (auth.Result, error) {
	unlock := d.locks.lock(bridge)
	defer unlock()

	result, err := d.verifier.Rotate(bridge, presented)
	now := d.now()
	switch {
	case err == nil:
		_ = d.events.Record(ctx, protocol.Event{Kind: result.Event, At: now, ActorBridge: bridge})
	case errors.Is(err, auth.ErrReuseDetected):
		_ = d.events.Record(ctx, protocol.Event{Kind: protocol.EventTokenReuseDetect, At: now, ActorBridge: bridge})
		_ = d.events.Record(ctx, protocol.Event{Kind: protocol.EventTokenFamilyRevkd, At: now, ActorBridge: bridge})
	default:
		_ = d.events.Record(ctx, protocol.Event{Kind: protocol.EventAuthFailure, At: now, ActorBridge: bridge})
	}
	return result, err
}

// RevokeBridge ends a Bridge's credential family — global, across every
// Swarm it belongs to. There is no partial, per-Swarm revocation in the
// auth protocol as it exists; see ADR 0016, resolved sub-decision 3.
func (d *Directory) RevokeBridge(ctx context.Context, bridge protocol.BridgeID, reason string) error {
	unlock := d.locks.lock(bridge)
	defer unlock()

	if err := d.verifier.Revoke(bridge, reason); err != nil {
		_ = d.events.Record(ctx, protocol.Event{Kind: protocol.EventAuthFailure, At: d.now(), ActorBridge: bridge})
		return err
	}
	_ = d.events.Record(ctx, protocol.Event{Kind: protocol.EventBridgeRevoked, At: d.now(), ActorBridge: bridge})
	return nil
}

// ReenrollBridge is the deliberate, owner-authorised escape from a revoked
// credential family. See auth.Verifier.ReEnroll.
func (d *Directory) ReenrollBridge(ctx context.Context, bridge protocol.BridgeID) (auth.Token, error) {
	unlock := d.locks.lock(bridge)
	defer unlock()

	token, err := d.verifier.ReEnroll(bridge)
	if err != nil {
		_ = d.events.Record(ctx, protocol.Event{Kind: protocol.EventAuthFailure, At: d.now(), ActorBridge: bridge})
		return "", err
	}
	_ = d.events.Record(ctx, protocol.Event{Kind: protocol.EventBridgeReenrolled, At: d.now(), ActorBridge: bridge})
	return token, nil
}

// BridgeMembership is one Bridge's membership in one Swarm, as returned to
// a caller. CredentialRevoked reflects the Bridge's global credential
// family state (bridge_credential_families), not this membership row —
// revocation is global; see RevokeBridge's own doc comment.
type BridgeMembership struct {
	BridgeID          protocol.BridgeID
	JoinedAt          time.Time
	State             string
	DisabledAt        time.Time
	RevokedAt         time.Time
	RevokedReason     string
	CredentialRevoked bool
}

// ListBridgesForSwarm returns every Bridge enrolled in swarm, most
// recently joined first.
func (d *Directory) ListBridgesForSwarm(ctx context.Context, swarm protocol.SwarmID) ([]BridgeMembership, error) {
	rows, err := d.DB.QueryContext(ctx, `
		SELECT m.bridge_id, m.joined_at, m.state, m.disabled_at, m.revoked_at, m.revoked_reason,
		       COALESCE(f.revoked, FALSE)
		FROM bridge_swarm_memberships m
		LEFT JOIN bridge_credential_families f ON f.bridge_id = m.bridge_id
		WHERE m.swarm_id = $1
		ORDER BY m.joined_at DESC`, string(swarm))
	if err != nil {
		return nil, fmt.Errorf("directory: listing Bridges for Swarm: %w", err)
	}
	defer rows.Close()

	var out []BridgeMembership
	for rows.Next() {
		var (
			bm            BridgeMembership
			disabledAt    sql.NullTime
			revokedAt     sql.NullTime
			revokedReason sql.NullString
		)
		if err := rows.Scan(&bm.BridgeID, &bm.JoinedAt, &bm.State, &disabledAt, &revokedAt, &revokedReason, &bm.CredentialRevoked); err != nil {
			return nil, fmt.Errorf("directory: reading Bridge membership row: %w", err)
		}
		if disabledAt.Valid {
			bm.DisabledAt = disabledAt.Time
		}
		if revokedAt.Valid {
			bm.RevokedAt = revokedAt.Time
		}
		if revokedReason.Valid {
			bm.RevokedReason = revokedReason.String
		}
		out = append(out, bm)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("directory: listing Bridges for Swarm: %w", err)
	}
	return out, nil
}
