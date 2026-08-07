package publish

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

var at = time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

// holding builds a publishable local item.
func holding(name string, platform protocol.PlatformID, size int64) protocol.Item {
	digest := protocol.DigestBytes([]byte(name))
	digest.Size = size
	return protocol.Item{
		FileID:    protocol.FileIDFromCanonicalDigest(digest.SHA256),
		GameID:    protocol.GameIDFromReference("no-intro", name),
		Platform:  platform,
		Stored:    digest,
		Canonical: digest,
		Adapter:   protocol.AdapterRef{ID: "raw", Version: "1"},
		Reference: &protocol.ReferenceMatch{
			Family: "no-intro", SetName: "Test", SetVersion: "1",
			EntryName: name, CanonicalKey: name, Strength: protocol.StrengthCatalogue,
		},
		Classification: protocol.ClassVerifiedEligible,
		Availability:   protocol.AvailAvailable,
		LastVerifiedAt: at,
	}
}

func membership(swarm protocol.SwarmID, policy Policy) Membership {
	policy.Swarm = swarm
	return Membership{
		Swarm:  swarm,
		Alias:  protocol.MustAliasFor(protocol.NewSwarmAliasKey(), swarm, protocol.BridgeIDFromPublicKey([]byte("bridge"))),
		Policy: policy,
	}
}

// TestPhase0_OneBridgePublishesDifferentManifestsWithoutLeaking is Phase 1
// acceptance: "The same Bridge can publish different filtered manifests to
// different Swarms without leaking excluded holdings", and Phase 3 acceptance:
// excluded inventory "never enters that Swarm's catalog or change feed".
func TestPhase0_OneBridgePublishesDifferentManifestsWithoutLeaking(t *testing.T) {
	// One local scan. The Bridge holds Game Boy and Mega Drive titles, plus one
	// item the owner withholds from everyone.
	secret := holding("Private Prototype", protocol.PlatformGB, 1024)
	holdings := []protocol.Item{
		holding("Kirby", protocol.PlatformGB, 262144),
		holding("Zelda", protocol.PlatformGB, 1048576),
		holding("Sonic", protocol.PlatformGenesis, 524288),
		holding("Streets of Rage", protocol.PlatformGenesis, 1048576),
		secret,
	}

	swarmA, swarmB := protocol.NewSwarmID(), protocol.NewSwarmID()

	// Swarm A sees only Game Boy, and never the withheld item.
	memberA := membership(swarmA, Policy{
		Platforms:     []protocol.PlatformID{protocol.PlatformGB},
		ExcludedFiles: []protocol.FileID{secret.FileID},
	})
	// Swarm B sees only Mega Drive.
	memberB := membership(swarmB, Policy{
		Platforms:     []protocol.PlatformID{protocol.PlatformGenesis},
		ExcludedFiles: []protocol.FileID{secret.FileID},
	})

	manifestA, err := Snapshot(memberA, holdings, at)
	if err != nil {
		t.Fatalf("Snapshot for swarm A: %v", err)
	}
	manifestB, err := Snapshot(memberB, holdings, at)
	if err != nil {
		t.Fatalf("Snapshot for swarm B: %v", err)
	}

	if len(manifestA.Items) != 2 || len(manifestB.Items) != 2 {
		t.Fatalf("swarm A got %d items, swarm B got %d; want 2 each", len(manifestA.Items), len(manifestB.Items))
	}

	// Nothing crosses between the two manifests.
	inA := map[protocol.FileID]bool{}
	for _, item := range manifestA.Items {
		inA[item.FileID] = true
		if item.Platform != protocol.PlatformGB {
			t.Errorf("swarm A received a %s holding", item.Platform)
		}
	}
	for _, item := range manifestB.Items {
		if inA[item.FileID] {
			t.Errorf("holding %s appears in both swarms' manifests", item.FileID)
		}
		if item.Platform != protocol.PlatformGenesis {
			t.Errorf("swarm B received a %s holding", item.Platform)
		}
	}

	// The withheld item must not appear anywhere in either manifest — not in the
	// items, not in a fingerprint input, not anywhere in the serialised form.
	for _, m := range []protocol.Manifest{manifestA, manifestB} {
		encoded, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("encoding the manifest: %v", err)
		}
		if strings.Contains(string(encoded), string(secret.FileID)) {
			t.Fatal("a withheld holding appears in a published manifest")
		}
		if strings.Contains(string(encoded), secret.Canonical.SHA256) {
			t.Fatal("a withheld holding's digest appears in a published manifest")
		}
	}

	// Each Swarm sees a different alias, and its own revision sequence.
	if manifestA.Alias == manifestB.Alias {
		t.Error("the same alias was presented to both swarms")
	}
	if manifestA.Swarm == manifestB.Swarm {
		t.Error("both manifests carry the same swarm id")
	}
}

