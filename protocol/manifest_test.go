package protocol

import (
	"strings"
	"testing"
	"time"
)

// buildItem produces a valid, publishable item for test manipulation.
func buildItem(t *testing.T, payload string) Item {
	t.Helper()
	canon := DigestBytes([]byte(payload))
	return Item{
		FileID:    FileIDFromCanonicalDigest(canon.SHA256),
		GameID:    GameIDFromReference("no-intro", payload),
		Platform:  PlatformGB,
		Stored:    canon,
		Canonical: canon,
		Adapter:   AdapterRef{ID: "raw", Version: "1"},
		Reference: &ReferenceMatch{
			Family:       "no-intro",
			SetName:      "Nintendo - Game Boy",
			SetVersion:   "20260101-000000",
			EntryName:    payload + ".gb",
			CanonicalKey: payload,
			Strength:     StrengthCatalogue,
		},
		Classification: ClassVerifiedEligible,
		Availability:   AvailAvailable,
		LastVerifiedAt: time.Now(),
	}
}

func buildManifest(t *testing.T, items ...Item) Manifest {
	t.Helper()
	m := Manifest{
		SchemaVersion: SchemaVersion,
		Swarm:         NewSwarmID(),
		Alias:         MustAliasFor(NewSwarmAliasKey(), NewSwarmID(), BridgeIDFromPublicKey([]byte("k"))),
		Revision:      1,
		GeneratedAt:   time.Now(),
		Items:         items,
	}
	m.ComputeFingerprints()
	return m
}

// TestPhase0_OnlyVerifiedEligibleContentIsPublishable is Phase 4 acceptance:
// unmatched and hash-unverified content never appears in the Swarm catalog.
func TestPhase0_OnlyVerifiedEligibleContentIsPublishable(t *testing.T) {
	classes := map[Classification]bool{
		ClassUnmatched:         false,
		ClassMatchedUnverified: false,
		ClassVerifiedExcluded:  false,
		ClassVerifiedEligible:  true,
		ClassConflict:          false,
	}
	for class, wantPublishable := range classes {
		if class.Publishable() != wantPublishable {
			t.Errorf("class %s: Publishable() = %v, want %v", class, class.Publishable(), wantPublishable)
		}

		item := buildItem(t, "Some Game")
		item.Classification = class
		m := buildManifest(t, item)
		err := m.Validate()
		if wantPublishable && err != nil {
			t.Errorf("class %s: manifest rejected a publishable item: %v", class, err)
		}
		if !wantPublishable && err == nil {
			t.Errorf("class %s: manifest accepted an item that must not be published", class)
		}
	}
}

func TestItemValidationCatchesForgedFileIDs(t *testing.T) {
	item := buildItem(t, "Game A")
	item.FileID = FileIDFromCanonicalDigest(DigestBytes([]byte("Game B")).SHA256)
	if err := item.Validate(); err == nil {
		t.Fatal("Validate accepted an item whose file id does not derive from its canonical digest")
	}
}

func TestItemValidationRequiresAnAdapterReference(t *testing.T) {
	item := buildItem(t, "Game A")
	item.Adapter = AdapterRef{}
	if err := item.Validate(); err == nil {
		t.Fatal("Validate accepted a verified item with no canonicalization adapter recorded")
	}
}

func TestItemValidationRequiresAReferenceMatchWhenPublishable(t *testing.T) {
	item := buildItem(t, "Game A")
	item.Reference = nil
	if err := item.Validate(); err == nil {
		t.Fatal("Validate accepted a verified_eligible item with no reference match")
	}
}

func TestManifestRejectsDuplicateFiles(t *testing.T) {
	item := buildItem(t, "Game A")
	m := buildManifest(t, item, item)
	if err := m.Validate(); err == nil {
		t.Fatal("Validate accepted a manifest containing the same file twice")
	}
}

func TestManifestRejectsWrongSchemaAndZeroRevision(t *testing.T) {
	m := buildManifest(t, buildItem(t, "Game A"))
	m.SchemaVersion = SchemaVersion + 1
	if err := m.Validate(); err == nil {
		t.Fatal("Validate accepted an unknown schema version")
	}

	m = buildManifest(t, buildItem(t, "Game A"))
	m.Revision = 0
	if err := m.Validate(); err == nil {
		t.Fatal("Validate accepted revision 0, which means no inventory was published")
	}
}

// Two Bridges that scanned the same holdings in a different order must produce
// the same fingerprint, or reconciliation reports drift that does not exist.
func TestFingerprintsAreOrderIndependent(t *testing.T) {
	a := buildItem(t, "Game A")
	b := buildItem(t, "Game B")
	c := buildItem(t, "Game C")

	forward := buildManifest(t, a, b, c)
	reverse := buildManifest(t, c, b, a)

	if forward.Fingerprints[PlatformGB] != reverse.Fingerprints[PlatformGB] {
		t.Fatal("scan order changed the platform fingerprint")
	}

	// A real change must move the fingerprint.
	fewer := buildManifest(t, a, b)
	if fewer.Fingerprints[PlatformGB] == forward.Fingerprints[PlatformGB] {
		t.Fatal("removing an item did not change the platform fingerprint")
	}
}

