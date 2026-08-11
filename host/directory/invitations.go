package directory

import (
	"context"
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
