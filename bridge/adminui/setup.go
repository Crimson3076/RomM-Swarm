package adminui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/romm"
)

type setupData struct {
	baseData
	RommURL string
}

func (s *Server) handleSetupForm(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.Backend.ConfigStore().Load()
	if cfg.AdminPasswordHash != "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	data := setupData{baseData: s.base(r, "Setup"), RommURL: cfg.RommURL}
	renderPage(w, "setup", data)
}

func (s *Server) handleSetupSubmit(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.Backend.ConfigStore().Load()
	if cfg.AdminPasswordHash != "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		s.setupError(w, r, "could not read the submitted form")
		return
	}
	rommURL := strings.TrimSpace(r.FormValue("romm_url"))
	rommToken := r.FormValue("romm_token")
	password := r.FormValue("password")
	confirm := r.FormValue("password_confirm")

	if rommURL == "" || rommToken == "" {
		s.setupError(w, r, "a RomM URL and Client API Token are both required")
		return
	}
	if len(password) < 8 {
		s.setupError(w, r, "the admin password must be at least 8 characters")
		return
	}
	if password != confirm {
		s.setupError(w, r, "the two password entries did not match")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if _, _, err := romm.Connect(ctx, rommURL, rommToken); err != nil {
		s.setupError(w, r, "could not connect to RomM: "+err.Error())
		return
	}

	hash, err := hashPassword(password)
	if err != nil {
		s.setupError(w, r, "could not secure the admin password: "+err.Error())
		return
	}

	// Preserve whatever bootstrap.go may already have seeded from
	// BRIDGE_DESTINATION_MODE/BRIDGE_LIBRARY_ROOT — setup only owns the
	// connection and the admin password, not the destination settings.
	cfg.RommURL = rommURL
	cfg.RommToken = rommToken
	cfg.AdminPasswordHash = hash
	if err := s.Backend.ConfigStore().Save(cfg); err != nil {
		s.setupError(w, r, "could not save configuration: "+err.Error())
		return
	}
	if err := s.Backend.Reconnect(ctx); err != nil {
		// The config is saved; a Reconnect failure here is surfaced on the
		// dashboard rather than blocking setup from completing at all.
	}

	token, err := s.sessions.create()
	if err != nil {
		s.setupError(w, r, "could not start a session: "+err.Error())
		return
	}
	s.setSessionCookie(w, r, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) setupError(w http.ResponseWriter, r *http.Request, msg string) {
	cfg, _ := s.Backend.ConfigStore().Load()
	data := setupData{baseData: s.base(r, "Setup"), RommURL: cfg.RommURL}
	data.Error = msg
	w.WriteHeader(http.StatusUnprocessableEntity)
	renderPage(w, "setup", data)
}
