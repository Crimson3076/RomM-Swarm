// Package publish turns a Bridge's local holdings into the per-Swarm manifests
// it advertises.
//
// Scope of Work §3: "Bridges publish a filtered inventory independently for each
// Swarm according to that Swarm's sharing policy." Phase 1 acceptance: "The same
// Bridge can publish different filtered manifests to different Swarms without
// leaking excluded holdings." Phase 3 acceptance: "Inventory excluded by one
// Swarm's publication policy never enters that Swarm's catalog or change feed."
//
// # Filtering happens here, not at the Host
//
// That is the design decision this package exists to enforce. A Host-side filter
// would mean the Host receives everything and is trusted to forget the parts it
// should not show — so a Host compromise, a query bug, or an over-broad
// administrative view would expose holdings their owner deliberately withheld.
//
// Filtering at the Bridge means excluded inventory is never transmitted at all.
// The Host cannot leak what it was never sent, and that is a much stronger
// statement than a promise about access control.
//
// The consequence, which is deliberate: a Bridge in three Swarms runs one local
// scan and produces three unrelated manifests, each with its own revision
// sequence, its own fingerprints, and its own Swarm-scoped alias. Nothing
// crosses between them.
package publish

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// Policy is one Swarm's sharing rules, as configured by the Bridge's owner.
//
// The default zero value shares nothing. Scope of Work §7 asks for conservative
// defaults, and for a mechanism whose whole purpose is deciding what leaves a
// member's server, "unconfigured" must not mean "everything".
type Policy struct {
	Swarm protocol.SwarmID

	// ShareAll opts into publishing every eligible holding, subject to the
	// exclusions below. Without it, only Platforms are published.
	ShareAll bool

	// Platforms is the allowlist. Ignored when ShareAll is set.
	Platforms []protocol.PlatformID

	// ExcludedPlatforms removes platforms even when ShareAll is set.
	ExcludedPlatforms []protocol.PlatformID

	// ExcludedFiles removes individual holdings. This is how an owner withholds
	// one item without reasoning about categories.
	ExcludedFiles []protocol.FileID

	// MaxItemBytes caps the size of a published holding, so an owner on a slow
	// connection can share a library without advertising the items that would
	// saturate it. Zero means no cap.
	MaxItemBytes int64
}

// Permits reports whether a holding may be published to this Swarm.
func (p Policy) Permits(item protocol.Item) bool {
	// The MVP rule, applied before anything the owner configured: only matched
	// and reference-hash-verified content is ever advertised. Scope of Work §3.
	if !item.Classification.Publishable() {
		return false
	}

	for _, id := range p.ExcludedFiles {
		if id == item.FileID {
			return false
		}
	}
	for _, plat := range p.ExcludedPlatforms {
		if plat == item.Platform {
			return false
		}
	}
	if p.MaxItemBytes > 0 && item.Canonical.Size > p.MaxItemBytes {
		return false
	}

	if p.ShareAll {
		return true
	}
	for _, plat := range p.Platforms {
		if plat == item.Platform {
			return true
		}
	}
	return false
}

// Validate checks a policy is usable.
func (p Policy) Validate() error {
	if err := p.Swarm.Validate(); err != nil {
		return err
	}
	if !p.ShareAll && len(p.Platforms) == 0 {
		return fmt.Errorf("publish: policy for %s shares nothing; set ShareAll or list platforms", p.Swarm)
	}
	return nil
}

// Membership is what the Bridge knows about its place in one Swarm.
type Membership struct {
	Swarm protocol.SwarmID

	// Alias is the Swarm-scoped pseudonym the Host issued at enrolment. The
	// Bridge never derives it: the alias key lives on the Host.
	Alias protocol.BridgeAlias

	Policy Policy

	// Revision is the last revision published to this Swarm. Each Swarm has its
	// own sequence, so activity in one reveals nothing about activity in
	// another.
	Revision protocol.Revision
}

