package directory_test

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/host/directory"
	"github.com/Crimson3076/RomM-Swarm/host/hoststore/hoststoretest"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// TestPhase2_ConcurrentRotateIsSerializedByTheMutex is the before/after
// pairing with TestPhase2_ConcurrentRotateWithoutLockingCorruptsState
// (host/hoststore/bridgecredentials_test.go): the exact same scenario — N
// goroutines presenting the same current token for one BridgeID
// concurrently — run through Directory.RotateBridgeCredential instead of
// the bare Store.
//
// The expected outcome is not "exactly one success": auth.Verifier's own
// protocol legitimately allows a second presentation of the token that was
// just superseded to succeed once, via the crash-recovery path (see
// auth/refresh.go's OutcomeRecovered) — that is what lets a Bridge that
// crashed mid-rotation recover. A third or later presentation of the same
// token is indistinguishable from replay and revokes the whole family.
// So with the mutex serializing access, this scenario has exactly one
// correct, deterministic outcome: the first presentation rotates, the
// second recovers, everything from the third onward is rejected as reuse
// and the family ends up revoked. That determinism — the same outcome
// every run — is the proof the mutex fixed the hazard: without it
// (host/hoststore's version of this test), the outcome is undefined and
// varies run to run, with most "successful" callers silently holding a
// token that was never actually live.
func TestPhase2_ConcurrentRotateIsSerializedByTheMutex(t *testing.T) {
	d := newTestDirectory(t)
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
	key := randomKey(t)
	bridgeID, _, _, initial, err := d.RedeemInvitation(ctx, code, key)
	if err != nil {
		t.Fatalf("RedeemInvitation: %v", err)
	}

	const n = 12
	var (
		wg        sync.WaitGroup
		rotated   int64
		recovered int64
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := d.RotateBridgeCredential(ctx, bridgeID, initial)
			if err != nil {
				return
			}
			switch result.Outcome {
			case auth.OutcomeRotated:
				atomic.AddInt64(&rotated, 1)
			case auth.OutcomeRecovered:
				atomic.AddInt64(&recovered, 1)
			}
		}()
	}
	wg.Wait()

	// Deterministic, every run: exactly one normal rotation and exactly
	// one grace-window recovery succeed; everything else is rejected. A
	// racy implementation would make this vary run to run instead.
	if rotated != 1 {
		t.Errorf("normal rotations = %d, want exactly 1", rotated)
	}
	if recovered != 1 {
		t.Errorf("grace-window recoveries = %d, want exactly 1", recovered)
	}

	// A third or later presentation of the same token looks like replay
	// and revokes the whole family — the protocol's own designed
	// response, not a bug.
	if _, err := d.RotateBridgeCredential(ctx, bridgeID, initial); !errors.Is(err, auth.ErrFamilyRevoked) {
		t.Fatalf("rotating a third time: err = %v, want ErrFamilyRevoked (the family should already be revoked by the concurrent replay above)", err)
	}
}

// TestPhase2_ConcurrentRedemptionOfASingleUseInvitationAllowsOnlyOne covers
// the other race named in ADR 0016: two different Bridge keys redeeming
// the same single-use invitation concurrently. The per-BridgeID mutex
// doesn't apply here (different keys, different BridgeIDs) — this is
// purely the invitation UPDATE's own row-level locking.
func TestPhase2_ConcurrentRedemptionOfASingleUseInvitationAllowsOnlyOne(t *testing.T) {
	d := newTestDirectory(t)
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

	const n = 8
	var (
		wg        sync.WaitGroup
		succeeded int64
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, _, _, err := d.RedeemInvitation(ctx, code, randomKey(t)); err == nil {
				atomic.AddInt64(&succeeded, 1)
			}
		}()
	}
	wg.Wait()

	if succeeded != 1 {
		t.Fatalf("concurrent redemptions succeeded = %d, want exactly 1", succeeded)
	}
}

func TestPhase2_RevokedBridgeCannotRotate(t *testing.T) {
	d := newTestDirectory(t)
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
	bridgeID, _, _, token, err := d.RedeemInvitation(ctx, code, randomKey(t))
	if err != nil {
		t.Fatalf("RedeemInvitation: %v", err)
	}

	if err := d.RevokeBridge(ctx, bridgeID, "operator disabled it"); err != nil {
		t.Fatalf("RevokeBridge: %v", err)
	}
	if _, err := d.RotateBridgeCredential(ctx, bridgeID, token); err == nil {
		t.Fatal("RotateBridgeCredential succeeded against a revoked Bridge")
	}
}

