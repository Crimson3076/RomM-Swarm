// Package preservation turns raw source claims into preservation risk.
//
// Scope of Work §3: "Resilience counts recently revalidated replicas across
// distinct operators or failure domains, not raw Bridge claims." That sentence
// is the whole package. Three separate things are deliberately not counted:
//
//   - A claim that has not been revalidated recently. A Bridge that said it had
//     a file six months ago is telling you about the past.
//   - A second Bridge in the same failure domain. Two machines in one house on
//     one disk array are one flood, one fire, one failed controller.
//   - A Bridge that is offline. Its inventory is still known and the item is
//     not missing, but it cannot serve, so it is not redundancy today.
//
// Getting this wrong is worse than not measuring at all: a dashboard that says
// "resilient" about a single operator's three machines actively discourages the
// replication that would make it true.
package preservation

import (
	"sort"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// RiskState is an item's preservation risk.
type RiskState string

const (
	// RiskResilient means three or more recently revalidated independent
	// replicas.
	RiskResilient RiskState = "resilient"

	// RiskFragile means exactly two.
	RiskFragile RiskState = "fragile"

	// RiskAtRisk means exactly one: losing that operator loses the item.
	RiskAtRisk RiskState = "at_risk"

	// RiskStale means replicas are online but none has revalidated within the
	// window, so nothing is currently confirmed.
	RiskStale RiskState = "stale"

	// RiskUnavailable means the item is known but no source can serve it now.
	// Distinct from missing: Scope of Work §3 requires offline inventory to
	// remain known.
	RiskUnavailable RiskState = "unavailable"

	// RiskMissing means no Bridge in the Swarm has ever offered it.
	RiskMissing RiskState = "missing"
)

// NeedsReplication reports whether the Swarm should be actively seeking another
// copy. Drives the Phase 8 replication recommendations.
func (r RiskState) NeedsReplication() bool {
	switch r {
	case RiskResilient:
		return false
	default:
		return true
	}
}

// Replica is one Bridge's claim to hold an item.
type Replica struct {
	// Alias is the Swarm-scoped pseudonym. The global Bridge identity never
	// reaches this package.
	Alias protocol.BridgeAlias

	// FailureDomain groups replicas that would be lost together: same operator,
	// same site, same storage array. Scope of Work Phase 3 requires this key to
	// exist and requires it not to be exposed to ordinary members, which is why
	// it is an opaque string here rather than anything derived from an owner's
	// identity.
	FailureDomain string

	Availability protocol.Availability

	// LastVerifiedAt is when the holding Bridge last confirmed the file is
	// present and still hashes to the expected canonical identity. Deliberately
	// separate from a heartbeat: a Bridge being online says nothing about
	// whether the file is still on the disk.
	LastVerifiedAt time.Time
}

// Rules are the versioned thresholds behind an assessment.
//
// Phase 4 requires "Revalidation window and failure-domain rules versioned with
// the metric definition", because changing a threshold changes every historical
// figure computed under it.
type Rules struct {
	Version string

	// RevalidationWindow is how recently a replica must have been confirmed to
	// count toward resilience.
	RevalidationWindow time.Duration

	// AtRiskAt, FragileAt, and ResilientAt are the independent-replica counts
	// at which each state begins.
	AtRiskAt    int
	FragileAt   int
	ResilientAt int
}

// DefaultRules implements the thresholds Scope of Work Phase 4 states in its
// acceptance criteria: one independent operator is at risk, two is fragile,
// three or more is resilient.
//
// The 30-day window is a starting point rather than a derived constant. It is
// long enough that a Bridge which is simply switched off for a holiday does not
// drag its whole library into "stale", and short enough that a disk that failed
// last month stops being counted as redundancy.
func DefaultRules() Rules {
	return Rules{
		Version:            "1",
		RevalidationWindow: 30 * 24 * time.Hour,
		AtRiskAt:           1,
		FragileAt:          2,
		ResilientAt:        3,
	}
}

// Assessment is the computed preservation position for one item.
type Assessment struct {
	// Confirmed is every replica the Swarm knows about, in any state. This is
	// the number that looks best and means least, so it is reported alongside
	// the others rather than instead of them.
	Confirmed int

	// Online is how many replicas could serve right now.
	Online int

	// Independent is the number of distinct failure domains holding a recently
	// revalidated, currently available copy. This is the only count that drives
	// the risk state.
	Independent int

	Risk RiskState

	// RulesVersion names the thresholds this assessment was computed under.
	RulesVersion string
}

// Assess computes the preservation position for one item.
func Assess(replicas []Replica, rules Rules, now time.Time) Assessment {
	a := Assessment{
		Confirmed:    len(replicas),
		RulesVersion: rules.Version,
	}

	if len(replicas) == 0 {
		a.Risk = RiskMissing
		return a
	}

	domains := map[string]bool{}
	for _, r := range replicas {
		if r.Availability == protocol.AvailRemoved {
			// A confirmed deletion removes that Bridge as a source entirely.
			a.Confirmed--
			continue
		}
		if r.Availability.CountsTowardResilience() {
			a.Online++
		}
		if !recentlyRevalidated(r, rules, now) {
			continue
		}
		if !r.Availability.CountsTowardResilience() {
			continue
		}
		// An empty failure domain is treated as its own domain rather than as a
		// shared one, so a missing key cannot silently collapse independent
		// operators into a single count.
		key := r.FailureDomain
		if key == "" {
			key = "unknown:" + string(r.Alias)
		}
		domains[key] = true
	}
	a.Independent = len(domains)

	switch {
	case a.Confirmed == 0:
		a.Risk = RiskMissing
	case a.Independent >= rules.ResilientAt:
		a.Risk = RiskResilient
	case a.Independent >= rules.FragileAt:
		a.Risk = RiskFragile
	case a.Independent >= rules.AtRiskAt:
		a.Risk = RiskAtRisk
	case a.Online > 0:
		// Sources are reachable but none has confirmed the file recently.
		a.Risk = RiskStale
	default:
		a.Risk = RiskUnavailable
	}
	return a
}

// recentlyRevalidated reports whether a replica's last verification falls
// inside the window.
func recentlyRevalidated(r Replica, rules Rules, now time.Time) bool {
	if r.LastVerifiedAt.IsZero() {
		return false
	}
	return !r.LastVerifiedAt.Before(now.Add(-rules.RevalidationWindow))
}

// Coverage is a platform's preservation position under one profile and one
// reference-set version.
//
// Phase 4 acceptance: "A completion percentage names its profile and
// reference-set version." Those two fields are not decoration; a figure without
// them cannot be compared with another figure.
type Coverage struct {
	Platform     protocol.PlatformID
	ProfileRef   string
	SetRef       string
	RulesVersion string

	// Expected is the number of entries the profile says a complete collection
	// holds: the denominator.
	Expected int

	// Content is how many expected entries the Swarm holds at all, including
	// through sources that are currently offline.
	Content int

	// Available is how many can be served right now.
	Available int

	// Resilient is how many meet the independent-replica threshold.
	Resilient int

	// AtRisk, Fragile, Stale, Unavailable, and Missing partition the expected
	// set, so the numbers can be checked against each other.
	AtRisk      int
	Fragile     int
	Stale       int
	Unavailable int
	Missing     int

	// PlayabilityKnown reports whether a playability-readiness figure can be
	// stated at all. Scope of Work §3 requires content coverage and playability
	// readiness to be reported separately because BIOS material is not shared;
	// for a platform needing BIOS files this stays false, and the interface must
	// not present content coverage as proof the platform is playable.
	PlayabilityKnown bool
}

// ComputeCoverage assesses every expected entry.
//
// replicasByFile supplies the known replicas for each expected file. An entry
// absent from the map is missing, which is the point: the denominator comes
// from the profile, never from what anyone happens to hold.
func ComputeCoverage(
	platform protocol.PlatformID,
	profileRef, setRef string,
	expected []protocol.FileID,
	replicasByFile map[protocol.FileID][]Replica,
	rules Rules,
	now time.Time,
) Coverage {
	cov := Coverage{
		Platform:     platform,
		ProfileRef:   profileRef,
		SetRef:       setRef,
		RulesVersion: rules.Version,
		Expected:     len(expected),
	}

	for _, id := range expected {
		a := Assess(replicasByFile[id], rules, now)

		if a.Confirmed > 0 {
			cov.Content++
		}
		if a.Online > 0 {
			cov.Available++
		}

		switch a.Risk {
		case RiskResilient:
			cov.Resilient++
		case RiskFragile:
			cov.Fragile++
		case RiskAtRisk:
			cov.AtRisk++
		case RiskStale:
			cov.Stale++
		case RiskUnavailable:
			cov.Unavailable++
		case RiskMissing:
			cov.Missing++
		}
	}
	return cov
}

// RiskOrder returns the risk states from most to least urgent, for interfaces
// that present a prioritised list.
func RiskOrder() []RiskState {
	return []RiskState{RiskMissing, RiskUnavailable, RiskAtRisk, RiskStale, RiskFragile, RiskResilient}
}

// SortReplicas orders replicas deterministically for display and for tests.
func SortReplicas(replicas []Replica) {
	sort.Slice(replicas, func(i, j int) bool {
		return replicas[i].Alias < replicas[j].Alias
	})
}