// Validate checks a membership.
func (m Membership) Validate() error {
	if err := m.Swarm.Validate(); err != nil {
		return err
	}
	if err := m.Alias.Validate(); err != nil {
		return err
	}
	if m.Policy.Swarm != m.Swarm {
		return fmt.Errorf("publish: policy is for %s but membership is for %s", m.Policy.Swarm, m.Swarm)
	}
	return m.Policy.Validate()
}

// ErrNothingToPublish means a snapshot would contain no items.
var ErrNothingToPublish = errors.New("publish: the policy permits no holdings, so there is nothing to publish")

// Snapshot builds a full manifest for one Swarm from the Bridge's local
// holdings.
//
// holdings is the complete local inventory, including everything this Swarm may
// not see. Nothing excluded reaches the returned manifest, and the fingerprints
// are computed over the filtered set — so a Host comparing fingerprints learns
// nothing about what was withheld.
func Snapshot(m Membership, holdings []protocol.Item, at time.Time) (protocol.Manifest, error) {
	if err := m.Validate(); err != nil {
		return protocol.Manifest{}, err
	}

	items := make([]protocol.Item, 0, len(holdings))
	seen := make(map[protocol.FileID]bool, len(holdings))
	for _, item := range holdings {
		if !m.Policy.Permits(item) {
			continue
		}
		if seen[item.FileID] {
			// One canonical payload held twice locally is one holding to the
			// Swarm. Publishing it twice would invent a replica.
			continue
		}
		seen[item.FileID] = true
		items = append(items, item)
	}

	if len(items) == 0 {
		return protocol.Manifest{}, ErrNothingToPublish
	}

	// Deterministic order, so two scans of an unchanged library produce an
	// identical manifest and reconciliation reports no drift.
	sort.Slice(items, func(i, j int) bool { return items[i].FileID < items[j].FileID })

	manifest := protocol.Manifest{
		SchemaVersion: protocol.SchemaVersion,
		Swarm:         m.Swarm,
		Alias:         m.Alias,
		Revision:      m.Revision.Next(),
		GeneratedAt:   at.UTC(),
		Items:         items,
	}
	manifest.ComputeFingerprints()

	if err := manifest.Validate(); err != nil {
		return protocol.Manifest{}, err
	}
	return manifest, nil
}

// Diff computes the delta between what a Swarm was last told and what it should
// be told now.
//
// The second return value reports whether anything changed. A Bridge that
// rescans an unchanged library must publish nothing: Phase 1 acceptance requires
// that "Repeated scans do not generate false additions or deletions".
func Diff(previous protocol.Manifest, m Membership, holdings []protocol.Item, at time.Time) (protocol.Delta, bool, error) {
	if err := m.Validate(); err != nil {
		return protocol.Delta{}, false, err
	}

	current, err := Snapshot(Membership{
		Swarm:    m.Swarm,
		Alias:    m.Alias,
		Policy:   m.Policy,
		Revision: previous.Revision,
	}, holdings, at)
	if err != nil && !errors.Is(err, ErrNothingToPublish) {
		return protocol.Delta{}, false, err
	}

	before := indexByFile(previous.Items)
	after := indexByFile(current.Items)

	var entries []protocol.DeltaEntry

	for id, item := range after {
		old, existed := before[id]
		switch {
		case !existed:
			entries = append(entries, protocol.DeltaEntry{Op: protocol.DeltaAdd, FileID: id, Item: copyItem(item)})
		case changed(old, item):
			entries = append(entries, protocol.DeltaEntry{Op: protocol.DeltaUpdate, FileID: id, Item: copyItem(item)})
		}
	}

	for id := range before {
		if _, still := after[id]; !still {
			// A tombstone is emitted whether the file was deleted from disk or
			// merely withdrawn by a policy change. From this Swarm's point of
			// view the two are the same event, and saying which would leak the
			// owner's reason.
			entries = append(entries, protocol.DeltaEntry{Op: protocol.DeltaTombstone, FileID: id})
		}
	}

	if len(entries) == 0 {
		return protocol.Delta{}, false, nil
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Op != entries[j].Op {
			return entries[i].Op < entries[j].Op
		}
		return entries[i].FileID < entries[j].FileID
	})

	delta := protocol.Delta{
		SchemaVersion: protocol.SchemaVersion,
		Swarm:         m.Swarm,
		Alias:         m.Alias,
		From:          previous.Revision,
		To:            previous.Revision.Next(),
		Entries:       entries,
		EmittedAt:     at.UTC(),
	}
	if err := delta.Validate(); err != nil {
		return protocol.Delta{}, false, err
	}
	return delta, true, nil
}

