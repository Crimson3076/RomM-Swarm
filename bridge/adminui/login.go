package adminui

import "net/http"

type loginData struct {
	baseData
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.Backend.ConfigStore().Load()
	if cfg.AdminPasswordHash == "" {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if s.authenticated(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	renderPage(w, "login", loginData{baseData: s.base(r, "Log in")})
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.Backend.ConfigStore().Load()
	if cfg.AdminPasswordHash == "" {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		s.loginError(w, r, "could not read the submitted form")
		return
	}
	password := r.FormValue("password")

	if !verifyPassword(password, cfg.AdminPasswordHash) {
		s.loginError(w, r, "wrong password")
		return
	}

	token, err := s.sessions.create()
	if err != nil {
		s.loginError(w, r, "could not start a session: "+err.Error())
		return
	}
	s.setSessionCookie(w, r, token)
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
		s.sessions.revoke(c.Value)
	}
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
