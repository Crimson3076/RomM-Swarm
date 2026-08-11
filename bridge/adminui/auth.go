package adminui

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"sync"
	"time"
)

// Session handling.
//
// Sessions are held in memory only — lost on restart, requiring a fresh
// login, which is an acceptable trade-off for a single-admin local tool and
// far simpler than persisting them. The session cookie is HttpOnly and
// SameSite=Lax (Secure too, whenever the connection is actually TLS): for a
// same-origin admin app, SameSite=Lax already blocks a cross-site POST from
// carrying the cookie in modern browsers, which is this tool's real CSRF
// defense — a separate synchronizer-token scheme would add meaningfully
// more code for a threat model ("a stranger's webpage forges a request into
// your LAN-only admin panel") this already closes off.

const sessionCookieName = "bridge_session"
const sessionLifetime = 24 * time.Hour

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time // token -> expiry
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: map[string]time.Time{}}
}

func (s *sessionStore) create() (string, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(sessionLifetime)
	s.mu.Unlock()
	return token, nil
}

func (s *sessionStore) valid(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, token)
		return false
	}
	return true
}

func (s *sessionStore) revoke(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionLifetime.Seconds()),
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

func (s *Server) authenticated(r *http.Request) bool {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	return s.sessions.valid(c.Value)
}

// requireAuth redirects an unauthenticated page request to /login (or
// returns 401 for an /api/ request, which isn't meant to be followed by a
// browser navigation).
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authenticated(r) {
			if isAPIRequest(r) {
				http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// requireConfiguredAndAuth additionally redirects to /setup when the Bridge
// has no admin password yet, since there is nothing to authenticate against.
func (s *Server) requireConfiguredAndAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, _ := s.Backend.ConfigStore().Load()
		if cfg.AdminPasswordHash == "" {
			if isAPIRequest(r) {
				http.Error(w, `{"error":"setup has not been completed"}`, http.StatusPreconditionRequired)
				return
			}
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		s.requireAuth(next)(w, r)
	}
}

func isAPIRequest(r *http.Request) bool {
	return len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/"
}
