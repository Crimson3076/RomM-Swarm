package hostui

import (
	"errors"
	"net/http"
	"time"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

func (s *Server) handleCreateSwarm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.dashboardError(w, r, "could not read the submitted form")
		return
	}
	name := r.FormValue("name")
	if name == "" {
		s.dashboardError(w, r, "Swarm name is required")
		return
	}

	id, err := s.Directory.CreateSwarm(r.Context(), userFromContext(r.Context()), name)
	if err != nil {
		s.dashboardError(w, r, "could not create Swarm: "+err.Error())
		return
	}
	http.Redirect(w, r, "/swarms/"+string(id), http.StatusSeeOther)
}

func (s *Server) dashboardError(w http.ResponseWriter, r *http.Request, msg string) {
	data := dashboardData{baseData: s.base(r, "Dashboard")}
	data.Error = msg
	if swarms, err := s.Directory.ListSwarms(r.Context(), userFromContext(r.Context())); err == nil {
		for _, sw := range swarms {
			data.Swarms = append(data.Swarms, swarmSummary{ID: string(sw.ID), Name: sw.Name})
		}
	}
	w.WriteHeader(http.StatusUnprocessableEntity)
	renderPage(w, "dashboard", data)
}

type swarmViewData struct {
	baseData
	Swarm       swarmSummary
	Invitations []invitationSummary
	Bridges     []bridgeSummary
}

type invitationSummary struct {
	ID        string
	MaxUses   int
	UseCount  int
	ExpiresAt string
	Revoked   bool
}

type bridgeSummary struct {
	ID                string
	DisplayName       string
	JoinedAt          string
	State             string
	CredentialRevoked bool
}

func (s *Server) handleSwarmView(w http.ResponseWriter, r *http.Request) {
	data, ok := s.loadSwarmView(w, r)
	if !ok {
		return
	}
	renderPage(w, "swarm", data)
}

// loadSwarmView loads the swarm named by the request's {swarmID} path
// value, scoped to the requesting owner, plus its invitations and Bridges.
// On failure it writes the response itself (404 or 500) and returns
// ok=false — callers must stop immediately in that case.
func (s *Server) loadSwarmView(w http.ResponseWriter, r *http.Request) (swarmViewData, bool) {
	swarmID := protocol.SwarmID(r.PathValue("swarmID"))
	sw, err := s.Directory.GetSwarm(r.Context(), userFromContext(r.Context()), swarmID)
	if errors.Is(err, directory.ErrSwarmNotFound) {
		http.NotFound(w, r)
		return swarmViewData{}, false
	}
	if err != nil {
		http.Error(w, "internal error loading Swarm", http.StatusInternalServerError)
		return swarmViewData{}, false
	}

	data := swarmViewData{
		baseData: s.base(r, sw.Name),
		Swarm:    swarmSummary{ID: string(sw.ID), Name: sw.Name},
	}

	invitations, err := s.Directory.ListInvitations(r.Context(), swarmID)
	if err != nil {
		data.Error = "listing invitations: " + err.Error()
	}
	for _, inv := range invitations {
		data.Invitations = append(data.Invitations, invitationSummary{
			ID:        string(inv.ID),
			MaxUses:   inv.MaxUses,
			UseCount:  inv.UseCount,
			ExpiresAt: inv.ExpiresAt.Format(time.RFC3339),
			Revoked:   !inv.RevokedAt.IsZero(),
		})
	}

	bridges, err := s.Directory.ListBridgesForSwarm(r.Context(), swarmID)
	if err != nil {
		data.Error = "listing Bridges: " + err.Error()
	}
	for _, b := range bridges {
		data.Bridges = append(data.Bridges, bridgeSummary{
			ID:                string(b.BridgeID),
			DisplayName:       b.DisplayName,
			JoinedAt:          b.JoinedAt.Format(time.RFC3339),
			State:             b.State,
			CredentialRevoked: b.CredentialRevoked,
		})
	}
	return data, true
}

func (s *Server) handleIssueInvitation(w http.ResponseWriter, r *http.Request) {
	swarmID := protocol.SwarmID(r.PathValue("swarmID"))
	sw, err := s.Directory.GetSwarm(r.Context(), userFromContext(r.Context()), swarmID)
	if errors.Is(err, directory.ErrSwarmNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error loading Swarm", http.StatusInternalServerError)
		return
	}

	code, _, err := s.Directory.IssueInvitation(r.Context(), swarmID, userFromContext(r.Context()), 0, 0)
	if err != nil {
		s.swarmViewError(w, r, swarmID, "could not issue invitation: "+err.Error())
		return
	}

	renderPage(w, "secret_display", secretDisplayData{
		baseData:  s.base(r, "Invitation issued"),
		SwarmID:   string(sw.ID),
		SwarmName: sw.Name,
		Heading:   "Invitation issued",
		Secret:    string(code),
		Notice:    "This code will not be shown again. Give it to whoever is enrolling the new Bridge.",
	})
}

// swarmViewError re-renders the Swarm view page with an inline error,
// mirroring how bridge/adminui surfaces form failures — no separate flash
// mechanism across a redirect exists here.
func (s *Server) swarmViewError(w http.ResponseWriter, r *http.Request, swarmID protocol.SwarmID, msg string) {
	data, ok := s.loadSwarmView(w, r)
	if !ok {
		return
	}
	data.Error = msg
	w.WriteHeader(http.StatusUnprocessableEntity)
	renderPage(w, "swarm", data)
}

// secretDisplayData renders a one-time secret — an invitation code or a
// re-enrollment refresh token — exactly once, never held server-side
// beyond this single response. Shared by handleIssueInvitation and
// handleReenrollBridge.
type secretDisplayData struct {
	baseData
	SwarmID   string
	SwarmName string
	Heading   string
	Secret    string
	Notice    string
}
