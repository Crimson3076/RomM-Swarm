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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/bridge/bridgeconfig"
	"github.com/Crimson3076/RomM-Swarm/bridge/ingest"
	"github.com/Crimson3076/RomM-Swarm/bridge/romm"
	"github.com/Crimson3076/RomM-Swarm/bridge/scan"
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

	publishInventoryResult InventoryPublishResult
	publishInventoryErr    error
	publishInventoryCalls  int
	// publishInventoryDelay, when set, is slept through before
	// PublishInventory resolves — lets a test observe the "running"
	// window (via PublishStatus) rather than only ever seeing the
	// instantly-finished state.
	publishInventoryDelay time.Duration

	publishStatusMu   sync.Mutex
	publishStatus     PublishStatus
	publishTriggering atomic.Bool

	library        []scan.ROMRecord
	libraryErr     error
	libraryCalls   int
	libraryRefresh []bool // one entry per call, recording forceRefresh
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

func (f *fakeBackend) PublishInventory(ctx context.Context) (InventoryPublishResult, error) {
	f.publishInventoryCalls++
	f.publishStatusMu.Lock()
	f.publishStatus = PublishStatus{Phase: PublishPhaseScanning, StartedAt: time.Now()}
	f.publishStatusMu.Unlock()

	if f.publishInventoryDelay > 0 {
		time.Sleep(f.publishInventoryDelay)
	}

	f.publishStatusMu.Lock()
	defer f.publishStatusMu.Unlock()
	if f.publishInventoryErr != nil {
		f.publishStatus = PublishStatus{Phase: PublishPhaseError, Err: f.publishInventoryErr.Error(), FinishedAt: time.Now()}
		return InventoryPublishResult{}, f.publishInventoryErr
	}
	f.publishStatus = PublishStatus{Phase: PublishPhaseDone, Result: f.publishInventoryResult, FinishedAt: time.Now()}
	return f.publishInventoryResult, nil
}

func (f *fakeBackend) PublishStatus() PublishStatus {
	f.publishStatusMu.Lock()
	defer f.publishStatusMu.Unlock()
	return f.publishStatus
}

func (f *fakeBackend) TriggerPublishInventory() PublishStatus {
	if f.publishTriggering.CompareAndSwap(false, true) {
		f.publishStatusMu.Lock()
		f.publishStatus = PublishStatus{Phase: PublishPhaseScanning, StartedAt: time.Now()}
		f.publishStatusMu.Unlock()
		go func() {
			defer f.publishTriggering.Store(false)
			f.PublishInventory(context.Background())
		}()
	}
	return f.PublishStatus()
}

func (f *fakeBackend) Library(ctx context.Context, forceRefresh bool) ([]scan.ROMRecord, error) {
	f.libraryCalls++
	f.libraryRefresh = append(f.libraryRefresh, forceRefresh)
	if f.libraryErr != nil {
		return nil, f.libraryErr
	}
	return f.library, nil
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

func TestPublishInventoryButtonPostsAndTheNewRevisionRendersOnThePage(t *testing.T) {
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

	backend.swarmStatus = SwarmStatus{Joined: true, HostURL: "https://host.example.com", BridgeID: "brg_test0000000000000000000000000"}

	page := doRequest(t, s, jar, http.MethodGet, "/swarm", nil)
	body, _ := io.ReadAll(page.Body)
	if !strings.Contains(string(body), "Never published.") {
		t.Fatalf("swarm page before any publish did not say so: %s", body)
	}
	if !strings.Contains(string(body), "publish-inventory") {
		t.Fatalf("swarm page did not render the Publish Inventory button: %s", body)
	}

	backend.publishInventoryResult = InventoryPublishResult{Published: true, ItemCount: 3, DistinctFiles: 5, Revision: 2}
	resp := doRequest(t, s, jar, http.MethodPost, "/api/swarm/publish-inventory", url.Values{})
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /api/swarm/publish-inventory: status %d, body %s", resp.StatusCode, respBody)
	}
	// The button no longer waits for the publish to finish — it starts it
	// in the background and returns whatever the current status is right
	// away. Poll the status endpoint for the real outcome.
	parsed := waitForPublishDone(t, s, jar)
	result, _ := parsed["result"].(map[string]any)
	if result["published"] != true || result["item_count"] != float64(3) || result["distinct_files"] != float64(5) || result["revision"] != float64(2) {
		t.Fatalf("publish status result = %v, want published:true item_count:3 distinct_files:5 revision:2", result)
	}
	if backend.publishInventoryCalls != 1 {
		t.Fatalf("PublishInventory was called %d times, want 1", backend.publishInventoryCalls)
	}

	// The page reflects the new revision once SwarmStatus (as cmd/bridge's
	// Daemon really would after a successful publish) reports it — the
	// button's own JSON response and the page's persisted status are two
	// independent paths to the same fact, and both must agree.
	backend.swarmStatus.LastPublishedRevision = 2
	backend.swarmStatus.LastPublishedAt = time.Now()
	page2 := doRequest(t, s, jar, http.MethodGet, "/swarm", nil)
	body2, _ := io.ReadAll(page2.Body)
	if !strings.Contains(string(body2), "revision 2") {
		t.Fatalf("swarm page after publish did not show the new revision: %s", body2)
	}
}

