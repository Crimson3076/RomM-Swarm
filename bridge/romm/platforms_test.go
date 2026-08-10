package romm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// platformsServer serves a minimal spec plus a platforms.list response shaped
// exactly like the fields a live RomM 5.0.0 instance was confirmed to return:
// id, slug, fs_slug, and name, alongside a realistic amount of unrelated
// clutter (RomM's real objects carry several dozen fields; FetchPlatforms must
// ignore what it doesn't need).
func platformsServer(t *testing.T, platforms []map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{
			"openapi": "3.1.0",
			"info":    map[string]any{"title": "RomM API", "version": "test"},
			"paths": map[string]any{
				"/api/platforms": map[string]any{"get": map[string]any{"operationId": "platforms"}},
			},
		})
	})
	mux.HandleFunc("/api/platforms", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, platforms)
	})

	return httptest.NewServer(mux)
}

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func probeAndFetch(t *testing.T, srv *httptest.Server) *Platforms {
	t.Helper()
	report, err := Probe(context.Background(), ProbeOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	client := NewClient(srv.URL, "", AuthScheme{})
	platforms, err := FetchPlatforms(context.Background(), client, report)
	if err != nil {
		t.Fatalf("FetchPlatforms: %v", err)
	}
	return platforms
}

// TestPhase0_PlatformLookupMatchesConfirmedRomMShape reproduces the exact
// object shape observed from a live server: id 11, slug "gb", name
// "Game Boy", among many unrelated fields.
func TestPhase0_PlatformLookupMatchesConfirmedRomMShape(t *testing.T) {
	srv := platformsServer(t, []map[string]any{
		{
			"id": 11, "slug": "gb", "fs_slug": "gb", "name": "Game Boy",
			"igdb_id": 33, "rom_count": 5372, "category": "Portable Console",
			"family_name": "Nintendo", "firmware": []any{},
		},
		{
			"id": 13, "slug": "gbc", "fs_slug": "gbc", "name": "Game Boy Color",
			"igdb_id": 22, "rom_count": 1200,
		},
	})
	defer srv.Close()

	platforms := probeAndFetch(t, srv)

	gb, err := platforms.Lookup("gb")
	if err != nil {
		t.Fatalf("Lookup(gb): %v", err)
	}
	if gb.ID != 11 || gb.Name != "Game Boy" {
		t.Fatalf("got %+v, want id 11 Game Boy", gb)
	}

	gbc, err := platforms.Lookup("gbc")
	if err != nil {
		t.Fatalf("Lookup(gbc): %v", err)
	}
	if gbc.ID != 13 {
		t.Fatalf("got id %d, want 13", gbc.ID)
	}
}

// A real probe found duplicate slugs (two "n64" entries, three "snes"
// entries) from what looks like re-imports under different metadata provider
// ids. The first one found must win, deterministically, rather than the
// lookup silently returning whichever happened to be iterated last.
func TestPhase0_DuplicateSlugsResolveToTheFirstEntry(t *testing.T) {
	srv := platformsServer(t, []map[string]any{
		{"id": 3, "slug": "n64", "name": "Nintendo 64", "rom_count": 218},
		{"id": 30, "slug": "n64", "name": "Nintendo 64", "rom_count": 0},
	})
	defer srv.Close()

	platforms := probeAndFetch(t, srv)
	got, err := platforms.Lookup("n64")
	if err != nil {
		t.Fatalf("Lookup(n64): %v", err)
	}
	if got.ID != 3 {
		t.Fatalf("got id %d, want 3 (the first entry)", got.ID)
	}
	if len(platforms.All()) != 2 {
		t.Fatalf("All() returned %d entries, want both duplicates preserved", len(platforms.All()))
	}
}

// A platform row with no usable id or slug (RomM does carry some
// legacy/placeholder rows) must not fail the whole fetch.
func TestPhase0_MalformedPlatformRowsAreSkipped(t *testing.T) {
	srv := platformsServer(t, []map[string]any{
		{"id": 11, "slug": "gb", "name": "Game Boy"},
		{"slug": "", "name": "Missing everything"},
		{"id": 0, "slug": "zero-id", "name": "Zero id"},
	})
	defer srv.Close()

	platforms := probeAndFetch(t, srv)
	if _, err := platforms.Lookup("gb"); err != nil {
		t.Fatalf("a well-formed entry was not resolved: %v", err)
	}
	if _, err := platforms.Lookup("zero-id"); err == nil {
		t.Fatal("a platform with id 0 was accepted as a lookup target")
	}
	if len(platforms.All()) != 1 {
		t.Fatalf("All() returned %d entries, want only the one well-formed row", len(platforms.All()))
	}
}

// An unresolvable slug must fail with a message naming what was asked for and
// what is actually available, not a bare "not found".
func TestLookupOfUnknownSlugListsWhatIsKnown(t *testing.T) {
	srv := platformsServer(t, []map[string]any{
		{"id": 11, "slug": "gb", "name": "Game Boy"},
	})
	defer srv.Close()

	platforms := probeAndFetch(t, srv)
	_, err := platforms.Lookup("dreamcast")
	if err == nil {
		t.Fatal("an unknown slug was accepted")
	}
	if !contains(err.Error(), "dreamcast") || !contains(err.Error(), "gb") {
		t.Errorf("error does not name both the requested and known slugs: %v", err)
	}
}

// FetchPlatforms must refuse to run against a server whose probe never
// resolved platforms.list, rather than guessing a path.
func TestFetchPlatformsRefusesWithoutAResolvedCapability(t *testing.T) {
	srv := platformsServer(t, nil)
	defer srv.Close()

	report, err := Probe(context.Background(), ProbeOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	for i := range report.Capabilities {
		if report.Capabilities[i].ID == "platforms.list" {
			report.Capabilities[i].Found = false
		}
	}

	client := NewClient(srv.URL, "", AuthScheme{})
	if _, err := FetchPlatforms(context.Background(), client, report); err == nil {
		t.Fatal("FetchPlatforms succeeded despite platforms.list not being resolved")
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
