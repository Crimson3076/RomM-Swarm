package directory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/host/hoststore"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// ErrBridgeNotEnrolledInSwarm means bridge has no active membership in the
// Swarm a manifest names — it never joined that Swarm, or its membership
// there has been disabled or revoked.
var ErrBridgeNotEnrolledInSwarm = errors.New("directory: bridge is not actively enrolled in this swarm")

// ErrInventoryAliasMismatch means a manifest's self-reported alias does not
// match the one the Host computes for this Bridge in this Swarm. This can
// only mean a bug or bit-rotted local Bridge state — the Swarm's alias key
// never leaves the Host, so a mismatch cannot be a forged value from an
// attacker who doesn't already hold the Bridge's refresh token. See ADR
// 0019's open questions for whether this should someday warn rather than
// reject.
var ErrInventoryAliasMismatch = errors.New("directory: manifest alias does not match the bridge's swarm-scoped alias")

// PublishInventory authenticates bridge — a read-only credential check, not
// a rotation; see auth.Verifier.Authenticate's own doc comment and ADR
// 0019, resolved sub-decision 1 — then validates and stores manifest as
// this Bridge's current inventory for the Swarm it names.
//
// Deliberately does not use the per-BridgeID mutex bridgeLocks provides:
// that lock exists to serialize auth.Store's Load-then-Save race on
// bridge_credential_families (ADR 0016, resolved sub-decision 1).
// Authenticate never calls Save, and InventoryStore.Replace gets its own
// atomicity from a single transaction — there is no cross-call
// read-modify-write hazard here for a mutex to close.
func (d *Directory) PublishInventory(ctx context.Context, bridge protocol.BridgeID, presented auth.Token, m protocol.Manifest) (hoststore.InventorySnapshot, error) {
	if err := d.verifier.Authenticate(bridge, presented); err != nil {
		_ = d.events.Record(ctx, protocol.Event{Kind: protocol.EventAuthFailure, At: d.now(), ActorBridge: bridge})
		return hoststore.InventorySnapshot{}, err
	}
	if err := m.Validate(); err != nil {
		return hoststore.InventorySnapshot{}, err
	}

	var (
		disabledAt sql.NullTime
		revokedAt  sql.NullTime
		aliasKey   []byte
	)
	err := d.DB.QueryRowContext(ctx, `
		SELECT m.disabled_at, m.revoked_at, s.alias_key
		FROM bridge_swarm_memberships m
		JOIN swarms s ON s.id = m.swarm_id
		WHERE m.bridge_id = $1 AND m.swarm_id = $2`,
		string(bridge), string(m.Swarm),
	).Scan(&disabledAt, &revokedAt, &aliasKey)
	if errors.Is(err, sql.ErrNoRows) {
		return hoststore.InventorySnapshot{}, ErrBridgeNotEnrolledInSwarm
	}
	if err != nil {
		return hoststore.InventorySnapshot{}, fmt.Errorf("directory: looking up bridge membership: %w", err)
	}
	if disabledAt.Valid || revokedAt.Valid {
		return hoststore.InventorySnapshot{}, ErrBridgeNotEnrolledInSwarm
	}

	wantAlias, err := protocol.AliasFor(aliasKey, m.Swarm, bridge)
	if err != nil {
		return hoststore.InventorySnapshot{}, fmt.Errorf("directory: computing expected bridge alias: %w", err)
	}
	if wantAlias != m.Alias {
		return hoststore.InventorySnapshot{}, ErrInventoryAliasMismatch
	}

	now := d.now()
	if err := d.inventory.Replace(ctx, bridge, m.Swarm, m, now); err != nil {
		return hoststore.InventorySnapshot{}, err
	}
	_ = d.events.Record(ctx, protocol.Event{
		Kind: protocol.EventInventorySnapshot, At: now, SwarmID: m.Swarm, ActorBridge: bridge,
	})

	snap, ok, err := d.inventory.SnapshotFor(ctx, bridge, m.Swarm)
	if err != nil {
		return hoststore.InventorySnapshot{}, err
	}
	if !ok {
		return hoststore.InventorySnapshot{}, fmt.Errorf("directory: inventory snapshot missing immediately after a successful publish")
	}
	return snap, nil
}

// SwarmInventorySummary returns swarm-wide inventory arithmetic plus every
// Bridge's latest snapshot — the read side both UIs use.
func (d *Directory) SwarmInventorySummary(ctx context.Context, swarm protocol.SwarmID) (hoststore.SwarmInventoryTotals, []hoststore.InventorySnapshot, error) {
	totals, err := d.inventory.SwarmTotals(ctx, swarm)
	if err != nil {
		return hoststore.SwarmInventoryTotals{}, nil, err
	}
	snapshots, err := d.inventory.PerBridgeSnapshots(ctx, swarm)
	if err != nil {
		return hoststore.SwarmInventoryTotals{}, nil, err
	}
	return totals, snapshots, nil
}
