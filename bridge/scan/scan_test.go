package scan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/romm"
	"github.com/Crimson3076/RomM-Swarm/internal/romfixture"
	"github.com/Crimson3076/RomM-Swarm/protocol"
	"github.com/Crimson3076/RomM-Swarm/reference"
)

// This package's own test server, deliberately separate from
// internal/fakeromm. fakeromm's fixed ROM object ("fake rom content", a
// non-analyzable payload) exists to prove the capability probe's shape
// detection, and other tests in bridge/romm assert against its exact fields.
// Proving the scan pipeline end to end needs a server that serves genuinely
// analyzable content — real romfixture-generated cartridge images — which is a
// different enough job to warrant its own small server rather than bending a
// shared one to fit two purposes.

type testROM struct {
	id           int
	platformSlug string
	name         string
	fsName       string
	payload      []byte
}

// testServer serves an OpenAPI document matching the capability table's
// candidates, a paginated ROM listing, and real downloadable content.
func testServer(t *testing.T, roms []testROM) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"openapi": "3.1.0",
			"info":    map[string]any{"title": "RomM API", "version": "test"},
			"paths": map[string]any{
				"/api/heartbeat": map[string]any{"get": map[string]any{"operationId": "heartbeat"}},
				"/api/users/me":  map[string]any{"get": map[string]any{"operationId": "me"}},
				"/api/platforms": map[string]any{"get": map[string]any{"operationId": "platforms"}},
				"/api/roms": map[string]any{
					"get": map[string]any{
						"operationId": "roms",
						"parameters": []map[string]any{
							{"name": "limit", "in": "query"},
							{"name": "offset", "in": "query"},
						},
					},
					// Not exercised by the scan pipeline, but required by the
					// capability table (roms.upload) — declared here purely so
					// EvaluateCapabilities resolves it and setup()'s
					// MissingRequired check does not fail on it.
					"post": map[string]any{"operationId": "upload"},
				},
				// Not exercised by the scan pipeline, but required (roms.detail).
				"/api/roms/{id}":         map[string]any{"get": map[string]any{"operationId": "rom_detail"}},
				"/api/roms/{id}/content": map[string]any{"get": map[string]any{"operationId": "download"}},
			},
		})
	})

	mux.HandleFunc("/api/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"VERSION": "test"})
	})
	mux.HandleFunc("/api/users/me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"id": 1, "username": "bridge"})
	})
	mux.HandleFunc("/api/platforms", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{{"id": 1, "slug": "gb", "name": "Game Boy"}})
	})

	mux.HandleFunc("/api/roms", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if limit <= 0 {
			limit = len(roms)
		}

		var page []map[string]any
		for i := offset; i < offset+limit && i < len(roms); i++ {
			rom := roms[i]
			d := protocol.DigestBytes(rom.payload)
			page = append(page, map[string]any{
				"id":            rom.id,
				"platform_slug": rom.platformSlug,
				"name":          rom.name,
				"fs_name":       rom.fsName,
				"fs_size_bytes": len(rom.payload),
				"sha1_hash":     d.SHA1, // reported, and never trusted; see ROMRecord.Hashes
			})
		}
		writeJSON(w, map[string]any{"items": page, "total": len(roms)})
	})

	mux.HandleFunc("/api/roms/", func(w http.ResponseWriter, r *http.Request) {
		// Path is /api/roms/{id}/content
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/roms/"), "/")
		if len(parts) != 2 || parts[1] != "content" {
			http.NotFound(w, r)
			return
		}
		id, err := strconv.Atoi(parts[0])
		if err != nil {
			http.NotFound(w, r)
			return
		}
		for _, rom := range roms {
			if rom.id == id {
				w.Header().Set("Content-Length", strconv.Itoa(len(rom.payload)))
				_, _ = w.Write(rom.payload)
				return
			}
		}
		http.NotFound(w, r)
	})

	return httptest.NewServer(mux)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// buildDAT renders a minimal Logiqx catalogue with real hashes, matching the
// pattern used in reference/reference_test.go.
func buildDAT(entries map[string][]byte) []byte {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\"?>\n<datafile>\n")
	b.WriteString("  <header>\n    <name>Nintendo - Game Boy</name>\n" +
		"    <version>20260806-000000</version>\n  </header>\n")
	for name, payload := range entries {
		d := protocol.DigestBytes(payload)
		fmt.Fprintf(&b, "  <game name=%q>\n    <rom name=\"%s.gb\" size=\"%d\" crc=\"%s\" md5=\"%s\" sha1=\"%s\"/>\n  </game>\n",
			name, name, d.Size, strings.ToUpper(d.CRC32), strings.ToUpper(d.MD5), strings.ToUpper(d.SHA1))
	}
	b.WriteString("</datafile>\n")
	return []byte(b.String())
}

