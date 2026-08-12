package hoststore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// InventoryStore persists Bridge inventory snapshots — ADR 0019, slice 1.
type InventoryStore struct {
	DB *sql.DB
}

// InventorySnapshot is one Bridge's latest published inventory for one
// Swarm, as returned to a caller.
type InventorySnapshot struct {
	BridgeID    protocol.BridgeID
	SwarmID     protocol.SwarmID
	Revision    protocol.Revision
	ItemCount   int
	TotalBytes  int64
	GeneratedAt time.Time
	PublishedAt time.Time
}

// Replace overwrites bridge's entire published inventory for swarm with
// manifest's contents — this package's first multi-statement transaction.
// That does not conflict with this package's "no transaction survives a
// call boundary" convention (see bridgecredentials.go's doc comment): that
// convention is about a transaction leaking *across* separate Go-level
// calls (the auth.Store Load-then-Save hazard host/directory's per-Bridge
// mutex exists to close); Replace opens and closes its transaction
// entirely within this one call, which is the property that convention
// actually cares about.
//
// The snapshot row is upserted FIRST, deliberately, before touching
// inventory_items — not last. Postgres serializes concurrent INSERT ...
// ON CONFLICT statements targeting the same primary key (the second
// transaction blocks until the first commits or rolls back, even on the
// very first insert for that key), so upserting the snapshot row first
// is what gives two truly concurrent Replace calls for the same
// (bridge, swarm) their only serialization point — without it, both
// transactions' DELETEs would run against the same pre-both-commits
// snapshot under READ COMMITTED and neither would see the other's
// not-yet-committed inserts, leaving a torn union of both manifests'
// items behind. This is why host/directory's PublishInventory doesn't
// need bridgeLocks: the serialization happens here, structurally, not by
// avoiding concurrency at the caller.
func (s *InventoryStore) Replace(ctx context.Context, bridge protocol.BridgeID, swarm protocol.SwarmID, manifest protocol.Manifest, publishedAt time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("hoststore: starting inventory replace transaction: %w", err)
	}
	defer tx.Rollback()

	var totalBytes int64
	for _, item := range manifest.Items {
		totalBytes += item.Canonical.Size
	}

	fingerprints, err := json.Marshal(manifest.Fingerprints)
	if err != nil {
		return fmt.Errorf("hoststore: encoding manifest fingerprints: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO inventory_snapshots
			(bridge_id, swarm_id, revision, item_count, total_bytes, fingerprints, generated_at, published_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (bridge_id, swarm_id) DO UPDATE SET
			revision     = EXCLUDED.revision,
			item_count   = EXCLUDED.item_count,
			total_bytes  = EXCLUDED.total_bytes,
			fingerprints = EXCLUDED.fingerprints,
			generated_at = EXCLUDED.generated_at,
			published_at = EXCLUDED.published_at`,
		string(bridge), string(swarm), uint64(manifest.Revision), len(manifest.Items), totalBytes,
		fingerprints, manifest.GeneratedAt, publishedAt); err != nil {
		return fmt.Errorf("hoststore: upserting inventory snapshot: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM inventory_items WHERE bridge_id = $1 AND swarm_id = $2`,
		string(bridge), string(swarm)); err != nil {
		return fmt.Errorf("hoststore: clearing previous inventory items: %w", err)
	}

	for _, item := range manifest.Items {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO inventory_items (bridge_id, swarm_id, file_id, platform, canonical_size)
			VALUES ($1, $2, $3, $4, $5)`,
			string(bridge), string(swarm), string(item.FileID), string(item.Platform), item.Canonical.Size); err != nil {
			return fmt.Errorf("hoststore: inserting inventory item %s: %w", item.FileID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("hoststore: committing inventory replace: %w", err)
	}
	return nil
}

// SnapshotFor returns bridge's latest snapshot for swarm, or false if it
// has never published to that Swarm.
func (s *InventoryStore) SnapshotFor(ctx context.Context, bridge protocol.BridgeID, swarm protocol.SwarmID) (InventorySnapshot, bool, error) {
	snap, err := scanSnapshotRow(s.DB.QueryRowContext(ctx, `
		SELECT bridge_id, swarm_id, revision, item_count, total_bytes, generated_at, published_at
		FROM inventory_snapshots
		WHERE bridge_id = $1 AND swarm_id = $2`,
		string(bridge), string(swarm)))
	if errors.Is(err, sql.ErrNoRows) {
		return InventorySnapshot{}, false, nil
	}
	if err != nil {
		return InventorySnapshot{}, false, fmt.Errorf("hoststore: reading inventory snapshot: %w", err)
	}
	return snap, true, nil
}

// PerBridgeSnapshots returns every Bridge's latest snapshot for swarm,
// most recently published first.
func (s *InventoryStore) PerBridgeSnapshots(ctx context.Context, swarm protocol.SwarmID) ([]InventorySnapshot, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT bridge_id, swarm_id, revision, item_count, total_bytes, generated_at, published_at
		FROM inventory_snapshots
		WHERE swarm_id = $1
		ORDER BY published_at DESC`, string(swarm))
	if err != nil {
		return nil, fmt.Errorf("hoststore: listing inventory snapshots: %w", err)
	}
	defer rows.Close()

	var out []InventorySnapshot
	for rows.Next() {
		snap, err := scanSnapshotRow(rows)
		if err != nil {
			return nil, fmt.Errorf("hoststore: reading inventory snapshot row: %w", err)
		}
		out = append(out, snap)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hoststore: listing inventory snapshots: %w", err)
	}
	return out, nil
}

// SwarmInventoryTotals is Swarm-wide inventory arithmetic across every
// Bridge that has published to it.
type SwarmInventoryTotals struct {
	// DistinctFiles is the number of unique FileIDs held by at least one
	// Bridge in the Swarm.
	DistinctFiles int
	// TotalReplicas is the sum of every Bridge's published item count —
	// how many (Bridge, file) pairs exist, counting duplicates.
	TotalReplicas int
	// DuplicatedFiles is the number of distinct FileIDs held by more than
	// one Bridge.
	DuplicatedFiles int
}

// SwarmTotals computes cross-Bridge inventory arithmetic for swarm.
func (s *InventoryStore) SwarmTotals(ctx context.Context, swarm protocol.SwarmID) (SwarmInventoryTotals, error) {
	var totals SwarmInventoryTotals

	if err := s.DB.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT file_id) FROM inventory_items WHERE swarm_id = $1`,
		string(swarm)).Scan(&totals.DistinctFiles); err != nil {
		return SwarmInventoryTotals{}, fmt.Errorf("hoststore: counting distinct inventory files: %w", err)
	}

	if err := s.DB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT file_id FROM inventory_items WHERE swarm_id = $1
			GROUP BY file_id HAVING COUNT(DISTINCT bridge_id) > 1
		) duplicated`,
		string(swarm)).Scan(&totals.DuplicatedFiles); err != nil {
		return SwarmInventoryTotals{}, fmt.Errorf("hoststore: counting duplicated inventory files: %w", err)
	}

	var totalReplicas sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, `
		SELECT SUM(item_count) FROM inventory_snapshots WHERE swarm_id = $1`,
		string(swarm)).Scan(&totalReplicas); err != nil {
		return SwarmInventoryTotals{}, fmt.Errorf("hoststore: summing inventory replicas: %w", err)
	}
	totals.TotalReplicas = int(totalReplicas.Int64)

	return totals, nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows, letting
// scanSnapshotRow share one column list between SnapshotFor's single-row
// read and PerBridgeSnapshots' multi-row read.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanSnapshotRow(row rowScanner) (InventorySnapshot, error) {
	var (
		snap     InventorySnapshot
		bridgeID string
		swarmID  string
		revision uint64
	)
	if err := row.Scan(&bridgeID, &swarmID, &revision, &snap.ItemCount, &snap.TotalBytes, &snap.GeneratedAt, &snap.PublishedAt); err != nil {
		return InventorySnapshot{}, err
	}
	snap.BridgeID = protocol.BridgeID(bridgeID)
	snap.SwarmID = protocol.SwarmID(swarmID)
	snap.Revision = protocol.Revision(revision)
	return snap, nil
}
