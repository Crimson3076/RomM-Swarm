package directory_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/host/directory"
	"github.com/Crimson3076/RomM-Swarm/host/hoststore"
	"github.com/Crimson3076/RomM-Swarm/host/hoststore/hoststoretest"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// openConcurrentTestDB creates a throwaway Postgres *database* (not just a
// schema) and returns a *sql.DB with an ordinary, multi-connection pool —
// unlike hoststoretest.OpenDB, which pins MaxOpenConns(1) for its
// search_path-based schema isolation and so cannot exercise genuine
// concurrent Postgres transactions. Only the one test that actually needs
// real concurrency uses this; everything else keeps using hoststoretest.
func openConcurrentTestDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("opening the admin connection: %v", err)
	}
	defer admin.Close()

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("crypto/rand unavailable: %v", err)
	}
	name := "concurrency_test_" + hex.EncodeToString(suffix)

	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatalf("creating a throwaway database: %v", err)
	}
	t.Cleanup(func() {
		cleanup, err := sql.Open("pgx", dsn)
		if err != nil {
			return
		}
		defer cleanup.Close()
		_, _ = cleanup.Exec("DROP DATABASE IF EXISTS " + name)
	})

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parsing the test DSN: %v", err)
	}
	u.Path = "/" + name
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatalf("opening the throwaway database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("pinging the throwaway database: %v", err)
	}
	if err := hoststore.ApplySchema(context.Background(), db); err != nil {
		t.Fatalf("applying schema to the throwaway database: %v", err)
	}
	return db
}

// validItem builds a minimal Item that satisfies protocol.Item.Validate
// and is Publishable — everything PublishInventory's own m.Validate() call
// requires, without needing a real DAT/reference set.
func validItem(seed string, platform protocol.PlatformID, size int64) protocol.Item {
	sha := fmt.Sprintf("%064x", []byte(seed))
	return protocol.Item{
		FileID:         protocol.FileIDFromCanonicalDigest(sha),
		Platform:       platform,
		Canonical:      protocol.Digest{SHA256: sha, Size: size},
		Classification: protocol.ClassVerifiedEligible,
		Reference: &protocol.ReferenceMatch{
			Family: "test-family", SetName: "test-set", SetVersion: "1",
			EntryName: "Test Entry", CanonicalKey: "test-entry", Strength: protocol.StrengthStrong,
		},
		Adapter: protocol.AdapterRef{ID: "test-adapter", Version: "1"},
	}
}

func validManifest(swarm protocol.SwarmID, alias protocol.BridgeAlias, revision protocol.Revision, items ...protocol.Item) protocol.Manifest {
	return protocol.Manifest{
		SchemaVersion: protocol.SchemaVersion,
		Swarm:         swarm,
		Alias:         alias,
		Revision:      revision,
		GeneratedAt:   time.Now().UTC(),
		Items:         items,
	}
}

// enrolledFixture sets up an owner, a Swarm, and one enrolled Bridge —
// everything a PublishInventory call needs.
func enrolledFixture(t *testing.T) (d *directory.Directory, owner protocol.UserID, swarmID protocol.SwarmID, bridgeID protocol.BridgeID, alias protocol.BridgeAlias, token auth.Token) {
	t.Helper()
	d = newTestDirectory(t)
	ctx := context.Background()

	owner, err := d.BootstrapOwner(ctx, "owner", "The Owner", "", "the-password")
	if err != nil {
		t.Fatalf("BootstrapOwner: %v", err)
	}
	swarmID, err = d.CreateSwarm(ctx, owner, "Test Swarm")
	if err != nil {
		t.Fatalf("CreateSwarm: %v", err)
	}
	code, _, err := d.IssueInvitation(ctx, swarmID, owner, 1, 0)
	if err != nil {
		t.Fatalf("IssueInvitation: %v", err)
	}
	bridgeID, _, alias, token, err = d.RedeemInvitation(ctx, code, randomKey(t))
	if err != nil {
		t.Fatalf("RedeemInvitation: %v", err)
	}
	return d, owner, swarmID, bridgeID, alias, token
}

