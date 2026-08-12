package adminui

import (
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/bridge/bridgeconfig"
	"github.com/Crimson3076/RomM-Swarm/bridge/ingest"
	"github.com/Crimson3076/RomM-Swarm/bridge/romm"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// fakeBackend implements Backend against a real bridgeconfig.FileStore and
// ingest.FileJournal (both already tested standalone), with a
// scriptable Connection/StartImport so tests can control connectivity
// without a real RomM server except where the test specifically wants one.
type fakeBackend struct {
	store   *bridgeconfig.FileStore
	journal *ingest.FileJournal

	conn         *romm.Connection
	reconnectErr error

	startImportCalls []startImportCall
	startImportErr   error
	startImportID    protocol.TransferID

	swarmStatus    SwarmStatus
	swarmStatusErr error

	joinSwarmCalls    []joinSwarmCall
	joinSwarmErr      error
	joinSwarmBridgeID protocol.BridgeID

	testSwarmResult auth.Result
	testSwarmErr    error
}

type joinSwarmCall struct {
	HostURL string
	Code    string
}

type startImportCall struct {
	LocalPath   string
	Platform    string
	DisplayName string
	Cleanup     func()
}

func newFakeBackend(t *testing.T) *fakeBackend {
	t.Helper()
	dir := t.TempDir()
	journal, err := ingest.NewFileJournal(filepath.Join(dir, "journal"))
	if err != nil {
		t.Fatalf("NewFileJournal: %v", err)
	}
	return &fakeBackend{
		store:   bridgeconfig.NewFileStore(filepath.Join(dir, "config.json")),
		journal: journal,
	}
}

func (f *fakeBackend) ConfigStore() *bridgeconfig.FileStore { return f.store }
func (f *fakeBackend) Connection() *romm.Connection         { return f.conn }
func (f *fakeBackend) Journal() *ingest.FileJournal         { return f.journal }

func (f *fakeBackend) Reconnect(ctx context.Context) error {
	if f.reconnectErr != nil {
		return f.reconnectErr
	}
	return nil
}

func (f *fakeBackend) StartImport(localPath, platformSlug, displayName string, timeout time.Duration, cleanup func()) (protocol.TransferID, error) {
	f.startImportCalls = append(f.startImportCalls, startImportCall{LocalPath: localPath, Platform: platformSlug, DisplayName: displayName, Cleanup: cleanup})
	if f.startImportErr != nil {
		return "", f.startImportErr
	}
	if cleanup != nil {
		cleanup() // simulate the background import finishing immediately, for test simplicity
	}
	id := f.startImportID
	if id == "" {
		id = protocol.NewTransferID()
	}
	return id, nil
}

func (f *fakeBackend) SwarmStatus() (SwarmStatus, error) {
	if f.swarmStatusErr != nil {
		return SwarmStatus{}, f.swarmStatusErr
	}
	return f.swarmStatus, nil
}

func (f *fakeBackend) JoinSwarm(ctx context.Context, hostURL, code string) (protocol.BridgeID, error) {
	f.joinSwarmCalls = append(f.joinSwarmCalls, joinSwarmCall{HostURL: hostURL, Code: code})
	if f.joinSwarmErr != nil {
		return "", f.joinSwarmErr
	}
	id := f.joinSwarmBridgeID
	if id == "" {
		id = "brg_test0000000000000000000000000"
	}
	f.swarmStatus = SwarmStatus{Joined: true, HostURL: hostURL, BridgeID: id, Generation: 1}
	return id, nil
}

func (f *fakeBackend) TestSwarmConnection(ctx context.Context) (auth.Result, error) {
	if f.testSwarmErr != nil {
		return auth.Result{}, f.testSwarmErr
	}
	return f.testSwarmResult, nil
}

func newTestServer(t *testing.T) (*Server, *fakeBackend) {
	t.Helper()
	backend := newFakeBackend(t)
	s := New(backend, "")
	return s, backend
}

// doRequest performs one request against s, following no redirects, and
// carrying cookies forward from prior calls when jar is non-nil.
func doRequest(t *testing.T, s *Server, jar http.CookieJar, method, path string, form url.Values) *http.Response {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
		Jar:           jar,
	}

	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, srv.URL+path, body)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func TestSetupRedirectsToLoginOnceConfigured(t *testing.T) {
	s, backend := newTestServer(t)
	if err := backend.store.Save(bridgeconfig.Config{RommURL: "https://x", RommToken: "t", AdminPasswordHash: "pbkdf2-sha256$1$00$00"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	resp := doRequest(t, s, nil, http.MethodGet, "/setup", nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("GET /setup on a configured Bridge: status %d, location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestDashboardRedirectsToSetupWhenUnconfigured(t *testing.T) {
	s, _ := newTestServer(t)
	resp := doRequest(t, s, nil, http.MethodGet, "/", nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/setup" {
		t.Fatalf("GET / unconfigured: status %d, location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// TestSetupSubmitCreatesConfigAndSession runs setup against a real fake RomM
// server (romm.Connect must succeed for setup to complete).
func TestSetupSubmitCreatesConfigAndSession(t *testing.T) {
	rommSrv := newMinimalFakeRomm(t)
	s, backend := newTestServer(t)

	jar := newJar(t)
	resp := doRequest(t, s, jar, http.MethodPost, "/setup", url.Values{
		"romm_url":         {rommSrv.URL},
		"romm_token":       {"tok"},
		"password":         {"a-long-enough-password"},
		"password_confirm": {"a-long-enough-password"},
	})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /setup: status %d, location %q, body %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}

	cfg, err := backend.store.Load()
	if err != nil {
		t.Fatalf("loading config after setup: %v", err)
	}
	if cfg.RommURL != rommSrv.URL || cfg.RommToken != "tok" || cfg.AdminPasswordHash == "" {
		t.Fatalf("config after setup = %+v, not fully populated", cfg)
	}

	// The session cookie from setup should already authenticate the dashboard.
	dash := doRequest(t, s, jar, http.MethodGet, "/", nil)
	if dash.StatusCode != http.StatusOK {
		t.Fatalf("dashboard after setup: status %d, want 200 (already authenticated)", dash.StatusCode)
	}
}

// TestSetupPreservesDestinationSettingsSeededByBootstrap covers a real
// Docker path: cmd/bridge's Bootstrap seeds RommURL/RommToken plus
// BRIDGE_DESTINATION_MODE/BRIDGE_LIBRARY_ROOT from the environment but
// leaves AdminPasswordHash empty, so the operator still lands on /setup.
// Completing setup must not wipe out the destination settings bootstrap
// already persisted.
func TestSetupPreservesDestinationSettingsSeededByBootstrap(t *testing.T) {
	rommSrv := newMinimalFakeRomm(t)
	s, backend := newTestServer(t)

	if err := backend.store.Save(bridgeconfig.Config{
		RommURL:         rommSrv.URL,
		RommToken:       "seeded-token",
		DestinationMode: protocol.ModeAPIOnly,
		LibraryRoot:     "/library",
	}); err != nil {
		t.Fatalf("seeding a bootstrap-shaped config: %v", err)
	}

	resp := doRequest(t, s, nil, http.MethodPost, "/setup", url.Values{
		"romm_url":         {rommSrv.URL},
		"romm_token":       {"tok"},
		"password":         {"a-long-enough-password"},
		"password_confirm": {"a-long-enough-password"},
	})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /setup: status %d, location %q, body %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}

	cfg, err := backend.store.Load()
	if err != nil {
		t.Fatalf("loading config after setup: %v", err)
	}
	if cfg.DestinationMode != protocol.ModeAPIOnly || cfg.LibraryRoot != "/library" {
		t.Fatalf("setup wiped bootstrap-seeded destination settings: %+v", cfg)
	}
}

func TestSetupRejectsAMismatchedPasswordConfirmation(t *testing.T) {
	rommSrv := newMinimalFakeRomm(t)
	s, backend := newTestServer(t)

	resp := doRequest(t, s, nil, http.MethodPost, "/setup", url.Values{
		"romm_url":         {rommSrv.URL},
		"romm_token":       {"tok"},
		"password":         {"a-long-enough-password"},
		"password_confirm": {"does-not-match"},
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("mismatched confirmation: status %d, want 422", resp.StatusCode)
	}
	if cfg, _ := backend.store.Load(); cfg.AdminPasswordHash != "" {
		t.Fatal("a rejected setup attempt still saved a password")
	}
}

func TestLoginAndLogout(t *testing.T) {
	s, backend := newTestServer(t)
	hash, err := hashPassword("the-real-password")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if err := backend.store.Save(bridgeconfig.Config{RommURL: "https://x", RommToken: "t", AdminPasswordHash: hash}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Wrong password.
	jar := newJar(t)
	bad := doRequest(t, s, jar, http.MethodPost, "/login", url.Values{"password": {"wrong"}})
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: status %d, want 401", bad.StatusCode)
	}

	// Right password.
	ok := doRequest(t, s, jar, http.MethodPost, "/login", url.Values{"password": {"the-real-password"}})
	if ok.StatusCode != http.StatusSeeOther {
		t.Fatalf("correct password: status %d, want 303", ok.StatusCode)
	}
	dash := doRequest(t, s, jar, http.MethodGet, "/", nil)
	if dash.StatusCode != http.StatusOK {
		t.Fatalf("dashboard after login: status %d, want 200", dash.StatusCode)
	}

	// Logout, then the dashboard should redirect again.
	doRequest(t, s, jar, http.MethodPost, "/logout", url.Values{})
	afterLogout := doRequest(t, s, jar, http.MethodGet, "/", nil)
	if afterLogout.StatusCode != http.StatusSeeOther || afterLogout.Header.Get("Location") != "/login" {
		t.Fatalf("dashboard after logout: status %d, location %q", afterLogout.StatusCode, afterLogout.Header.Get("Location"))
	}
}

func TestUnauthenticatedAPIRequestGets401NotARedirect(t *testing.T) {
	s, backend := newTestServer(t)
	hash, _ := hashPassword("pw")
	backend.store.Save(bridgeconfig.Config{RommURL: "https://x", RommToken: "t", AdminPasswordHash: hash})

	resp := doRequest(t, s, nil, http.MethodPost, "/api/connection/test", url.Values{})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API call: status %d, want 401", resp.StatusCode)
	}
}

func TestConnectionTestEndpoint(t *testing.T) {
	rommSrv := newMinimalFakeRomm(t)
	s, jar := loggedInServer(t, "https://placeholder", "placeholder-token")

	resp := doRequest(t, s, jar, http.MethodPost, "/api/connection/test", url.Values{
		"romm_url":   {rommSrv.URL},
		"romm_token": {"tok"},
	})
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("connection test: status %d, body %s", resp.StatusCode, body)
	}
	var parsed map[string]any
	json.NewDecoder(resp.Body).Decode(&parsed)
	if parsed["connected"] != true {
		t.Fatalf("connection test body = %v, want connected:true", parsed)
	}
}

func TestSwarmPageShowsStatusJoinsAndTestsConnection(t *testing.T) {
	s, backend := newTestServer(t)
	hash, err := hashPassword("pw")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if err := backend.store.Save(bridgeconfig.Config{RommURL: "https://x", RommToken: "t", AdminPasswordHash: hash}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	jar := newJar(t)
	if resp := doRequest(t, s, jar, http.MethodPost, "/login", url.Values{"password": {"pw"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: status %d", resp.StatusCode)
	}

	// Before joining, the page says so rather than showing stale/zero fields.
	page := doRequest(t, s, jar, http.MethodGet, "/swarm", nil)
	if page.StatusCode != http.StatusOK {
		t.Fatalf("GET /swarm: status %d", page.StatusCode)
	}
	body, _ := io.ReadAll(page.Body)
	if !strings.Contains(string(body), "has not joined a Swarm") {
		t.Fatalf("swarm page before joining did not say so: %s", body)
	}

	backend.joinSwarmBridgeID = "brg_test0000000000000000000000000"
	join := doRequest(t, s, jar, http.MethodPost, "/swarm", url.Values{
		"host_url":        {"https://host.example.com"},
		"invitation_code": {"the-code"},
	})
	if join.StatusCode != http.StatusOK {
		joinBody, _ := io.ReadAll(join.Body)
		t.Fatalf("POST /swarm: status %d, body %s", join.StatusCode, joinBody)
	}
	if len(backend.joinSwarmCalls) != 1 || backend.joinSwarmCalls[0].HostURL != "https://host.example.com" || backend.joinSwarmCalls[0].Code != "the-code" {
		t.Fatalf("JoinSwarm calls = %+v, want one call with the submitted host URL and code", backend.joinSwarmCalls)
	}
	joinBody, _ := io.ReadAll(join.Body)
	if !strings.Contains(string(joinBody), "Joined the Swarm.") || !strings.Contains(string(joinBody), "brg_test0000000000000000000000000") {
		t.Fatalf("swarm page after joining: %s", joinBody)
	}

	backend.testSwarmResult = auth.Result{Outcome: auth.OutcomeRotated, Generation: 2}
	test := doRequest(t, s, jar, http.MethodPost, "/api/swarm/test", url.Values{})
	if test.StatusCode != http.StatusOK {
		testBody, _ := io.ReadAll(test.Body)
		t.Fatalf("POST /api/swarm/test: status %d, body %s", test.StatusCode, testBody)
	}
	var parsed map[string]any
	json.NewDecoder(test.Body).Decode(&parsed)
	if parsed["connected"] != true || parsed["outcome"] != string(auth.OutcomeRotated) || parsed["generation"] != float64(2) {
		t.Fatalf("swarm test body = %v, want connected:true outcome:rotated generation:2", parsed)
	}
}

func TestImportFromUploadCleansUpTheTempFileButInboxFilesAreNeverTouched(t *testing.T) {
	s, backend := loggedInServerWithBackend(t, "https://x", "t")
	backend.conn = &romm.Connection{} // presence is all StartImport-gating in the fake needs

	// Upload path: a temp file the Bridge staged itself.
	var buf strings.Builder
	mw := multipart.NewWriter(&buf)
	mw.WriteField("source", "upload")
	mw.WriteField("platform", "gb")
	part, _ := mw.CreateFormFile("file", "Pokemon Ruby.gba")
	part.Write([]byte("fake rom bytes"))
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/import", strings.NewReader(buf.String()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: mustSessionToken(t, s)})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("upload import: status %d, body %s", rec.Code, rec.Body.String())
	}
	if len(backend.startImportCalls) != 1 {
		t.Fatalf("StartImport called %d times, want 1", len(backend.startImportCalls))
	}
	uploadedPath := backend.startImportCalls[0].LocalPath
	if _, err := os.Stat(uploadedPath); !os.IsNotExist(err) {
		t.Fatalf("the uploaded temp file %s was not cleaned up (err=%v)", uploadedPath, err)
	}
	// RomM must see the file's real name, not the randomly-generated temp
	// path it was staged under.
	if got := backend.startImportCalls[0].DisplayName; got != "Pokemon Ruby.gba" {
		t.Fatalf("upload import passed display name %q, want the original filename", got)
	}

	// Inbox path: must never be deleted, even though the fake "completes"
	// the import synchronously the same way the upload case does above.
	inboxDir := t.TempDir()
	inboxFile := filepath.Join(inboxDir, "existing.gb")
	if err := os.WriteFile(inboxFile, []byte("owner's own file"), 0o644); err != nil {
		t.Fatalf("seeding the inbox file: %v", err)
	}
	s.InboxDir = inboxDir

	form := url.Values{"source": {"inbox"}, "path": {"existing.gb"}, "platform": {"gb"}}
	req2 := httptest.NewRequest(http.MethodPost, "/api/import", strings.NewReader(form.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.AddCookie(&http.Cookie{Name: sessionCookieName, Value: mustSessionToken(t, s)})
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusSeeOther {
		t.Fatalf("inbox import: status %d, body %s", rec2.Code, rec2.Body.String())
	}
	if _, err := os.Stat(inboxFile); err != nil {
		t.Fatalf("an inbox file was deleted after import: %v", err)
	}
}

// TestInboxPageListsAllConnectedRommPlatforms is a regression test: the
// platform dropdowns must reflect every platform the connected RomM server
// actually reports, not a hard-coded handful.
func TestInboxPageListsAllConnectedRommPlatforms(t *testing.T) {
	rommSrv := newMinimalFakeRomm(t)
	conn, err := romm.ConnectAndResolvePlatforms(context.Background(), rommSrv.URL, "tok")
	if err != nil {
		t.Fatalf("ConnectAndResolvePlatforms: %v", err)
	}

	s, backend := loggedInServerWithBackend(t, rommSrv.URL, "tok")
	backend.conn = conn

	req := httptest.NewRequest(http.MethodGet, "/inbox", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: mustSessionToken(t, s)})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /inbox: status %d, body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, slug := range []string{"gb", "gba", "gbc", "nds", "n64", "snes", "genesis", "psx"} {
		if !strings.Contains(body, `value="`+slug+`"`) {
			t.Errorf("inbox page is missing platform option %q: %s", slug, body)
		}
	}
}

func TestActivityDetailShowsHistory(t *testing.T) {
	s, backend := loggedInServerWithBackend(t, "https://x", "t")

	id := protocol.NewTransferID()
	if err := backend.journal.Append(id, ingest.Transition{To: protocol.StateReceiving, Detail: "receiving into staging"}); err != nil {
		t.Fatalf("seeding the journal: %v", err)
	}

	token := mustSessionToken(t, s)

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/activity?id="+string(id), nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	s.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("activity detail: status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "receiving into staging") {
		t.Fatalf("activity detail did not show the transition detail: %s", rec.Body.String())
	}
}

// --- test helpers ---

// newJar returns a real cookie jar, so a sequence of doRequest calls against
// distinct httptest.Server URLs (doRequest spins up a fresh listener per
// call, sharing the same underlying *Server/session store) still carries the
// session cookie forward the way a browser would.
func newJar(t *testing.T) http.CookieJar {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	return jar
}

// loggedInServer returns a Server already configured and authenticated,
// with a real, live-cookie-jar client identity.
func loggedInServer(t *testing.T, rommURL, rommToken string) (*Server, http.CookieJar) {
	t.Helper()
	s, backend := newTestServer(t)
	hash, err := hashPassword("pw")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if err := backend.store.Save(bridgeconfig.Config{RommURL: rommURL, RommToken: rommToken, AdminPasswordHash: hash}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	jar := newJar(t)
	resp := doRequest(t, s, jar, http.MethodPost, "/login", url.Values{"password": {"pw"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login during test setup: status %d", resp.StatusCode)
	}
	return s, jar
}

func loggedInServerWithBackend(t *testing.T, rommURL, rommToken string) (*Server, *fakeBackend) {
	t.Helper()
	s, backend := newTestServer(t)
	hash, err := hashPassword("pw")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if err := backend.store.Save(bridgeconfig.Config{RommURL: rommURL, RommToken: rommToken, AdminPasswordHash: hash}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return s, backend
}

// mustSessionToken creates a session directly (bypassing HTTP login) for
// tests that build their own *http.Request rather than going through
// doRequest's client+jar.
func mustSessionToken(t *testing.T, s *Server) string {
	t.Helper()
	token, err := s.sessions.create()
	if err != nil {
		t.Fatalf("creating a session: %v", err)
	}
	return token
}

// newMinimalFakeRomm serves just enough of the confirmed real RomM protocol
// (ADR 0003) — an OpenAPI document declaring every capability romm.Connect
// requires, plus live handlers for each — for romm.Connect to succeed. It
// does not implement uploads or a roms listing: adminui tests that need
// StartImport to actually move bytes go through fakeBackend.StartImport
// instead, which is scripted rather than backed by a real transfer.
func newMinimalFakeRomm(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{
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
							{"name": "limit", "in": "query"}, {"name": "offset", "in": "query"},
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
		writeTestJSON(w, map[string]any{"VERSION": "5.0.0-fake"})
	})
	mux.HandleFunc("/api/users/me", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"id": 1, "username": "tester", "role": "admin"})
	})
	mux.HandleFunc("/api/platforms", func(w http.ResponseWriter, r *http.Request) {
		// More than the old hard-coded 5-option dropdown covered, so a
		// regression back to a hard-coded list would be caught here — RomM
		// itself reports dozens of platforms on a real instance.
		writeTestJSON(w, []map[string]any{
			{"id": 11, "slug": "gb", "name": "Game Boy"},
			{"id": 12, "slug": "gba", "name": "Game Boy Advance"},
			{"id": 13, "slug": "gbc", "name": "Game Boy Color"},
			{"id": 14, "slug": "nds", "name": "Nintendo DS"},
			{"id": 15, "slug": "n64", "name": "Nintendo 64"},
			{"id": 16, "slug": "snes", "name": "Super Nintendo Entertainment System"},
			{"id": 17, "slug": "genesis", "name": "Sega Genesis"},
			{"id": 18, "slug": "psx", "name": "PlayStation"},
		})
	})
	mux.HandleFunc("/api/roms", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{"items": []any{}, "total": 0})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
