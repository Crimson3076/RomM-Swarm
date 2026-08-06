package protocol

import (
	"fmt"
	"sort"
	"time"
)

// The normalized inventory manifest.
//
// Scope of Work §13.4 requires the manifest and canonicalization harness to
// exist before the central database schema, so that the schema is derived from
// a shape that has already survived contact with a real library rather than the
// other way round.
//
// One manifest describes what a single Bridge offers to a single Swarm. A
// Bridge that belongs to several Swarms produces several manifests from the same
// local scan, each filtered by that Swarm's sharing policy. That per-Swarm split
// is what Phase 3 acceptance means by "Inventory excluded by one Swarm's
// publication policy never enters that Swarm's catalog or change feed", and it
// is why the manifest is addressed by BridgeAlias rather than BridgeID.

// PlatformID is the Swarm's own stable platform key, independent of RomM's
// internal numbering and of any reference catalogue's naming.
type PlatformID string

// The initial platform set. Scope of Work Phase 0 requires three to five
// primarily single-file platforms; the candidate set named there is adopted in
// docs/adr/0004-initial-platforms.md.
const (
	PlatformGB      PlatformID = "gb"
	PlatformGBC     PlatformID = "gbc"
	PlatformGBA     PlatformID = "gba"
	PlatformNDS     PlatformID = "nds"
	PlatformGenesis PlatformID = "genesis"
)

// InitialPlatforms is the MVP platform set.
func InitialPlatforms() []PlatformID {
	return []PlatformID{PlatformGB, PlatformGBC, PlatformGBA, PlatformNDS, PlatformGenesis}
}

// Classification is the verification state of one held item.
//
// Scope of Work Phase 4 fixes these five states and the rule that only
// ClassVerifiedEligible may be advertised, transferred, or counted.
type Classification string

const (
	// ClassUnmatched means no reference entry and no accepted metadata identity.
	// Hacks, prototypes, homebrew, and bad dumps land here. They stay local.
	ClassUnmatched Classification = "unmatched"

	// ClassMatchedUnverified means RomM's metadata names a game but the
	// canonical payload did not match any reference entry. Never advertised,
	// never counted: this is precisely the state that filename-based catalogues
	// mistake for a verified holding.
	ClassMatchedUnverified Classification = "matched_unverified"

	// ClassVerifiedExcluded means the payload matched a reference entry, but the
	// active collection profile excludes it (wrong region, superseded revision
	// under a 1G1R profile). Verified, deliberately not counted for completion.
	ClassVerifiedExcluded Classification = "verified_excluded"

	// ClassVerifiedEligible means the payload matched a reference entry that the
	// active collection profile includes. The only class that may be advertised,
	// transferred, or counted in the MVP.
	ClassVerifiedEligible Classification = "verified_eligible"

	// ClassConflict means the evidence disagrees with itself: the canonical
	// payload matches a reference entry for a different game than the metadata
	// claims, or it matches entries that cannot both be true. Requires review;
	// never advertised.
	ClassConflict Classification = "conflict"
)

// Publishable reports whether an item in this class may be advertised to a
// Swarm, offered for transfer, or counted toward coverage.
func (c Classification) Publishable() bool {
	return c == ClassVerifiedEligible
}

// NeedsReview reports whether the class requires operator attention.
func (c Classification) NeedsReview() bool {
	return c == ClassConflict
}

// Availability is the Host's view of whether a known source can serve right now.
//
// Scope of Work §3: "Offline inventory remains known but is marked unavailable",
// and Phase 3 acceptance: "Taking a Bridge offline preserves its inventory but
// changes availability." Availability and existence are separate facts and are
// never collapsed into one flag.
type Availability string

const (
	// AvailAvailable means the source Bridge is online and the file was present
	// at its last revalidation.
	AvailAvailable Availability = "available"

	// AvailOffline means the source Bridge is not currently reachable. The
	// inventory is still known.
	AvailOffline Availability = "offline"

	// AvailStale means the source has not revalidated the file within the
	// freshness window. It still counts as known inventory, but it must not
	// count toward resilience.
	AvailStale Availability = "stale"

	// AvailRemoved means the source confirmed deletion through a tombstone.
	AvailRemoved Availability = "removed"
)

// CountsTowardResilience reports whether a replica in this availability state
// may contribute to a resilience count.
//
// Phase 4 acceptance: "A raw or stale self-reported claim does not increase
// resilient replica count."
func (a Availability) CountsTowardResilience() bool {
	return a == AvailAvailable
}

// ContainerKind describes the envelope a stored file arrives in.
type ContainerKind string

const (
	// ContainerNone means the stored file is the ROM itself.
	ContainerNone ContainerKind = "none"
	// ContainerZip means a zip archive.
	ContainerZip ContainerKind = "zip"
)