// TestPublishInventoryWithNothingToPublishIsAClearMessageNotA500 proves
// publish.ErrNothingToPublish's real, expected default state (no reference
// catalogue loaded yet) surfaces to the operator as a plain, readable
// result — not a 500, and not silently swallowed either.
func TestPublishInventoryWithNothingToPublishIsAClearMessageNotA500(t *testing.T) {
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
	backend.swarmStatus = SwarmStatus{Joined: true, HostURL: "https://host.example.com", BridgeID: "brg_test0000000000000000000000000"}
	backend.publishInventoryResult = InventoryPublishResult{
		Published:    false,
		SkippedCount: 4,
		SkipReasons:  map[string]int{"no reference catalogue is loaded for this platform, so nothing on it can be verified": 4},
	}

	resp := doRequest(t, s, jar, http.MethodPost, "/api/swarm/publish-inventory", url.Values{})
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /api/swarm/publish-inventory with nothing to publish: status %d, body %s", resp.StatusCode, respBody)
	}
	parsed := waitForPublishDone(t, s, jar)
	result, _ := parsed["result"].(map[string]any)
	if result["published"] != false {
		t.Fatalf("publish status result = %v, want published:false", result)
	}
	reasons, _ := result["skip_reasons"].(map[string]any)
	if len(reasons) == 0 {
		t.Fatalf("publish status result did not carry skip_reasons: %v", result)
	}
}

// waitForPublishDone polls GET /api/swarm/publish-status until running is
// false, then returns the decoded body — the async publish contract means
// a caller can no longer assume the POST that started it already carried
// the final outcome.
func waitForPublishDone(t *testing.T, s *Server, jar http.CookieJar) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp := doRequest(t, s, jar, http.MethodGet, "/api/swarm/publish-status", nil)
		var parsed map[string]any
		json.NewDecoder(resp.Body).Decode(&parsed)
		if parsed["running"] == false {
			return parsed
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the publish to finish")
	return nil
}

// loggedInSwarmReadyServer sets up a server past login, joined to a Swarm,
// with a delayed fake publish so tests can observe the "running" window
// instead of only ever seeing the instantly-finished state.
func loggedInSwarmReadyServer(t *testing.T, delay time.Duration) (*Server, *fakeBackend, http.CookieJar) {
	t.Helper()
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
	backend.swarmStatus = SwarmStatus{Joined: true, HostURL: "https://host.example.com", BridgeID: "brg_test0000000000000000000000000"}
	backend.publishInventoryDelay = delay
	return s, backend, jar
}

