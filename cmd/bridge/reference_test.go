package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Crimson3076/RomM-Swarm/bridge/bridgeconfig"
	"github.com/Crimson3076/RomM-Swarm/internal/romfixture"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// TestPhase2_BootstrapReferenceCataloguesLoadsARealFixtureDAT proves the
// env-var bootstrap does more than parse a file — the resulting Selection
// actually classifies real, matching content, exercised through
// scanHoldings exactly as production would use it.
func TestPhase2_BootstrapReferenceCataloguesLoadsARealFixtureDAT(t *testing.T) {
	payload := romfixture.GameBoy("BOOTSTRAP TEST GAME", 65536, false)
	datPath := filepath.Join(t.TempDir(), "gb.dat")
	dat := buildInventoryTestDAT(map[string][]byte{"Bootstrap Test Game": payload})
	if err := os.WriteFile(datPath, dat, 0o644); err != nil {
		t.Fatalf("writing the fixture DAT: %v", err)
	}
	t.Setenv("SWARM_REFERENCE_DAT_GB", datPath)

	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	d.BootstrapReferenceCatalogues()

	if d.referenceSelections[protocol.PlatformGB] == nil {
		t.Fatal("no Selection was loaded for gb")
	}

	fb := newFakeBridgeServer()
	seedROM(fb, "gb", "Bootstrap Test Game.gb", payload)
	srv := fb.start(t)
	if err := d.ConfigStore().Save(bridgeconfig.Config{RommURL: srv.URL, RommToken: "t"}); err != nil {
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

// TestPhase2_BootstrapReferenceCataloguesIsNonFatalOnABadPath proves a
// missing or unreadable DAT path never blocks the daemon from starting —
// it just leaves that platform with no Selection, same as never setting
// the env var at all.
func TestPhase2_BootstrapReferenceCataloguesIsNonFatalOnABadPath(t *testing.T) {
	t.Setenv("SWARM_REFERENCE_DAT_GB", filepath.Join(t.TempDir(), "does-not-exist.dat"))

	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}

	d.BootstrapReferenceCatalogues() // must not panic or return an error

	if d.referenceSelections[protocol.PlatformGB] != nil {
		t.Error("a Selection was set from a path that does not exist")
	}
}

// TestPhase2_BootstrapReferenceCataloguesLoadsIndependentlyPerPlatform
// proves one platform's bad path doesn't block another's good one — a
// real production scenario, since an operator is likely to have DATs for
// some platforms and not others.
func TestPhase2_BootstrapReferenceCataloguesLoadsIndependentlyPerPlatform(t *testing.T) {
	payload := romfixture.GameBoy("INDEPENDENT LOAD", 65536, false)
	goodPath := filepath.Join(t.TempDir(), "gb.dat")
	if err := os.WriteFile(goodPath, buildInventoryTestDAT(map[string][]byte{"Independent Load": payload}), 0o644); err != nil {
		t.Fatalf("writing the fixture DAT: %v", err)
	}
	t.Setenv("SWARM_REFERENCE_DAT_GB", goodPath)
	t.Setenv("SWARM_REFERENCE_DAT_GBA", filepath.Join(t.TempDir(), "missing.dat"))

	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	d.BootstrapReferenceCatalogues()

	if d.referenceSelections[protocol.PlatformGB] == nil {
		t.Error("gb's Selection was not loaded despite a valid DAT")
	}
	if d.referenceSelections[protocol.PlatformGBA] != nil {
		t.Error("gba's Selection was set despite a missing DAT path")
	}
}