// ContainerInfo describes an archive envelope and its members.
//
// Phase 4 requires "Archive payload verification without trusting the outer
// archive hash alone": two zips of the same ROM made by different tools have
// different stored-file hashes and identical canonical payloads. The container
// identity is recorded for provenance and deduplication, never for matching.
type ContainerInfo struct {
	Kind    ContainerKind     `json:"kind"`
	Members []ContainerMember `json:"members,omitempty"`
}

// ContainerMember is one entry inside an archive.
type ContainerMember struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	CRC32 string `json:"crc32,omitempty"`
}

// ReferenceMatch records which reference entry a canonical payload matched, and
// how strongly.
type ReferenceMatch struct {
	// Family is the catalogue family, for example "no-intro".
	Family string `json:"family"`
	// SetName and SetVersion identify the exact imported reference set, so a
	// completion figure can name the version it was computed against.
	SetName    string `json:"set_name"`
	SetVersion string `json:"set_version"`
	// EntryName is the reference entry's name, for example
	// "Super Mario Land (World).gb".
	EntryName string `json:"entry_name"`
	// CanonicalKey is the profile-independent game key: the entry name with
	// region, revision, and language tags stripped.
	CanonicalKey string `json:"canonical_key"`
	// Strength is the strongest hash agreement achieved against the entry.
	Strength StrengthLevel `json:"strength"`
}

// AdapterRef names the canonicalization adapter and version that produced a
// canonical payload.
//
// Phase 4 acceptance: "Verification results name the canonicalization-adapter
// version used." Without this, a rule change silently reclassifies history and
// nobody can tell which figures were computed under which rules.
type AdapterRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// String renders the adapter reference as "id/version".
func (a AdapterRef) String() string { return a.ID + "/" + a.Version }

// Item is one held file as published to one Swarm.
type Item struct {
	FileID   FileID     `json:"file_id"`
	GameID   GameID     `json:"game_id,omitempty"`
	Platform PlatformID `json:"platform"`

	// Stored is the identity of the bytes exactly as they sit on the owner's
	// disk. Recorded, never modified, never used for reference matching.
	Stored Digest `json:"stored"`

	// Container describes the envelope, when there is one.
	Container *ContainerInfo `json:"container,omitempty"`

	// Canonical is the identity of the format-aware canonical payload. This is
	// what reference matching and transfer verification both use.
	Canonical Digest `json:"canonical"`

	// Adapter names the rules that produced Canonical.
	Adapter AdapterRef `json:"adapter"`

	Classification Classification   `json:"classification"`
	Reference      *ReferenceMatch  `json:"reference,omitempty"`
	Notes          []string         `json:"notes,omitempty"`
	Availability   Availability     `json:"availability"`
	Destination    DestinationState `json:"destination_state,omitempty"`

	// LastVerifiedAt is when this Bridge last confirmed the file is present and
	// still hashes to Canonical. Distinct from the Bridge's last heartbeat and
	// from its last inventory publication; Phase 3 requires all three to be
	// tracked separately because they answer different questions.
	LastVerifiedAt time.Time `json:"last_verified_at"`
}

// Publishable reports whether the item may be advertised to a Swarm.
func (i Item) Publishable() bool { return i.Classification.Publishable() }

// Validate checks internal consistency of an item.
func (i Item) Validate() error {
	if err := i.FileID.Validate(); err != nil {
		return err
	}
	if i.Platform == "" {
		return fmt.Errorf("item %s: missing platform", i.FileID)
	}
	if i.Canonical.SHA256 == "" {
		return fmt.Errorf("item %s: missing canonical sha256", i.FileID)
	}
	// The FileID is derived from the canonical digest; a mismatch means the item
	// was assembled by hand or corrupted in transit.
	if want := FileIDFromCanonicalDigest(i.Canonical.SHA256); want != i.FileID {
		return fmt.Errorf("item %s: file id does not match canonical digest (want %s)", i.FileID, want)
	}
	if i.Classification.Publishable() && i.Reference == nil {
		return fmt.Errorf("item %s: classified %s without a reference match", i.FileID, i.Classification)
	}
	if i.Adapter.ID == "" || i.Adapter.Version == "" {
		return fmt.Errorf("item %s: missing canonicalization adapter reference", i.FileID)
	}
	return nil
}

// Manifest is a full inventory snapshot from one Bridge to one Swarm.
type Manifest struct {
	SchemaVersion int         `json:"schema_version"`
	Swarm         SwarmID     `json:"swarm_id"`
	Alias         BridgeAlias `json:"bridge_alias"`
	Revision      Revision    `json:"revision"`
	GeneratedAt   time.Time   `json:"generated_at"`
	Items         []Item      `json:"items"`

	// Fingerprints are per-platform digests of the published item set, used by
	// Phase 3 reconciliation to detect a missed delta without re-uploading the
	// whole inventory.
	Fingerprints map[PlatformID]string `json:"fingerprints,omitempty"`
}

