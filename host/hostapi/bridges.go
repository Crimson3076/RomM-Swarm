package hostapi

import (
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/host/directory"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

type enrollBridgeRequest struct {
	Code      string `json:"code"`
	PublicKey string `json:"public_key"` // base64-encoded
}

func (s *Server) handleEnrollBridge(w http.ResponseWriter, r *http.Request) {
	var req enrollBridgeRequest
	if err := decodeJSON(w, r, &req, defaultMaxBodyBytes); err != nil {
		writeDecodeError(w, err)
		return
	}
	key, err := base64.StdEncoding.DecodeString(req.PublicKey)
	if err != nil || len(key) == 0 {
		writeJSONError(w, http.StatusBadRequest, "public_key must be base64-encoded and non-empty")
		return
	}

	bridgeID, swarmID, alias, token, err := s.Directory.RedeemInvitation(r.Context(), directory.InvitationCode(req.Code), key)
	if errors.Is(err, directory.ErrInvitationInvalid) {
		writeJSONError(w, http.StatusUnprocessableEntity, "invitation is invalid, expired, or already used")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not enroll the Bridge")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"bridge_id":     string(bridgeID),
		"swarm_id":      string(swarmID),
		"bridge_alias":  string(alias),
		"refresh_token": string(token),
	})
}

type rotateBridgeRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func (s *Server) handleRotateBridge(w http.ResponseWriter, r *http.Request) {
	bridgeID := protocol.BridgeID(r.PathValue("bridgeID"))
	if err := bridgeID.Validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid bridge id")
		return
	}
	var req rotateBridgeRequest
	if err := decodeJSON(w, r, &req, defaultMaxBodyBytes); err != nil {
		writeDecodeError(w, err)
		return
	}

	result, err := s.Directory.RotateBridgeCredential(r.Context(), bridgeID, auth.Token(req.RefreshToken))
	if err != nil {
		writeCredentialError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"refresh_token":     string(result.Refresh),
		"access_expires_at": result.AccessExpiresAt,
		"outcome":           string(result.Outcome),
	})
}

func (s *Server) handleRevokeBridge(w http.ResponseWriter, r *http.Request) {
	bridgeID := protocol.BridgeID(r.PathValue("bridgeID"))
	if err := bridgeID.Validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid bridge id")
		return
	}
	if err := s.Directory.RevokeBridge(r.Context(), bridgeID, "revoked by owner"); err != nil {
		writeCredentialError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReenrollBridge(w http.ResponseWriter, r *http.Request) {
	bridgeID := protocol.BridgeID(r.PathValue("bridgeID"))
	if err := bridgeID.Validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid bridge id")
		return
	}
	token, err := s.Directory.ReenrollBridge(r.Context(), bridgeID)
	if err != nil {
		writeCredentialError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"refresh_token": string(token)})
}

// writeCredentialError maps auth's distinguished errors to HTTP status
// codes. auth.ErrGraceExpired is deliberately not handled here: it is
// declared in auth/refresh.go but never returned by any code path — the
// "previous token presented late" case routes through the same internal
// revoke() helper as "presented twice" and always surfaces as
// ErrReuseDetected instead. See ADR 0016, resolved sub-decision 1.
func writeCredentialError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrUnknownToken), errors.Is(err, auth.ErrWrongBridge):
		writeJSONError(w, http.StatusUnauthorized, "credential not recognised")
	case errors.Is(err, auth.ErrFamilyRevoked), errors.Is(err, auth.ErrReuseDetected):
		writeJSONError(w, http.StatusForbidden, "credential family has been revoked")
	default:
		writeJSONError(w, http.StatusInternalServerError, "could not process the request")
	}
}