// TestPublishStatusEndpointReportsRunningWhileInProgress is the direct
// proof for the reported problem: while a publish is genuinely still
// working, the status endpoint must say so plainly (running:true), not
// leave the operator guessing whether it's hung.
func TestPublishStatusEndpointReportsRunningWhileInProgress(t *testing.T) {
	s, backend, jar := loggedInSwarmReadyServer(t, 300*time.Millisecond)
	backend.publishInventoryResult = InventoryPublishResult{Published: true, ItemCount: 1, Revision: 1}

	resp := doRequest(t, s, jar, http.MethodPost, "/api/swarm/publish-inventory", url.Values{})
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /api/swarm/publish-inventory: status %d, body %s", resp.StatusCode, body)
	}
	var started map[string]any
	json.NewDecoder(resp.Body).Decode(&started)
	if started["running"] != true {
		t.Fatalf("immediately after starting a publish, running = %v, want true", started["running"])
	}

	statusResp := doRequest(t, s, jar, http.MethodGet, "/api/swarm/publish-status", nil)
	var status map[string]any
	json.NewDecoder(statusResp.Body).Decode(&status)
	if status["running"] != true {
		t.Fatalf("GET /api/swarm/publish-status mid-flight: running = %v, want true", status["running"])
	}

	final := waitForPublishDone(t, s, jar)
	result, _ := final["result"].(map[string]any)
	if result["published"] != true || result["item_count"] != float64(1) {
		t.Fatalf("final publish status result = %v, want published:true item_count:1", result)
	}
}

// TestPublishInventoryButtonDoesNotStackRunsWhileOneIsInProgress proves
// clicking the button again while a publish is already running doesn't
// queue up a redundant second scan.
func TestPublishInventoryButtonDoesNotStackRunsWhileOneIsInProgress(t *testing.T) {
	s, backend, jar := loggedInSwarmReadyServer(t, 300*time.Millisecond)
	backend.publishInventoryResult = InventoryPublishResult{Published: true, ItemCount: 1, Revision: 1}

	first := doRequest(t, s, jar, http.MethodPost, "/api/swarm/publish-inventory", url.Values{})
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first POST /api/swarm/publish-inventory: status %d", first.StatusCode)
	}
	second := doRequest(t, s, jar, http.MethodPost, "/api/swarm/publish-inventory", url.Values{})
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second POST /api/swarm/publish-inventory: status %d", second.StatusCode)
	}

	waitForPublishDone(t, s, jar)

	if backend.publishInventoryCalls != 1 {
		t.Fatalf("PublishInventory was called %d time(s) across two overlapping button clicks, want exactly 1", backend.publishInventoryCalls)
	}
}