// setup probes the test server and returns a ready-to-use RommSource plus the
// probe report, mirroring exactly what a real Bridge would do: probe first,
// then build every subsequent request from what the probe resolved.
func setup(t *testing.T, srv *httptest.Server) (*RommSource, *romm.Report) {
	t.Helper()

	report, err := romm.Probe(context.Background(), romm.ProbeOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(romm.MissingRequired(report.Capabilities)) != 0 {
		t.Fatalf("the test server is missing required capabilities: %v", romm.MissingRequired(report.Capabilities))
	}

	client := romm.NewClient(srv.URL, "", romm.AuthScheme{})
	return &RommSource{Client: client, Report: report}, report
}

// TestPhase0_ScanProducesANormalizedManifestFromARomMServer is the Phase 0
// deliverable itself: "Prototype a normalized local inventory manifest from
// one RomM server."
func TestPhase0_ScanProducesANormalizedManifestFromARomMServer(t *testing.T) {
	verified := romfixture.GameBoy("VERIFIED QUEST", 65536, false)
	excludedRegion := romfixture.GameBoy("VERIFIED QUEST JP", 65536, false)
	unverified := romfixture.GameBoy("NOT IN CATALOGUE", 32768, false)

	roms := []testROM{
		{1, "gb", "Quest (USA)", "Quest (USA).gb", verified},
		{2, "gb", "Quest (Japan)", "Quest (Japan).gb", excludedRegion},
		{3, "gb", "Something Uncatalogued (USA)", "Uncatalogued (USA).gb", unverified},
		{4, "genesis", "Off Platform", "Off Platform.bin", romfixture.GenesisBin("OTHER", 65536)},
	}
	srv := testServer(t, roms)
	defer srv.Close()

	source, _ := setup(t, srv)

	set, err := reference.ImportDAT(bytes.NewReader(buildDAT(map[string][]byte{
		"Quest (USA)":   verified,
		"Quest (Japan)": excludedRegion,
	})), reference.ImportOptions{Platform: protocol.PlatformGB})
	if err != nil {
		t.Fatalf("ImportDAT: %v", err)
	}
	selection := reference.DefaultProfile().Apply(set)

	scanner := &Scanner{
		Source: source,
		Now:    func() time.Time { return time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC) },
	}

	result, err := scanner.ScanPlatform(context.Background(), PlatformSource{
		RomMSlug:  "gb",
		Platform:  protocol.PlatformGB,
		Selection: selection,
	})
	if err != nil {
		t.Fatalf("ScanPlatform: %v", err)
	}

	// Only the platform requested, and only the verified-eligible release,
	// becomes an item. The off-platform record and the excluded region are
	// accounted for, never silently dropped.
	if result.Scanned != 3 {
		t.Fatalf("scanned %d gb records, want 3 (the genesis record must not be counted)", result.Scanned)
	}
	if len(result.Items) != 1 {
		t.Fatalf("produced %d items, want 1: only the verified USA release", len(result.Items))
	}
	if len(result.Skipped) != 2 {
		t.Fatalf("produced %d skipped entries, want 2", len(result.Skipped))
	}

	item := result.Items[0]
	if item.Classification != protocol.ClassVerifiedEligible {
		t.Fatalf("the produced item is classified %s, want verified_eligible", item.Classification)
	}
	if item.Canonical.SHA256 != protocol.DigestBytes(verified).SHA256 {
		t.Fatal("the item's canonical identity does not match the verified payload")
	}
	if err := item.Validate(); err != nil {
		t.Fatalf("the scanned item fails its own validation: %v", err)
	}
	if item.Availability != protocol.AvailAvailable {
		t.Errorf("availability = %s, want available: it was just downloaded successfully", item.Availability)
	}

	// The Japanese release is verified but out of the default profile —
	// verified_excluded, not simply absent.
	var sawExcluded, sawUnmatched bool
	for _, s := range result.Skipped {
		if strings.Contains(s.Reason, string(protocol.ClassVerifiedExcluded)) {
			sawExcluded = true
		}
		if strings.Contains(s.Reason, string(protocol.ClassUnmatched)) || strings.Contains(s.Reason, string(protocol.ClassMatchedUnverified)) {
			sawUnmatched = true
		}
	}
	if !sawExcluded {
		t.Errorf("the out-of-profile release was not reported as excluded; skipped: %+v", result.Skipped)
	}
	if !sawUnmatched {
		t.Errorf("the uncatalogued release was not reported as unmatched; skipped: %+v", result.Skipped)
	}
}

