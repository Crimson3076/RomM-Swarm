package ingest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Crimson3076/RomM-Swarm/bridge/romm"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// chunkedUploadServer reproduces the confirmed real protocol precisely:
// headers on start, x-chunk-index on each PUT, raw octet-stream chunk bodies,
// an empty-body 2xx on complete, and 404 with {"detail": "..."} for an
// upload_id that is unknown or already completed — exactly what a live RomM
// 5.0.0 instance was observed to do.
type chunkedUploadServer struct {
	mu       sync.Mutex
	sessions map[string]*uploadSession
	nextID   int

	// canceled and completed record which sessions reached each terminal call,
	// for assertions.
	canceled  map[string]bool
	completed map[string][]byte

	// failChunkIndex, when >= 0, makes that chunk index fail once.
	failChunkIndex int
}

type uploadSession struct {
	platform    string
	filename    string
	totalSize   int64
	totalChunks int64
	chunks      map[int64][]byte
}

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newChunkedUploadServer() *chunkedUploadServer {
	return &chunkedUploadServer{
		sessions:       map[string]*uploadSession{},
		canceled:       map[string]bool{},
		completed:      map[string][]byte{},
		failChunkIndex: -1,
	}
}

func (s *chunkedUploadServer) mux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{
			"openapi": "3.1.0",
			"info":    map[string]any{"title": "RomM API", "version": "5.0.0"},
			"paths": map[string]any{
				"/api/roms/upload/start": map[string]any{"post": map[string]any{"operationId": "start"}},
			},
		})
	})

	mux.HandleFunc("/api/roms/upload/start", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()

		platform := r.Header.Get("x-upload-platform")
		filename := r.Header.Get("x-upload-filename")
		size, _ := strconv.ParseInt(r.Header.Get("x-upload-total-size"), 10, 64)
		chunks, _ := strconv.ParseInt(r.Header.Get("x-upload-total-chunks"), 10, 64)
		if platform == "" || filename == "" || size <= 0 || chunks <= 0 {
			w.WriteHeader(http.StatusUnprocessableEntity)
			writeTestJSON(w, map[string]any{"detail": "missing or invalid upload headers"})
			return
		}

		s.nextID++
		id := fmt.Sprintf("upload-%d", s.nextID)
		s.sessions[id] = &uploadSession{
			platform: platform, filename: filename,
			totalSize: size, totalChunks: chunks,
			chunks: map[int64][]byte{},
		}
		w.WriteHeader(http.StatusCreated)
		writeTestJSON(w, map[string]any{"upload_id": id})
	})

	mux.HandleFunc("/api/roms/upload/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/roms/upload/")
		parts := strings.Split(rest, "/")
		id := parts[0]

		s.mu.Lock()
		sess, ok := s.sessions[id]
		s.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			writeTestJSON(w, map[string]any{"detail": "Upload session not found or expired"})
			return
		}

		switch {
		case len(parts) == 1 && r.Method == http.MethodPut:
			s.handleChunk(w, r, id, sess)
		case len(parts) == 2 && parts[1] == "complete" && r.Method == http.MethodPost:
			s.handleComplete(w, id, sess)
		case len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost:
			s.mu.Lock()
			s.canceled[id] = true
			delete(s.sessions, id)
			s.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	return mux
}

func (s *chunkedUploadServer) handleChunk(w http.ResponseWriter, r *http.Request, id string, sess *uploadSession) {
	index, err := strconv.ParseInt(r.Header.Get("x-chunk-index"), 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
		writeTestJSON(w, map[string]any{"detail": "missing x-chunk-index"})
		return
	}
	body, _ := io.ReadAll(r.Body)

	s.mu.Lock()
	if index == int64(s.failChunkIndex) {
		s.failChunkIndex = -1 // fail once
		s.mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		writeTestJSON(w, map[string]any{"detail": "simulated chunk failure"})
		return
	}
	sess.chunks[index] = body
	received := len(sess.chunks)
	s.mu.Unlock()

	writeTestJSON(w, map[string]any{"received": received, "total": sess.totalChunks})
}

func (s *chunkedUploadServer) handleComplete(w http.ResponseWriter, id string, sess *uploadSession) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if int64(len(sess.chunks)) != sess.totalChunks {
		w.WriteHeader(http.StatusBadRequest)
		writeTestJSON(w, map[string]any{"detail": "not all chunks received"})
		return
	}

	var assembled bytes.Buffer
	for i := int64(0); i < sess.totalChunks; i++ {
		assembled.Write(sess.chunks[i])
	}
	s.completed[id] = assembled.Bytes()
	delete(s.sessions, id)

	// Confirmed real behaviour: 201 with an empty body.
	w.WriteHeader(http.StatusCreated)
}

