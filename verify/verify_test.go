package verify

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Crimson3076/RomM-Swarm/internal/romfixture"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

func analyze(t *testing.T, data []byte, name string, hint protocol.PlatformID) *Result {
	t.Helper()
	a := &Analyzer{}
	res, err := a.Analyze(bytes.NewReader(data), int64(len(data)), name, hint)
	if err != nil {
		t.Fatalf("Analyze(%s): %v", name, err)
	}
	return res
}

// TestPhase0_EveryInitialPlatformIsRecognised is Phase 0 acceptance: every
// initial platform has repeatable canonicalization fixtures that accept
// known-good variants.
func TestPhase0_EveryInitialPlatformIsRecognised(t *testing.T) {
	genesisBin := romfixture.GenesisBin("SONIC", 131072)
	genesisSMD, err := romfixture.GenesisSMD(genesisBin)
	if err != nil {
		t.Fatalf("building the interleaved fixture: %v", err)
	}

	cases := []struct {
		name     string
		data     []byte
		platform protocol.PlatformID
		adapter  string
	}{
		{"game boy", romfixture.GameBoy("TETRIS", 32768, false), protocol.PlatformGB, "nintendo.gb"},
		{"game boy color", romfixture.GameBoy("ZELDA DX", 1048576, true), protocol.PlatformGBC, "nintendo.gbc"},
		{"game boy advance", romfixture.GameBoyAdvance("METROID", 4194304), protocol.PlatformGBA, "nintendo.gba"},
		{"nintendo ds", romfixture.NintendoDS(romfixture.NintendoDSOptions{
			Title: "MARIOKART", CapacityShift: 2, UsedBytes: 300000,
		}), protocol.PlatformNDS, "nintendo.nds"},
		{"mega drive", genesisBin, protocol.PlatformGenesis, "sega.genesis.bin"},
		{"mega drive copier format", genesisSMD, protocol.PlatformGenesis, "sega.genesis.smd"},
	}

	// Every platform named in the protocol's initial set must be covered here,
	// so adding a platform without a fixture fails rather than passing quietly.
	covered := map[protocol.PlatformID]bool{}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := analyze(t, tc.data, tc.name, "")

			if !res.Canonicalized {
				t.Fatalf("no canonical payload was produced; notes: %v", res.Notes)
			}
			if res.Platform != tc.platform {
				t.Errorf("platform = %s, want %s", res.Platform, tc.platform)
			}
			if res.Adapter.ID != tc.adapter {
				t.Errorf("adapter = %s, want %s", res.Adapter.ID, tc.adapter)
			}
			if res.Confidence != ConfidenceStrong {
				t.Errorf("confidence = %s, want strong", res.Confidence)
			}
			if res.Adapter.Version == "" {
				t.Error("the result does not name the adapter version it was produced under")
			}
			if res.Stored.SHA256 == "" || res.Canonical.SHA256 == "" {
				t.Error("an identity is missing from the result")
			}
			covered[tc.platform] = true
		})
	}

	for _, p := range protocol.InitialPlatforms() {
		if !covered[p] {
			t.Errorf("platform %s is in the initial set but has no canonicalization fixture", p)
		}
	}
}

// TestPhase0_GenesisSMDAndBinCanonicalizeIdentically is the case a single
// raw-file hash rule cannot handle: the same game in two encodings that share no
// bytes in common order must produce one canonical identity.
func TestPhase0_GenesisSMDAndBinCanonicalizeIdentically(t *testing.T) {
	bin := romfixture.GenesisBin("STREETS OF RAGE", 262144)
	smd, err := romfixture.GenesisSMD(bin)
	if err != nil {
		t.Fatalf("building the interleaved fixture: %v", err)
	}

	binRes := analyze(t, bin, "game.bin", "")
	smdRes := analyze(t, smd, "game.smd", "")

	// The premise: the stored files really are different.
	if binRes.Stored.SHA256 == smdRes.Stored.SHA256 {
		t.Fatal("the fixtures are byte-identical, so this test proves nothing")
	}

	if binRes.Canonical.SHA256 != smdRes.Canonical.SHA256 {
		t.Fatalf("the two encodings produced different canonical identities:\n plain: %s\n copier: %s",
			binRes.Canonical.SHA256, smdRes.Canonical.SHA256)
	}

	// And the canonical form is the plain image the catalogue describes.
	if binRes.Canonical.SHA256 != protocol.DigestBytes(bin).SHA256 {
		t.Fatal("the canonical payload is not the plain image the reference set describes")
	}
	if smdRes.Canonical.Size != int64(len(bin)) {
		t.Fatalf("the de-interleaved payload is %d bytes, want %d", smdRes.Canonical.Size, len(bin))
	}

	// A Bridge holding either encoding must offer the same exact-file id.
	if protocol.FileIDFromCanonicalDigest(binRes.Canonical.SHA256) !=
		protocol.FileIDFromCanonicalDigest(smdRes.Canonical.SHA256) {
		t.Fatal("the two encodings produced different exact-file identifiers")
	}
}

