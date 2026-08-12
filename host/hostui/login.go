package hostui

import (
	"net/http"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
)

type loginData struct {
	baseData
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	exists, err := s.Directory.OwnerExists(r.Context())
	if err != nil {
		http.Error(w, "internal error checking setup state", http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if _, ok := s.authenticatedUser(r); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	renderPage(w, "login", loginData{baseData: s.base(r, "Log in")})
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	exists, err := s.Directory.OwnerExists(r.Context())
	if err != nil {
		http.Error(w, "internal error checking setup state", http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		s.loginError(w, r, "could not read the submitted form")
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")

	token, expiresAt, err := s.Directory.Authenticate(r.Context(), username, password)
	if err != nil {
		s.loginError(w, r, "invalid username or password")
		return
	}
	s.setSessionCookie(w, r, token, expiresAt)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) loginError(w http.ResponseWriter, r *http.Request, msg string) {
	data := loginData{baseData: s.base(r, "Log in")}
	data.Error = msg
	w.WriteHeader(http.StatusUnauthorized)
	renderPage(w, "login", data)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		_ = s.Directory.Logout(r.Context(), directory.SessionToken(c.Value))
	}
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
