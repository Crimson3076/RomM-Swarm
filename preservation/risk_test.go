package preservation

import (
	"fmt"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

var now = time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

// replica builds a recently revalidated, currently available replica.
func replica(alias, domain string) Replica {
	return Replica{
		Alias:          protocol.BridgeAlias(alias),
		FailureDomain:  domain,
		Availability:   protocol.AvailAvailable,
		LastVerifiedAt: now.Add(-24 * time.Hour),
	}
}

// TestPhase0_ResilienceThresholds is Phase 4 acceptance, stated there as four
// separate criteria and asserted here as one table.
func TestPhase0_ResilienceThresholds(t *testing.T) {
	rules := DefaultRules()

	cases := []struct {
		name     string
		replicas []Replica
		want     RiskState
		wantInd  int
	}{
		{"no source at all", nil, RiskMissing, 0},
		{"one recently revalidated independent operator",
			[]Replica{replica("a", "op1")}, RiskAtRisk, 1},
		{"two recently revalidated independent operators",
			[]Replica{replica("a", "op1"), replica("b", "op2")}, RiskFragile, 2},
		{"three recently revalidated independent operators",
			[]Replica{replica("a", "op1"), replica("b", "op2"), replica("c", "op3")}, RiskResilient, 3},
		{"four independent operators stays resilient",
			[]Replica{replica("a", "op1"), replica("b", "op2"), replica("c", "op3"), replica("d", "op4")},
			RiskResilient, 4},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Assess(tc.replicas, rules, now)
			if got.Risk != tc.want {
				t.Errorf("risk = %s, want %s", got.Risk, tc.want)
			}
			if got.Independent != tc.wantInd {
				t.Errorf("independent = %d, want %d", got.Independent, tc.wantInd)
			}
			if got.RulesVersion != rules.Version {
				t.Error("the assessment does not name the rules version it was computed under")
			}
		})
	}
}

// TestPhase0_SameFailureDomainIsNotRedundancy is the rule that makes the
// resilience figure worth reading: three machines belonging to one operator are
// one failure.
func TestPhase0_SameFailureDomainIsNotRedundancy(t *testing.T) {
	sameOwner := []Replica{
		replica("a", "operator-1"),
		replica("b", "operator-1"),
		replica("c", "operator-1"),
	}
	got := Assess(sameOwner, DefaultRules(), now)

	if got.Independent != 1 {
		t.Fatalf("independent = %d, want 1: three Bridges in one failure domain are one replica", got.Independent)
	}
	if got.Risk != RiskAtRisk {
		t.Fatalf("risk = %s, want at_risk", got.Risk)
	}
	// The raw count is still reported. It is honest, it is just not resilience.
	if got.Confirmed != 3 {
		t.Errorf("confirmed = %d, want 3", got.Confirmed)
	}
	if got.Online != 3 {
		t.Errorf("online = %d, want 3", got.Online)
	}
}

// TestPhase0_StaleClaimsDoNotCountAsResilience is Phase 4 acceptance: "A raw or
// stale self-reported claim does not increase resilient replica count."
func TestPhase0_StaleClaimsDoNotCountAsResilience(t *testing.T) {
	rules := DefaultRules()

	stale := func(alias, domain string) Replica {
		r := replica(alias, domain)
		r.LastVerifiedAt = now.Add(-rules.RevalidationWindow - time.Hour)
		return r
	}

	got := Assess([]Replica{stale("a", "op1"), stale("b", "op2"), stale("c", "op3")}, rules, now)
	if got.Independent != 0 {
		t.Fatalf("independent = %d, want 0: none of these was revalidated inside the window", got.Independent)
	}
	if got.Risk != RiskStale {
		t.Fatalf("risk = %s, want stale", got.Risk)
	}
	// The sources are reachable, so the item is not unavailable.
	if got.Online != 3 {
		t.Errorf("online = %d, want 3", got.Online)
	}

	// A replica that has never been verified is not evidence of anything.
	never := replica("d", "op4")
	never.LastVerifiedAt = time.Time{}
	if Assess([]Replica{never}, rules, now).Independent != 0 {
		t.Error("a never-verified replica counted toward resilience")
	}

	// One fresh replica among stale ones is at risk, not fragile.
	mixed := Assess([]Replica{stale("a", "op1"), stale("b", "op2"), replica("c", "op3")}, rules, now)
	if mixed.Risk != RiskAtRisk {
		t.Errorf("risk = %s, want at_risk", mixed.Risk)
	}
}