// TestSwarmPageRendersLiveStatusServerSideOnReload proves the status is
// real server-side state, not something only the JS remembers — a page
// reload mid-publish must show the current phase immediately, without
// waiting for a poll to land.
func TestSwarmPageRendersLiveStatusServerSideOnReload(t *testing.T) {
	s, backend, jar := loggedInSwarmReadyServer(t, 300*time.Millisecond)
	backend.publishInventoryResult = InventoryPublishResult{Published: true, ItemCount: 1, Revision: 1}

	if resp := doRequest(t, s, jar, http.MethodPost, "/api/swarm/publish-inventory", url.Values{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/swarm/publish-inventory: status %d", resp.StatusCode)
	}

	page := doRequest(t, s, jar, http.MethodGet, "/swarm", nil)
	body, _ := io.ReadAll(page.Body)
	if !strings.Contains(string(body), `data-running="true"`) {
		t.Fatalf("reloaded Swarm page did not show running=true mid-publish: %s", body)
	}
	if !strings.Contains(string(body), "Scanning") {
		t.Fatalf("reloaded Swarm page did not show the scanning status text: %s", body)
	}

	waitForPublishDone(t, s, jar)

	after := doRequest(t, s, jar, http.MethodGet, "/swarm", nil)
	afterBody, _ := io.ReadAll(after.Body)
	if !strings.Contains(string(afterBody), `data-running="false"`) {
		t.Fatalf("Swarm page after completion still shows running=true: %s", afterBody)
	}
	if !strings.Contains(string(afterBody), "Published revision 1") {
		t.Fatalf("Swarm page after completion did not show the outcome: %s", afterBody)
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

func fakeROMRecords(n int, platform string) []scan.ROMRecord {
	out := make([]scan.ROMRecord, n)
	for i := range out {
		out[i] = scan.ROMRecord{
			ID: strconv.Itoa(i), PlatformSlug: platform,
			FSName: "Game " + strconv.Itoa(i) + ".rom", FSSizeBytes: 1024,
		}
	}
	return out
}

// TestLibraryPageRendersAnEmptyShellWithoutCallingLibrary is the direct
// regression proof for the reported bug: the page must render instantly,
// even against a cold cache — which means it can't call Backend.Library
// (the potentially-slow RomM listing) at all. static/library.js is what
// fetches the first chunk, via GET /api/library/items, once the page has
// already loaded; that request is exercised separately below.
func TestLibraryPageRendersAnEmptyShellWithoutCallingLibrary(t *testing.T) {
	s, backend := loggedInServerWithBackend(t, "https://x", "t")
	backend.conn = &romm.Connection{}
	backend.library = fakeROMRecords(250, "gb")

	req := httptest.NewRequest(http.MethodGet, "/library", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: mustSessionToken(t, s)})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /library: status %d, body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "Download</a>") {
		t.Fatalf("GET /library rendered item rows server-side; it must render an empty shell only: %s", body)
	}
	if !strings.Contains(body, "Loading your library") {
		t.Fatalf("GET /library did not show a loading indicator: %s", body)
	}
	if backend.libraryCalls != 0 {
		t.Fatalf("GET /library called Backend.Library %d time(s), want 0 — the page must render before that fetch even starts", backend.libraryCalls)
	}
}

// TestLibraryItemsFirstChunkCarriesTotalsForTheLoadingJS proves the JSON
// endpoint — now also responsible for the very first chunk the page loads
// with, not just later scroll-triggered ones — reports total/scanned
// alongside the items, which static/library.js needs to render the status
// line the old server-rendered page used to fill in directly.
func TestLibraryItemsFirstChunkCarriesTotalsForTheLoadingJS(t *testing.T) {
	s, backend := loggedInServerWithBackend(t, "https://x", "t")
	backend.conn = &romm.Connection{}
	backend.library = fakeROMRecords(250, "gb")

	req := httptest.NewRequest(http.MethodGet, "/api/library/items?offset=0&limit=100", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: mustSessionToken(t, s)})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/library/items: status %d, body %s", rec.Code, rec.Body.String())
	}
	var parsed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	items, _ := parsed["items"].([]any)
	if len(items) != libraryPageSize || parsed["has_more"] != true {
		t.Fatalf("first chunk = %d item(s), has_more=%v, want %d item(s) and has_more=true", len(items), parsed["has_more"], libraryPageSize)
	}
	if parsed["total"] != float64(250) || parsed["scanned"] != float64(250) {
		t.Fatalf("total=%v scanned=%v, want both 250", parsed["total"], parsed["scanned"])
	}
	if backend.libraryCalls != 1 || backend.libraryRefresh[0] != false {
		t.Fatalf("Library was called %d time(s) with refresh %v, want one plain (non-forced) call", backend.libraryCalls, backend.libraryRefresh)
	}
}

