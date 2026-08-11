package adminui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/bridgeconfig"
	"github.com/Crimson3076/RomM-Swarm/bridge/romm"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

type settingsData struct {
	baseData
	RommURL         string
	TokenConfigured bool
	DestinationMode string
	LibraryRoot     string
}

func settingsDataFrom(r *http.Request, s *Server, cfg bridgeconfig.Config) settingsData {
	mode := string(cfg.DestinationMode)
	if mode == "" {
		mode = string(protocol.ModeAPIOnly)
	}
	return settingsData{
		baseData:        s.base(r, "Settings"),
		RommURL:         cfg.RommURL,
		TokenConfigured: cfg.RommToken != "",
		DestinationMode: mode,
		LibraryRoot:     cfg.LibraryRoot,
	}
}

func (s *Server) handleSettingsForm(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.Backend.ConfigStore().Load()
	renderPage(w, "settings", settingsDataFrom(r, s, cfg))
}

func (s *Server) handleSettingsSubmit(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.Backend.ConfigStore().Load()
	if err != nil {
		s.settingsError(w, r, cfg, "could not load the current configuration: "+err.Error())
		return
	}

	if err := r.ParseForm(); err != nil {
		s.settingsError(w, r, cfg, "could not read the submitted form")
		return
	}

	switch r.FormValue("section") {
	case "password":
		s.handleChangePassword(w, r, cfg)
	default:
		s.handleSaveConnection(w, r, cfg)
	}
}

func (s *Server) handleSaveConnection(w http.ResponseWriter, r *http.Request, cfg bridgeconfig.Config) {
	rommURL := strings.TrimSpace(r.FormValue("romm_url"))
	rommToken := r.FormValue("romm_token") // blank means "keep the stored one"
	mode := protocol.DestinationMode(r.FormValue("destination_mode"))
	libraryRoot := strings.TrimSpace(r.FormValue("library_root"))

	if rommURL == "" {
		s.settingsError(w, r, cfg, "a RomM URL is required")
		return
	}
	if !mode.Valid() {
		s.settingsError(w, r, cfg, "unsupported destination mode")
		return
	}

	next := cfg
	next.RommURL = rommURL
	if rommToken != "" {
		next.RommToken = rommToken
	}
	next.DestinationMode = mode
	next.LibraryRoot = libraryRoot

	if err := s.Backend.ConfigStore().Save(next); err != nil {
		s.settingsError(w, r, cfg, "could not save settings: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	notice := "Settings saved."
	if err := s.Backend.Reconnect(ctx); err != nil {
		notice = "Settings saved, but connecting to RomM with them failed: " + err.Error()
	}

	data := settingsDataFrom(r, s, next)
	data.Notice = notice
	renderPage(w, "settings", data)
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request, cfg bridgeconfig.Config) {
	current := r.FormValue("current_password")
	newPassword := r.FormValue("new_password")
	confirm := r.FormValue("new_password_confirm")

	if !verifyPassword(current, cfg.AdminPasswordHash) {
		s.settingsError(w, r, cfg, "the current password was not correct")
		return
	}
	if len(newPassword) < 8 {
		s.settingsError(w, r, cfg, "the new password must be at least 8 characters")
		return
	}
	if newPassword != confirm {
		s.settingsError(w, r, cfg, "the two new password entries did not match")
		return
	}

	hash, err := hashPassword(newPassword)
	if err != nil {
		s.settingsError(w, r, cfg, "could not secure the new password: "+err.Error())
		return
	}
	next := cfg
	next.AdminPasswordHash = hash
	if err := s.Backend.ConfigStore().Save(next); err != nil {
		s.settingsError(w, r, cfg, "could not save the new password: "+err.Error())
		return
	}

	data := settingsDataFrom(r, s, next)
	data.Notice = "Password changed."
	renderPage(w, "settings", data)
}

func (s *Server) settingsError(w http.ResponseWriter, r *http.Request, cfg bridgeconfig.Config, msg string) {
	data := settingsDataFrom(r, s, cfg)
	data.Error = msg
	w.WriteHeader(http.StatusUnprocessableEntity)
	renderPage(w, "settings", data)
}

// handleConnectionTest wraps romm.Connect without changing any stored
// state — a pure connectivity check the settings page can call before
// saving.
func (s *Server) handleConnectionTest(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSONError(w, http.StatusBadRequest, "could not read the submitted form")
		return
	}
	rommURL := strings.TrimSpace(r.FormValue("romm_url"))
	rommToken := r.FormValue("romm_token")
	if rommToken == "" {
		if cfg, err := s.Backend.ConfigStore().Load(); err == nil {
			rommToken = cfg.RommToken
		}
	}
	if rommURL == "" || rommToken == "" {
		writeJSONError(w, http.StatusBadRequest, "a RomM URL and token are both required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	_, report, err := romm.Connect(ctx, rommURL, rommToken)
	if err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"connected":      true,
		"server_version": report.ServerVersion,
	})
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
