// Package fakeromm is a stand-in RomM server for tests.
//
// It exists because this project's egress policy did not permit reaching a live
// RomM instance when the probe was written, and because CI must be able to
// prove the probe's behaviour without one in any case.
//
// It is explicitly not a claim about RomM's real API. Its shape encodes the
// same assumptions as the capability table in bridge/romm/capability.go, so the
// two agree with each other and both may be wrong together. What it proves is
// that the probe correctly reports what a server offers, correctly identifies a
// credential presentation, and correctly turns a missing capability into a
// blocker. Replace its assumptions with a recorded fixture from a real server as
// soon as one can be reached; the probe writes exactly that fixture.
package fakeromm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
)

// Options configure the fake server.
type Options struct {
	// Token is the credential the server accepts. Empty means no auth is
	// enforced.
	Token string

	// AuthScheme is how the server expects the credential: "bearer",
	// "x-api-key", or "authorization-raw".
	AuthScheme string

	// Version is the version string the health endpoint reports.
	Version string

	// OmitPaths removes operations from the published specification, to
	// simulate an older or restricted server.
	OmitPaths []string

	// NoHashes strips hash fields from ROM objects, to simulate a server whose
	// content cannot be matched against a catalogue.
	NoHashes bool

	// EmptyROMs makes the roms listing report zero items, to simulate a
	// freshly installed server or an empty library — as opposed to NoHashes,
	// which simulates a library that has content but no matchable hashes.
	EmptyROMs bool

	// SpecPath overrides where the specification is published.
	SpecPath string

	// RejectAllCredentials makes every authenticated request fail, whatever is
	// presented.
	RejectAllCredentials bool
}

// Server is a running fake.
type Server struct {
	*httptest.Server
	opts Options
}

// New starts a fake RomM server. The caller must Close it.
func New(opts Options) *Server {
	if opts.AuthScheme == "" {
		opts.AuthScheme = "bearer"
	}
	if opts.Version == "" {
		opts.Version = "3.11.0"
	}
	if opts.SpecPath == "" {
		opts.SpecPath = "/openapi.json"
	}

	s := &Server{opts: opts}
	mux := http.NewServeMux()

	mux.HandleFunc(opts.SpecPath, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.spec())
	})

	mux.HandleFunc("/api/heartbeat", s.authed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"VERSION": opts.Version, "NAME": "RomM"})
	}))

	mux.HandleFunc("/api/users/me", s.authed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"id": 7, "username": "bridge", "role": "viewer",
			"oauth_scopes": []string{"roms.read", "roms.write", "platforms.read"},
		})
	}))

	mux.HandleFunc("/api/platforms", s.authed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{
			{"id": 1, "slug": "gb", "name": "Game Boy", "rom_count": 12},
			{"id": 2, "slug": "genesis", "name": "Mega Drive", "rom_count": 30},
		})
	}))

	mux.HandleFunc("/api/roms", s.authed(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{"id": 99, "status": "accepted"})
			return
		}
		if opts.EmptyROMs {
			writeJSON(w, map[string]any{"items": []map[string]any{}, "total": 0, "limit": 1})
			return
		}
		writeJSON(w, map[string]any{
			"items": []map[string]any{s.rom()},
			"total": 1,
			"limit": 1,
		})
	}))

	mux.HandleFunc("/api/roms/", s.authed(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/content/") {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("fake rom content"))
			return
		}
		writeJSON(w, s.rom())
	}))

	s.Server = httptest.NewServer(mux)
	return s
}

// rom returns one ROM object.
func (s *Server) rom() map[string]any {
	obj := map[string]any{
		"id":            1,
		"platform_id":   1,
		"name":          "Super Test Land",
		"fs_name":       "Super Test Land (USA).gb",
		"fs_size_bytes": 65536,
		"summary":       "",
		"platform_slug": "gb",
	}
	if !s.opts.NoHashes {
		obj["crc_hash"] = "b0f97d50"
		obj["md5_hash"] = "3d4b1d3b4a3f1c2e5a6b7c8d9e0f1a2b"
		obj["sha1_hash"] = "e1f2a3b4c5d6e7f8091a2b3c4d5e6f7a8b9c0d1e"
	}
	return obj
}

// authed wraps a handler with the configured credential check.
func (s *Server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.opts.Token == "" {
			next(w, r)
			return
		}
		if s.opts.RejectAllCredentials || !s.credentialAccepted(r) {
			w.WriteHeader(http.StatusUnauthorized)
			writeJSON(w, map[string]any{"detail": "Not authenticated"})
			return
		}
		next(w, r)
	}
}

func (s *Server) credentialAccepted(r *http.Request) bool {
	switch s.opts.AuthScheme {
	case "bearer":
		return r.Header.Get("Authorization") == "Bearer "+s.opts.Token
	case "x-api-key":
		return r.Header.Get("X-Api-Key") == s.opts.Token
	case "authorization-raw":
		return r.Header.Get("Authorization") == s.opts.Token
	default:
		return false
	}
}

// spec renders the published OpenAPI document.
func (s *Server) spec() map[string]any {
	omitted := map[string]bool{}
	for _, p := range s.opts.OmitPaths {
		omitted[p] = true
	}

	paths := map[string]any{}
	add := func(path, method string, op map[string]any) {
		if omitted[path] {
			return
		}
		item, ok := paths[path].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[path] = item
		}
		item[method] = op
	}

	add("/api/heartbeat", "get", map[string]any{"operationId": "heartbeat"})
	add("/api/users/me", "get", map[string]any{"operationId": "current_user"})
	add("/api/platforms", "get", map[string]any{"operationId": "get_platforms"})
	add("/api/roms", "get", map[string]any{
		"operationId": "get_roms",
		"parameters": []map[string]any{
			{"name": "platform_id", "in": "query"},
			{"name": "limit", "in": "query"},
			{"name": "offset", "in": "query"},
			{"name": "search_term", "in": "query"},
		},
	})
	add("/api/roms", "post", map[string]any{"operationId": "add_rom"})
	add("/api/roms/{id}", "get", map[string]any{"operationId": "get_rom"})
	add("/api/roms/{id}/content/{file_name}", "get", map[string]any{"operationId": "get_rom_content"})

	return map[string]any{
		"openapi": "3.1.0",
		"info":    map[string]any{"title": "RomM API", "version": s.opts.Version},
		"paths":   paths,
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		panic(fmt.Sprintf("fakeromm: encoding response: %v", err))
	}
}
