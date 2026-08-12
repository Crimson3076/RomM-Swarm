package main

import (
	"bytes"
	"context"
	"log"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/bridge/hostclient"
	"github.com/Crimson3076/RomM-Swarm/protocol"
	"github.com/Crimson3076/RomM-Swarm/reference"
)

// refreshReferenceCatalogues fetches every reference catalogue currently
// stored on the Host for swarm and replaces d.referenceSelections with
// them wholesale (ADR 0023) — called once at the start of every publish
// attempt, so an update the Swarm owner makes on the Host reaches this
// Bridge on its own, without requiring it to leave and rejoin.
//
// The Host is deliberately the sole authority here: earlier drafts of
// this feature let a Bridge keep using its own locally-loaded catalogue
// for a platform the Host hadn't covered yet, but that was dropped —
// once a Bridge is joined to a Swarm, a self-supplied catalogue would let
// its operator fabricate "verified" status for anything, defeating the
// whole reason a reference catalogue exists. See ADR 0023.
//
// A failure to reach the Host (network blip, Host restart) is logged and
// otherwise ignored — d.referenceSelections is left exactly as it was,
// so a transient failure doesn't silently stop everything from verifying
// for one publish cycle. It self-corrects on the next attempt.
func (d *Daemon) refreshReferenceCatalogues(ctx context.Context, hostURL string, bridge protocol.BridgeID, refresh auth.Token, swarm protocol.SwarmID) {
	catalogues, err := hostclient.New(hostURL).FetchReferenceCatalogues(ctx, bridge, refresh, swarm)
	if err != nil {
		log.Printf("bridge: fetching reference catalogues from the Host: %v", err)
		return
	}

	selections := make(map[protocol.PlatformID]*reference.Selection, len(catalogues))
	for _, c := range catalogues {
		set, err := reference.ImportDAT(bytes.NewReader(c.Content), reference.ImportOptions{Platform: c.Platform})
		if err != nil {
			// The Host already validates a catalogue with the same parser
			// before accepting an upload (Directory.UploadReferenceCatalogue),
			// so this should be unreachable in practice — surfaced rather
			// than silently dropped in case that ever changes.
			log.Printf("bridge: parsing the %s reference catalogue from the Host: %v", c.Platform, err)
			continue
		}
		selections[c.Platform] = reference.DefaultProfile().Apply(set)
	}
	d.referenceSelections = selections
}
