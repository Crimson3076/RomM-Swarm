package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/scan"
	"github.com/Crimson3076/RomM-Swarm/internal/romfixture"
	"github.com/Crimson3076/RomM-Swarm/protocol"
	"github.com/Crimson3076/RomM-Swarm/verify"
)

// fakeScanSource is a stand-in RomM inventory a test can script directly.
// bridge/scan.Source is a plain Go interface, so unlike RommUploader's tests
// (which need a real chunked-upload protocol server), no HTTP server is
// needed here — only what RommLibrary does with a Source matters.
type fakeScanSource struct {
	records []scan.ROMRecord
	payload map[string][]byte // keyed by ROMRecord.ID

	listErr     error
	downloadErr map[string]error
}

func (f *fakeScanSource) ListROMs(_ context.Context, page scan.Page) ([]scan.ROMRecord, bool, error) {
	if f.listErr != nil {
		return nil, false, f.listErr
	}
	start := page.Offset
	if start > len(f.records) {
		start = len(f.records)
	}
	end := len(f.records)
	if page.Limit > 0 && start+page.Limit < end {
		end = start + page.Limit
	}
	hasMore := end < len(f.records)
	return f.records[start:end], hasMore, nil
}

func (f *fakeScanSource) Download(_ context.Context, rec scan.ROMRecord) (io.ReadCloser, int64, error) {
	if err := f.downloadErr[rec.ID]; err != nil {
		return nil, 0, err
	}
	payload, ok := f.payload[rec.ID]
	if !ok {
		return nil, 0, fmt.Errorf("fakeScanSource: no payload recorded for %s", rec.ID)
	}
	return io.NopCloser(bytes.NewReader(payload)), int64(len(payload)), nil
}

// canonicalOf recomputes the canonical digest the same way RommLibrary does,
// so a test can state its expectation without duplicating adapter logic.
func canonicalOf(t *testing.T, payload []byte, hint protocol.PlatformID) protocol.Digest {
	t.Helper()
	res, err := (&verify.Analyzer{}).Analyze(bytes.NewReader(payload), int64(len(payload)), "x", hint)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.Canonical.SHA256 == "" {
		t.Fatal("test fixture did not canonicalize; the test payload is not recognisable")
	}
	return res.Canonical
}

// TestPhase0_RommLibraryIndependentlyVerifiesRatherThanTrustingRomMsHashes is
// the closing half of Phase 0's "API read, download, upload, and ingestion
// workflow" deliverable: the observe side, which docs/phase0/multi-file-
// archive-and-ingestion-behavior.md flagged as real, tested code's missing
// counterpart to RommUploader.
//
// RomM's own reported hash field is deliberately wrong here. If RommLibrary
// ever trusted it instead of downloading and recomputing, this test would not
// catch a mismatch — the whole point of independent re-verification.
func TestPhase0_RommLibraryIndependentlyVerifiesRatherThanTrustingRomMsHashes(t *testing.T) {
	payload := romfixture.GameBoy("VERIFIED QUEST", 65536, false)
	want := canonicalOf(t, payload, protocol.PlatformGB)

	src := &fakeScanSource{
		records: []scan.ROMRecord{
			{ID: "1", PlatformSlug: "gb", Name: "Quest (USA)", FSName: "Quest (USA).gb",
				FSSizeBytes: int64(len(payload)), Hashes: map[string]string{"sha1_hash": "0000000000000000000000000000000000dead"}},
		},
		payload: map[string][]byte{"1": payload},
	}

	lib := &RommLibrary{Source: src}
	obs, err := lib.Observe(context.Background(), "Quest (USA).gb", Expected{
		Platform:  protocol.PlatformGB,
		FileID:    protocol.FileIDFromCanonicalDigest(want.SHA256),
		Canonical: want,
	})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !obs.Present {
		t.Fatal("Observe reported the item as absent despite a matching listing entry")
	}
	if obs.Canonical.SHA256 != want.SHA256 {
		t.Fatalf("observed canonical %s, want %s", obs.Canonical.SHA256, want.SHA256)
	}
	if obs.Platform != protocol.PlatformGB {
		t.Fatalf("observed platform %s, want gb", obs.Platform)
	}
	if obs.Name != "Quest (USA)" {
		t.Fatalf("observed name %q, want RomM's own metadata title", obs.Name)
	}
}

// TestPhase0_RommLibraryLocatesByFilenameAcrossPages proves the listing walk
// covers every page rather than only the first, and that platform and
// filename both have to agree before a record is even downloaded.
func TestPhase0_RommLibraryLocatesByFilenameAcrossPages(t *testing.T) {
	target := romfixture.GameBoy("PAGE TWO GAME", 32768, false)
	want := canonicalOf(t, target, protocol.PlatformGB)

	src := &fakeScanSource{
		records: []scan.ROMRecord{
			{ID: "1", PlatformSlug: "gb", Name: "Decoy One", FSName: "Decoy One.gb"},
			{ID: "2", PlatformSlug: "gba", Name: "Wrong Platform", FSName: "Page Two Game.gb"},
			{ID: "3", PlatformSlug: "gb", Name: "Page Two Game (USA)", FSName: "Page Two Game.gb"},
		},
		payload: map[string][]byte{
			"1": romfixture.GameBoy("DECOY", 32768, false),
			"3": target,
		},
	}

	lib := &RommLibrary{Source: src, PageSize: 1} // force multiple pages over 3 records
	obs, err := lib.Observe(context.Background(), "Page Two Game.gb", Expected{
		Platform: protocol.PlatformGB, FileID: protocol.FileIDFromCanonicalDigest(want.SHA256), Canonical: want,
	})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !obs.Present || obs.Canonical.SHA256 != want.SHA256 {
		t.Fatalf("did not locate the matching record across pages: %+v", obs)
	}
}