// Preview describes what a policy would publish, without publishing it.
//
// Phase 1 deliverable: "Per-Swarm sharing policy and filtered publication
// preview". An owner should be able to see exactly what a Swarm would learn
// before it learns it, because a sharing policy that can only be evaluated by
// applying it is one nobody will trust.
type Preview struct {
	Swarm protocol.SwarmID

	// Published and Withheld count holdings on each side of the policy.
	Published int
	Withheld  int

	// PublishedBytes is the total canonical size advertised.
	PublishedBytes int64

	// ByPlatform counts published holdings per platform.
	ByPlatform map[protocol.PlatformID]int

	// WithheldReasons counts why holdings were withheld, so an owner can tell a
	// deliberate exclusion from an accidental one.
	WithheldReasons map[string]int
}

// PreviewPolicy evaluates a policy against local holdings.
func PreviewPolicy(p Policy, holdings []protocol.Item) Preview {
	out := Preview{
		Swarm:           p.Swarm,
		ByPlatform:      map[protocol.PlatformID]int{},
		WithheldReasons: map[string]int{},
	}

	for _, item := range holdings {
		if p.Permits(item) {
			out.Published++
			out.PublishedBytes += item.Canonical.Size
			out.ByPlatform[item.Platform]++
			continue
		}
		out.Withheld++
		out.WithheldReasons[withheldReason(p, item)]++
	}
	return out
}

// withheldReason explains, in one phrase, why a holding was not published.
func withheldReason(p Policy, item protocol.Item) string {
	if !item.Classification.Publishable() {
		return "not verified against the reference catalogue"
	}
	for _, id := range p.ExcludedFiles {
		if id == item.FileID {
			return "excluded individually"
		}
	}
	for _, plat := range p.ExcludedPlatforms {
		if plat == item.Platform {
			return "platform excluded"
		}
	}
	if p.MaxItemBytes > 0 && item.Canonical.Size > p.MaxItemBytes {
		return "larger than the size limit"
	}
	return "platform not shared with this Swarm"
}

func indexByFile(items []protocol.Item) map[protocol.FileID]protocol.Item {
	out := make(map[protocol.FileID]protocol.Item, len(items))
	for _, item := range items {
		out[item.FileID] = item
	}
	return out
}

// changed reports whether a published holding differs in any way a Swarm needs
// to know about.
//
// Deliberately narrow. LastVerifiedAt moves on every revalidation, and treating
// that as a change would make every Bridge republish its whole library daily,
// which is both a load problem and a privacy one — a stream of updates is a
// stream of activity.
func changed(before, after protocol.Item) bool {
	return before.Canonical.SHA256 != after.Canonical.SHA256 ||
		before.Availability != after.Availability ||
		before.Platform != after.Platform ||
		before.Adapter != after.Adapter ||
		referenceChanged(before.Reference, after.Reference)
}

func referenceChanged(a, b *protocol.ReferenceMatch) bool {
	if a == nil || b == nil {
		return a != b
	}
	return *a != *b
}

func copyItem(item protocol.Item) *protocol.Item {
	out := item
	if item.Reference != nil {
		ref := *item.Reference
		out.Reference = &ref
	}
	return &out
}
