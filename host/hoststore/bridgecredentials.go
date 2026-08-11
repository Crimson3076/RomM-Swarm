package hoststore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// BridgeCredentialStore implements auth.Store against
// bridge_credential_families.
//
// Load and Save are independent, single-statement, autocommit-equivalent
// operations — neither ever holds a transaction open across the two calls.
// That is deliberate, not an oversight: auth.Verifier's Rotate/Revoke/
// ReEnroll methods have code paths that call Load and return without ever
// calling Save (an unknown token, a revoked family — see auth/refresh.go),
// so a transaction spanning Load-to-Save would leak on ordinary, expected
// control flow. Making the two calls atomic with respect to each other for
// the *same* BridgeID under concurrent access is host/directory's job (a
// per-BridgeID mutex around the whole Verifier call), not this type's. See
// ADR 0016, resolved sub-decision 1.
type BridgeCredentialStore struct {
	DB *sql.DB
}

// Load implements auth.Store.
func (s *BridgeCredentialStore) Load(id protocol.BridgeID) (auth.FamilyState, bool) {
	row := s.DB.QueryRowContext(context.Background(), `
		SELECT current_hash, previous_hash, previous_issued_at, previous_consumed,
		       generation, revoked, revoked_reason
		FROM bridge_credential_families
		WHERE bridge_id = $1`, string(id))

	var (
		state            auth.FamilyState
		previousIssuedAt sql.NullTime
	)
	state.BridgeID = id
	if err := row.Scan(&state.CurrentHash, &state.PreviousHash, &previousIssuedAt,
		&state.PreviousConsumed, &state.Generation, &state.Revoked, &state.RevokedReason); err != nil {
		// Both "no such row" and any other read error are reported as "not
		// found" here, matching auth.Store's two-value contract — Verifier
		// treats an unreadable family the same as a genuinely unknown one
		// (ErrUnknownToken), which is the safe default when the alternative
		// is inventing a third outcome the interface has no room for.
		return auth.FamilyState{}, false
	}
	if previousIssuedAt.Valid {
		state.PreviousIssuedAt = previousIssuedAt.Time
	}
	return state, true
}

// Save implements auth.Store.
func (s *BridgeCredentialStore) Save(state auth.FamilyState) error {
	var previousIssuedAt any
	if !state.PreviousIssuedAt.IsZero() {
		previousIssuedAt = state.PreviousIssuedAt
	}

	_, err := s.DB.ExecContext(context.Background(), `
		INSERT INTO bridge_credential_families
			(bridge_id, current_hash, previous_hash, previous_issued_at,
			 previous_consumed, generation, revoked, revoked_reason, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (bridge_id) DO UPDATE SET
			current_hash = EXCLUDED.current_hash,
			previous_hash = EXCLUDED.previous_hash,
			previous_issued_at = EXCLUDED.previous_issued_at,
			previous_consumed = EXCLUDED.previous_consumed,
			generation = EXCLUDED.generation,
			revoked = EXCLUDED.revoked,
			revoked_reason = EXCLUDED.revoked_reason,
			updated_at = EXCLUDED.updated_at`,
		string(state.BridgeID), state.CurrentHash, state.PreviousHash, previousIssuedAt,
		state.PreviousConsumed, state.Generation, state.Revoked, state.RevokedReason, time.Now())
	if err != nil {
		return fmt.Errorf("hoststore: saving credential family for %s: %w", state.BridgeID, err)
	}
	return nil
}

// EnsureBridge upserts a bridges row — RedeemInvitation calls this before
// the first Save for a new identity, since bridge_credential_families.
// bridge_id references bridges(id).
func EnsureBridge(ctx context.Context, db *sql.DB, id protocol.BridgeID, publicKey []byte) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO bridges (id, public_identity_key, created_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (id) DO NOTHING`,
		string(id), publicKey, time.Now())
	if err != nil {
		return fmt.Errorf("hoststore: ensuring bridge %s: %w", id, err)
	}
	return nil
}

// ErrBridgeUnknown is returned by lookups against a BridgeID with no
// bridges row.
var ErrBridgeUnknown = errors.New("hoststore: no such bridge")