// Fingerprints are computed over the filtered set, so a Host comparing them
// learns nothing about what was withheld.
func TestFingerprintsCoverOnlyPublishedHoldings(t *testing.T) {
	shared := holding("Shared", protocol.PlatformGB, 1024)
	withheld := holding("Withheld", protocol.PlatformGB, 2048)

	swarm := protocol.NewSwarmID()
	policy := Policy{Platforms: []protocol.PlatformID{protocol.PlatformGB}}
	m := membership(swarm, policy)

	onlyShared, err := Snapshot(m, []protocol.Item{shared}, at)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	m.Policy.ExcludedFiles = []protocol.FileID{withheld.FileID}
	bothHeld, err := Snapshot(m, []protocol.Item{shared, withheld}, at)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Holding an extra file that this Swarm may not see must not change the
	// fingerprint it is shown.
	if onlyShared.Fingerprints[protocol.PlatformGB] != bothHeld.Fingerprints[protocol.PlatformGB] {
		t.Fatal("acquiring a withheld holding changed the fingerprint published to a swarm that cannot see it")
	}
}

// TestPhase0_UnverifiedContentIsNeverPublished is Scope of Work §3's central
// rule, enforced before any owner configuration is consulted.
func TestPhase0_UnverifiedContentIsNeverPublished(t *testing.T) {
	swarm := protocol.NewSwarmID()
	// The most permissive policy an owner can write.
	m := membership(swarm, Policy{ShareAll: true})

	verified := holding("Verified", protocol.PlatformGB, 1024)

	for _, class := range []protocol.Classification{
		protocol.ClassUnmatched,
		protocol.ClassMatchedUnverified,
		protocol.ClassVerifiedExcluded,
		protocol.ClassConflict,
	} {
		item := holding("Unpublishable "+string(class), protocol.PlatformGB, 1024)
		item.Classification = class

		manifest, err := Snapshot(m, []protocol.Item{verified, item}, at)
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if len(manifest.Items) != 1 {
			t.Fatalf("class %s: published %d items, want only the verified one", class, len(manifest.Items))
		}
		if manifest.Items[0].FileID != verified.FileID {
			t.Errorf("class %s: the wrong item was published", class)
		}
	}
}

// TestPhase0_RepeatedScansProduceNoChange is Phase 1 acceptance: "Repeated scans
// do not generate false additions or deletions."
func TestPhase0_RepeatedScansProduceNoChange(t *testing.T) {
	holdings := []protocol.Item{
		holding("A", protocol.PlatformGB, 1024),
		holding("B", protocol.PlatformGB, 2048),
	}
	m := membership(protocol.NewSwarmID(), Policy{ShareAll: true})

	first, err := Snapshot(m, holdings, at)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Rescan an hour later. Revalidation timestamps have moved on; nothing else
	// has. A Bridge that republished here would turn a daily rescan into a daily
	// stream of activity for the Host to record.
	later := at.Add(time.Hour)
	for i := range holdings {
		holdings[i].LastVerifiedAt = later
	}

	m.Revision = first.Revision
	if _, changed, err := Diff(first, m, holdings, later); err != nil {
		t.Fatalf("Diff: %v", err)
	} else if changed {
		t.Fatal("an unchanged library produced a delta")
	}

	// Scan order must not matter either.
	reversed := []protocol.Item{holdings[1], holdings[0]}
	if _, changed, err := Diff(first, m, reversed, later); err != nil {
		t.Fatalf("Diff: %v", err)
	} else if changed {
		t.Fatal("rescanning in a different order produced a delta")
	}
}