// TestPhase0_TrimmedNintendoDSDumpIsReconstructed covers the trimmed-dump
// behaviour Phase 0 requires be tested rather than assumed.
func TestPhase0_TrimmedNintendoDSDumpIsReconstructed(t *testing.T) {
	full := romfixture.NintendoDS(romfixture.NintendoDSOptions{
		Title: "ADVANCE WARS", CapacityShift: 2, UsedBytes: 400000,
	})
	trimmed := romfixture.NintendoDSTrimmed(full)

	if len(trimmed) >= len(full) {
		t.Fatal("the trimmed fixture is not actually smaller than the full dump")
	}

	fullRes := analyze(t, full, "full.nds", "")
	trimmedRes := analyze(t, trimmed, "trimmed.nds", "")

	if !trimmedRes.Canonicalized {
		t.Fatalf("the trimmed dump was not canonicalized; notes: %v", trimmedRes.Notes)
	}
	if trimmedRes.Canonical.SHA256 != fullRes.Canonical.SHA256 {
		t.Fatal("the reconstructed trimmed dump does not match the full cartridge dump")
	}
	if trimmedRes.Canonical.Size != int64(len(full)) {
		t.Fatalf("the reconstruction is %d bytes, want the %d-byte cartridge size",
			trimmedRes.Canonical.Size, len(full))
	}

	// The stored identity must still describe the trimmed file on disk. The
	// owner's file was not rewritten, and the manifest must not claim it was.
	if trimmedRes.Stored.Size != int64(len(trimmed)) {
		t.Fatalf("stored size is %d, want the on-disk %d", trimmedRes.Stored.Size, len(trimmed))
	}
	if trimmedRes.Stored.SHA256 == trimmedRes.Canonical.SHA256 {
		t.Fatal("stored and canonical identities collapsed into one for a trimmed dump")
	}

	// The reconstruction is a guess about the padding byte, and the operator is
	// told so.
	if !hasNoteContaining(trimmedRes.Notes, "trimmed") {
		t.Errorf("the result does not explain that padding was reconstructed; notes: %v", trimmedRes.Notes)
	}
}

// A truncated dump is missing real data. Padding it would fabricate content, so
// the adapter must leave it alone.
func TestTruncatedNintendoDSDumpIsNotPaddedIntoValidity(t *testing.T) {
	full := romfixture.NintendoDS(romfixture.NintendoDSOptions{
		Title: "TRUNCATED", CapacityShift: 2, UsedBytes: 400000,
	})
	truncated := full[:200000] // below the declared used size

	res := analyze(t, truncated, "truncated.nds", "")
	if res.Canonical.SHA256 == analyze(t, full, "full.nds", "").Canonical.SHA256 {
		t.Fatal("a truncated dump was padded into matching the full cartridge dump")
	}
	if !hasNoteContaining(res.Notes, "incomplete") {
		t.Errorf("the result does not report the dump as incomplete; notes: %v", res.Notes)
	}
}

// TestPhase0_ArchivedAndBareFilesShareOneCanonicalIdentity covers Phase 4's
// "Archive payload verification without trusting the outer archive hash alone".
func TestPhase0_ArchivedAndBareFilesShareOneCanonicalIdentity(t *testing.T) {
	rom := romfixture.GameBoy("KIRBY", 262144, false)
	archive := zipOf(t, map[string][]byte{"Kirby (USA).gb": rom})

	bare := analyze(t, rom, "Kirby (USA).gb", "")
	zipped := analyze(t, archive, "Kirby (USA).zip", "")

	if bare.Stored.SHA256 == zipped.Stored.SHA256 {
		t.Fatal("the archive and the bare file have the same stored identity, so this test proves nothing")
	}
	if bare.Canonical.SHA256 != zipped.Canonical.SHA256 {
		t.Fatal("archiving a ROM changed its canonical identity")
	}
	if zipped.Container == nil || zipped.Container.Kind != protocol.ContainerZip {
		t.Fatal("the container envelope was not recorded")
	}
	if len(zipped.Container.Members) != 1 {
		t.Fatalf("recorded %d archive members, want 1", len(zipped.Container.Members))
	}
	if bare.Container == nil || bare.Container.Kind != protocol.ContainerNone {
		t.Error("a bare file was not recorded as having no container")
	}
}