func TestFingerprintsAreSeparatePerPlatform(t *testing.T) {
	gb := buildItem(t, "Game A")
	gba := buildItem(t, "Game B")
	gba.Platform = PlatformGBA

	m := buildManifest(t, gb, gba)
	if len(m.Fingerprints) != 2 {
		t.Fatalf("got %d platform fingerprints, want 2", len(m.Fingerprints))
	}
	if m.Fingerprints[PlatformGB] == m.Fingerprints[PlatformGBA] {
		t.Fatal("two platforms holding different items share a fingerprint")
	}
}

func TestDeltaRevisionSequencingIsEnforced(t *testing.T) {
	base := Delta{
		SchemaVersion: SchemaVersion,
		Swarm:         NewSwarmID(),
		Alias:         MustAliasFor(NewSwarmAliasKey(), NewSwarmID(), BridgeIDFromPublicKey([]byte("k"))),
		From:          4,
		To:            5,
		Entries:       []DeltaEntry{{Op: DeltaAdd, FileID: buildItem(t, "G").FileID, Item: ptr(buildItem(t, "G"))}},
		EmittedAt:     time.Now(),
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("a well-formed delta was rejected: %v", err)
	}

	// Out-of-order and gapped revisions are the failure Phase 3 must detect.
	skipped := base
	skipped.To = 7
	if err := skipped.Validate(); err == nil {
		t.Fatal("Validate accepted a delta that skips a revision")
	}

	backwards := base
	backwards.From, backwards.To = 5, 4
	if err := backwards.Validate(); err == nil {
		t.Fatal("Validate accepted a delta that moves backwards")
	}
}

func TestDeltaTombstonesCarryNoItem(t *testing.T) {
	item := buildItem(t, "G")
	d := Delta{
		SchemaVersion: SchemaVersion,
		Swarm:         NewSwarmID(),
		Alias:         MustAliasFor(NewSwarmAliasKey(), NewSwarmID(), BridgeIDFromPublicKey([]byte("k"))),
		From:          1, To: 2,
		Entries:   []DeltaEntry{{Op: DeltaTombstone, FileID: item.FileID, Item: &item}},
		EmittedAt: time.Now(),
	}
	if err := d.Validate(); err == nil {
		t.Fatal("Validate accepted a tombstone carrying an item payload")
	}

	d.Entries[0].Item = nil
	if err := d.Validate(); err != nil {
		t.Fatalf("Validate rejected a well-formed tombstone: %v", err)
	}
}

func TestDeltaRejectsUnpublishableItems(t *testing.T) {
	item := buildItem(t, "G")
	item.Classification = ClassMatchedUnverified
	d := Delta{
		SchemaVersion: SchemaVersion,
		Swarm:         NewSwarmID(),
		Alias:         MustAliasFor(NewSwarmAliasKey(), NewSwarmID(), BridgeIDFromPublicKey([]byte("k"))),
		From:          1, To: 2,
		Entries:   []DeltaEntry{{Op: DeltaAdd, FileID: item.FileID, Item: &item}},
		EmittedAt: time.Now(),
	}
	if err := d.Validate(); err == nil {
		t.Fatal("Validate accepted a delta publishing hash-unverified content")
	}
}

func TestAvailabilityAndResilience(t *testing.T) {
	// Only a currently available replica may count toward resilience. Offline
	// and stale replicas remain known inventory.
	cases := map[Availability]bool{
		AvailAvailable: true,
		AvailOffline:   false,
		AvailStale:     false,
		AvailRemoved:   false,
	}
	for a, want := range cases {
		if a.CountsTowardResilience() != want {
			t.Errorf("%s: CountsTowardResilience() = %v, want %v", a, a.CountsTowardResilience(), want)
		}
	}
}

func TestInitialPlatformSetSize(t *testing.T) {
	// Scope of Work Phase 0: three to five primarily single-file platforms.
	got := InitialPlatforms()
	if len(got) < 3 || len(got) > 5 {
		t.Fatalf("initial platform set has %d platforms, Scope of Work requires three to five", len(got))
	}
	seen := map[PlatformID]bool{}
	for _, p := range got {
		if seen[p] {
			t.Fatalf("platform %s appears twice in the initial set", p)
		}
		if strings.TrimSpace(string(p)) == "" {
			t.Fatal("initial platform set contains an empty platform id")
		}
		seen[p] = true
	}
}

func ptr[T any](v T) *T { return &v }