func TestDiffReportsAdditionsUpdatesAndTombstones(t *testing.T) {
	a := holding("A", protocol.PlatformGB, 1024)
	b := holding("B", protocol.PlatformGB, 2048)
	c := holding("C", protocol.PlatformGB, 4096)

	m := membership(protocol.NewSwarmID(), Policy{ShareAll: true})
	previous, err := Snapshot(m, []protocol.Item{a, b}, at)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	m.Revision = previous.Revision

	// A is gone, B went offline, C is new.
	b.Availability = protocol.AvailOffline
	delta, changed, err := Diff(previous, m, []protocol.Item{b, c}, at)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !changed {
		t.Fatal("a changed library produced no delta")
	}

	ops := map[protocol.FileID]protocol.DeltaOp{}
	for _, e := range delta.Entries {
		ops[e.FileID] = e.Op
	}
	if ops[c.FileID] != protocol.DeltaAdd {
		t.Errorf("new holding recorded as %s, want add", ops[c.FileID])
	}
	if ops[b.FileID] != protocol.DeltaUpdate {
		t.Errorf("an availability change recorded as %s, want update", ops[b.FileID])
	}
	if ops[a.FileID] != protocol.DeltaTombstone {
		t.Errorf("a removed holding recorded as %s, want tombstone", ops[a.FileID])
	}

	if delta.To != delta.From.Next() {
		t.Errorf("revision moved from %d to %d", delta.From, delta.To)
	}
}

// Withdrawing an item by policy must look exactly like deleting it. Saying which
// happened would leak the owner's reason for withholding.
func TestPolicyWithdrawalIsIndistinguishableFromDeletion(t *testing.T) {
	a := holding("Kept", protocol.PlatformGB, 1024)
	b := holding("Withdrawn", protocol.PlatformGB, 2048)

	m := membership(protocol.NewSwarmID(), Policy{ShareAll: true})
	previous, err := Snapshot(m, []protocol.Item{a, b}, at)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	m.Revision = previous.Revision

	// The owner withdraws B. It is still on disk.
	m.Policy.ExcludedFiles = []protocol.FileID{b.FileID}
	delta, changed, err := Diff(previous, m, []protocol.Item{a, b}, at)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !changed || len(delta.Entries) != 1 {
		t.Fatalf("expected one change, got changed=%v entries=%d", changed, len(delta.Entries))
	}
	entry := delta.Entries[0]
	if entry.Op != protocol.DeltaTombstone || entry.FileID != b.FileID {
		t.Fatalf("withdrawal produced %s for %s, want a tombstone for the withdrawn item", entry.Op, entry.FileID)
	}
	if entry.Item != nil {
		t.Error("the tombstone carries item data, which would reveal what was withdrawn")
	}
}

// An unconfigured policy must share nothing. For a mechanism that decides what
// leaves a member's server, "unconfigured" cannot mean "everything".
func TestTheDefaultPolicySharesNothing(t *testing.T) {
	var p Policy
	item := holding("Anything", protocol.PlatformGB, 1024)
	if p.Permits(item) {
		t.Fatal("the zero-value policy published a holding")
	}

	m := Membership{
		Swarm:  protocol.NewSwarmID(),
		Alias:  protocol.MustAliasFor(protocol.NewSwarmAliasKey(), protocol.NewSwarmID(), protocol.BridgeIDFromPublicKey([]byte("b"))),
		Policy: Policy{},
	}
	m.Policy.Swarm = m.Swarm
	if _, err := Snapshot(m, []protocol.Item{item}, at); err == nil {
		t.Fatal("an unconfigured membership published a manifest")
	}
}

func TestExclusionsOverrideShareAll(t *testing.T) {
	gb := holding("GB game", protocol.PlatformGB, 1024)
	genesis := holding("Genesis game", protocol.PlatformGenesis, 1024)
	huge := holding("Huge game", protocol.PlatformGB, 1<<30)

	p := Policy{
		Swarm:             protocol.NewSwarmID(),
		ShareAll:          true,
		ExcludedPlatforms: []protocol.PlatformID{protocol.PlatformGenesis},
		MaxItemBytes:      1 << 20,
	}

	if !p.Permits(gb) {
		t.Error("ShareAll did not publish an ordinary holding")
	}
	if p.Permits(genesis) {
		t.Error("an excluded platform was published under ShareAll")
	}
	if p.Permits(huge) {
		t.Error("a holding above the size limit was published under ShareAll")
	}
}

