package hostclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// fakeHost reproduces host/hostapi's exact JSON shapes for /api/bridges/enroll
// and /api/bridges/{id}/rotate, without depending on host/hoststore or a
// real Postgres — this package must stay a plain unit-tested HTTP client.
type fakeHost struct {
	mux *http.ServeMux

	validCode      string
	enrolledBridge protocol.BridgeID
	currentToken   auth.Token

	// revoked, when true, makes rotate return 403 regardless of the token
	// presented.
	revoked bool
}

func newFakeHost() *fakeHost {
	f := &fakeHost{mux: http.NewServeMux()}
	f.mux.HandleFunc("POST /api/bridges/enroll", f.handleEnroll)
	f.mux.HandleFunc("POST /api/bridges/{bridgeID}/rotate", f.handleRotate)
	return f
}

func (f *fakeHost) ServeHTTP(w http.ResponseWriter, r *http.Request) { f.mux.ServeHTTP(w, r) }

func (f *fakeHost) handleEnroll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code      string `json:"code"`
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body")
		return
	}
	if req.Code != f.validCode {
		writeError(w, http.StatusUnprocessableEntity, "invitation is invalid, expired, or already used")
		return
	}
	key, err := base64.StdEncoding.DecodeString(req.PublicKey)
	if err != nil || len(key) == 0 {
		writeError(w, http.StatusBadRequest, "public_key must be base64-encoded and non-empty")
		return
	}
	f.enrolledBridge = protocol.BridgeIDFromPublicKey(key)
	f.currentToken = auth.NewToken()
	writeJSON(w, http.StatusCreated, map[string]string{
		"bridge_id":     string(f.enrolledBridge),
		"refresh_token": string(f.currentToken),
	})
}

func (f *fakeHost) handleRotate(w http.ResponseWriter, r *http.Request) {
	bridgeID := protocol.BridgeID(r.PathValue("bridgeID"))
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body")
		return
	}
	if f.revoked {
		writeError(w, http.StatusForbidden, "credential family has been revoked")
		return
	}
	if bridgeID != f.enrolledBridge || auth.Token(req.RefreshToken) != f.currentToken {
		writeError(w, http.StatusUnauthorized, "credential not recognised")
		return
	}
	f.currentToken = auth.NewToken()
	writeJSON(w, http.StatusOK, map[string]any{
		"refresh_token":     string(f.currentToken),
		"access_expires_at": time.Now().Add(5 * time.Minute),
		"outcome":           "rotated",
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func newTestKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a test key: %v", err)
	}
	return pub
}

func TestEnrollSucceedsAgainstAValidCode(t *testing.T) {
	fake := newFakeHost()
	fake.validCode = "the-real-code"
	srv := httptest.NewServer(fake)
	defer srv.Close()

	pub := newTestKey(t)
	result, err := New(srv.URL).Enroll(context.Background(), "the-real-code", pub)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if result.BridgeID != protocol.BridgeIDFromPublicKey(pub) {
		t.Fatalf("Enroll returned bridge id %s, want the one derived from the public key", result.BridgeID)
	}
	if result.Refresh == "" {
		t.Fatal("Enroll returned an empty refresh token")
	}
}

func TestEnrollRejectsAnInvalidCode(t *testing.T) {
	fake := newFakeHost()
	fake.validCode = "the-real-code"
	srv := httptest.NewServer(fake)
	defer srv.Close()

	_, err := New(srv.URL).Enroll(context.Background(), "a-guessed-code", newTestKey(t))
	if err != ErrInvitationInvalid {
		t.Fatalf("Enroll with a wrong code: err = %v, want ErrInvitationInvalid", err)
	}
}

func TestRotateFuncSucceedsAndRecoversGeneration(t *testing.T) {
	fake := newFakeHost()
	fake.validCode = "the-real-code"
	srv := httptest.NewServer(fake)
	defer srv.Close()

	client := New(srv.URL)
	pub := newTestKey(t)
	enrolled, err := client.Enroll(context.Background(), "the-real-code", pub)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	store := auth.NewFileStore(filepath.Join(t.TempDir(), "credential.json"))
	if err := store.Save(auth.Credential{BridgeID: enrolled.BridgeID, Refresh: enrolled.Refresh, Generation: 1}); err != nil {
		t.Fatalf("seeding the credential store: %v", err)
	}

	rotate := client.RotateFunc(store)

	result, err := rotate(enrolled.BridgeID, enrolled.Refresh)
	if err != nil {
		t.Fatalf("first rotate: %v", err)
	}
	if result.Generation != 2 {
		t.Fatalf("first rotate: Generation = %d, want 2", result.Generation)
	}
	if err := store.Save(auth.Credential{BridgeID: enrolled.BridgeID, Refresh: result.Refresh, Generation: result.Generation}); err != nil {
		t.Fatalf("persisting after first rotate: %v", err)
	}

	result2, err := rotate(enrolled.BridgeID, result.Refresh)
	if err != nil {
		t.Fatalf("second rotate: %v", err)
	}
	if result2.Generation != 3 {
		t.Fatalf("second rotate: Generation = %d, want 3", result2.Generation)
	}
}

func TestRotateFuncMapsUnauthorizedToTheSameAuthSentinelError(t *testing.T) {
	fake := newFakeHost()
	fake.validCode = "the-real-code"
	srv := httptest.NewServer(fake)
	defer srv.Close()

	client := New(srv.URL)
	pub := newTestKey(t)
	enrolled, err := client.Enroll(context.Background(), "the-real-code", pub)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	store := auth.NewFileStore(filepath.Join(t.TempDir(), "credential.json"))
	if err := store.Save(auth.Credential{BridgeID: enrolled.BridgeID, Refresh: "the-wrong-token", Generation: 1}); err != nil {
		t.Fatalf("seeding the credential store: %v", err)
	}

	_, err = client.RotateFunc(store)(enrolled.BridgeID, "the-wrong-token")
	if err != auth.ErrUnknownToken {
		t.Fatalf("rotate with a wrong token: err = %v, want auth.ErrUnknownToken", err)
	}
}

func TestRotateFuncMapsForbiddenToTheSameAuthSentinelError(t *testing.T) {
	fake := newFakeHost()
	fake.validCode = "the-real-code"
	fake.revoked = true
	srv := httptest.NewServer(fake)
	defer srv.Close()

	client := New(srv.URL)
	pub := newTestKey(t)
	enrolled, err := client.Enroll(context.Background(), "the-real-code", pub)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	store := auth.NewFileStore(filepath.Join(t.TempDir(), "credential.json"))
	if err := store.Save(auth.Credential{BridgeID: enrolled.BridgeID, Refresh: enrolled.Refresh, Generation: 1}); err != nil {
		t.Fatalf("seeding the credential store: %v", err)
	}

	_, err = client.RotateFunc(store)(enrolled.BridgeID, enrolled.Refresh)
	if err != auth.ErrFamilyRevoked {
		t.Fatalf("rotate against a revoked family: err = %v, want auth.ErrFamilyRevoked", err)
	}
}
