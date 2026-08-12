package hostapi

import (
	"encoding/base64"
	"net/http"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

type getReferenceCataloguesRequest struct {
	RefreshToken string `json:"refresh_token"`
	SwarmID      string `json:"swarm_id"`
}

type referenceCatalogueResponse struct {
	Platform      string `json:"platform"`
	Filename      string `json:"filename"`
	ContentBase64 string `json:"content_base64"`
	ContentSHA256 string `json:"content_sha256"`
	EntryCount    int    `json:"entry_count"`
}

// handleGetReferenceCatalogues is unauthenticated at the route level, the
// same bucket as enroll/rotate/inventory/display-name: the refresh token
// in the body is the credential (ADR 0023). Every catalogue currently
// stored for the named Swarm comes back in one response — a Bridge's
// PublishInventory calls this before every scan, replacing whatever
// selections it had wholesale, so there's no partial-update case to
// design for here.
func (s *Server) handleGetReferenceCatalogues(w http.ResponseWriter, r *http.Request) {
	bridgeID := protocol.BridgeID(r.PathValue("bridgeID"))
	if err := bridgeID.Validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid bridge id")
		return
	}

	var req getReferenceCataloguesRequest
	if err := decodeJSON(w, r, &req, defaultMaxBodyBytes); err != nil {
		writeDecodeError(w, err)
		return
	}

	catalogues, err := s.Directory.GetReferenceCatalogues(r.Context(), bridgeID, auth.Token(req.RefreshToken), protocol.SwarmID(req.SwarmID))
	if err != nil {
		writeInventoryError(w, err)
		return
	}

	out := make([]referenceCatalogueResponse, 0, len(catalogues))
	for _, c := range catalogues {
		out = append(out, referenceCatalogueResponse{
			Platform:      string(c.Platform),
			Filename:      c.Filename,
			ContentBase64: base64.StdEncoding.EncodeToString(c.Content),
			ContentSHA256: c.ContentSHA256,
			EntryCount:    c.EntryCount,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"catalogues": out})
}
