package hostui

import (
	"context"
	"net/http"
	"time"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// Session handling.
//
// Unlike bridge/adminui, there is no in-memory sessionStore here.
// Directory.Authenticate/ValidateSession/Logout are already fully
// transport-agnostic and already durably persist sessions in Postgres
// (the sessions table) — this file is only the cookie-delivery glue on
// top, the same way host/hostapi's auth.go is only the bearer-header
// glue. Cookie flags mirror bridge/adminui's own reasoning: HttpOnly,
// SameSite=Lax (the real CSRF defense for a same-origin admin app),
// Secure whenever the connection is actually TLS.

const sessionCookieName = "host_session"

type contextKey int

const userContextKey contextKey = 0

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token directory.SessionToken, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    string(token),
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// authenticatedUser resolves the session cookie, if any, to the account it
// belongs to.
func (s *Server) authenticatedUser(r *http.Request) (protocol.UserID, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return "", false
	}
	id, err := s.Directory.ValidateSession(r.Context(), directory.SessionToken(c.Value))
	if err != nil {
		return "", false
	}
	return id, true
}

func userFromContext(ctx context.Context) protocol.UserID {
	id, _ := ctx.Value(userContextKey).(protocol.UserID)
	return id
}

// requireAuth redirects an unauthenticated page request to /login.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := s.authenticatedUser(r)
		if !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey, userID)
		next(w, r.WithContext(ctx))
	}
}

// requireBootstrappedAndAuth additionally redirects to /setup when no owner
// account exists yet, since there is nothing to authenticate against.
func (s *Server) requireBootstrappedAndAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		exists, err := s.Directory.OwnerExists(r.Context())
		if err != nil || !exists {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		s.requireAuth(next)(w, r)
	}
}
