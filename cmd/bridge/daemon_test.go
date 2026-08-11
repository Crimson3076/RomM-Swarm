package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/bridgeconfig"
	"github.com/Crimson3076/RomM-Swarm/internal/romfixture"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// fakeBridgeServer is a combined fake RomM server exercising the full
// confirmed real protocol (ADR 0003): capability probing, platform listing,
// the chunked upload sequence, and a mutable roms listing that reflects a
// completed upload immediately — unlike real RomM's confirmed five-minute
// watcher debounce, since this test is proving the daemon's wiring, not
// re-proving RomM's own timing (already covered in bridge/ingest and
// docs/phase0/multi-file-archive-and-ingestion-behavior.md).
type fakeBridgeServer struct {
	mu       sync.Mutex
	uploads  map[string]*fakeUpload
	nextID   int
	roms     []map[string]any
	nextRom  int
	platform map[string]int // slug -> id
}

type fakeUpload struct {
	filename   string
	platformID int
	chunks     map[int64][]byte
}

func newFakeBridgeServer() *fakeBridgeServer {
	return &fakeBridgeServer{
		uploads:  map[string]*fakeUpload{},
		nextRom:  1000,
		platform: map[string]int{"gb": 11, "gba": 12},
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *fakeBridgeServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"openapi": "3.1.0",
			"info":    map[string]any{"title": "fake RomM", "version": "test"},
			"paths": map[string]any{
				"/api/heartbeat": map[string]any{"get": map[string]any{"operationId": "heartbeat"}},
				"/api/users/me":  map[string]any{"get": map[string]any{"operationId": "me"}},
				"/api/platforms": map[string]any{"get": map[string]any{"operationId": "platforms"}},
				"/api/roms": map[string]any{
					"get": map[string]any{
						"operationId": "roms",
						"parameters": []map[string]any{
							{"name": "limit", "in": "query"}, {"name": "offset", "in": "query"}, {"name": "platform_id", "in": "query"},
						},
					},
				},
				"/api/roms/{id}":                map[string]any{"get": map[string]any{"operationId": "rom_detail"}},
				"/api/roms/{id}/content/{name}": map[string]any{"get": map[string]any{"operationId": "download"}},
				"/api/roms/upload/start":        map[string]any{"post": map[string]any{"operationId": "upload_start"}},
			},
		})
	})

	mux.HandleFunc("/api/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"VERSION": "5.0.0-fake"})
	})
	mux.HandleFunc("/api/users/me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"id": 1, "username": "tester", "role": "admin"})
	})
	mux.HandleFunc("/api/platforms", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var out []map[string]any
		for slug, id := range s.platform {
			out = append(out, map[string]any{"id": id, "slug": slug, "name": slug})
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("/api/roms", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, map[string]any{"items": s.roms, "total": len(s.roms)})
	})
	mux.HandleFunc("/api/roms/upload/start", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		filename := r.Header.Get("x-upload-filename")
		platformID, _ := strconv.Atoi(r.Header.Get("x-upload-platform"))
		s.nextID++
		id := fmt.Sprintf("up-%d", s.nextID)
		s.uploads[id] = &fakeUpload{filename: filename, platformID: platformID, chunks: map[int64][]byte{}}
		writeJSON(w, map[string]any{"upload_id": id})
	})
	mux.HandleFunc("/api/roms/upload/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/roms/upload/")
		parts := strings.Split(rest, "/")
		id := parts[0]

		s.mu.Lock()
		up, ok := s.uploads[id]
		s.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, map[string]any{"detail": "Upload session not found or expired"})
			return
		}

		switch {
		case len(parts) == 1 && r.Method == http.MethodPut:
			index, _ := strconv.ParseInt(r.Header.Get("x-chunk-index"), 10, 64)
			buf := make([]byte, r.ContentLength)
			readFull(r.Body, buf)
			s.mu.Lock()
			up.chunks[index] = buf
			s.mu.Unlock()
			writeJSON(w, map[string]any{"received": index + 1, "total": len(up.chunks)})

		case len(parts) == 2 && parts[1] == "complete" && r.Method == http.MethodPost:
			s.mu.Lock()
			var full []byte
			for i := 0; i < len(up.chunks); i++ {
				full = append(full, up.chunks[int64(i)]...)
			}
			sum := sha1.Sum(full)
			// RommUploader sends the platform as a numeric id (x-upload-
			// platform), resolved beforehand via bridge/romm.Platforms.Lookup
			// — the filename itself carries no platform information by the
			// time it reaches here (Flow strips the relativeDest's leading
			// platform-slug path component before calling Upload). Reverse
			// the id back to a slug the same way a real RomM object would
			// report one.
			platformSlug := ""
			for slug, id := range s.platform {
				if id == up.platformID {
					platformSlug = slug
					break
				}
			}
			s.nextRom++
			s.roms = append(s.roms, map[string]any{
				"id": s.nextRom, "platform_id": up.platformID, "platform_slug": platformSlug,
				"fs_name": up.filename, "fs_size_bytes": len(full), "sha1_hash": hex.EncodeToString(sum[:]),
			})
			s.mu.Unlock()
			w.WriteHeader(http.StatusOK)

		case len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	mux.HandleFunc("/api/roms/", func(w http.ResponseWriter, r *http.Request) {
		// /api/roms/{id}/content/{name}
		p := strings.TrimPrefix(r.URL.Path, "/api/roms/")
		parts := strings.SplitN(p, "/", 3)
		if len(parts) != 3 || parts[1] != "content" {
			http.NotFound(w, r)
			return
		}
		id, _ := strconv.Atoi(parts[0])
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, rom := range s.roms {
			if rom["id"] != id {
				continue
			}
			fsName := rom["fs_name"].(string)
			for _, up := range s.uploads {
				if up.filename != fsName {
					continue
				}
				var full []byte
				for i := 0; i < len(up.chunks); i++ {
					full = append(full, up.chunks[int64(i)]...)
				}
				w.Header().Set("Content-Length", strconv.Itoa(len(full)))
				w.Write(full)
				return
			}
		}
		http.NotFound(w, r)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func readFull(r interface{ Read([]byte) (int, error) }, buf []byte) {
	n := 0
	for n < len(buf) {
		m, err := r.Read(buf[n:])
		n += m
		if err != nil {
			break
		}
	}
}

// TestPhase0_DaemonBootstrapsConnectsAndImports is the daemon-level
// substitution proof: the same real chunked-upload-plus-reconciliation
// pipeline cmd/swarm-bridge exercises against the user's live server, run
// here against a fake one through the persistent Daemon wiring (FileJournal
// and a fixed staging directory, not the CLI's in-memory journal and
// throwaway temp directory).
func TestPhase0_DaemonBootstrapsConnectsAndImports(t *testing.T) {
	srv := newFakeBridgeServer().start(t)

	configDir := t.TempDir()
	d, err := NewDaemon(configDir)
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}

	t.Setenv("ROMM_URL", srv.URL)
	t.Setenv("ROMM_TOKEN", "test-token")

	if err := d.Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if d.Connection() == nil {
		t.Fatal("Bootstrap did not leave a live connection")
	}

	cfg, err := d.ConfigStore().Load()
	if err != nil {
		t.Fatalf("loading the seeded config: %v", err)
	}
	if cfg.RommURL != srv.URL || cfg.RommToken != "test-token" {
		t.Fatalf("seeded config = %+v, want the env-supplied values", cfg)
	}

	payload := romfixture.GameBoy("DAEMON TEST GAME", 65536, false)
	path := filepath.Join(t.TempDir(), "Daemon Test Game.gb")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	id, err := d.StartImport(path, "gb", "", 30*time.Second, nil)
	if err != nil {
		t.Fatalf("StartImport: %v", err)
	}

	deadline := time.Now().Add(15 * time.Second)
	var state protocol.DestinationState
	for time.Now().Before(deadline) {
		var ok bool
		state, ok = d.Journal().Current(id)
		if ok && (state.Terminal() || state == protocol.StateSourceActive) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if state != protocol.StateSourceActive {
		history := d.Journal().History(id)
		t.Fatalf("final state = %s, want source_active. History: %+v", state, history)
	}

	// The journal is really on disk, under this Daemon's own journal
	// directory, not held in memory.
	entries, err := os.ReadDir(d.Journal().Dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("journal directory has %d entries (err %v), want exactly 1", len(entries), err)
	}
}

func TestStartImportFailsFastWithoutAConnection(t *testing.T) {
	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	if _, err := d.StartImport("/does/not/matter", "gb", "", time.Minute, nil); err == nil {
		t.Fatal("StartImport succeeded with no RomM connection established")
	}
}

func TestStartImportRejectsAWrongPlatformFile(t *testing.T) {
	srv := newFakeBridgeServer().start(t)
	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	if err := d.ConfigStore().Save(bridgeconfig.Config{RommURL: srv.URL, RommToken: "t"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := d.Reconnect(context.Background()); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}

	payload := romfixture.GameBoy("WRONG PLATFORM", 65536, false)
	path := filepath.Join(t.TempDir(), "wrong.gb")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	if _, err := d.StartImport(path, "gba", "", time.Minute, nil); err == nil {
		t.Fatal("StartImport accepted a Game Boy file declared as gba")
	}
}

// TestStartImportUsesDisplayNameNotTheLocalTempPath is a regression test: a
// browser upload stages its bytes under a Bridge-generated temp path (e.g.
// "bridge-upload-1861139377.gb"), and RomM must still see the file's real
// name — otherwise RomM can't parse or match it, even though the bytes
// themselves import and play fine.
func TestStartImportUsesDisplayNameNotTheLocalTempPath(t *testing.T) {
	fb := newFakeBridgeServer()
	srv := fb.start(t)

	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	if err := d.ConfigStore().Save(bridgeconfig.Config{RommURL: srv.URL, RommToken: "t"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := d.Reconnect(context.Background()); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}

	payload := romfixture.GameBoy("DISPLAY NAME TEST", 65536, false)
	// The on-disk path deliberately looks like a temp-upload path, distinct
	// from the name RomM should end up seeing.
	tempLikePath := filepath.Join(t.TempDir(), "bridge-upload-1861139377.gb")
	if err := os.WriteFile(tempLikePath, payload, 0o644); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	id, err := d.StartImport(tempLikePath, "gb", "Display Name Test.gb", 30*time.Second, nil)
	if err != nil {
		t.Fatalf("StartImport: %v", err)
	}

	deadline := time.Now().Add(15 * time.Second)
	var state protocol.DestinationState
	for time.Now().Before(deadline) {
		var ok bool
		state, ok = d.Journal().Current(id)
		if ok && (state.Terminal() || state == protocol.StateSourceActive) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if state != protocol.StateSourceActive {
		t.Fatalf("final state = %s, want source_active", state)
	}

	fb.mu.Lock()
	defer fb.mu.Unlock()
	if len(fb.roms) != 1 {
		t.Fatalf("fake RomM recorded %d rom(s), want 1", len(fb.roms))
	}
	if got := fb.roms[0]["fs_name"]; got != "Display Name Test.gb" {
		t.Fatalf("RomM saw filename %q, want the original display name, not the local temp path's name", got)
	}
}

func TestReconnectRequiresAConfiguredStore(t *testing.T) {
	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	if err := d.Reconnect(context.Background()); err == nil {
		t.Fatal("Reconnect succeeded against an unconfigured store")
	}
}
