package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/Crimson3076/RomM-Swarm/protocol"
	"github.com/Crimson3076/RomM-Swarm/reference"
)

// BootstrapReferenceCatalogues loads a Logiqx DAT per platform from
// SWARM_REFERENCE_DAT_<PLATFORM> env vars (e.g. SWARM_REFERENCE_DAT_GB),
// mirroring Bootstrap/BootstrapSwarm's own env-var convenience idiom
// exactly: a one-time, non-fatal load at startup, no UI (ADR 0019). A
// platform with no env var set, or one pointing at a path that can't be
// read or parsed, is simply left with no Selection — scanHoldings then
// reports every holding on it as skipped rather than silently guessing,
// and PublishInventory surfaces that plainly.
//
// Unlike Bootstrap/BootstrapSwarm, this has no "already configured, leave
// it alone" guard: referenceSelections is in-memory only, so there is
// nothing persisted to check against, and reloading the same env vars on
// every restart is exactly the desired behavior.
func (d *Daemon) BootstrapReferenceCatalogues() {
	for _, platform := range protocol.InitialPlatforms() {
		envVar := "SWARM_REFERENCE_DAT_" + strings.ToUpper(string(platform))
		path := strings.TrimSpace(os.Getenv(envVar))
		if path == "" {
			continue
		}

		selection, err := loadReferenceSelection(path, platform)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bridge: %s: %v\n", envVar, err)
			continue
		}
		d.referenceSelections[platform] = selection
	}
}

func loadReferenceSelection(path string, platform protocol.PlatformID) (*reference.Selection, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	set, err := reference.ImportDAT(f, reference.ImportOptions{Platform: platform})
	if err != nil {
		return nil, fmt.Errorf("parsing %s as a reference catalogue: %w", path, err)
	}
	return reference.DefaultProfile().Apply(set), nil
}
