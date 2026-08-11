package hostapi

import (
	"net/http"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

type createSwarmRequest struct {
	Name string `json:"name"`
}

func (s *Server) handleCreateSwarm(w http.ResponseWriter, r *http.Request) {
	var req createSwarmRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "could not read the request body")
		return
	}
	if req.Name == "" {
		writeJSONError(w, http.StatusUnprocessableEntity, "name is required")
		return
	}

	id, err := s.Directory.CreateSwarm(r.Context(), ownerFromContext(r.Context()), req.Name)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not create the Swarm")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"swarm_id": string(id)})
}

func (s *Server) handleListSwarms(w http.ResponseWriter, r *http.Request) {
	swarms, err := s.Directory.ListSwarms(r.Context(), ownerFromContext(r.Context()))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not list Swarms")
		return
	}
	writeJSON(w, http.StatusOK, swarms)
}

type issueInvitationRequest struct {
	MaxUses int `json:"max_uses"`
	TTLSecs int `json:"ttl_seconds"`
}

func (s *Server) handleIssueInvitation(w http.ResponseWriter, r *http.Request) {
	swarmID := protocol.SwarmID(r.PathValue("swarmID"))
	if err := swarmID.Validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid swarm id")
		return
	}

	var req issueInvitationRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "could not read the request body")
			return
		}
	}

	code, id, err := s.Directory.IssueInvitation(r.Context(), swarmID, ownerFromContext(r.Context()),
		req.MaxUses, secondsToDuration(req.TTLSecs))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not issue the invitation")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"invitation_id": string(id),
		"code":          string(code),
	})
}