// TestPhase0_CanonicalizationNeverMutatesTheSource is the rule Phase 0 requires
// be defined, asserted against a real file on disk.
func TestPhase0_CanonicalizationNeverMutatesTheSource(t *testing.T) {
	dir := t.TempDir()

	full := romfixture.NintendoDS(romfixture.NintendoDSOptions{
		Title: "IMMUTABLE", CapacityShift: 1, UsedBytes: 100000,
	})
	// A trimmed DS dump is the strongest case: canonicalization genuinely
	// produces different bytes from the ones on disk.
	trimmed := romfixture.NintendoDSTrimmed(full)

	path := filepath.Join(dir, "trimmed.nds")
	if err := os.WriteFile(path, trimmed, 0o644); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	a := &Analyzer{TempDir: dir}
	res, err := a.AnalyzeFile(path, "")
	if err != nil {
		t.Fatalf("AnalyzeFile: %v", err)
	}
	if !res.Canonicalized || res.Canonical.Size == res.Stored.Size {
		t.Fatal("the fixture did not exercise a transforming canonicalization")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-reading the fixture: %v", err)
	}
	if !bytes.Equal(after, trimmed) {
		t.Fatal("canonicalization modified the stored source file")
	}
	afterStat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if afterStat.Size() != before.Size() {
		t.Fatalf("the stored file changed size: %d then %d", before.Size(), afterStat.Size())
	}
	if !afterStat.ModTime().Equal(before.ModTime()) {
		t.Error("canonicalization changed the stored file's modification time")
	}

	// No temporary files may be left behind either.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the temp dir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("analysis left files behind: %v", names)
	}
}

func TestGameBoyAndColorAreSeparatedByTheCartridgeNotTheFolder(t *testing.T) {
	mono := romfixture.GameBoy("MONO", 32768, false)
	color := romfixture.GameBoy("COLOR", 32768, true)

	// Even when the library insists both are plain Game Boy, the cartridge's own
	// Color flag decides.
	if got := analyze(t, mono, "mono.gb", protocol.PlatformGB).Platform; got != protocol.PlatformGB {
		t.Errorf("monochrome cartridge classified as %s", got)
	}
	res := analyze(t, color, "color.gb", protocol.PlatformGB)
	if res.Platform != protocol.PlatformGBC {
		t.Errorf("colour cartridge classified as %s, want %s", res.Platform, protocol.PlatformGBC)
	}
	if !hasNoteContaining(res.Notes, "the library says") {
		t.Errorf("the disagreement with the library was not recorded; notes: %v", res.Notes)
	}
}

func TestPayloadsWithNoValidHeaderAreNotClaimed(t *testing.T) {
	// Random bytes, and a file that is far too short to be anything.
	for _, data := range [][]byte{
		bytes.Repeat([]byte{0x5A}, 65536),
		{0x01, 0x02, 0x03},
		{},
	} {
		res := analyze(t, data, "junk.bin", "")
		if res.Canonicalized {
			t.Errorf("a payload of %d bytes with no valid header was canonicalized as %s",
				len(data), res.Platform)
		}
		if res.Stored.Size != int64(len(data)) {
			t.Error("the stored identity was not recorded for an unrecognised payload")
		}
	}
}

func TestCorruptedHeadersAreDetected(t *testing.T) {
	// A Game Boy image whose logo is damaged will not boot on hardware and must
	// not be claimed by the adapter.
	gb := romfixture.GameBoy("DAMAGED", 32768, false)
	if res := analyze(t, romfixture.Corrupt(gb, 0x110), "damaged.gb", ""); res.Canonicalized {
		t.Error("a Game Boy image with a damaged logo was still claimed")
	}

	// A damaged header checksum is a weaker signal: the image is still
	// recognisably a cartridge, so it is claimed but flagged.
	damagedChecksum := romfixture.Corrupt(gb, 0x14D)
	res := analyze(t, damagedChecksum, "damaged-checksum.gb", "")
	if !res.Canonicalized {
		t.Fatal("a Game Boy image with only a bad header checksum was not claimed at all")
	}
	if !hasNoteContaining(res.Notes, "may be damaged") {
		t.Errorf("the bad header checksum was not reported; notes: %v", res.Notes)
	}
}