func TestPhase2_PublishInventoryRejectsABadToken(t *testing.T) {
	d, _, swarmID, bridgeID, alias, _ := enrolledFixture(t)
	ctx := context.Background()

	m := validManifest(swarmID, alias, 1, validItem("item-a", protocol.PlatformGB, 100))
	if _, err := d.PublishInventory(ctx, bridgeID, "not-the-real-token", m); !errors.Is(err, auth.ErrUnknownToken) {
		t.Fatalf("PublishInventory with a bad token: err = %v, want ErrUnknownToken", err)
	}
}

func TestPhase2_PublishInventoryRejectsABridgeNotEnrolledInTheNamedSwarm(t *testing.T) {
	d, owner, _, bridgeID, alias, token := enrolledFixture(t)
	ctx := context.Background()

	// A second Swarm the Bridge never joined.
	otherSwarm, err := d.CreateSwarm(ctx, owner, "Other Swarm")
	if err != nil {
		t.Fatalf("CreateSwarm (other): %v", err)
	}

	m := validManifest(otherSwarm, alias, 1, validItem("item-a", protocol.PlatformGB, 100))
	if _, err := d.PublishInventory(ctx, bridgeID, token, m); !errors.Is(err, directory.ErrBridgeNotEnrolledInSwarm) {
		t.Fatalf("PublishInventory for a Swarm never joined: err = %v, want ErrBridgeNotEnrolledInSwarm", err)
	}
}

func TestPhase2_PublishInventoryRejectsAnAliasMismatch(t *testing.T) {
	d, _, swarmID, bridgeID, _, token := enrolledFixture(t)
	ctx := context.Background()

	// A validly-shaped alias, just not the one this Bridge actually has in
	// this Swarm -- computed with an unrelated key so it's syntactically
	// valid (passes Manifest.Validate's own Alias.Validate() check) but
	// wrong, exercising PublishInventory's own comparison rather than
	// Manifest.Validate's format check.
	wrongAlias := protocol.MustAliasFor(randomKey(t), swarmID, bridgeID)

	m := validManifest(swarmID, wrongAlias, 1, validItem("item-a", protocol.PlatformGB, 100))
	if _, err := d.PublishInventory(ctx, bridgeID, token, m); !errors.Is(err, directory.ErrInventoryAliasMismatch) {
		t.Fatalf("PublishInventory with a wrong alias: err = %v, want ErrInventoryAliasMismatch", err)
	}
}

func TestPhase2_PublishInventoryHappyPath(t *testing.T) {
	d, _, swarmID, bridgeID, alias, token := enrolledFixture(t)
	ctx := context.Background()

	m := validManifest(swarmID, alias, 1,
		validItem("item-a", protocol.PlatformGB, 100),
		validItem("item-b", protocol.PlatformGBA, 200),
	)
	snap, err := d.PublishInventory(ctx, bridgeID, token, m)
	if err != nil {
		t.Fatalf("PublishInventory: %v", err)
	}
	if snap.BridgeID != bridgeID || snap.SwarmID != swarmID || snap.Revision != 1 || snap.ItemCount != 2 || snap.TotalBytes != 300 {
		t.Fatalf("PublishInventory snapshot = %+v, want BridgeID %s SwarmID %s Revision 1 ItemCount 2 TotalBytes 300",
			snap, bridgeID, swarmID)
	}

	totals, snapshots, err := d.SwarmInventorySummary(ctx, swarmID)
	if err != nil {
		t.Fatalf("SwarmInventorySummary: %v", err)
	}
	if totals.DistinctFiles != 2 || totals.TotalReplicas != 2 {
		t.Fatalf("SwarmInventorySummary totals = %+v, want DistinctFiles 2 TotalReplicas 2", totals)
	}
	if len(snapshots) != 1 || snapshots[0].BridgeID != bridgeID {
		t.Fatalf("SwarmInventorySummary snapshots = %+v, want one entry for %s", snapshots, bridgeID)
	}
}

