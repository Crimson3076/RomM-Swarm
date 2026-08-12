package hostapi

import (
	"errors"
	"net/http"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/host/directory"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

type publishInventoryRequest struct {
	RefreshToken string            `json:"refresh_token"`
	Manifest     protocol.Manifest `json:"manifest"`

	// DisplayName is the Bridge's own self-declared name (ADR 0022),
	// optional — see Directory.PublishInventory's own doc comment for the
	// precedence rule against a Host-set name.
	DisplayName string `json:"display_name,omitempty"`
}

// handlePublishInventory is unauthenticated at the route level, the same
// bucket as enroll/rotate: the refresh token in the body is the
// credential, verified via the read-only Directory.PublishInventory ->
// auth.Verifier.Authenticate path rather than a rotation (ADR 0019).
func (s *Server) handlePublishInventory(w http.ResponseWriter, r *http.Request) {
	bridgeID := protocol.BridgeID(r.PathValue("bridgeID"))
	if err := bridgeID.Validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid bridge id")
		return
	}

	var req publishInventoryRequest
	if err := decodeJSON(w, r, &req, s.maxInventoryBytes()); err != nil {
		writeDecodeError(w, err)
		return
	}

	snap, err := s.Directory.PublishInventory(r.Context(), bridgeID, auth.Token(req.RefreshToken), req.Manifest, req.DisplayName)
	if err != nil {
		writeInventoryError(w, err)
		return
	}

	// The Swarm-wide distinct-file count rides along so the Bridge's
	// "Publish Inventory" button can show something useful about the whole
	// Swarm, not just this one Bridge, in a single round trip.
	totals, _, err := s.Directory.SwarmInventorySummary(r.Context(), snap.SwarmID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not summarise the swarm's inventory")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"item_count":     snap.ItemCount,
		"revision":       uint64(snap.Revision),
		"published_at":   snap.PublishedAt,
		"distinct_files": totals.DistinctFiles,
	})
}

// writeInventoryError maps PublishInventory's distinguished errors to HTTP
// status codes. Auth-credential errors delegate to writeCredentialError
// (bridges.go) rather than duplicating its switch.
func writeInventoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, directory.ErrBridgeNotEnrolledInSwarm):
		writeJSONError(w, http.StatusForbidden, "bridge is not actively enrolled in this swarm")
	case errors.Is(err, directory.ErrInventoryAliasMismatch):
		writeJSONError(w, http.StatusUnprocessableEntity, "manifest alias does not match this bridge's swarm-scoped alias")
	case errors.Is(err, auth.ErrUnknownToken), errors.Is(err, auth.ErrWrongBridge),
		errors.Is(err, auth.ErrFamilyRevoked), errors.Is(err, auth.ErrReuseDetected):
		writeCredentialError(w, err)
	default:
		// Covers protocol.Manifest.Validate's own errors (a wrong schema
		// version, an unpublishable item, a duplicate file id, ...) — these
		// are specific and worth surfacing, not a generic "bad request".
		writeJSONError(w, http.StatusUnprocessableEntity, "could not publish inventory: "+err.Error())
	}
}
