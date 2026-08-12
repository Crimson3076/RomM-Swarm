package hostui

import (
	"net/http"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

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
