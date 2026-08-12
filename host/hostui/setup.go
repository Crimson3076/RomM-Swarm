package hostui

import (
	"errors"
	"net/http"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
)

type setupData struct {
	baseData
	Username    string
	DisplayName string
}

func (s *Server) handleSetupForm(w http.ResponseWriter, r *http.Request) {
	exists, err := s.Directory.OwnerExists(r.Context())
	if err != nil {
		http.Error(w, "internal error checking setup state", http.StatusInternalServerError)
		return
	}
	if exists {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	renderPage(w, "setup", setupData{baseData: s.base(r, "Setup")})
}

func (s *Server) handleSetupSubmit(w http.ResponseWriter, r *http.Request) {
	exists, err := s.Directory.OwnerExists(r.Context())
	if err != nil {
		http.Error(w, "internal error checking setup state", http.StatusInternalServerError)
		return
	}
	if exists {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		s.setupError(w, r, "could not read the submitted form", "", "")
		return
	}
	username := r.FormValue("username")
	displayName := r.FormValue("display_name")
	email := r.FormValue("email")
	password := r.FormValue("password")
	confirm := r.FormValue("password_confirm")

	if username == "" || displayName == "" {
		s.setupError(w, r, "username and display name are both required", username, displayName)
		return
	}
	if len(password) < 8 {
		s.setupError(w, r, "the admin password must be at least 8 characters", username, displayName)
		return
	}
	if password != confirm {
		s.setupError(w, r, "the two password entries did not match", username, displayName)
		return
	}

	if _, err := s.Directory.BootstrapOwner(r.Context(), username, displayName, email, password); err != nil {
		if errors.Is(err, directory.ErrOwnerAlreadyExists) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		s.setupError(w, r, "could not create the owner account: "+err.Error(), username, displayName)
		return
	}

	token, expiresAt, err := s.Directory.Authenticate(r.Context(), username, password)
	if err != nil {
		// The account exists; a login failure here is surfaced on the
		// login page rather than blocking setup from completing at all.
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.setSessionCookie(w, r, token, expiresAt)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) setupError(w http.ResponseWriter, r *http.Request, msg, username, displayName string) {
	data := setupData{baseData: s.base(r, "Setup"), Username: username, DisplayName: displayName}
	data.Error = msg
	w.WriteHeader(http.StatusUnprocessableEntity)
	renderPage(w, "setup", data)
}
