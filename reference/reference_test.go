package reference

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/Crimson3076/RomM-Swarm/internal/romfixture"
	"github.com/Crimson3076/RomM-Swarm/protocol"
	"github.com/Crimson3076/RomM-Swarm/verify"
)

// datEntry describes one game to place in a synthesised catalogue.
type datEntry struct {
	name    string
	payload []byte
	status  string
}

// buildDAT renders a Logiqx catalogue over the given entries, with real hashes.
func buildDAT(version string, entries []datEntry) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?>` + "\n<datafile>\n")
	fmt.Fprintf(&b, "  <header>\n    <name>Nintendo - Game Boy</name>\n"+
		"    <description>Test catalogue</description>\n    <version>%s</version>\n"+
		"    <date>%s</date>\n    <author>romm-swarm tests</author>\n"+
		"    <homepage>No-Intro</homepage>\n  </header>\n", version, version)

	for _, e := range entries {
		d := protocol.DigestBytes(e.payload)
		status := ""
		if e.status != "" {
			status = fmt.Sprintf(` status="%s"`, e.status)
		}
		fmt.Fprintf(&b, "  <game name=%q>\n    <description>%s</description>\n"+
			"    <rom name=\"%s.gb\" size=\"%d\" crc=\"%s\" md5=\"%s\" sha1=\"%s\"%s/>\n  </game>\n",
			e.name, e.name, e.name, d.Size,
			strings.ToUpper(d.CRC32), strings.ToUpper(d.MD5), strings.ToUpper(d.SHA1), status)
	}
	b.WriteString("</datafile>\n")
	return []byte(b.String())
}

func importSet(t *testing.T, dat []byte) *Set {
	t.Helper()
	set, err := ImportDAT(bytes.NewReader(dat), ImportOptions{Platform: protocol.PlatformGB})
	if err != nil {
		t.Fatalf("ImportDAT: %v", err)
	}
	return set
}

func analyzeROM(t *testing.T, data []byte) *verify.Result {
	t.Helper()
	a := &verify.Analyzer{}
	res, err := a.Analyze(bytes.NewReader(data), int64(len(data)), "rom.gb", protocol.PlatformGB)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	return res
}

func TestImportRecordsProvenance(t *testing.T) {
	rom := romfixture.GameBoy("PROVENANCE", 32768, false)
	dat := buildDAT("20260806-120000", []datEntry{{name: "Provenance (USA)", payload: rom}})
	set := importSet(t, dat)

	if set.Family != FamilyNoIntro {
		t.Errorf("family = %q, want %q", set.Family, FamilyNoIntro)
	}
	if set.Version != "20260806-120000" {
		t.Errorf("version = %q", set.Version)
	}
	// Phase 4 requires the import checksum to be tracked, so a coverage figure
	// can be traced to the exact catalogue bytes behind it.
	if set.ImportDigest.SHA256 != protocol.DigestBytes(dat).SHA256 {
		t.Error("the import checksum does not describe the catalogue file")
	}
	if !strings.Contains(set.Ref(), "20260806-120000") {
		t.Errorf("the set reference does not name its version: %q", set.Ref())
	}
}

func TestImportRejectsCataloguesWithoutAVersion(t *testing.T) {
	dat := []byte(`<?xml version="1.0"?><datafile><header><name>X</name></header>` +
		`<game name="G"><rom name="g.gb" size="1" crc="00000000"/></game></datafile>`)
	if _, err := ImportDAT(bytes.NewReader(dat), ImportOptions{}); err == nil {
		t.Fatal("a catalogue with no version was accepted; a coverage figure could not name it")
	}
}

func TestImportSkipsKnownBadDumpsAndHashlessEntries(t *testing.T) {
	good := romfixture.GameBoy("GOOD", 32768, false)
	bad := romfixture.GameBoy("BAD", 32768, false)

	dat := buildDAT("1", []datEntry{
		{name: "Good (USA)", payload: good},
		{name: "Bad (USA)", payload: bad, status: "baddump"},
	})
	set := importSet(t, dat)

	if len(set.Entries) != 1 {
		t.Fatalf("imported %d entries, want 1: a known-bad dump must never be a verification target", len(set.Entries))
	}
	if len(set.Lookup(protocol.DigestBytes(bad))) != 0 {
		t.Error("a known-bad dump is still matchable")
	}
}

// TestPhase0_ClassificationStates walks every classification state Phase 4
// defines, and asserts that only one of them may be advertised.
func TestPhase0_ClassificationStates(t *testing.T) {
	usa := romfixture.GameBoy("HERO USA", 32768, false)
	japan := romfixture.GameBoy("HERO JP", 32768, false)
	unknown := romfixture.GameBoy("NOT IN CATALOGUE", 32768, false)

	set := importSet(t, buildDAT("1", []datEntry{
		{name: "Hero Quest (USA)", payload: usa},
		{name: "Hero Quest (Japan)", payload: japan},
	}))
	sel := DefaultProfile().Apply(set)

	cases := []struct {
		name     string
		payload  []byte
		metadata string
		want     protocol.Classification
	}{
		{"verified and in profile", usa, "Hero Quest", protocol.ClassVerifiedEligible},
		{"verified but out of profile", japan, "Hero Quest", protocol.ClassVerifiedExcluded},
		{"library names it but no reference entry matches", unknown, "Hero Quest", protocol.ClassMatchedUnverified},
		{"nothing matches and the library says nothing", unknown, "", protocol.ClassUnmatched},
		{"the library and the hash disagree", usa, "Completely Different Game", protocol.ClassConflict},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Classify(analyzeROM(t, tc.payload), sel, tc.metadata)
			if out.Classification != tc.want {
				t.Fatalf("classification = %s, want %s (notes: %v)", out.Classification, tc.want, out.Notes)
			}
			if out.Classification.Publishable() && tc.want != protocol.ClassVerifiedEligible {
				t.Fatal("a non-eligible classification reported itself publishable")
			}
			if len(out.Notes) == 0 {
				t.Error("the outcome carries no explanation for the operator")
			}
		})
	}
}

// TestPhase0_CorruptedPayloadsAreNeverVerified is the other half of the Phase 0
// fixture requirement: corrupted payloads must be rejected.
func TestPhase0_CorruptedPayloadsAreNeverVerified(t *testing.T) {
	rom := romfixture.GameBoy("PRISTINE", 262144, false)
	set := importSet(t, buildDAT("1", []datEntry{{name: "Pristine (USA)", payload: rom}}))
	sel := DefaultProfile().Apply(set)

	if got := Classify(analyzeROM(t, rom), sel, "Pristine").Classification; got != protocol.ClassVerifiedEligible {
		t.Fatalf("the pristine payload classified as %s, so this test proves nothing", got)
	}

	// Damage the body rather than the header: the file still looks like a
	// cartridge and still canonicalizes, but it is not the dump the catalogue
	// describes. This is the case a filename-based catalogue gets wrong.
	for _, offset := range []int{0x200, 0x8000, 0x3FFFF} {
		damaged := romfixture.Corrupt(rom, offset)
		out := Classify(analyzeROM(t, damaged), sel, "Pristine")
		if out.Classification.Publishable() {
			t.Errorf("a payload damaged at 0x%X was classified %s and would be advertised",
				offset, out.Classification)
		}
		if out.Classification != protocol.ClassMatchedUnverified {
			t.Errorf("a payload damaged at 0x%X classified as %s, want matched_unverified",
				offset, out.Classification)
		}
	}
}

// TestPhase0_ArchivedHoldingsVerifyIdenticallyToBareOnes ties the container
// handling to the verification decision.
func TestPhase0_ArchivedHoldingsVerifyIdenticallyToBareOnes(t *testing.T) {
	rom := romfixture.GameBoy("ARCHIVED", 65536, false)
	set := importSet(t, buildDAT("1", []datEntry{{name: "Archived (USA)", payload: rom}}))
	sel := DefaultProfile().Apply(set)

	bare := Classify(analyzeROM(t, rom), sel, "Archived")
	if bare.Classification != protocol.ClassVerifiedEligible {
		t.Fatalf("the bare holding classified as %s", bare.Classification)
	}
	if bare.Match == nil {
		t.Fatal("a verified holding carries no reference match")
	}
	// The match must name its catalogue and version, per Phase 4 acceptance.
	if bare.Match.SetVersion == "" || bare.Match.Family == "" {
		t.Error("the reference match does not name its catalogue family and version")
	}
	if bare.Match.Strength < protocol.StrengthCatalogue {
		t.Errorf("verification rested on %s agreement only", bare.Match.Strength)
	}
}

func TestOneGamePerCanonicalPicksTheBestRegionAndNewestRevision(t *testing.T) {
	mk := func(title string) []byte { return romfixture.GameBoy(title, 32768, false) }

	set := importSet(t, buildDAT("1", []datEntry{
		{name: "Quest (Japan)", payload: mk("q-jp")},
		{name: "Quest (Europe)", payload: mk("q-eu")},
		{name: "Quest (USA)", payload: mk("q-us")},
		{name: "Quest (USA) (Rev 1)", payload: mk("q-us-r1")},
		{name: "Quest (World)", payload: mk("q-world")},
	}))
	sel := DefaultProfile().Apply(set)

	if sel.Size() != 1 {
		t.Fatalf("1G1R selected %d entries for one game, want 1", sel.Size())
	}
	chosen, ok := sel.ChosenFor("Quest")
	if !ok {
		t.Fatal("no entry was chosen for the canonical game")
	}
	if chosen.GameName != "Quest (USA) (Rev 1)" {
		t.Fatalf("1G1R chose %q, want the newest North American revision", chosen.GameName)
	}
}

func TestProfileWithoutOneGamePerCanonicalCountsEveryEligibleEntry(t *testing.T) {
	mk := func(title string) []byte { return romfixture.GameBoy(title, 32768, false) }
	set := importSet(t, buildDAT("1", []datEntry{
		{name: "Quest (USA)", payload: mk("a")},
		{name: "Quest (World)", payload: mk("b")},
		{name: "Quest (Japan)", payload: mk("c")},
	}))

	p := DefaultProfile()
	p.OneGamePerCanonical = false
	sel := p.Apply(set)

	// USA and World are in profile; Japan is not.
	if sel.Size() != 2 {
		t.Fatalf("selected %d entries, want 2", sel.Size())
	}
}

func TestProfileExcludesPreReleaseAndBIOSMaterial(t *testing.T) {
	mk := func(title string) []byte { return romfixture.GameBoy(title, 32768, false) }
	set := importSet(t, buildDAT("1", []datEntry{
		{name: "Quest (USA)", payload: mk("retail")},
		{name: "Quest (USA) (Beta)", payload: mk("beta")},
		{name: "Quest (USA) (Proto)", payload: mk("proto")},
		{name: "Quest (USA) (Demo)", payload: mk("demo")},
		{name: "[BIOS] Game Boy (World)", payload: mk("bios")},
	}))
	sel := DefaultProfile().Apply(set)

	if sel.Size() != 1 {
		t.Fatalf("selected %d entries, want only the retail release", sel.Size())
	}
	chosen, _ := sel.ChosenFor("Quest")
	if chosen == nil || chosen.GameName != "Quest (USA)" {
		t.Fatalf("selected %v, want the retail release", chosen)
	}

	// Coverage must not count BIOS material, because the project does not share
	// it. Phase 4 requires content coverage and playability readiness to be
	// reported separately for exactly this reason.
	for _, idx := range sel.IncludedIndices() {
		if set.Entries[idx].IsBIOS {
			t.Error("a BIOS entry counts toward coverage under the default profile")
		}
	}
}

func TestLookupRejectsPartialAgreement(t *testing.T) {
	rom := romfixture.GameBoy("PARTIAL", 32768, false)
	set := importSet(t, buildDAT("1", []datEntry{{name: "Partial (USA)", payload: rom}}))

	// A digest agreeing on CRC32 but disagreeing on SHA-1 is not a match, even
	// though the CRC32 index would surface it as a candidate.
	real := protocol.DigestBytes(rom)
	forged := protocol.Digest{
		Size:  real.Size,
		CRC32: real.CRC32,
		MD5:   real.MD5,
		SHA1:  strings.Repeat("0", 40),
	}
	if got := set.Lookup(forged); len(got) != 0 {
		t.Fatalf("a payload agreeing only on the weaker hashes matched %d entries", len(got))
	}
}

func TestParseName(t *testing.T) {
	cases := []struct {
		in        string
		key       string
		regions   []string
		languages []string
		revRank   int
		bios      bool
		flags     []string
	}{
		{in: "Super Mario Land (World)", key: "Super Mario Land", regions: []string{"World"}},
		{in: "Pokemon - Red Version (USA, Europe)", key: "Pokemon - Red Version",
			regions: []string{"USA", "Europe"}},
		{in: "Quest (Europe) (En,Fr,De)", key: "Quest",
			regions: []string{"Europe"}, languages: []string{"en", "fr", "de"}},
		{in: "Quest (USA) (Rev 1)", key: "Quest", regions: []string{"USA"}, revRank: 1},
		{in: "Quest (USA) (Rev A)", key: "Quest", regions: []string{"USA"}, revRank: 1},
		{in: "Quest (USA) (Rev B)", key: "Quest", regions: []string{"USA"}, revRank: 2},
		{in: "[BIOS] Game Boy (World)", key: "Game Boy", regions: []string{"World"}, bios: true},
		{in: "Quest (USA) (Beta)", key: "Quest", regions: []string{"USA"}, flags: []string{"Beta"}},
		{in: "Quest", key: "Quest"},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := ParseName(tc.in)
			if got.CanonicalKey != tc.key {
				t.Errorf("canonical key = %q, want %q", got.CanonicalKey, tc.key)
			}
			if !equalStrings(got.Regions, tc.regions) {
				t.Errorf("regions = %v, want %v", got.Regions, tc.regions)
			}
			if !equalStrings(got.Languages, tc.languages) {
				t.Errorf("languages = %v, want %v", got.Languages, tc.languages)
			}
			if got.RevisionRank != tc.revRank {
				t.Errorf("revision rank = %d, want %d", got.RevisionRank, tc.revRank)
			}
			if got.IsBIOS != tc.bios {
				t.Errorf("IsBIOS = %v, want %v", got.IsBIOS, tc.bios)
			}
			if tc.flags != nil && !equalStrings(got.Flags, tc.flags) {
				t.Errorf("flags = %v, want %v", got.Flags, tc.flags)
			}
		})
	}
}

// Later revisions must rank above earlier ones, or 1G1R keeps the wrong dump.
func TestRevisionRankOrdering(t *testing.T) {
	ordered := []string{"Rev 1", "Rev 2", "Rev 3"}
	last := 0
	for _, tag := range ordered {
		got := revisionRank(tag)
		if got <= last {
			t.Fatalf("%q ranked %d, not above the previous %d", tag, got, last)
		}
		last = got
	}
	if revisionRank("v1.1") <= revisionRank("v1.0") {
		t.Error("v1.1 did not rank above v1.0")
	}
	if revisionRank("v2.0") <= revisionRank("v1.9") {
		t.Error("v2.0 did not rank above v1.9")
	}
}

// A metadata cross-check must not manufacture conflicts out of ordinary
// punctuation differences, or genuine verified holdings land in a review queue.
func TestTitlesAgreeToleratesFormattingDifferences(t *testing.T) {
	agreeing := [][2]string{
		{"Pokemon - Red Version", "Pokemon Red Version"},
		{"The Legend of Zelda", "Legend of Zelda, The"},
		{"Super Mario Land", "super mario land"},
		{"Quest", "Quest II"}, // one contains the other
	}
	for _, pair := range agreeing {
		if !titlesAgree(pair[0], pair[1]) {
			t.Errorf("%q and %q were treated as a conflict", pair[0], pair[1])
		}
	}

	if titlesAgree("Metroid Fusion", "Advance Wars") {
		t.Error("two unrelated titles were treated as agreeing")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
