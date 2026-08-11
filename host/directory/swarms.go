package directory

import (
	"context"
	"fmt"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

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