// TestPhase2_ConcurrentPublishInventoryNeverProducesInconsistentState is the
// positive analogue of TestPhase2_ConcurrentRotateWithoutLockingCorruptsState
// (host/hoststore/bridgecredentials_test.go): N goroutines calling
// PublishInventory concurrently for the same Bridge and Swarm, presenting
// the same still-live token each time (Authenticate never consumes it,
// unlike Rotate — this is exactly the scenario it was designed for).
//
// Uses openConcurrentTestDB, not hoststoretest.OpenDB: that helper pins
// MaxOpenConns(1) for its per-test-schema search_path trick, which would
// silently serialize every call through one connection regardless of
// InventoryStore.Replace's statement order — proving nothing about real
// concurrent Postgres transactions, which is what production actually sees
// (cmd/host's normal, multi-connection pool). This test needs genuine
// concurrent connections to mean anything, confirmed by temporarily
// reverting Replace's statement order and watching this test fail before
// writing it back the safe way.
//
// Unlike the credential race, this path deliberately carries no
// bridgeLocks entry. What makes it safe is InventoryStore.Replace
// upserting the snapshot row first, inside its own transaction — Postgres
// serializes concurrent INSERT ... ON CONFLICT statements against the same
// primary key, so two overlapping Replace calls for the same (Bridge,
// Swarm) queue behind each other there rather than racing. The property
// this test actually checks is the one that would catch a regression:
// whichever manifest's data ends up stored, the snapshot's reported
// ItemCount must always match the real number of inventory_items rows —
// never a torn mix of two different publishes.
func TestPhase2_ConcurrentPublishInventoryNeverProducesInconsistentState(t *testing.T) {
	dsn := hoststoretest.SkipWithoutPostgres(t)
	db := openConcurrentTestDB(t, dsn)
	d := directory.New(db)
	ctx := context.Background()

	owner, err := d.BootstrapOwner(ctx, "owner", "The Owner", "", "the-password")
	if err != nil {
		t.Fatalf("BootstrapOwner: %v", err)
	}
	swarmID, err := d.CreateSwarm(ctx, owner, "Test Swarm")
	if err != nil {
		t.Fatalf("CreateSwarm: %v", err)
	}
	code, _, err := d.IssueInvitation(ctx, swarmID, owner, 1, 0)
	if err != nil {
		t.Fatalf("IssueInvitation: %v", err)
	}
	bridgeID, _, alias, token, err := d.RedeemInvitation(ctx, code, randomKey(t))
	if err != nil {
		t.Fatalf("RedeemInvitation: %v", err)
	}

	const n = 12
	var wg sync.WaitGroup
	var succeeded int64
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			item := validItem(fmt.Sprintf("concurrent-item-%d", i), protocol.PlatformGB, int64(100+i))
			m := validManifest(swarmID, alias, protocol.Revision(i+1), item)
			if _, err := d.PublishInventory(ctx, bridgeID, token, m); err == nil {
				atomic.AddInt64(&succeeded, 1)
			}
		}(i)
	}
	wg.Wait()

	if succeeded == 0 {
		t.Fatal("every concurrent PublishInventory call failed")
	}

	_, snapshots, err := d.SwarmInventorySummary(ctx, swarmID)
	if err != nil {
		t.Fatalf("SwarmInventorySummary: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("SwarmInventorySummary returned %d snapshots, want exactly 1", len(snapshots))
	}

	var actualItemCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM inventory_items WHERE bridge_id = $1 AND swarm_id = $2`,
		string(bridgeID), string(swarmID)).Scan(&actualItemCount); err != nil {
		t.Fatalf("counting actual inventory_items rows: %v", err)
	}
	if actualItemCount != snapshots[0].ItemCount {
		t.Fatalf("snapshot reports ItemCount %d but inventory_items actually has %d rows for this Bridge+Swarm -- "+
			"concurrent publishes left a torn, inconsistent mix behind", snapshots[0].ItemCount, actualItemCount)
	}
}
