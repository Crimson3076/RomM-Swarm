package reference

import (
	"sort"
	"strings"
)

// Collection profiles.
//
// A profile answers "which reference entries should this Swarm be trying to
// have?". It is the denominator of every completion percentage, which is why
// Phase 4 acceptance requires that "A completion percentage names its profile
// and reference-set version". A holding outside the profile is not a failure and
// is not missing; it is verified_excluded, which is a different and deliberately
// visible thing.
//
// One-game-one-ROM (1G1R) is the part that does real work: without it, a
// catalogue counts the USA, Europe, and Japan releases of one game as three
// separate things to own, and a collection that is complete in every practical
// sense reports as a third complete.

// Profile is a set of rules for choosing which reference entries count.
type Profile struct {
	// Name and Version identify the profile in a coverage figure.
	Name    string
	Version string

	// Regions is the allowed region list in priority order. Under 1G1R the
	// earliest matching region wins.
	Regions []string

	// ExcludedFlags removes entries carrying any of these tags.
	ExcludedFlags []string

	// IncludeBIOS controls whether BIOS entries count.
	//
	// Default false, and not merely as a preference: Scope of Work §8 places
	// firmware and BIOS sharing outside the initial release, and §3 requires
	// content coverage and playability readiness to be reported separately
	// precisely because BIOS files are not shared. Counting BIOS entries in
	// coverage would state something the project has deliberately chosen not to
	// support.
	IncludeBIOS bool

	// IncludeUntaggedRegion controls whether entries with no region tag count.
	// Default true: some catalogues omit the tag, and dropping those entries
	// would silently shrink the denominator.
	IncludeUntaggedRegion bool

	// OneGamePerCanonical enables 1G1R selection.
	OneGamePerCanonical bool
}

// DefaultProfile is the proposed default: North America plus World, one game
// per canonical title, excluding pre-release and unlicensed material.
//
// Scope of Work §4 lists the default collection profile as a Phase 0 gate and
// names "North America plus World 1G1R" as an example. This is that profile
// written down concretely so it can be argued with; see
// docs/adr/0005-default-collection-profile.md. It is a proposal, not a settled
// decision.
func DefaultProfile() Profile {
	return Profile{
		Name:    "North America plus World 1G1R",
		Version: "1",
		// Priority order. A region-specific North American release is preferred
		// over a World release when both exist, because that is the release a
		// North American collection is usually understood to want.
		Regions: []string{"USA", "Canada", "World"},
		ExcludedFlags: []string{
			"Beta", "Proto", "Prototype", "Demo", "Sample", "Kiosk",
			"Program", "Test Program", "Debug", "Pirate", "Unl",
		},
		IncludeBIOS:           false,
		IncludeUntaggedRegion: true,
		OneGamePerCanonical:   true,
	}
}

// Ref names the profile for the record.
func (p Profile) Ref() string { return p.Name + " v" + p.Version }

// regionPriority returns the entry's best region rank, and whether any of its
// regions is allowed. Lower is better.
func (p Profile) regionPriority(e *Entry) (int, bool) {
	if len(e.Regions) == 0 {
		if p.IncludeUntaggedRegion {
			// Rank untagged entries last, so a properly tagged release wins any
			// 1G1R contest against one.
			return len(p.Regions), true
		}
		return 0, false
	}
	best := -1
	for _, r := range e.Regions {
		for i, allowed := range p.Regions {
			if strings.EqualFold(r, allowed) && (best < 0 || i < best) {
				best = i
			}
		}
	}
	if best < 0 {
		return 0, false
	}
	return best, true
}

// excluded reports whether an entry carries an excluded flag.
func (p Profile) excluded(e *Entry) bool {
	for _, f := range e.Flags {
		for _, bad := range p.ExcludedFlags {
			if strings.EqualFold(f, bad) {
				return true
			}
		}
	}
	return false
}

// eligible reports whether an entry passes the profile's filters, before 1G1R.
func (p Profile) eligible(e *Entry) bool {
	if e.IsBIOS && !p.IncludeBIOS {
		return false
	}
	if p.excluded(e) {
		return false
	}
	_, ok := p.regionPriority(e)
	return ok
}

// Selection is a profile applied to a reference set: the concrete list of
// entries that count.
type Selection struct {
	Profile Profile
	Set     *Set

	included map[int]bool
	// chosen maps a canonical key to the entry index chosen for it under 1G1R.
	chosen map[string]int
}

// Apply computes the selection.
func (p Profile) Apply(s *Set) *Selection {
	sel := &Selection{
		Profile:  p,
		Set:      s,
		included: map[int]bool{},
		chosen:   map[string]int{},
	}

	if !p.OneGamePerCanonical {
		for i := range s.Entries {
			if p.eligible(&s.Entries[i]) {
				sel.included[i] = true
			}
		}
		return sel
	}

	byKey := map[string][]int{}
	for i := range s.Entries {
		if !p.eligible(&s.Entries[i]) {
			continue
		}
		key := s.Entries[i].CanonicalKey
		byKey[key] = append(byKey[key], i)
	}

	for key, indices := range byKey {
		sort.Slice(indices, func(a, b int) bool {
			ea, eb := &s.Entries[indices[a]], &s.Entries[indices[b]]
			pa, _ := p.regionPriority(ea)
			pb, _ := p.regionPriority(eb)
			if pa != pb {
				return pa < pb // better region first
			}
			if ea.RevisionRank != eb.RevisionRank {
				return ea.RevisionRank > eb.RevisionRank // newest revision first
			}
			// Deterministic final tie-break.
			return ea.ROMName < eb.ROMName
		})
		best := indices[0]
		sel.chosen[key] = best
		sel.included[best] = true
	}
	return sel
}

// Includes reports whether the entry at index counts under this profile.
func (sel *Selection) Includes(index int) bool { return sel.included[index] }

// IncludedIndices returns every included entry index, in a stable order.
func (sel *Selection) IncludedIndices() []int {
	out := make([]int, 0, len(sel.included))
	for i := range sel.included {
		out = append(out, i)
	}
	sort.Ints(out)
	return out
}

// Size is the number of entries the profile expects a complete collection to
// hold: the denominator of a completion percentage.
func (sel *Selection) Size() int { return len(sel.included) }

// ChosenFor returns the entry 1G1R selected for a canonical key.
func (sel *Selection) ChosenFor(canonicalKey string) (*Entry, bool) {
	idx, ok := sel.chosen[canonicalKey]
	if !ok {
		return nil, false
	}
	return &sel.Set.Entries[idx], true
}