// TestLibraryItemsEndpointServesTheRemainingChunks proves the
// infinite-scroll JSON endpoint actually returns the rest of the library
// past the first page, and reports has_more correctly at the boundary.
func TestLibraryItemsEndpointServesTheRemainingChunks(t *testing.T) {
	s, backend := loggedInServerWithBackend(t, "https://x", "t")
	backend.conn = &romm.Connection{}
	backend.library = fakeROMRecords(250, "gb")

	get := func(query string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/library/items?"+query, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: mustSessionToken(t, s)})
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/library/items?%s: status %d, body %s", query, rec.Code, rec.Body.String())
		}
		var parsed map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		return parsed
	}

	second := get("offset=100&limit=100")
	items, _ := second["items"].([]any)
	if len(items) != 100 || second["has_more"] != true {
		t.Fatalf("second chunk = %d item(s), has_more=%v, want 100 item(s) and has_more=true", len(items), second["has_more"])
	}

	third := get("offset=200&limit=100")
	items, _ = third["items"].([]any)
	if len(items) != 50 || third["has_more"] != false {
		t.Fatalf("third chunk = %d item(s), has_more=%v, want 50 item(s) and has_more=false", len(items), third["has_more"])
	}

	past := get("offset=300&limit=100")
	items, _ = past["items"].([]any)
	if len(items) != 0 || past["has_more"] != false {
		t.Fatalf("past-the-end chunk = %d item(s), has_more=%v, want 0 item(s) and has_more=false", len(items), past["has_more"])
	}
}

// TestLibraryItemsFiltersByPlatformBeforeCounting proves total/scanned in
// the JSON response reflect only the platform-filtered subset against the
// whole library's scanned count, not two copies of the same number.
func TestLibraryItemsFiltersByPlatformBeforeCounting(t *testing.T) {
	s, backend := loggedInServerWithBackend(t, "https://x", "t")
	backend.conn = &romm.Connection{}
	backend.library = append(fakeROMRecords(3, "gb"), fakeROMRecords(150, "gba")...)

	req := httptest.NewRequest(http.MethodGet, "/api/library/items?platform=gb&offset=0&limit=100", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: mustSessionToken(t, s)})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/library/items?platform=gb: status %d, body %s", rec.Code, rec.Body.String())
	}
	var parsed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	items, _ := parsed["items"].([]any)
	if len(items) != 3 || parsed["has_more"] != false {
		t.Fatalf("filtered JSON chunk = %d item(s), has_more=%v, want 3 item(s) and has_more=false", len(items), parsed["has_more"])
	}
	if parsed["total"] != float64(3) || parsed["scanned"] != float64(153) {
		t.Fatalf("total=%v scanned=%v, want 3 and 153", parsed["total"], parsed["scanned"])
	}
}

// TestLibraryPageDoesNotCallLibraryEvenWithRefreshInTheURL proves the page
// route itself never touches Backend.Library regardless of query
// parameters — refresh=1 only has an effect once static/library.js passes
// it through to its own GET /api/library/items call, tested next.
func TestLibraryPageDoesNotCallLibraryEvenWithRefreshInTheURL(t *testing.T) {
	s, backend := loggedInServerWithBackend(t, "https://x", "t")
	backend.conn = &romm.Connection{}
	backend.library = fakeROMRecords(1, "gb")

	req := httptest.NewRequest(http.MethodGet, "/library?refresh=1", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: mustSessionToken(t, s)})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /library?refresh=1: status %d, body %s", rec.Code, rec.Body.String())
	}
	if backend.libraryCalls != 0 {
		t.Fatalf("GET /library?refresh=1 called Backend.Library %d time(s), want 0", backend.libraryCalls)
	}
}

// TestLibraryItemsRefreshBypassesTheCache proves the JSON endpoint's own
// refresh=1 — what static/library.js sends on its first fetch when the
// page URL carries it — actually asks Backend.Library for a forced
// refresh, not a plain cached read.
func TestLibraryItemsRefreshBypassesTheCache(t *testing.T) {
	s, backend := loggedInServerWithBackend(t, "https://x", "t")
	backend.conn = &romm.Connection{}
	backend.library = fakeROMRecords(1, "gb")

	req := httptest.NewRequest(http.MethodGet, "/api/library/items?offset=0&limit=100&refresh=1", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: mustSessionToken(t, s)})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/library/items?refresh=1: status %d, body %s", rec.Code, rec.Body.String())
	}
	if backend.libraryCalls != 1 || backend.libraryRefresh[0] != true {
		t.Fatalf("Library was called %d time(s) with refresh %v, want one forced call", backend.libraryCalls, backend.libraryRefresh)
	}
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
