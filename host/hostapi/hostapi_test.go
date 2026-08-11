package hostapi_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
	"github.com/Crimson3076/RomM-Swarm/host/hostapi"
	"github.com/Crimson3076/RomM-Swarm/host/hoststore/hoststoretest"
)

func newTestServer(t *testing.T) *hostapi.Server {
	t.Helper()
	dsn := hoststoretest.SkipWithoutPostgres(t)
	db := hoststoretest.OpenDB(t, dsn)
	return hostapi.New(directory.New(db))
}

func doJSON(t *testing.T, s *hostapi.Server, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encoding request body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding response body %q: %v", rec.Body.String(), err)
	}
	return out
}

// TestPhase2_FullHappyPath walks bootstrap -> login -> create Swarm ->
// issue invitation -> enroll a Bridge -> rotate its credential -> revoke
// it, the same sequence a real operator and a real Bridge would go
// through.
func TestPhase2_FullHappyPath(t *testing.T) {
	s := newTestServer(t)

	setup := doJSON(t, s, http.MethodPost, "/api/setup", "", map[string]string{
		"username": "owner", "display_name": "The Owner", "password": "a-long-enough-password",
	})
	if setup.Code != http.StatusCreated {
		t.Fatalf("POST /api/setup: status %d, body %s", setup.Code, setup.Body.String())
	}

	login := doJSON(t, s, http.MethodPost, "/api/login", "", map[string]string{
		"username": "owner", "password": "a-long-enough-password",
	})
	if login.Code != http.StatusOK {
		t.Fatalf("POST /api/login: status %d, body %s", login.Code, login.Body.String())
	}
	token, _ := decodeBody(t, login)["token"].(string)
	if token == "" {
		t.Fatal("login response had no token")
	}

	createSwarm := doJSON(t, s, http.MethodPost, "/api/swarms", token, map[string]string{"name": "Test Swarm"})
	if createSwarm.Code != http.StatusCreated {
		t.Fatalf("POST /api/swarms: status %d, body %s", createSwarm.Code, createSwarm.Body.String())
	}
	swarmID, _ := decodeBody(t, createSwarm)["swarm_id"].(string)

	list := doJSON(t, s, http.MethodGet, "/api/swarms", token, nil)
	if list.Code != http.StatusOK {
		t.Fatalf("GET /api/swarms: status %d, body %s", list.Code, list.Body.String())
	}

	issue := doJSON(t, s, http.MethodPost, "/api/swarms/"+swarmID+"/invitations", token, nil)
	if issue.Code != http.StatusCreated {
		t.Fatalf("POST /api/swarms/{id}/invitations: status %d, body %s", issue.Code, issue.Body.String())
	}
	code, _ := decodeBody(t, issue)["code"].(string)
	if code == "" {
		t.Fatal("issue-invitation response had no code")
	}

	publicKey := base64.StdEncoding.EncodeToString([]byte("a-test-bridge-public-key"))
	enroll := doJSON(t, s, http.MethodPost, "/api/bridges/enroll", "", map[string]string{
		"code": code, "public_key": publicKey,
	})
	if enroll.Code != http.StatusCreated {
		t.Fatalf("POST /api/bridges/enroll: status %d, body %s", enroll.Code, enroll.Body.String())
	}
	enrollBody := decodeBody(t, enroll)
	bridgeID, _ := enrollBody["bridge_id"].(string)
	refreshToken, _ := enrollBody["refresh_token"].(string)
	if bridgeID == "" || refreshToken == "" {
		t.Fatalf("enroll response missing bridge_id or refresh_token: %+v", enrollBody)
	}

	rotate := doJSON(t, s, http.MethodPost, "/api/bridges/"+bridgeID+"/rotate", "", map[string]string{
		"refresh_token": refreshToken,
	})
	if rotate.Code != http.StatusOK {
		t.Fatalf("POST /api/bridges/{id}/rotate: status %d, body %s", rotate.Code, rotate.Body.String())
	}
	rotateBody := decodeBody(t, rotate)
	if rotateBody["outcome"] != "rotated" {
		t.Fatalf("rotate outcome = %v, want \"rotated\"", rotateBody["outcome"])
	}

	revoke := doJSON(t, s, http.MethodPost, "/api/bridges/"+bridgeID+"/revoke", token, nil)
	if revoke.Code != http.StatusNoContent {
		t.Fatalf("POST /api/bridges/{id}/revoke: status %d, body %s", revoke.Code, revoke.Body.String())
	}

	newRefresh, _ := rotateBody["refresh_token"].(string)
	afterRevoke := doJSON(t, s, http.MethodPost, "/api/bridges/"+bridgeID+"/rotate", "", map[string]string{
		"refresh_token": newRefresh,
	})
	if afterRevoke.Code != http.StatusForbidden {
		t.Fatalf("rotate after revoke: status %d, want 403", afterRevoke.Code)
	}
}

func TestPhase2_AuthRealmBoundary(t *testing.T) {
	s := newTestServer(t)

	// Owner-only endpoints reject a request with no bearer token.
	for _, req := range []struct{ method, path string }{
		{http.MethodPost, "/api/swarms"},
		{http.MethodGet, "/api/swarms"},
	} {
		rec := doJSON(t, s, req.method, req.path, "", map[string]string{"name": "x"})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with no token: status %d, want 401", req.method, req.path, rec.Code)
		}
	}

	// Bridge enroll/rotate never require a bearer token — a bad request
	// body is a 400/422, never a 401, proving the handler doesn't sit
	// behind requireOwnerAuth.
	enroll := doJSON(t, s, http.MethodPost, "/api/bridges/enroll", "", map[string]string{
		"code": "not-a-real-code", "public_key": base64.StdEncoding.EncodeToString([]byte("key")),
	})
	if enroll.Code == http.StatusUnauthorized {
		t.Fatal("POST /api/bridges/enroll rejected an unauthenticated request with 401 — it must not require owner auth")
	}
}

func TestPhase2_SetupThenSetupAgainConflicts(t *testing.T) {
	s := newTestServer(t)
	first := doJSON(t, s, http.MethodPost, "/api/setup", "", map[string]string{
		"username": "owner", "display_name": "The Owner", "password": "a-long-enough-password",
	})
	if first.Code != http.StatusCreated {
		t.Fatalf("first setup: status %d, body %s", first.Code, first.Body.String())
	}
	second := doJSON(t, s, http.MethodPost, "/api/setup", "", map[string]string{
		"username": "someone-else", "display_name": "Someone Else", "password": "another-long-password",
	})
	if second.Code != http.StatusConflict {
		t.Fatalf("second setup: status %d, want 409", second.Code)
	}
}
