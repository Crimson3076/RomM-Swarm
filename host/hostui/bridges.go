package hostui

import (
	"net/http"
	"strings"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// handleSetBridgeName sets the Host-assigned, per-Swarm label an owner sees
// for this Bridge — see the schema comment on bridge_swarm_memberships for
// why this is Host-authoritative rather than something the Bridge itself
// declares.
func (s *Server) handleSetBridgeName(w http.ResponseWriter, r *http.Request) {
	swarmID := protocol.SwarmID(r.PathValue("swarmID"))
	bridgeID := protocol.BridgeID(r.PathValue("bridgeID"))

	if err := r.ParseForm(); err != nil {
		s.swarmViewError(w, r, swarmID, "could not read the submitted form")
		return
	}
	name := strings.TrimSpace(r.FormValue("display_name"))

	if err := s.Directory.SetBridgeDisplayName(r.Context(), swarmID, bridgeID, name); err != nil {
		s.swarmViewError(w, r, swarmID, "could not rename Bridge: "+err.Error())
		return
	}
	http.Redirect(w, r, "/swarms/"+string(swarmID), http.StatusSeeOther)
}

// handleRevokeBridge ends a Bridge's credential family globally, across
// every Swarm it belongs to — not just the one this page is nested under.
// See directory.RevokeBridge's own doc comment; the confirmation copy in
// swarm.html says so explicitly.
func (s *Server) handleRevokeBridge(w http.ResponseWriter, r *http.Request) {
	swarmID := protocol.SwarmID(r.PathValue("swarmID"))
	bridgeID := protocol.BridgeID(r.PathValue("bridgeID"))

	if err := s.Directory.RevokeBridge(r.Context(), bridgeID, "revoked by owner"); err != nil {
		s.swarmViewError(w, r, swarmID, "could not revoke Bridge: "+err.Error())
		return
	}
	http.Redirect(w, r, "/swarms/"+string(swarmID), http.StatusSeeOther)
}

// handleRemoveBridge removes a Bridge's membership from this Swarm — the
// UI only offers this once the Bridge's credential is already revoked
// (see swarm.html), but the handler itself doesn't re-check that: an
// owner clearing out a Bridge they never want back is a legitimate call
// to make regardless. See directory.RemoveBridgeFromSwarm's own doc
// comment for exactly what is and isn't removed.
func (s *Server) handleRemoveBridge(w http.ResponseWriter, r *http.Request) {
	swarmID := protocol.SwarmID(r.PathValue("swarmID"))
	bridgeID := protocol.BridgeID(r.PathValue("bridgeID"))

	if err := s.Directory.RemoveBridgeFromSwarm(r.Context(), swarmID, bridgeID); err != nil {
		s.swarmViewError(w, r, swarmID, "could not remove Bridge: "+err.Error())
		return
	}
	http.Redirect(w, r, "/swarms/"+string(swarmID), http.StatusSeeOther)
}

// handleReenrollBridge is the owner-authorised escape from a revoked
// credential family. The new refresh token is a secret and is shown
// exactly once, the same way an invitation code is.
func (s *Server) handleReenrollBridge(w http.ResponseWriter, r *http.Request) {
	swarmID := protocol.SwarmID(r.PathValue("swarmID"))
	bridgeID := protocol.BridgeID(r.PathValue("bridgeID"))

	sw, err := s.Directory.GetSwarm(r.Context(), userFromContext(r.Context()), swarmID)
	if err != nil {
		s.swarmViewError(w, r, swarmID, "could not load Swarm: "+err.Error())
		return
	}

	token, err := s.Directory.ReenrollBridge(r.Context(), bridgeID)
	if err != nil {
		s.swarmViewError(w, r, swarmID, "could not re-enroll Bridge: "+err.Error())
		return
	}

	renderPage(w, "secret_display", secretDisplayData{
		baseData:  s.base(r, "Bridge re-enrolled"),
		SwarmID:   string(sw.ID),
		SwarmName: sw.Name,
		Heading:   "Bridge re-enrolled",
		Secret:    string(token),
		Notice:    "This refresh token will not be shown again. Configure the Bridge with it to restore its connection.",
	})
}
