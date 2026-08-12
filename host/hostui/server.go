// Package hostui is the Network Host's local browser admin UI — ADR 0018.
// It is a second, independent HTTP front end over the same
// *directory.Directory host/hostapi already uses, served on its own port
// (see cmd/host/main.go) — not routes on hostapi.Server, which is
// deliberately JSON-only (see its own package doc comment). Mirrors
// bridge/adminui's shape: html/template + embed.FS, a cookie session, no
// third-party dependencies.
package hostui

import (
	"embed"
	"html/template"
	"net/http"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*.css
var staticFS embed.FS

var pages = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// Server serves the Host admin UI over a Directory.
//
// Unlike bridge/adminui.Server, which depends on a narrow Backend
// interface, Server holds a concrete *directory.Directory — the same
// choice host/hostapi.Server already made. Bridge has no database to lean
// on and needed an interface seam for testability; Host's convention,
// since ADR 0016, is to test every layer against real Postgres.
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
	mux.Handle("GET /static/", http.FileServerFS(staticFS))

	mux.HandleFunc("GET /setup", s.handleSetupForm)
	mux.HandleFunc("POST /setup", s.handleSetupSubmit)
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("POST /logout", s.requireAuth(s.handleLogout))

	mux.HandleFunc("GET /{$}", s.requireBootstrappedAndAuth(s.handleDashboard))
	mux.HandleFunc("POST /swarms", s.requireBootstrappedAndAuth(s.handleCreateSwarm))
	mux.HandleFunc("GET /swarms/{swarmID}", s.requireBootstrappedAndAuth(s.handleSwarmView))
	mux.HandleFunc("POST /swarms/{swarmID}/invitations", s.requireBootstrappedAndAuth(s.handleIssueInvitation))
	mux.HandleFunc("POST /swarms/{swarmID}/bridges/{bridgeID}/revoke", s.requireBootstrappedAndAuth(s.handleRevokeBridge))
	mux.HandleFunc("POST /swarms/{swarmID}/bridges/{bridgeID}/reenroll", s.requireBootstrappedAndAuth(s.handleReenrollBridge))
	mux.HandleFunc("POST /swarms/{swarmID}/bridges/{bridgeID}/name", s.requireBootstrappedAndAuth(s.handleSetBridgeName))

	s.mux = mux
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// baseData is embedded (by value, not reference) into every page's
// template data, so layout.html's header/nav/flash blocks always have
// what they need regardless of which handler is rendering.
type baseData struct {
	Title         string
	Authenticated bool
	Error         string
	Notice        string
}

func (s *Server) base(r *http.Request, title string) baseData {
	_, authenticated := s.authenticatedUser(r)
	return baseData{Title: title, Authenticated: authenticated}
}

// renderPage executes a named page template. A template execution error at
// this point means the handler built the wrong shape of data for its own
// template — a programming error, not something the request caused — so it
// is turned into a plain 500 rather than partially written HTML.
func renderPage(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pages.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "internal error rendering the page", http.StatusInternalServerError)
	}
}