// Validate checks a manifest end to end.
func (m Manifest) Validate() error {
	if m.SchemaVersion != SchemaVersion {
		return fmt.Errorf("manifest: schema version %d, want %d", m.SchemaVersion, SchemaVersion)
	}
	if err := m.Swarm.Validate(); err != nil {
		return err
	}
	if err := m.Alias.Validate(); err != nil {
		return err
	}
	if m.Revision.IsZero() {
		return fmt.Errorf("manifest: revision must be at least 1")
	}
	seen := make(map[FileID]bool, len(m.Items))
	for _, it := range m.Items {
		if err := it.Validate(); err != nil {
			return err
		}
		if seen[it.FileID] {
			return fmt.Errorf("manifest: duplicate file id %s", it.FileID)
		}
		seen[it.FileID] = true
		if !it.Publishable() {
			return fmt.Errorf("manifest: item %s is classified %s and must not be published",
				it.FileID, it.Classification)
		}
	}
	return nil
}

// ComputeFingerprints derives the per-platform fingerprints for a manifest.
//
// The fingerprint is order independent: it is a digest over the sorted FileIDs
// for that platform, so two Bridges that scanned in a different order but hold
// the same set agree, and a reconciliation pass does not report phantom drift.
func (m *Manifest) ComputeFingerprints() {
	byPlatform := map[PlatformID][]string{}
	for _, it := range m.Items {
		byPlatform[it.Platform] = append(byPlatform[it.Platform], string(it.FileID))
	}
	out := make(map[PlatformID]string, len(byPlatform))
	for p, ids := range byPlatform {
		sort.Strings(ids)
		h := NewHasher()
		for _, id := range ids {
			_, _ = h.Write([]byte(id))
			_, _ = h.Write([]byte{0})
		}
		out[p] = h.Digest().SHA256
	}
	m.Fingerprints = out
}

// DeltaOp is the kind of change carried by a delta entry.
type DeltaOp string

const (
	// DeltaAdd introduces a new item.
	DeltaAdd DeltaOp = "add"
	// DeltaUpdate changes an existing item's availability or verification data.
	DeltaUpdate DeltaOp = "update"
	// DeltaTombstone confirms deletion. Scope of Work §3: a tombstone removes
	// only that Bridge as a source, never the item from the catalogue.
	DeltaTombstone DeltaOp = "tombstone"
)

// DeltaEntry is one change within a delta.
type DeltaEntry struct {
	Op     DeltaOp `json:"op"`
	FileID FileID  `json:"file_id"`
	// Item is present for add and update, absent for tombstone.
	Item *Item `json:"item,omitempty"`
}

// Delta is an incremental inventory change from one Bridge to one Swarm.
type Delta struct {
	SchemaVersion int         `json:"schema_version"`
	Swarm         SwarmID     `json:"swarm_id"`
	Alias         BridgeAlias `json:"bridge_alias"`
	// From is the revision this delta applies to; To is the revision it
	// produces. The Host rejects a delta whose From does not match the revision
	// it currently holds, which is how Phase 3's "out-of-order revision
	// detection" is enforced rather than hoped for.
	From      Revision     `json:"from_revision"`
	To        Revision     `json:"to_revision"`
	Entries   []DeltaEntry `json:"entries"`
	EmittedAt time.Time    `json:"emitted_at"`
}

// Validate checks a delta.
func (d Delta) Validate() error {
	if d.SchemaVersion != SchemaVersion {
		return fmt.Errorf("delta: schema version %d, want %d", d.SchemaVersion, SchemaVersion)
	}
	if err := d.Swarm.Validate(); err != nil {
		return err
	}
	if err := d.Alias.Validate(); err != nil {
		return err
	}
	if d.To != d.From.Next() {
		return fmt.Errorf("delta: to_revision %d must be from_revision %d plus one", d.To, d.From)
	}
	if len(d.Entries) == 0 {
		return fmt.Errorf("delta: no entries")
	}
	for _, e := range d.Entries {
		if err := e.FileID.Validate(); err != nil {
			return err
		}
		switch e.Op {
		case DeltaAdd, DeltaUpdate:
			if e.Item == nil {
				return fmt.Errorf("delta: %s for %s carries no item", e.Op, e.FileID)
			}
			if err := e.Item.Validate(); err != nil {
				return err
			}
			if !e.Item.Publishable() {
				return fmt.Errorf("delta: item %s is classified %s and must not be published",
					e.FileID, e.Item.Classification)
			}
		case DeltaTombstone:
			if e.Item != nil {
				return fmt.Errorf("delta: tombstone for %s must not carry an item", e.FileID)
			}
		default:
			return fmt.Errorf("delta: unknown op %q", e.Op)
		}
	}
	return nil
}