// TestOnItemScannedReportsRunningProgress proves the fix for an operator
// report: a large single-platform scan with no per-item feedback looked
// indistinguishable from a hang. OnItemScanned must fire once per record,
// with a strictly increasing count, so a caller can show real progress
// instead of only "started"/"finished" for the whole platform.
func TestOnItemScannedReportsRunningProgress(t *testing.T) {
	verified := romfixture.GameBoy("PROGRESS QUEST", 65536, false)
	roms := []testROM{
		{1, "gb", "Progress Quest (USA)", "Progress Quest (USA).gb", verified},
		{2, "gb", "Second Game (USA)", "Second Game (USA).gb", romfixture.GameBoy("SECOND GAME", 65536, false)},
		{3, "gb", "Third Game (USA)", "Third Game (USA).gb", romfixture.GameBoy("THIRD GAME", 65536, false)},
	}
	srv := testServer(t, roms)
	defer srv.Close()

	source, _ := setup(t, srv)

	set, err := reference.ImportDAT(bytes.NewReader(buildDAT(map[string][]byte{
		"Progress Quest (USA)": verified,
	})), reference.ImportOptions{Platform: protocol.PlatformGB})
	if err != nil {
		t.Fatalf("ImportDAT: %v", err)
	}
	selection := reference.DefaultProfile().Apply(set)

	var progress []int
	scanner := &Scanner{
		Source:        source,
		Now:           func() time.Time { return time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC) },
		OnItemScanned: func(scanned int) { progress = append(progress, scanned) },
	}

	result, err := scanner.ScanPlatform(context.Background(), PlatformSource{
		RomMSlug:  "gb",
		Platform:  protocol.PlatformGB,
		Selection: selection,
	})
	if err != nil {
		t.Fatalf("ScanPlatform: %v", err)
	}

	if len(progress) != result.Scanned {
		t.Fatalf("OnItemScanned fired %d time(s), want once per scanned record (%d)", len(progress), result.Scanned)
	}
	for i, count := range progress {
		if count != i+1 {
			t.Fatalf("progress[%d] = %d, want %d (a strictly increasing running count)", i, count, i+1)
		}
	}
}

// TestPhase0_ScannedItemsAssembleIntoAPublishableManifest closes the loop the
// deliverable actually asks for: not just a list of items, but a manifest.
func TestPhase0_ScannedItemsAssembleIntoAPublishableManifest(t *testing.T) {
	payload := romfixture.GameBoy("MANIFEST GAME", 65536, false)
	roms := []testROM{{1, "gb", "Manifest Game (USA)", "Manifest Game (USA).gb", payload}}
	srv := testServer(t, roms)
	defer srv.Close()

	source, _ := setup(t, srv)

	set, err := reference.ImportDAT(bytes.NewReader(buildDAT(map[string][]byte{"Manifest Game (USA)": payload})),
		reference.ImportOptions{Platform: protocol.PlatformGB})
	if err != nil {
		t.Fatalf("ImportDAT: %v", err)
	}

	scanner := &Scanner{Source: source, Now: func() time.Time { return time.Now() }}
	result, err := scanner.ScanPlatform(context.Background(), PlatformSource{
		RomMSlug: "gb", Platform: protocol.PlatformGB, Selection: reference.DefaultProfile().Apply(set),
	})
	if err != nil {
		t.Fatalf("ScanPlatform: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(result.Items))
	}

	// This import cycle would be circular (bridge/publish already depends on
	// protocol, and scan depends on protocol too), so the manifest assembly
	// itself is re-derived here rather than imported, using exactly the
	// fields bridge/publish.Snapshot relies on — proving the scanned Item
	// really does satisfy what a manifest needs.
	item := result.Items[0]
	manifest := protocol.Manifest{
		SchemaVersion: protocol.SchemaVersion,
		Swarm:         protocol.NewSwarmID(),
		Alias:         protocol.MustAliasFor(protocol.NewSwarmAliasKey(), protocol.NewSwarmID(), protocol.BridgeIDFromPublicKey([]byte("k"))),
		Revision:      1,
		GeneratedAt:   time.Now(),
		Items:         []protocol.Item{item},
	}
	manifest.ComputeFingerprints()

	if err := manifest.Validate(); err != nil {
		t.Fatalf("a manifest built from a scanned item failed validation: %v", err)
	}
}

func TestScanReportsRomMUnreachableRatherThanPanicking(t *testing.T) {
	srv := testServer(t, nil)
	source, _ := setup(t, srv)
	srv.Close() // now unreachable

	scanner := &Scanner{Source: source}
	set, _ := reference.ImportDAT(bytes.NewReader(buildDAT(map[string][]byte{"X": romfixture.GameBoy("X", 32768, false)})),
		reference.ImportOptions{Platform: protocol.PlatformGB})

	_, err := scanner.ScanPlatform(context.Background(), PlatformSource{
		RomMSlug: "gb", Platform: protocol.PlatformGB, Selection: reference.DefaultProfile().Apply(set),
	})
	if err == nil {
		t.Fatal("scanning an unreachable server returned no error")
	}
}

func TestUnconfiguredPlatformSkipsRatherThanGuessing(t *testing.T) {
	payload := romfixture.GameBoy("NO CATALOGUE", 32768, false)
	srv := testServer(t, []testROM{{1, "gb", "X", "X.gb", payload}})
	defer srv.Close()
	source, _ := setup(t, srv)

	scanner := &Scanner{Source: source}
	// No Selection configured for this platform.
	result, err := scanner.ScanPlatform(context.Background(), PlatformSource{RomMSlug: "gb", Platform: protocol.PlatformGB})
	if err != nil {
		t.Fatalf("ScanPlatform: %v", err)
	}
	if len(result.Items) != 0 {
		t.Fatal("an item was produced with no reference catalogue configured")
	}
	if len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0].Reason, "no reference catalogue") {
		t.Fatalf("skip reason does not explain the missing catalogue: %+v", result.Skipped)
	}
}