func setupUploader(t *testing.T, srv *chunkedUploadServer, chunkSize int64) (*RommUploader, *httptest.Server) {
	t.Helper()
	testSrv := httptest.NewServer(srv.mux())
	t.Cleanup(testSrv.Close)

	report, err := romm.Probe(context.Background(), romm.ProbeOptions{BaseURL: testSrv.URL})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	client := romm.NewClient(testSrv.URL, "test-token", romm.AuthScheme{
		ID: "bearer",
		Apply: func(req *http.Request, token string) {
			req.Header.Set("Authorization", "Bearer "+token)
		},
	})

	return &RommUploader{Client: client, Report: report, ChunkSize: chunkSize}, testSrv
}

// platformsFor builds a *romm.Platforms without a network round trip, by
// wrapping a tiny local platforms endpoint. Kept minimal since RommUploader
// only calls Platforms.Lookup, not FetchPlatforms itself.
func platformsFor(t *testing.T, slugToID map[string]int) *romm.Platforms {
	t.Helper()
	var objs []map[string]any
	for slug, id := range slugToID {
		objs = append(objs, map[string]any{"id": id, "slug": slug, "name": slug})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{
			"openapi": "3.1.0", "info": map[string]any{"title": "x", "version": "1"},
			"paths": map[string]any{"/api/platforms": map[string]any{"get": map[string]any{"operationId": "p"}}},
		})
	})
	mux.HandleFunc("/api/platforms", func(w http.ResponseWriter, r *http.Request) { writeTestJSON(w, objs) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	report, err := romm.Probe(context.Background(), romm.ProbeOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	client := romm.NewClient(srv.URL, "", romm.AuthScheme{})
	platforms, err := romm.FetchPlatforms(context.Background(), client, report)
	if err != nil {
		t.Fatalf("FetchPlatforms: %v", err)
	}
	return platforms
}

func stagedFile(t *testing.T, size int) string {
	t.Helper()
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("generating payload: %v", err)
	}
	path := filepath.Join(t.TempDir(), "staged.part")
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatalf("writing staged file: %v", err)
	}
	return path
}