// TestRommLibraryReportsAbsentWhenNothingMatches is the ordinary "RomM has
// not indexed this yet" case Reconciler polls through.
func TestRommLibraryReportsAbsentWhenNothingMatches(t *testing.T) {
	src := &fakeScanSource{records: []scan.ROMRecord{
		{ID: "1", PlatformSlug: "gb", FSName: "Something Else.gb"},
	}}
	lib := &RommLibrary{Source: src}

	obs, err := lib.Observe(context.Background(), "Not Indexed Yet.gb", Expected{
		Platform: protocol.PlatformGB, FileID: protocol.FileIDFromCanonicalDigest("a"), Canonical: protocol.Digest{SHA256: "a"},
	})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.Present {
		t.Fatal("Observe reported an item present when nothing in the listing matched")
	}
}

// A download failure on a matched candidate is a transient RomM problem, not
// a verdict — it must be an error, which Reconciler treats as retryable,
// never a false "absent".
func TestRommLibraryDownloadFailureIsAnErrorNotAnAbsence(t *testing.T) {
	src := &fakeScanSource{
		records:     []scan.ROMRecord{{ID: "1", PlatformSlug: "gb", FSName: "x.gb"}},
		downloadErr: map[string]error{"1": errors.New("connection reset")},
	}
	lib := &RommLibrary{Source: src}

	_, err := lib.Observe(context.Background(), "x.gb", Expected{
		Platform: protocol.PlatformGB, FileID: protocol.FileIDFromCanonicalDigest("a"), Canonical: protocol.Digest{SHA256: "a"},
	})
	if err == nil {
		t.Fatal("a download failure on a matched candidate was not reported as an error")
	}
}

func TestRommLibraryRequiresItsDependencies(t *testing.T) {
	lib := &RommLibrary{}
	if _, err := lib.Observe(context.Background(), "x.gb", Expected{Platform: protocol.PlatformGB}); err == nil {
		t.Fatal("Observe succeeded with no Source configured")
	}

	lib = &RommLibrary{Source: &fakeScanSource{}}
	if _, err := lib.Observe(context.Background(), "", Expected{Platform: protocol.PlatformGB}); err == nil {
		t.Fatal("Observe succeeded with no filename to locate the item by")
	}
}

// TestPhase0_ReconcilerMatchesAgainstARealRommLibrary runs the actual
// Reconciler on top of RommLibrary end to end, the same way flow.go wires
// them in production — proving the two proven halves (Reconciler's polling
// logic, RommLibrary's observation) work together, not just individually.
func TestPhase0_ReconcilerMatchesAgainstARealRommLibrary(t *testing.T) {
	payload := romfixture.GameBoy("END TO END", 65536, false)
	want := canonicalOf(t, payload, protocol.PlatformGB)

	src := &fakeScanSource{payload: map[string][]byte{"1": payload}}
	lib := &RommLibrary{Source: src}

	c := &clock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	calls := 0
	r := &Reconciler{
		Library: libraryFunc(func(ctx context.Context, filename string, expected Expected) (Observation, error) {
			calls++
			if calls < 2 {
				// RomM has not indexed it on the first poll, same as the real
				// five-minute-debounce behaviour confirmed in ADR 0003.
				src.records = nil
				return lib.Observe(ctx, filename, expected)
			}
			src.records = []scan.ROMRecord{
				{ID: "1", PlatformSlug: "gb", Name: "End To End (USA)", FSName: "End To End.gb", FSSizeBytes: int64(len(payload))},
			}
			return lib.Observe(ctx, filename, expected)
		}),
		Poll: time.Second, Timeout: time.Minute, Now: c.Now, After: c.After,
	}

	result, err := r.Await(context.Background(), "End To End.gb", Expected{
		Platform: protocol.PlatformGB, FileID: protocol.FileIDFromCanonicalDigest(want.SHA256), Canonical: want,
	})
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if result.State != protocol.StateRommMatched {
		t.Fatalf("final state is %s, want romm_matched (%s)", result.State, result.Detail)
	}
}

// libraryFunc adapts a function to the Library interface, for tests that
// need to script Observe's behaviour across calls without a full fake type.
type libraryFunc func(context.Context, string, Expected) (Observation, error)

func (f libraryFunc) Observe(ctx context.Context, filename string, expected Expected) (Observation, error) {
	return f(ctx, filename, expected)
}
