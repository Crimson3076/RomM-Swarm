package adminui

import (
	"embed"
	"html/template"
	"net/http"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*.css static/*.js
var staticFS embed.FS

var pages = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// Server serves the local Bridge admin UI and its JSON API over a Backend.
type Server struct {
	Backend Backend

	// InboxDir is an optional directory the Inbox page browses for files to
	// import — typically a Docker volume mount. Empty means no inbox is
	// configured; the browser-upload path still works either way.
	InboxDir string

	// ImportTimeout bounds how long a triggered import waits for RomM to
	// index and match it. Zero means protocol.DefaultIngestionTimeout.
	ImportTimeout string

	sessions *sessionStore
	mux      *http.ServeMux
}

// New builds a ready-to-serve Server.
func New(backend Backend, inboxDir string) *Server {
	s := &Server{Backend: backend, InboxDir: inboxDir, sessions: newSessionStore()}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) routes() {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.Handle("GET /static/", http.FileServerFS(staticFS))

	mux.HandleFunc("GET /setup", s.handleSetupForm)
	mux.HandleFunc("POST /setup", s.handleSetupSubmit)
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("POST /logout", s.requireAuth(s.handleLogout))

	mux.HandleFunc("GET /{$}", s.requireConfiguredAndAuth(s.handleDashboard))
	mux.HandleFunc("GET /settings", s.requireConfiguredAndAuth(s.handleSettingsForm))
	mux.HandleFunc("POST /settings", s.requireConfiguredAndAuth(s.handleSettingsSubmit))
	mux.HandleFunc("POST /api/connection/test", s.requireConfiguredAndAuth(s.handleConnectionTest))

	mux.HandleFunc("GET /swarm", s.requireConfiguredAndAuth(s.handleSwarmPage))
	mux.HandleFunc("POST /swarm", s.requireConfiguredAndAuth(s.handleSwarmJoin))
	mux.HandleFunc("POST /api/swarm/test", s.requireConfiguredAndAuth(s.handleSwarmTest))
	mux.HandleFunc("POST /api/swarm/publish-inventory", s.requireConfiguredAndAuth(s.handlePublishInventory))

	mux.HandleFunc("GET /library", s.requireConfiguredAndAuth(s.handleLibraryPage))
	mux.HandleFunc("GET /api/library/download", s.requireConfiguredAndAuth(s.handleLibraryDownload))

	mux.HandleFunc("GET /inbox", s.requireConfiguredAndAuth(s.handleInboxPage))
	mux.HandleFunc("POST /api/import", s.requireConfiguredAndAuth(s.handleImport))

	mux.HandleFunc("GET /activity", s.requireConfiguredAndAuth(s.handleActivityPage))

	s.mux = mux
}

// baseData is embedded (by value, not reference) into every page's template
// data, so layout.html's header/footer always has what it needs regardless
// of which handler is rendering.
type baseData struct {
	Title         string
	Authenticated bool
	AutoRefresh   bool
	Error         string
	Notice        string
}

func (s *Server) base(r *http.Request, title string) baseData {
	return baseData{Title: title, Authenticated: s.authenticated(r)}
}

// renderPage executes a named page template. A template execution error at
// this point means the handler built the wrong shape of data for its own
// template — a programming error, not something the request caused — so it
// is logged and turned into a plain 500 rather than partially written HTML.
func renderPage(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pages.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "internal error rendering the page", http.StatusInternalServerError)
	}
}