func TestPaginationCoversMultiplePages(t *testing.T) {
	var roms []testROM
	for i := 0; i < 5; i++ {
		roms = append(roms, testROM{
			id: i + 1, platformSlug: "gb",
			name: fmt.Sprintf("Game %d (USA)", i), fsName: fmt.Sprintf("Game %d (USA).gb", i),
			payload: romfixture.GameBoy(fmt.Sprintf("GAME %d", i), 32768, false),
		})
	}
	srv := testServer(t, roms)
	defer srv.Close()
	source, _ := setup(t, srv)

	entries := map[string][]byte{}
	for _, r := range roms {
		entries[r.name] = r.payload
	}
	set, err := reference.ImportDAT(bytes.NewReader(buildDAT(entries)), reference.ImportOptions{Platform: protocol.PlatformGB})
	if err != nil {
		t.Fatalf("ImportDAT: %v", err)
	}

	scanner := &Scanner{Source: source, PageSize: 2} // force multiple pages over 5 records
	result, err := scanner.ScanPlatform(context.Background(), PlatformSource{
		RomMSlug: "gb", Platform: protocol.PlatformGB, Selection: reference.DefaultProfile().Apply(set),
	})
	if err != nil {
		t.Fatalf("ScanPlatform: %v", err)
	}
	if result.Scanned != 5 {
		t.Fatalf("scanned %d records across pages, want 5", result.Scanned)
	}
	if len(result.Items) != 5 {
		t.Fatalf("produced %d items, want 5", len(result.Items))
	}
}

func TestSubstitutePath(t *testing.T) {
	cases := []struct {
		template string
		values   []string
		want     string
		wantErr  bool
	}{
		{"/api/roms/{id}/download", []string{"7", "ignored.gb"}, "/api/roms/7/download", false},
		{"/api/roms/{id}/content/{file_name}", []string{"7", "a b.gb"}, "/api/roms/7/content/a%20b.gb", false},
		{"/api/roms", []string{"7"}, "", true},
	}
	for _, tc := range cases {
		got, err := substitutePath(tc.template, tc.values...)
		if tc.wantErr {
			if err == nil {
				t.Errorf("substitutePath(%q): expected an error", tc.template)
			}
			continue
		}
		if err != nil {
			t.Errorf("substitutePath(%q): %v", tc.template, err)
			continue
		}
		if got != tc.want {
			t.Errorf("substitutePath(%q) = %q, want %q", tc.template, got, tc.want)
		}
	}
}

func TestRommSourceRefusesWithoutAProbedDownloadCapability(t *testing.T) {
	srv := testServer(t, nil)
	defer srv.Close()

	// A report with roms.download deliberately absent, as if probed against a
	// server that doesn't offer it.
	_, report := setup(t, srv)
	for i := range report.Capabilities {
		if report.Capabilities[i].ID == "roms.download" {
			report.Capabilities[i].Found = false
		}
	}
	source := &RommSource{Client: romm.NewClient(srv.URL, "", romm.AuthScheme{}), Report: report}

	_, _, err := source.Download(context.Background(), ROMRecord{ID: "1", FSName: "x.gb"})
	if err == nil {
		t.Fatal("Download succeeded despite roms.download not being resolved")
	}
}