// TestPhase0_OfflineIsUnavailableNotMissing is Phase 4 acceptance: "One offline
// source produces an unavailable state without becoming missing."
func TestPhase0_OfflineIsUnavailableNotMissing(t *testing.T) {
	offline := replica("a", "op1")
	offline.Availability = protocol.AvailOffline

	got := Assess([]Replica{offline}, DefaultRules(), now)
	if got.Risk != RiskUnavailable {
		t.Fatalf("risk = %s, want unavailable", got.Risk)
	}
	if got.Risk == RiskMissing {
		t.Fatal("an offline source made the item missing; its inventory must stay known")
	}
	if got.Confirmed != 1 {
		t.Errorf("confirmed = %d, want 1: the holding is still known", got.Confirmed)
	}
	if got.Online != 0 {
		t.Errorf("online = %d, want 0", got.Online)
	}
}

// A confirmed deletion removes that Bridge as a source. With no other source,
// the item becomes missing rather than merely unavailable.
func TestConfirmedDeletionRemovesTheSource(t *testing.T) {
	removed := replica("a", "op1")
	removed.Availability = protocol.AvailRemoved

	got := Assess([]Replica{removed}, DefaultRules(), now)
	if got.Confirmed != 0 {
		t.Errorf("confirmed = %d, want 0 after a tombstone", got.Confirmed)
	}
	if got.Risk != RiskMissing {
		t.Errorf("risk = %s, want missing", got.Risk)
	}

	// With another live source, the tombstone removes only that Bridge.
	both := Assess([]Replica{removed, replica("b", "op2")}, DefaultRules(), now)
	if both.Confirmed != 1 || both.Risk != RiskAtRisk {
		t.Errorf("confirmed = %d risk = %s, want 1 and at_risk", both.Confirmed, both.Risk)
	}
}

// Missing failure-domain data is not evidence that Bridges are independent.
// Unknown sources establish that at least one copy exists, but must never make
// an item look resilient on their own.
func TestMissingFailureDomainDoesNotCreateFalseResilience(t *testing.T) {
	a := replica("alias-a", "")
	b := replica("alias-b", "")
	c := replica("alias-c", "")

	got := Assess([]Replica{a, b, c}, DefaultRules(), now)
	if got.Independent != 1 {
		t.Fatalf("independent = %d, want 1: unknown ownership cannot prove independent failure domains", got.Independent)
	}
	if got.Risk != RiskAtRisk {
		t.Fatalf("risk = %s, want at_risk: three unknown domains must not appear resilient", got.Risk)
	}
	if got.Confirmed != 3 || got.Online != 3 {
		t.Fatalf("confirmed = %d online = %d, want 3 and 3: the raw source counts must remain visible", got.Confirmed, got.Online)
	}
}

func TestDuplicateBridgeAliasDoesNotInflateCounts(t *testing.T) {
	one := replica("same-bridge", "operator-1")
	duplicate := one

	got := Assess([]Replica{one, duplicate}, DefaultRules(), now)
	if got.Confirmed != 1 || got.Online != 1 || got.Independent != 1 {
		t.Fatalf("duplicate claim produced confirmed=%d online=%d independent=%d, want 1/1/1",
			got.Confirmed, got.Online, got.Independent)
	}
	if got.Risk != RiskAtRisk {
		t.Fatalf("risk = %s, want at_risk", got.Risk)
	}

	// One Bridge claiming contradictory ownership is still one Bridge and is
	// conservatively assigned to the shared unknown bucket.
	contradictory := duplicate
	contradictory.FailureDomain = "operator-2"
	got = Assess([]Replica{one, contradictory}, DefaultRules(), now)
	if got.Independent != 1 {
		t.Fatalf("one Bridge in contradictory domains counted as %d independent replicas", got.Independent)
	}
}

