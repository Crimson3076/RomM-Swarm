package directory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// InvitationCode is the secret a prospective Bridge presents to redeem an
// invitation. Deliberately not a protocol type — protocol/ids.go's
// identifiers are public, safe-to-log, self-describing strings, and mixing
// a secret into that namespace is exactly what auth.Token already avoids
// keeping separate from protocol.BridgeID. Built by converting through
// auth.Token, reusing its already-tested "random bearer secret, hashed at
// rest" primitive rather than duplicating the crypto.
type InvitationCode string

func newInvitationCode() InvitationCode { return InvitationCode(auth.NewToken()) }

func (c InvitationCode) hash() string { return auth.Token(c).Hash() }

// defaultInvitationTTL is used when a caller doesn't specify one.
const defaultInvitationTTL = 7 * 24 * time.Hour

// IssueInvitation mints an invitation for swarm and stores only its hash —
// the plaintext code is returned exactly once, here, and is never
// retrievable again, the same pattern auth.Verifier.Enroll uses for a
// Bridge's first refresh token. maxUses <= 0 defaults to 1 (single use);
// ttl <= 0 defaults to defaultInvitationTTL.
func (d *Directory) IssueInvitation(ctx context.Context, swarm protocol.SwarmID, issuedBy protocol.UserID, maxUses int, ttl time.Duration) (InvitationCode, protocol.InvitationID, error) {
	if maxUses <= 0 {
		maxUses = 1
	}
	if ttl <= 0 {
		ttl = defaultInvitationTTL
	}

	id := protocol.NewInvitationID()
	code := newInvitationCode()
	now := d.now()

	_, err := d.DB.ExecContext(ctx, `
		INSERT INTO invitations (id, swarm_id, code_hash, issued_by, max_uses, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		string(id), string(swarm), code.hash(), string(issuedBy), maxUses, now.Add(ttl), now)
	if err != nil {
		return "", "", fmt.Errorf("directory: issuing invitation: %w", err)
	}

	_ = d.events.Record(ctx, protocol.Event{
		Kind: protocol.EventInvitationIssued, At: now, SwarmID: swarm, ActorUser: issuedBy,
	})
	return code, id, nil
}

// Invitation is one issued invitation, as returned to a caller. The secret
// code itself is never included — only its hash is ever stored, and even
// that isn't exposed here; see IssueInvitation's doc comment.
type Invitation struct {
	ID        protocol.InvitationID
	MaxUses   int
	UseCount  int
	ExpiresAt time.Time
	CreatedAt time.Time
	RevokedAt time.Time
}

// ErrInvitationNotFound means invitation has no row in swarm — either it
// was never issued there or has already been deleted.
var ErrInvitationNotFound = errors.New("directory: no such invitation")

// DeleteInvitation permanently removes one invitation from swarm —
// active, expired, exhausted, or already revoked; delete doesn't care
// which. Any Bridge enrolled through it (bridge_swarm_memberships'
// nullable enrolled_via_invitation) keeps its membership row: this
// forgets which invitation a Bridge came in through, not that the Bridge
// is a member.
func (d *Directory) DeleteInvitation(ctx context.Context, swarm protocol.SwarmID, invitation protocol.InvitationID) error {
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("directory: starting delete-invitation transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE bridge_swarm_memberships SET enrolled_via_invitation = NULL
		WHERE enrolled_via_invitation = $1`, string(invitation)); err != nil {
		return fmt.Errorf("directory: clearing invitation references: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
		DELETE FROM invitations WHERE id = $1 AND swarm_id = $2`,
		string(invitation), string(swarm))
	if err != nil {
		return fmt.Errorf("directory: deleting invitation: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("directory: deleting invitation: %w", err)
	}
	if n == 0 {
		return ErrInvitationNotFound
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("directory: committing invitation deletion: %w", err)
	}

	_ = d.events.Record(ctx, protocol.Event{Kind: protocol.EventInvitationDeleted, At: d.now(), SwarmID: swarm})
	return nil
}

// ListInvitations returns every invitation ever issued for swarm, most
// recent first.
func (d *Directory) ListInvitations(ctx context.Context, swarm protocol.SwarmID) ([]Invitation, error) {
	rows, err := d.DB.QueryContext(ctx, `
		SELECT id, max_uses, use_count, expires_at, created_at, revoked_at
		FROM invitations
		WHERE swarm_id = $1
		ORDER BY created_at DESC`, string(swarm))
	if err != nil {
		return nil, fmt.Errorf("directory: listing invitations: %w", err)
	}
	defer rows.Close()

	var out []Invitation
	for rows.Next() {
		var (
			inv       Invitation
			revokedAt sql.NullTime
		)
		if err := rows.Scan(&inv.ID, &inv.MaxUses, &inv.UseCount, &inv.ExpiresAt, &inv.CreatedAt, &revokedAt); err != nil {
			return nil, fmt.Errorf("directory: reading invitation row: %w", err)
		}
		if revokedAt.Valid {
			inv.RevokedAt = revokedAt.Time
		}
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("directory: listing invitations: %w", err)
	}
	return out, nil
}
