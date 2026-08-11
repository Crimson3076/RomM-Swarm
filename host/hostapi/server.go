// Package hostapi is the Network Host's JSON HTTP layer — ADR 0016, slice
// 1. No HTML here; per ADR 0013 (still Open, deferred to Phase 5) the
// eventual web frontend and management application share one role-scoped
// API with no direct database access, and this is that contract's first
// implementation. Routing follows the same stdlib net/http.ServeMux,
// Go 1.22+ method-pattern approach bridge/adminui/server.go already uses
// twice in this repo — JSON instead of html/template.
package hostapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
)

// Server serves the Host's JSON API over a Directory.
type Server struct {
	Directory *directory.Directory

	mux *http.ServeMux
}

// New builds a ready-to-serve Server.
func New(d *directory.Directory) *Server {
	s := &Server{Directory: d}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) routes() {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", handleHealthz)

	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.requireOwnerAuth(s.handleLogout))

	mux.HandleFunc("POST /api/swarms", s.requireOwnerAuth(s.handleCreateSwarm))
	mux.HandleFunc("GET /api/swarms", s.requireOwnerAuth(s.handleListSwarms))
	mux.HandleFunc("POST /api/swarms/{swarmID}/invitations", s.requireOwnerAuth(s.handleIssueInvitation))

	// Unauthenticated: the invitation code or refresh token presented in
	// the request body IS the credential for these two calls.
	mux.HandleFunc("POST /api/bridges/enroll", s.handleEnrollBridge)
	mux.HandleFunc("POST /api/bridges/{bridgeID}/rotate", s.handleRotateBridge)

	mux.HandleFunc("POST /api/bridges/{bridgeID}/revoke", s.requireOwnerAuth(s.handleRevokeBridge))
	mux.HandleFunc("POST /api/bridges/{bridgeID}/reenroll", s.requireOwnerAuth(s.handleReenrollBridge))

	s.mux = mux
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// secondsToDuration turns a JSON-friendly seconds count into a
// time.Duration. Zero or negative means "use the caller's default" — see
// Directory.IssueInvitation.
func secondsToDuration(secs int) time.Duration {
	if secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}