func TestPhase2_ReenrolledBridgeCanRotateAgain(t *testing.T) {
	d := newTestDirectory(t)
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
	bridgeID, _, _, _, err := d.RedeemInvitation(ctx, code, randomKey(t))
	if err != nil {
		t.Fatalf("RedeemInvitation: %v", err)
	}
	if err := d.RevokeBridge(ctx, bridgeID, "testing re-enrollment"); err != nil {
		t.Fatalf("RevokeBridge: %v", err)
	}

	fresh, err := d.ReenrollBridge(ctx, bridgeID)
	if err != nil {
		t.Fatalf("ReenrollBridge: %v", err)
	}
	if _, err := d.RotateBridgeCredential(ctx, bridgeID, fresh); err != nil {
		t.Fatalf("RotateBridgeCredential after re-enrollment: %v", err)
	}
}

func TestPhase2_RedeemInvitationRejectsAnInvalidCode(t *testing.T) {
	d := newTestDirectory(t)
	ctx := context.Background()

	if _, _, _, _, err := d.RedeemInvitation(ctx, "not-a-real-code", randomKey(t)); err == nil {
		t.Fatal("RedeemInvitation accepted a code that was never issued")
	}
}

func randomKey(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("generating a test key: %v", err)
	}
	return b
}

func TestPhase2_ListBridgesForSwarmReflectsRevocation(t *testing.T) {
	d := newTestDirectory(t)
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
	bridgeID, _, _, _, err := d.RedeemInvitation(ctx, code, randomKey(t))
	if err != nil {
		t.Fatalf("RedeemInvitation: %v", err)
	}

	before, err := d.ListBridgesForSwarm(ctx, swarmID)
	if err != nil {
		t.Fatalf("ListBridgesForSwarm: %v", err)
	}
	if len(before) != 1 || before[0].BridgeID != bridgeID || before[0].CredentialRevoked {
		t.Fatalf("ListBridgesForSwarm before revoke = %+v, want one entry for %s with CredentialRevoked false", before, bridgeID)
	}

	if err := d.RevokeBridge(ctx, bridgeID, "testing revocation visibility"); err != nil {
		t.Fatalf("RevokeBridge: %v", err)
	}

	after, err := d.ListBridgesForSwarm(ctx, swarmID)
	if err != nil {
		t.Fatalf("ListBridgesForSwarm: %v", err)
	}
	if len(after) != 1 || !after[0].CredentialRevoked {
		t.Fatalf("ListBridgesForSwarm after revoke = %+v, want CredentialRevoked true", after)
	}
}

func TestPhase2_SetBridgeDisplayName(t *testing.T) {
	d := newTestDirectory(t)
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
	bridgeID, _, _, _, err := d.RedeemInvitation(ctx, code, randomKey(t))
	if err != nil {
		t.Fatalf("RedeemInvitation: %v", err)
	}

	before, err := d.ListBridgesForSwarm(ctx, swarmID)
	if err != nil {
		t.Fatalf("ListBridgesForSwarm: %v", err)
	}
	if len(before) != 1 || before[0].DisplayName != "" {
		t.Fatalf("ListBridgesForSwarm before naming = %+v, want an empty DisplayName", before)
	}

	if err := d.SetBridgeDisplayName(ctx, swarmID, bridgeID, "Living Room Shelf"); err != nil {
		t.Fatalf("SetBridgeDisplayName: %v", err)
	}

	after, err := d.ListBridgesForSwarm(ctx, swarmID)
	if err != nil {
		t.Fatalf("ListBridgesForSwarm: %v", err)
	}
	if len(after) != 1 || after[0].DisplayName != "Living Room Shelf" {
		t.Fatalf("ListBridgesForSwarm after naming = %+v, want DisplayName \"Living Room Shelf\"", after)
	}

	// Clearing back to empty is allowed.
	if err := d.SetBridgeDisplayName(ctx, swarmID, bridgeID, ""); err != nil {
		t.Fatalf("SetBridgeDisplayName (clear): %v", err)
	}
	cleared, err := d.ListBridgesForSwarm(ctx, swarmID)
	if err != nil {
		t.Fatalf("ListBridgesForSwarm: %v", err)
	}
	if len(cleared) != 1 || cleared[0].DisplayName != "" {
		t.Fatalf("ListBridgesForSwarm after clearing = %+v, want an empty DisplayName", cleared)
	}

	// A Bridge that never joined this Swarm can't be renamed through it.
	otherSwarmID, err := d.CreateSwarm(ctx, owner, "Other Swarm")
	if err != nil {
		t.Fatalf("CreateSwarm (other): %v", err)
	}
	if err := d.SetBridgeDisplayName(ctx, otherSwarmID, bridgeID, "Should Not Apply"); !errors.Is(err, directory.ErrBridgeNotInSwarm) {
		t.Fatalf("SetBridgeDisplayName for a Bridge not in the Swarm: err = %v, want ErrBridgeNotInSwarm", err)
	}
}