// TestPhase0_RommUploaderMatchesConfirmedRomMBehavior is the Phase 0 result of
// probing a live RomM 5.0.0 instance: the chunked upload flow, implemented
// against the exact confirmed shapes rather than the OpenAPI schema alone.
func TestPhase0_RommUploaderMatchesConfirmedRomMBehavior(t *testing.T) {
	fake := newChunkedUploadServer()
	uploader, _ := setupUploader(t, fake, 1<<20) // one chunk is enough for this payload
	uploader.Platforms = platformsFor(t, map[string]int{"gb": 11})

	payload := stagedFile(t, 32768)
	original, _ := os.ReadFile(payload)

	err := uploader.Upload(context.Background(), payload, "Uncatalogued (USA).gb", Expected{
		Platform:  protocol.PlatformGB,
		FileID:    protocol.FileIDFromCanonicalDigest(protocol.DigestBytes(original).SHA256),
		Canonical: protocol.DigestBytes(original),
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	if len(fake.completed) != 1 {
		t.Fatalf("%d sessions completed, want 1", len(fake.completed))
	}
	for _, assembled := range fake.completed {
		if !bytes.Equal(assembled, original) {
			t.Fatal("the assembled upload does not match the staged payload")
		}
	}
}

// The confirmed protocol requires the file be split correctly across several
// PUT calls when it exceeds one chunk, and reassembled in order.
func TestPhase0_MultiChunkUploadReassemblesInOrder(t *testing.T) {
	fake := newChunkedUploadServer()
	const chunkSize = 4096
	uploader, _ := setupUploader(t, fake, chunkSize)
	uploader.Platforms = platformsFor(t, map[string]int{"gb": 11})

	payload := stagedFile(t, chunkSize*3+777) // not a whole number of chunks
	original, _ := os.ReadFile(payload)

	err := uploader.Upload(context.Background(), payload, "Multi Chunk (USA).gb", Expected{
		Platform:  protocol.PlatformGB,
		FileID:    protocol.FileIDFromCanonicalDigest(protocol.DigestBytes(original).SHA256),
		Canonical: protocol.DigestBytes(original),
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	var assembled []byte
	for _, b := range fake.completed {
		assembled = b
	}
	if !bytes.Equal(assembled, original) {
		t.Fatal("a multi-chunk upload did not reassemble to the original payload")
	}
}

// TestPhase0_ChunkFailureCancelsTheSession is the confirmed-shape counterpart
// to a transfer that cannot complete: the server-side session must not be
// left dangling.
func TestPhase0_ChunkFailureCancelsTheSession(t *testing.T) {
	fake := newChunkedUploadServer()
	fake.failChunkIndex = 1
	uploader, _ := setupUploader(t, fake, 4096)
	uploader.Platforms = platformsFor(t, map[string]int{"gb": 11})

	payload := stagedFile(t, 4096*3)

	err := uploader.Upload(context.Background(), payload, "Fails (USA).gb", Expected{
		Platform:  protocol.PlatformGB,
		Canonical: protocol.Digest{SHA256: "x"},
		FileID:    protocol.FileIDFromCanonicalDigest("dead"),
	})
	if err == nil {
		t.Fatal("Upload succeeded despite a failing chunk")
	}
	if !strings.Contains(err.Error(), "simulated chunk failure") {
		t.Errorf("error does not surface the server's detail message: %v", err)
	}

	if len(fake.canceled) != 1 {
		t.Fatalf("%d sessions canceled, want 1", len(fake.canceled))
	}
	if len(fake.completed) != 0 {
		t.Fatal("a failed upload was recorded as completed")
	}
}

// An unknown platform slug must fail before any network call, not partway
// through an upload session that would then need cleaning up.
func TestUploadFailsFastOnUnknownPlatform(t *testing.T) {
	fake := newChunkedUploadServer()
	uploader, _ := setupUploader(t, fake, 4096)
	uploader.Platforms = platformsFor(t, map[string]int{"gb": 11})

	payload := stagedFile(t, 1024)
	err := uploader.Upload(context.Background(), payload, "x.n64", Expected{
		Platform:  "n64",
		Canonical: protocol.Digest{SHA256: "x"},
		FileID:    protocol.FileIDFromCanonicalDigest("dead"),
	})
	if err == nil {
		t.Fatal("Upload accepted a platform with no known RomM id")
	}
	if len(fake.sessions) != 0 {
		t.Fatal("a session was started despite the platform lookup failing")
	}
}

// TestErrorResponsesSurfaceRomMsDetailField matches the confirmed real error
// shape seen twice against a live server: {"detail": "..."}.
func TestErrorResponsesSurfaceRomMsDetailField(t *testing.T) {
	got := apiError(403, []byte(`{"detail":"Forbidden"}`))
	if !strings.Contains(got.Error(), "Forbidden") {
		t.Errorf("apiError did not surface the detail field: %v", got)
	}

	got = apiError(404, []byte(`{"detail":"Upload session not found or expired"}`))
	if !strings.Contains(got.Error(), "not found or expired") {
		t.Errorf("apiError did not surface the detail field: %v", got)
	}

	// A body with no detail field falls back to something readable rather than
	// silently dropping the response.
	got = apiError(500, []byte(`internal error, not json`))
	if !strings.Contains(got.Error(), "internal error") {
		t.Errorf("apiError dropped a non-JSON body: %v", got)
	}
}

// The confirmed real behaviour: a successful complete call returns an empty
// body. That must never be treated as a parse failure or an error.
func TestCompleteWithAnEmptyBodyIsSuccess(t *testing.T) {
	fake := newChunkedUploadServer()
	uploader, _ := setupUploader(t, fake, 1<<20)
	uploader.Platforms = platformsFor(t, map[string]int{"gb": 11})

	payload := stagedFile(t, 512)
	err := uploader.Upload(context.Background(), payload, "Empty Body Complete.gb", Expected{
		Platform:  protocol.PlatformGB,
		Canonical: protocol.Digest{SHA256: "x"},
		FileID:    protocol.FileIDFromCanonicalDigest("dead"),
	})
	if err != nil {
		t.Fatalf("an empty-but-successful complete response was treated as an error: %v", err)
	}
}

func TestUploaderRequiresItsDependencies(t *testing.T) {
	u := &RommUploader{}
	err := u.Upload(context.Background(), "x", "y", Expected{Platform: "gb"})
	if err == nil {
		t.Fatal("Upload succeeded with no Client, Report, or Platforms configured")
	}
}
