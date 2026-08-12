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
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
)

// Server serves the Host's JSON API over a Directory.
type Server struct {
	Directory *directory.Directory

	// MaxInventoryBytes bounds a POST /api/bridges/{id}/inventory request
	// body. Zero means defaultMaxInventoryBodyBytes. Every other route uses
	// the much smaller defaultMaxBodyBytes — a manifest is the one payload
	// in this API with a legitimate reason to be large.
	MaxInventoryBytes int64

	mux *http.ServeMux
}

// New builds a ready-to-serve Server.
func New(d *directory.Directory) *Server {
	s := &Server{Directory: d}
	s.routes()
	return s
}

func (s *Server) maxInventoryBytes() int64 {
	if s.MaxInventoryBytes > 0 {
		return s.MaxInventoryBytes
	}
	return defaultMaxInventoryBodyBytes
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

	// Unauthenticated at the route level, same as enroll/rotate above: the
	// refresh token in the body is the credential (ADR 0019).
	mux.HandleFunc("POST /api/bridges/{bridgeID}/inventory", s.handlePublishInventory)
	mux.HandleFunc("POST /api/bridges/{bridgeID}/display-name", s.handleSetBridgeDisplayName)

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

// defaultMaxBodyBytes bounds every route's request body except inventory
// publishing — generous for setup/login/swarm/invitation/enroll/rotate
// bodies, which are all small, fixed-shape JSON, while still closing what
// was previously an unbounded read.
const defaultMaxBodyBytes = 1 << 20 // 1 MiB

// defaultMaxInventoryBodyBytes is the default cap for a manifest upload,
// overridable via Server.MaxInventoryBytes (see cmd/host/main.go's
// HOST_MAX_INVENTORY_BYTES). A Swarm's full inventory can run to tens of
// thousands of small JSON items; 64MiB is a generous, explicit default
// rather than leaving the route unbounded.
const defaultMaxInventoryBodyBytes = 64 << 20 // 64 MiB

// decodeJSON reads and decodes a JSON body, capped at maxBytes via
// http.MaxBytesReader so a request body is never read unbounded into
// memory. A body exceeding maxBytes surfaces as a *http.MaxBytesError,
// which callers can distinguish from an ordinary decode failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// writeDecodeError maps a decodeJSON failure to a response — a body over
// the size cap gets its own 413, everything else stays the existing 400
// "could not read the request body" shape.
func writeDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeJSONError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("request body exceeds the %d byte limit", tooLarge.Limit))
		return
	}
	writeJSONError(w, http.StatusBadRequest, "could not read the request body: "+err.Error())
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