func TestFutureVerificationTimestampDoesNotCount(t *testing.T) {
	future := replica("future-bridge", "operator-1")
	future.LastVerifiedAt = now.Add(24 * time.Hour)

	got := Assess([]Replica{future}, DefaultRules(), now)
	if got.Independent != 0 {
		t.Fatalf("future-dated claim counted as %d independent replicas", got.Independent)
	}
	if got.Risk != RiskStale {
		t.Fatalf("risk = %s, want stale for an online source with no valid recent verification", got.Risk)
	}
}

func TestTombstoneDominatesDuplicateClaim(t *testing.T) {
	active := replica("same-bridge", "operator-1")
	removed := active
	removed.Availability = protocol.AvailRemoved

	got := Assess([]Replica{active, removed}, DefaultRules(), now)
	if got.Confirmed != 0 || got.Online != 0 || got.Independent != 0 {
		t.Fatalf("tombstoned duplicate produced confirmed=%d online=%d independent=%d, want 0/0/0",
			got.Confirmed, got.Online, got.Independent)
	}
	if got.Risk != RiskMissing {
		t.Fatalf("risk = %s, want missing", got.Risk)
	}
}

func TestComputeCoverageNamesItsProfileAndSet(t *testing.T) {
	rules := DefaultRules()
	expected := []protocol.FileID{}
	for i := 0; i < 6; i++ {
		expected = append(expected,
			protocol.FileIDFromCanonicalDigest(protocol.DigestBytes([]byte(fmt.Sprintf("game-%d", i))).SHA256))
	}

	offline := replica("x", "op9")
	offline.Availability = protocol.AvailOffline

	replicas := map[protocol.FileID][]Replica{
		expected[0]: {replica("a", "op1"), replica("b", "op2"), replica("c", "op3")}, // resilient
		expected[1]: {replica("a", "op1"), replica("b", "op2")},                      // fragile
		expected[2]: {replica("a", "op1")},                                           // at risk
		expected[3]: {offline},                                                       // unavailable
		// expected[4] and [5] are held by nobody: missing.
	}

	cov := ComputeCoverage(protocol.PlatformGB, "North America plus World 1G1R v1",
		"no-intro Nintendo - Game Boy 20260806", expected, replicas, rules, now)

	if cov.ProfileRef == "" || cov.SetRef == "" || cov.RulesVersion == "" {
		t.Fatal("a coverage figure was produced that does not name its profile, reference set, and rules version")
	}
	if cov.Expected != 6 {
		t.Errorf("expected = %d, want 6", cov.Expected)
	}
	if cov.Content != 4 {
		t.Errorf("content = %d, want 4: an offline holding is still content", cov.Content)
	}
	if cov.Available != 3 {
		t.Errorf("available = %d, want 3: the offline holding cannot be served", cov.Available)
	}
	if cov.Resilient != 1 {
		t.Errorf("resilient = %d, want 1", cov.Resilient)
	}
	if cov.Missing != 2 {
		t.Errorf("missing = %d, want 2", cov.Missing)
	}

	// The risk states must partition the expected set exactly, so the figures
	// can be checked against each other rather than taken on trust.
	sum := cov.Resilient + cov.Fragile + cov.AtRisk + cov.Stale + cov.Unavailable + cov.Missing
	if sum != cov.Expected {
		t.Errorf("risk states sum to %d but %d entries were expected", sum, cov.Expected)
	}

	// Content coverage must never be presented as playability. BIOS material is
	// outside the initial release, so nothing here can claim readiness.
	if cov.PlayabilityKnown {
		t.Error("a playability-readiness figure was claimed while BIOS material is not shared")
	}
}

func TestOnlyResilientItemsStopNeedingReplication(t *testing.T) {
	for _, r := range RiskOrder() {
		want := r != RiskResilient
		if r.NeedsReplication() != want {
			t.Errorf("%s: NeedsReplication() = %v, want %v", r, r.NeedsReplication(), want)
		}
	}
}

func TestRiskOrderIsMostUrgentFirst(t *testing.T) {
	order := RiskOrder()
	if order[0] != RiskMissing {
		t.Errorf("most urgent state is %s, want missing", order[0])
	}
	if order[len(order)-1] != RiskResilient {
		t.Errorf("least urgent state is %s, want resilient", order[len(order)-1])
	}
	if len(order) != 6 {
		t.Errorf("RiskOrder lists %d states, want all 6", len(order))
	}
}
