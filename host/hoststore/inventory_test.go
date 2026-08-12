package hoststore_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/host/hoststore"
	"github.com/Crimson3076/RomM-Swarm/host/hoststore/hoststoretest"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// newTestSwarm inserts the minimal accounts+swarms fixture InventoryStore's
// foreign keys require, via raw SQL -- hoststore's own tests deliberately
// don't import host/directory, the same layering every other test in this
// package already keeps.
func newTestSwarm(t *testing.T, db *sql.DB, name string) protocol.SwarmID {
	t.Helper()
	ctx := context.Background()
	accountID := protocol.NewUserID()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO accounts (id, username, display_name, password_hash, is_owner, created_at)
		VALUES ($1, $2, $2, 'x', TRUE, $3)`,
		string(accountID), "owner-"+string(accountID), time.Now()); err != nil {
		t.Fatalf("seeding a test account: %v", err)
	}
	swarmID := protocol.NewSwarmID()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO swarms (id, name, alias_key, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5)`,
		string(swarmID), name, protocol.NewSwarmAliasKey(), string(accountID), time.Now()); err != nil {
		t.Fatalf("seeding a test swarm: %v", err)
	}
	return swarmID
}

func newTestBridge(t *testing.T, db *sql.DB, seed string) protocol.BridgeID {
	t.Helper()
	id := protocol.BridgeIDFromPublicKey([]byte(seed))
	if err := hoststore.EnsureBridge(context.Background(), db, id, []byte(seed)); err != nil {
		t.Fatalf("EnsureBridge: %v", err)
	}
	return id
}

// testItem builds a minimal, internally-consistent Item -- enough for
// InventoryStore.Replace, which only touches FileID/Platform/Canonical.Size
// and never calls Manifest.Validate itself.
func testItem(seed string, platform protocol.PlatformID, size int64) protocol.Item {
	sha := fmt.Sprintf("%064x", []byte(seed))
	return protocol.Item{
		FileID:    protocol.FileIDFromCanonicalDigest(sha),
		Platform:  platform,
		Canonical: protocol.Digest{SHA256: sha, Size: size},
	}
}

func testManifest(swarm protocol.SwarmID, revision protocol.Revision, items ...protocol.Item) protocol.Manifest {
	return protocol.Manifest{
		SchemaVersion: protocol.SchemaVersion,
		Swarm:         swarm,
		Revision:      revision,
		GeneratedAt:   time.Now().UTC(),
		Items:         items,
	}
}

// TestPhase2_InventoryReplaceOverwritesNotAccumulates proves Replace does
// what its name says: a second publish with a different item set leaves
// exactly that set behind, not the union of both.
func TestPhase2_InventoryReplaceOverwritesNotAccumulates(t *testing.T) {
	dsn := hoststoretest.SkipWithoutPostgres(t)
	db := hoststoretest.OpenDB(t, dsn)
	store := &hoststore.InventoryStore{DB: db}
	ctx := context.Background()

	swarmID := newTestSwarm(t, db, "Test Swarm")
	bridgeID := newTestBridge(t, db, "replace-test-bridge")

	first := testManifest(swarmID, 1,
		testItem("item-a", protocol.PlatformGB, 100),
		testItem("item-b", protocol.PlatformGB, 200),
	)
	if err := store.Replace(ctx, bridgeID, swarmID, first, time.Now()); err != nil {
		t.Fatalf("first Replace: %v", err)
	}

	snap, ok, err := store.SnapshotFor(ctx, bridgeID, swarmID)
	if err != nil || !ok {
		t.Fatalf("SnapshotFor after first Replace: ok=%v err=%v", ok, err)
	}
	if snap.ItemCount != 2 || snap.TotalBytes != 300 || snap.Revision != 1 {
		t.Fatalf("snapshot after first Replace = %+v, want ItemCount 2, TotalBytes 300, Revision 1", snap)
	}

	second := testManifest(swarmID, 2, testItem("item-c", protocol.PlatformGBA, 50))
	if err := store.Replace(ctx, bridgeID, swarmID, second, time.Now()); err != nil {
		t.Fatalf("second Replace: %v", err)
	}

	snap, ok, err = store.SnapshotFor(ctx, bridgeID, swarmID)
	if err != nil || !ok {
		t.Fatalf("SnapshotFor after second Replace: ok=%v err=%v", ok, err)
	}
	if snap.ItemCount != 1 || snap.TotalBytes != 50 || snap.Revision != 2 {
		t.Fatalf("snapshot after second Replace = %+v, want ItemCount 1, TotalBytes 50, Revision 2 (overwritten, not accumulated)", snap)
	}

	totals, err := store.SwarmTotals(ctx, swarmID)
	if err != nil {
		t.Fatalf("SwarmTotals: %v", err)
	}
	if totals.DistinctFiles != 1 {
		t.Fatalf("SwarmTotals.DistinctFiles = %d, want 1 (item-a/item-b should be gone)", totals.DistinctFiles)
	}
}

// TestPhase2_SwarmTotalsDedupeArithmetic is the cross-Bridge stats math:
// two Bridges sharing one FileID must count that file once toward
// DistinctFiles and once toward DuplicatedFiles, while each Bridge's
// unique file only adds to DistinctFiles.
func TestPhase2_SwarmTotalsDedupeArithmetic(t *testing.T) {
	dsn := hoststoretest.SkipWithoutPostgres(t)
	db := hoststoretest.OpenDB(t, dsn)
	store := &hoststore.InventoryStore{DB: db}
	ctx := context.Background()

	swarmID := newTestSwarm(t, db, "Dedupe Swarm")
	bridgeA := newTestBridge(t, db, "dedupe-bridge-a")
	bridgeB := newTestBridge(t, db, "dedupe-bridge-b")

	shared := testItem("shared-rom", protocol.PlatformGB, 100)
	onlyA := testItem("only-a", protocol.PlatformGB, 50)
	onlyB := testItem("only-b", protocol.PlatformGBA, 75)

	if err := store.Replace(ctx, bridgeA, swarmID, testManifest(swarmID, 1, shared, onlyA), time.Now()); err != nil {
		t.Fatalf("Replace for bridgeA: %v", err)
	}
	if err := store.Replace(ctx, bridgeB, swarmID, testManifest(swarmID, 1, shared, onlyB), time.Now()); err != nil {
		t.Fatalf("Replace for bridgeB: %v", err)
	}

	totals, err := store.SwarmTotals(ctx, swarmID)
	if err != nil {
		t.Fatalf("SwarmTotals: %v", err)
	}
	if totals.DistinctFiles != 3 {
		t.Errorf("DistinctFiles = %d, want 3 (shared, only-a, only-b)", totals.DistinctFiles)
	}
	if totals.DuplicatedFiles != 1 {
		t.Errorf("DuplicatedFiles = %d, want 1 (only \"shared\" is held by more than one Bridge)", totals.DuplicatedFiles)
	}
	if totals.TotalReplicas != 4 {
		t.Errorf("TotalReplicas = %d, want 4 (2 items from each of 2 Bridges)", totals.TotalReplicas)
	}

	perBridge, err := store.PerBridgeSnapshots(ctx, swarmID)
	if err != nil {
		t.Fatalf("PerBridgeSnapshots: %v", err)
	}
	if len(perBridge) != 2 {
		t.Fatalf("PerBridgeSnapshots returned %d snapshots, want 2", len(perBridge))
	}
	for _, snap := range perBridge {
		if snap.ItemCount != 2 {
			t.Errorf("Bridge %s snapshot ItemCount = %d, want 2", snap.BridgeID, snap.ItemCount)
		}
	}
}