func TestMultiMemberArchivesProduceNoCanonicalIdentity(t *testing.T) {
	// Multi-file content is outside the MVP. It must be visible locally and
	// never advertised, rather than being silently reduced to its first member.
	archive := zipOf(t, map[string][]byte{
		"disc.cue": []byte("FILE \"disc.bin\" BINARY\n"),
		"disc.bin": bytes.Repeat([]byte{0x11}, 4096),
	})
	res := analyze(t, archive, "disc.zip", "")

	if res.Canonicalized {
		t.Fatal("a multi-member archive produced a canonical identity")
	}
	if res.Container == nil || len(res.Container.Members) != 2 {
		t.Fatal("the multi-member container was not recorded")
	}
	if !hasNoteContaining(res.Notes, "outside the MVP") {
		t.Errorf("the reason was not explained; notes: %v", res.Notes)
	}
}

func TestUnsafeArchiveEntriesAreIgnored(t *testing.T) {
	rom := romfixture.GameBoy("SAFE", 32768, false)
	archive := zipOf(t, map[string][]byte{
		"../../etc/passwd": []byte("root:x:0:0"),
		"Safe (USA).gb":    rom,
	})

	res := analyze(t, archive, "mixed.zip", "")
	if !res.Canonicalized {
		t.Fatalf("the safe member was not used; notes: %v", res.Notes)
	}
	if res.Canonical.SHA256 != protocol.DigestBytes(rom).SHA256 {
		t.Fatal("the wrong archive member was canonicalized")
	}
	for _, m := range res.Container.Members {
		if m.Name == "../../etc/passwd" {
			t.Error("a path-traversal entry was recorded as a usable member")
		}
	}
	if !hasNoteContaining(res.Notes, "unsafe name") {
		t.Errorf("the unsafe entry was not reported; notes: %v", res.Notes)
	}
}

func TestUnsafeArchiveNames(t *testing.T) {
	unsafe := []string{
		"", "/etc/passwd", `\windows\system32`, "C:/games/rom.gb",
		"../rom.gb", "a/../../b/rom.gb", `a\..\..\b`, "rom\x00.gb",
	}
	for _, name := range unsafe {
		if !unsafeArchiveName(name) {
			t.Errorf("%q was not rejected", name)
		}
	}
	safe := []string{"rom.gb", "Game (USA).gb", "subdir/rom.gb", "a..b.gb"}
	for _, name := range safe {
		if unsafeArchiveName(name) {
			t.Errorf("%q was rejected but is safe", name)
		}
	}
}

func TestOversizedArchiveMembersSpillToDiskAndAreCleanedUp(t *testing.T) {
	dir := t.TempDir()
	rom := romfixture.GameBoy("BIG", 262144, false)
	archive := zipOf(t, map[string][]byte{"Big (USA).gb": rom})

	// Force the spill path by setting the in-memory limit below the payload.
	a := &Analyzer{TempDir: dir, MaxInMemory: 1024}
	res, err := a.Analyze(bytes.NewReader(archive), int64(len(archive)), "big.zip", "")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !res.Canonicalized {
		t.Fatalf("the spilled member was not canonicalized; notes: %v", res.Notes)
	}
	if res.Canonical.SHA256 != protocol.DigestBytes(rom).SHA256 {
		t.Fatal("spilling to disk changed the canonical identity")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the temp dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the spill file was not cleaned up: %d entries remain", len(entries))
	}
}

func TestRegistryRefusesDuplicateAdapters(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("registering the same adapter twice did not panic")
		}
	}()
	r := NewRegistry()
	r.Register(&GameBoyAdapter{})
	r.Register(&GameBoyAdapter{})
}

func TestAdapterSelectionIsDeterministic(t *testing.T) {
	rom := romfixture.GameBoyAdvance("DETERMINISM", 65536)
	first := analyze(t, rom, "rom.gba", "").Adapter
	for i := 0; i < 20; i++ {
		if got := analyze(t, rom, "rom.gba", "").Adapter; got != first {
			t.Fatalf("adapter selection varied between runs: %s then %s", first, got)
		}
	}
}

func TestDefaultRegistryCoversTheInitialPlatformSet(t *testing.T) {
	covered := map[protocol.PlatformID]bool{}
	for _, a := range DefaultRegistry().Adapters() {
		covered[a.Platform()] = true
	}
	for _, p := range protocol.InitialPlatforms() {
		if !covered[p] {
			t.Errorf("no adapter is registered for initial platform %s", p)
		}
	}
}

// zipOf builds a zip archive in memory.
func zipOf(t *testing.T, members map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// Sorted iteration would be nicer, but map order does not affect what the
	// analyser does with the result, and the tests assert on content not order.
	for name, data := range members {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("creating archive entry %q: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("writing archive entry %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing the archive: %v", err)
	}
	return buf.Bytes()
}

func hasNoteContaining(notes []string, substr string) bool {
	for _, n := range notes {
		if bytes.Contains([]byte(n), []byte(substr)) {
			return true
		}
	}
	return false
}