// A policy an owner cannot inspect before applying is one nobody will trust.
func TestPreviewExplainsWhatWouldBePublishedAndWhy(t *testing.T) {
	unverified := holding("Unverified", protocol.PlatformGB, 1024)
	unverified.Classification = protocol.ClassMatchedUnverified
	secret := holding("Secret", protocol.PlatformGB, 1024)
	huge := holding("Huge", protocol.PlatformGB, 1<<30)

	holdings := []protocol.Item{
		holding("Published A", protocol.PlatformGB, 1024),
		holding("Published B", protocol.PlatformGB, 2048),
		holding("Other platform", protocol.PlatformGenesis, 1024),
		unverified,
		secret,
		huge,
	}

	p := Policy{
		Swarm:         protocol.NewSwarmID(),
		Platforms:     []protocol.PlatformID{protocol.PlatformGB},
		ExcludedFiles: []protocol.FileID{secret.FileID},
		MaxItemBytes:  1 << 20,
	}

	preview := PreviewPolicy(p, holdings)

	if preview.Published != 2 {
		t.Errorf("preview says %d published, want 2", preview.Published)
	}
	if preview.Withheld != 4 {
		t.Errorf("preview says %d withheld, want 4", preview.Withheld)
	}
	if preview.PublishedBytes != 3072 {
		t.Errorf("preview says %d bytes, want 3072", preview.PublishedBytes)
	}
	if preview.ByPlatform[protocol.PlatformGB] != 2 {
		t.Errorf("preview counts %d Game Boy holdings", preview.ByPlatform[protocol.PlatformGB])
	}

	// An owner needs to tell a deliberate exclusion from an accidental one.
	for _, want := range []string{
		"not verified against the reference catalogue",
		"excluded individually",
		"larger than the size limit",
		"platform not shared with this Swarm",
	} {
		if preview.WithheldReasons[want] == 0 {
			t.Errorf("preview does not report the reason %q; got %v", want, preview.WithheldReasons)
		}
	}
}

func TestSnapshotIsDeterministic(t *testing.T) {
	holdings := []protocol.Item{
		holding("C", protocol.PlatformGB, 1024),
		holding("A", protocol.PlatformGB, 1024),
		holding("B", protocol.PlatformGB, 1024),
	}
	m := membership(protocol.NewSwarmID(), Policy{ShareAll: true})

	first, err := Snapshot(m, holdings, at)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	shuffled := []protocol.Item{holdings[2], holdings[0], holdings[1]}
	second, err := Snapshot(m, shuffled, at)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Fatal("two scans of the same library in different orders produced different manifests")
	}
}

// One canonical payload held twice locally is one holding to the Swarm.
// Publishing it twice would invent a replica out of a duplicate file.
func TestLocalDuplicatesArePublishedOnce(t *testing.T) {
	item := holding("Duplicated", protocol.PlatformGB, 1024)
	m := membership(protocol.NewSwarmID(), Policy{ShareAll: true})

	manifest, err := Snapshot(m, []protocol.Item{item, item}, at)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(manifest.Items) != 1 {
		t.Fatalf("a locally duplicated holding was published %d times", len(manifest.Items))
	}
}

func TestMembershipValidation(t *testing.T) {
	swarm := protocol.NewSwarmID()
	good := membership(swarm, Policy{ShareAll: true})
	if err := good.Validate(); err != nil {
		t.Fatalf("a valid membership was rejected: %v", err)
	}

	// A policy for a different Swarm than the membership is a configuration
	// mistake that would publish one Swarm's rules to another.
	mismatched := good
	mismatched.Policy.Swarm = protocol.NewSwarmID()
	if err := mismatched.Validate(); err == nil {
		t.Error("a policy for the wrong swarm was accepted")
	}

	noAlias := good
	noAlias.Alias = ""
	if err := noAlias.Validate(); err == nil {
		t.Error("a membership with no alias was accepted")
	}
}

func TestSnapshotWithNoPermittedHoldings(t *testing.T) {
	m := membership(protocol.NewSwarmID(), Policy{Platforms: []protocol.PlatformID{protocol.PlatformNDS}})
	_, err := Snapshot(m, []protocol.Item{holding("GB only", protocol.PlatformGB, 1024)}, at)
	if !errors.Is(err, ErrNothingToPublish) {
		t.Fatalf("got %v, want ErrNothingToPublish", err)
	}
}