func TestPhase2_RemoveBridgeFromSwarmDeletesMembershipAndInventoryButNotIdentity(t *testing.T) {
	d, _, swarmID, bridgeID, alias, token := enrolledFixture(t)
	ctx := context.Background()

	m := validManifest(swarmID, alias, 1, validItem("item-a", protocol.PlatformGB, 100))
	if _, err := d.PublishInventory(ctx, bridgeID, token, m); err != nil {
		t.Fatalf("PublishInventory: %v", err)
	}

	if err := d.RemoveBridgeFromSwarm(ctx, swarmID, bridgeID); err != nil {
		t.Fatalf("RemoveBridgeFromSwarm: %v", err)
	}

	bridges, err := d.ListBridgesForSwarm(ctx, swarmID)
	if err != nil {
		t.Fatalf("ListBridgesForSwarm: %v", err)
	}
	if len(bridges) != 0 {
		t.Fatalf("ListBridgesForSwarm after removal = %+v, want none", bridges)
	}

	var itemCount, snapshotCount, bridgeCount int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM inventory_items WHERE swarm_id = $1 AND bridge_id = $2`,
		string(swarmID), string(bridgeID)).Scan(&itemCount); err != nil {
		t.Fatalf("counting inventory_items: %v", err)
	}
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM inventory_snapshots WHERE swarm_id = $1 AND bridge_id = $2`,
		string(swarmID), string(bridgeID)).Scan(&snapshotCount); err != nil {
		t.Fatalf("counting inventory_snapshots: %v", err)
	}
	if itemCount != 0 || snapshotCount != 0 {
		t.Fatalf("inventory rows survived removal: items=%d snapshots=%d", itemCount, snapshotCount)
	}

	// The Bridge's own global identity must survive removal from one Swarm.
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM bridges WHERE id = $1`, string(bridgeID)).Scan(&bridgeCount); err != nil {
		t.Fatalf("counting bridges: %v", err)
	}
	if bridgeCount != 1 {
		t.Fatalf("the Bridge's own identity row was deleted along with its Swarm membership (count=%d)", bridgeCount)
	}
}

func TestPhase2_RemoveBridgeFromSwarmRejectsANonMember(t *testing.T) {
	d := newTestDirectory(t)
	ctx := context.Background()

	owner, err := d.BootstrapOwner(ctx, "owner", "The Owner", "", "the-password")
	if err != nil {
		t.Fatalf("BootstrapOwner: %v", err)
	}
	swarmID, err := d.CreateSwarm(ctx, owner, "Test Swarm")
	if err != nil {
		t.Fatalf("CreateSwarm: %v", err)
	}

	nonMember := protocol.BridgeIDFromPublicKey(randomKey(t))
	if err := d.RemoveBridgeFromSwarm(ctx, swarmID, nonMember); !errors.Is(err, directory.ErrBridgeNotInSwarm) {
		t.Fatalf("RemoveBridgeFromSwarm for a non-member: err = %v, want ErrBridgeNotInSwarm", err)
	}
}

// TestPhase2_RedeemInvitationReturnsTheCorrectSwarmAndAlias is ADR 0019's
// enrollment-fix proof: a Bridge cannot compute its own alias (the Swarm's
// alias key never leaves the Host, protocol/alias.go), so RedeemInvitation
// must hand back the correct one directly. The only way a test can verify
// "correct" rather than merely "present" is to read the alias key straight
// out of Postgres and recompute protocol.AliasFor independently.
func TestPhase2_RedeemInvitationReturnsTheCorrectSwarmAndAlias(t *testing.T) {
	dsn := hoststoretest.SkipWithoutPostgres(t)
	db := hoststoretest.OpenDB(t, dsn)
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

	bridgeID, gotSwarmID, gotAlias, _, err := d.RedeemInvitation(ctx, code, randomKey(t))
	if err != nil {
		t.Fatalf("RedeemInvitation: %v", err)
	}
	if gotSwarmID != swarmID {
		t.Fatalf("RedeemInvitation returned SwarmID %s, want %s", gotSwarmID, swarmID)
	}

	var aliasKey []byte
	if err := db.QueryRowContext(ctx, `SELECT alias_key FROM swarms WHERE id = $1`, string(swarmID)).Scan(&aliasKey); err != nil {
		t.Fatalf("reading the Swarm's alias key directly: %v", err)
	}
	want, err := protocol.AliasFor(aliasKey, swarmID, bridgeID)
	if err != nil {
		t.Fatalf("protocol.AliasFor: %v", err)
	}
	if gotAlias != want {
		t.Fatalf("RedeemInvitation returned alias %s, want %s (independently recomputed)", gotAlias, want)
	}
}
