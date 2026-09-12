package directory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// ErrSwarmNotFound means account is not a member of swarm, or no such
// Swarm exists — the two are deliberately not distinguished, the same
// caution RedeemInvitation already applies to invitation-lookup failures:
// a caller shouldn't be able to use the error to probe which Swarm IDs
// exist.
var ErrSwarmNotFound = errors.New("directory: no such swarm")

// Swarm is one trust group, as returned to a caller. AliasKey is
// deliberately not exposed here — see protocol/alias.go's own doc comment:
// it never leaves the Host.
type Swarm struct {
	ID   protocol.SwarmID
	Name string
}

// CreateSwarm mints a Swarm and its per-Swarm alias key, and makes owner
// its first member with role "owner".
func (d *Directory) CreateSwarm(ctx context.Context, owner protocol.UserID, name string) (protocol.SwarmID, error) {
	id := protocol.NewSwarmID()
	aliasKey := protocol.NewSwarmAliasKey()
	now := d.now()

	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("directory: starting create-Swarm transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO swarms (id, name, alias_key, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5)`,
		string(id), name, aliasKey, string(owner), now); err != nil {
		return "", fmt.Errorf("directory: creating Swarm: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO swarm_memberships (swarm_id, account_id, role, joined_at)
		VALUES ($1, $2, 'owner', $3)`,
		string(id), string(owner), now); err != nil {
		return "", fmt.Errorf("directory: creating Swarm owner membership: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("directory: committing Swarm creation: %w", err)
	}

	_ = d.events.Record(ctx, protocol.Event{Kind: protocol.EventSwarmCreated, At: now, SwarmID: id, ActorUser: owner})
	return id, nil
}

// GetSwarm returns one Swarm, scoped to account's membership — the same
// scoping ListSwarms already applies, just for a single row.
func (d *Directory) GetSwarm(ctx context.Context, account protocol.UserID, swarm protocol.SwarmID) (Swarm, error) {
	var s Swarm
	err := d.DB.QueryRowContext(ctx, `
		SELECT s.id, s.name
		FROM swarms s
		JOIN swarm_memberships m ON m.swarm_id = s.id
		WHERE m.account_id = $1 AND s.id = $2`, string(account), string(swarm),
	).Scan(&s.ID, &s.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return Swarm{}, ErrSwarmNotFound
	}
	if err != nil {
		return Swarm{}, fmt.Errorf("directory: looking up Swarm: %w", err)
	}
	return s, nil
}

// DeleteSwarm permanently removes swarm and everything scoped to it —
// invitations, memberships, and both inventory tables — but never touches
// a Bridge's own global identity or credential rows (the `bridges` and
// `bridge_credential_families` tables): a Bridge may belong to other
// Swarms, and even when this was its only one, its identity is a
// Bridge-owned key pair the Host never minted and has no business
// discarding on the Swarm owner's behalf.
//
// Scoped to account's membership, the same check GetSwarm already
// applies, so a caller can't delete a Swarm by guessing its ID.
func (d *Directory) DeleteSwarm(ctx context.Context, account protocol.UserID, swarm protocol.SwarmID) error {
	if _, err := d.GetSwarm(ctx, account, swarm); err != nil {
		return err
	}

	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("directory: starting delete-Swarm transaction: %w", err)
	}
	defer tx.Rollback()

	// Children before parents, so no foreign key is ever violated —
	// mirrors InventoryStore's own reasoning for why row order matters.
	for _, stmt := range []string{
		`DELETE FROM inventory_items WHERE swarm_id = $1`,
		`DELETE FROM inventory_snapshots WHERE swarm_id = $1`,
		`DELETE FROM bridge_swarm_memberships WHERE swarm_id = $1`,
		`DELETE FROM invitations WHERE swarm_id = $1`,
		`DELETE FROM swarm_reference_catalogues WHERE swarm_id = $1`,
		`DELETE FROM swarm_memberships WHERE swarm_id = $1`,
		`DELETE FROM swarms WHERE id = $1`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, string(swarm)); err != nil {
			return fmt.Errorf("directory: deleting Swarm: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("directory: committing Swarm deletion: %w", err)
	}

	_ = d.events.Record(ctx, protocol.Event{Kind: protocol.EventSwarmDeleted, At: d.now(), SwarmID: swarm, ActorUser: account})
	return nil
}

// ListSwarms returns every Swarm account belongs to.
func (d *Directory) ListSwarms(ctx context.Context, account protocol.UserID) ([]Swarm, error) {
	rows, err := d.DB.QueryContext(ctx, `
		SELECT s.id, s.name
		FROM swarms s
		JOIN swarm_memberships m ON m.swarm_id = s.id
		WHERE m.account_id = $1
		ORDER BY s.created_at`, string(account))
	if err != nil {
		return nil, fmt.Errorf("directory: listing Swarms: %w", err)
	}
	defer rows.Close()

	var out []Swarm
	for rows.Next() {
		var s Swarm
		if err := rows.Scan(&s.ID, &s.Name); err != nil {
			return nil, fmt.Errorf("directory: reading Swarm row: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("directory: listing Swarms: %w", err)
	}
	return out, nil
}
