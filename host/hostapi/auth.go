package hostapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

type contextKey int

const ownerUserContextKey contextKey = 0

// requireOwnerAuth extracts a bearer session token from the Authorization
// header, validates it, and attaches the resolved account to the request
// context. Returns 401 JSON on any failure — there is no redirect-based
// flow here, unlike bridge/adminui, since this API has no browser pages of
// its own.
func (s *Server) requireOwnerAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "missing or malformed Authorization header")
			return
		}
		userID, err := s.Directory.ValidateSession(r.Context(), directory.SessionToken(token))
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, "invalid or expired session")
			return
		}
		ctx := context.WithValue(r.Context(), ownerUserContextKey, userID)
		next(w, r.WithContext(ctx))
	}
}

func ownerFromContext(ctx context.Context) protocol.UserID {
	id, _ := ctx.Value(ownerUserContextKey).(protocol.UserID)
	return id
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(h, prefix))
	if token == "" {
		return "", false
	}
	return token, true
}

type setupRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Password    string `json:"password"`
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "could not read the request body: "+err.Error())
		return
	}
	if req.Username == "" || req.DisplayName == "" {
		writeJSONError(w, http.StatusUnprocessableEntity, "username and display_name are required")
		return
	}
	if len(req.Password) < 8 {
		writeJSONError(w, http.StatusUnprocessableEntity, "password must be at least 8 characters")
		return
	}

	id, err := s.Directory.BootstrapOwner(r.Context(), req.Username, req.DisplayName, req.Email, req.Password)
	if errors.Is(err, directory.ErrOwnerAlreadyExists) {
		writeJSONError(w, http.StatusConflict, "an owner account already exists")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not create the owner account")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"user_id": string(id)})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "could not read the request body: "+err.Error())
		return
	}
	token, expiresAt, err := s.Directory.Authenticate(r.Context(), req.Username, req.Password)
	if errors.Is(err, directory.ErrInvalidCredentials) {
		writeJSONError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not authenticate")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      string(token),
		"expires_at": expiresAt,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	token, _ := bearerToken(r)
	if err := s.Directory.Logout(r.Context(), directory.SessionToken(token)); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not log out")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
