package main

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Crimson3076/RomM-Swarm/bridge/bridgeconfig"
	"github.com/Crimson3076/RomM-Swarm/internal/romfixture"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// fakeCatalogueHost is a minimal stand-in for host/hostapi's
// POST /api/bridges/{bridgeID}/reference-catalogues route.
type fakeCatalogueHost struct {
	catalogues map[protocol.PlatformID][]byte // platform -> raw DAT content
}

func (h *fakeCatalogueHost) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/bridges/{bridgeID}/reference-catalogues", func(w http.ResponseWriter, r *http.Request) {
		type wireCatalogue struct {
			Platform      string `json:"platform"`
			Filename      string `json:"filename"`
			ContentBase64 string `json:"content_base64"`
			ContentSHA256 string `json:"content_sha256"`
			EntryCount    int    `json:"entry_count"`
		}
		var out []wireCatalogue
		for platform, content := range h.catalogues {
			out = append(out, wireCatalogue{
				Platform:      string(platform),
				Filename:      string(platform) + ".dat",
				ContentBase64: base64.StdEncoding.EncodeToString(content),
			})
		}
		writeJSON(w, map[string]any{"catalogues": out})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestPhase2_RefreshReferenceCataloguesLoadsWhatTheHostServes proves the
// fetched catalogue actually classifies real, matching content — exercised
// through scanHoldings exactly as production would use it, the same
// end-to-end proof the old env-var-bootstrap test used to make.
func TestPhase2_RefreshReferenceCataloguesLoadsWhatTheHostServes(t *testing.T) {
	payload := romfixture.GameBoy("HOST CATALOGUE GAME", 65536, false)
	dat := buildInventoryTestDAT(map[string][]byte{"Host Catalogue Game": payload})

	host := &fakeCatalogueHost{catalogues: map[protocol.PlatformID][]byte{protocol.PlatformGB: dat}}
	hostSrv := host.start(t)

	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	d.refreshReferenceCatalogues(context.Background(), hostSrv.URL, protocol.BridgeID("brg_test"), "refresh-token", protocol.SwarmID("swm_test"))

	if d.referenceSelections[protocol.PlatformGB] == nil {
		t.Fatal("no Selection was loaded for gb")
	}

	fb := newFakeBridgeServer()
	seedROM(fb, "gb", "Host Catalogue Game.gb", payload)
	rommSrv := fb.start(t)
	if err := d.ConfigStore().Save(bridgeconfig.Config{RommURL: rommSrv.URL, RommToken: "t"}); err != nil {
		t.Fatalf("saving the RomM config: %v", err)
	}
	if err := d.Reconnect(context.Background()); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}

	items, skipped, err := d.scanHoldings(context.Background())
	if err != nil {
		t.Fatalf("scanHoldings: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("scanHoldings found %d items, want 1; skipped: %+v", len(items), skipped)
	}
	if items[0].Classification != protocol.ClassVerifiedEligible {
		t.Errorf("classification = %s, want verified_eligible", items[0].Classification)
	}
}

// TestPhase2_RefreshReferenceCataloguesReplacesWhollyNotMerges proves the
// Host's set of catalogues fully replaces whatever the Bridge had before —
// a platform the Host no longer serves loses its Selection too, since the
// Host is the sole authority (ADR 0023), not a mix-in on top of local
// state.
func TestPhase2_RefreshReferenceCataloguesReplacesWhollyNotMerges(t *testing.T) {
	payload := romfixture.GameBoy("REPLACED GAME", 65536, false)
	dat := buildInventoryTestDAT(map[string][]byte{"Replaced Game": payload})

	host := &fakeCatalogueHost{catalogues: map[protocol.PlatformID][]byte{protocol.PlatformGB: dat}}
	hostSrv := host.start(t)

	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	d.refreshReferenceCatalogues(context.Background(), hostSrv.URL, protocol.BridgeID("brg_test"), "refresh-token", protocol.SwarmID("swm_test"))
	if d.referenceSelections[protocol.PlatformGB] == nil {
		t.Fatal("gb's Selection was not loaded on the first fetch")
	}

	// The Host stops serving any catalogue at all.
	host.catalogues = map[protocol.PlatformID][]byte{}
	d.refreshReferenceCatalogues(context.Background(), hostSrv.URL, protocol.BridgeID("brg_test"), "refresh-token", protocol.SwarmID("swm_test"))
	if d.referenceSelections[protocol.PlatformGB] != nil {
		t.Error("gb's Selection survived after the Host stopped serving any catalogue — it must be replaced wholly, not merged")
	}
}

// TestPhase2_RefreshReferenceCataloguesKeepsThePreviousSetOnAFetchFailure
// proves a transient failure to reach the Host (network blip, Host
// restart) doesn't wipe out an already-loaded, still-good set of
// catalogues — it just leaves things as they were, to self-correct on the
// next publish attempt.
func TestPhase2_RefreshReferenceCataloguesKeepsThePreviousSetOnAFetchFailure(t *testing.T) {
	payload := romfixture.GameBoy("STICKY GAME", 65536, false)
	dat := buildInventoryTestDAT(map[string][]byte{"Sticky Game": payload})

	host := &fakeCatalogueHost{catalogues: map[protocol.PlatformID][]byte{protocol.PlatformGB: dat}}
	hostSrv := host.start(t)

	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	d.refreshReferenceCatalogues(context.Background(), hostSrv.URL, protocol.BridgeID("brg_test"), "refresh-token", protocol.SwarmID("swm_test"))
	if d.referenceSelections[protocol.PlatformGB] == nil {
		t.Fatal("gb's Selection was not loaded on the first fetch")
	}

	// A second fetch against an unreachable Host must not clear what's
	// already loaded.
	d.refreshReferenceCatalogues(context.Background(), "http://127.0.0.1:1", protocol.BridgeID("brg_test"), "refresh-token", protocol.SwarmID("swm_test"))
	if d.referenceSelections[protocol.PlatformGB] == nil {
		t.Error("gb's Selection was cleared after a failed fetch — a transient failure must not wipe out the last-known-good set")
	}
}
