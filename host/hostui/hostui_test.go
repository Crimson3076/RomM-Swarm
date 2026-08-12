package hostui_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
	"github.com/Crimson3076/RomM-Swarm/host/hoststore/hoststoretest"
	"github.com/Crimson3076/RomM-Swarm/host/hostui"
)

func newTestServer(t *testing.T) (*httptest.Server, *directory.Directory) {
	t.Helper()
	dsn := hoststoretest.SkipWithoutPostgres(t)
	db := hoststoretest.OpenDB(t, dsn)
	dir := directory.New(db)
	srv := httptest.NewServer(hostui.New(dir))
	t.Cleanup(srv.Close)
	return srv, dir
}

func newClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	return &http.Client{
		Jar:           jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func TestPhase2_SetupRedirectsToLoginOnceConfigured(t *testing.T) {
	srv, _ := newTestServer(t)
	client := newClient(t)

	// First /setup: unconfigured, shows the form (200).
	resp, err := client.Get(srv.URL + "/setup")
	if err != nil {
		t.Fatalf("GET /setup: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /setup before bootstrap: status %d, want 200", resp.StatusCode)
	}

	form := url.Values{
		"username": {"owner"}, "display_name": {"The Owner"},
		"password": {"a-long-enough-password"}, "password_confirm": {"a-long-enough-password"},
	}
	resp, err = client.PostForm(srv.URL+"/setup", form)
	if err != nil {
		t.Fatalf("POST /setup: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("POST /setup: status %d, location %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// A second GET /setup, now that an owner exists, redirects to /login.
	client2 := newClient(t)
	resp, err = client2.Get(srv.URL + "/setup")
	if err != nil {
		t.Fatalf("second GET /setup: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("GET /setup once configured: status %d, location %q, want redirect to /login", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestPhase2_FullBrowserHappyPathSetupLoginEmptyDashboard(t *testing.T) {
	srv, _ := newTestServer(t)
	client := newClient(t)

	form := url.Values{
		"username": {"owner"}, "display_name": {"The Owner"},
		"password": {"a-long-enough-password"}, "password_confirm": {"a-long-enough-password"},
	}
	resp, err := client.PostForm(srv.URL+"/setup", form)
	if err != nil {
		t.Fatalf("POST /setup: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /setup: status %d", resp.StatusCode)
	}

	// The session cookie from setup should already authenticate the dashboard.
	resp, err = client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / after setup: status %d, want 200 (already authenticated)", resp.StatusCode)
	}
	body, _ := readAll(resp)
	if !strings.Contains(body, "No Swarms yet") {
		t.Fatalf("dashboard did not show the empty-Swarms state: %s", body)
	}

	// Log out, then a fresh client must be redirected to /login.
	if _, err := client.Post(srv.URL+"/logout", "application/x-www-form-urlencoded", nil); err != nil {
		t.Fatalf("POST /logout: %v", err)
	}
	resp, err = client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET / after logout: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("GET / after logout: status %d, location %q, want redirect to /login", resp.StatusCode, resp.Header.Get("Location"))
	}

	// Logging back in via the login form (not /setup) reaches the
	// dashboard again.
	client2 := newClient(t)
	loginResp, err := client2.PostForm(srv.URL+"/login", url.Values{"username": {"owner"}, "password": {"a-long-enough-password"}})
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	if loginResp.StatusCode != http.StatusSeeOther || loginResp.Header.Get("Location") != "/" {
		t.Fatalf("POST /login: status %d, location %q", loginResp.StatusCode, loginResp.Header.Get("Location"))
	}
	dashResp, err := client2.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET / after login: %v", err)
	}
	if dashResp.StatusCode != http.StatusOK {
		t.Fatalf("GET / after login: status %d, want 200", dashResp.StatusCode)
	}
}

func TestPhase2_LoginRejectsWrongPassword(t *testing.T) {
	srv, _ := newTestServer(t)
	client := newClient(t)

	form := url.Values{
		"username": {"owner"}, "display_name": {"The Owner"},
		"password": {"the-real-password"}, "password_confirm": {"the-real-password"},
	}
	if _, err := client.PostForm(srv.URL+"/setup", form); err != nil {
		t.Fatalf("POST /setup: %v", err)
	}

	client2 := newClient(t)
	resp, err := client2.PostForm(srv.URL+"/login", url.Values{"username": {"owner"}, "password": {"wrong-password"}})
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /login with the wrong password: status %d, want 401", resp.StatusCode)
	}
}

func TestPhase2_SwarmLifecycleCreateInviteRevokeReenroll(t *testing.T) {
	srv, dir := newTestServer(t)
	client := newClient(t)

	form := url.Values{
		"username": {"owner"}, "display_name": {"The Owner"},
		"password": {"a-long-enough-password"}, "password_confirm": {"a-long-enough-password"},
	}
	if _, err := client.PostForm(srv.URL+"/setup", form); err != nil {
		t.Fatalf("POST /setup: %v", err)
	}

	resp, err := client.PostForm(srv.URL+"/swarms", url.Values{"name": {"Test Swarm"}})
	if err != nil {
		t.Fatalf("POST /swarms: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /swarms: status %d", resp.StatusCode)
	}
	swarmPath := resp.Header.Get("Location")
	if !strings.HasPrefix(swarmPath, "/swarms/") {
		t.Fatalf("POST /swarms: unexpected redirect location %q", swarmPath)
	}

	resp, err = client.Get(srv.URL + swarmPath)
	if err != nil {
		t.Fatalf("GET %s: %v", swarmPath, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", swarmPath, resp.StatusCode)
	}
	body, _ := readAll(resp)
	if !strings.Contains(body, "Test Swarm") {
		t.Fatalf("Swarm page did not show the Swarm name: %s", body)
	}

	// Issuing an invitation shows the code exactly once.
	resp, err = client.PostForm(srv.URL+swarmPath+"/invitations", nil)
	if err != nil {
		t.Fatalf("POST %s/invitations: %v", swarmPath, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s/invitations: status %d", swarmPath, resp.StatusCode)
	}
	body, _ = readAll(resp)
	if !strings.Contains(body, "will not be shown again") {
		t.Fatalf("invitation page missing one-time warning: %s", body)
	}
	code := extractCodeDisplay(t, body)

	// Redeem the invitation directly against the Directory to enroll a
	// real test Bridge -- the same convention host/hostapi's own tests
	// use, since bridge/hostclient isn't needed to exercise this UI.
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	bridgeID, _, _, _, err := dir.RedeemInvitation(context.Background(), directory.InvitationCode(code), pub)
	if err != nil {
		t.Fatalf("RedeemInvitation: %v", err)
	}

	resp, err = client.Get(srv.URL + swarmPath)
	if err != nil {
		t.Fatalf("GET %s: %v", swarmPath, err)
	}
	body, _ = readAll(resp)
	if !strings.Contains(body, string(bridgeID)) {
		t.Fatalf("Swarm page did not list the enrolled Bridge: %s", body)
	}

	// Name it, and confirm the name shows up on the Swarm page afterward.
	resp, err = client.PostForm(srv.URL+swarmPath+"/bridges/"+string(bridgeID)+"/name", url.Values{
		"display_name": {"Living Room Shelf"},
	})
	if err != nil {
		t.Fatalf("POST name: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST name: status %d", resp.StatusCode)
	}
	resp, err = client.Get(srv.URL + swarmPath)
	if err != nil {
		t.Fatalf("GET %s: %v", swarmPath, err)
	}
	body, _ = readAll(resp)
	if !strings.Contains(body, "Living Room Shelf") {
		t.Fatalf("Swarm page did not show the Bridge's display name: %s", body)
	}

	// Revoke it.
	resp, err = client.PostForm(srv.URL+swarmPath+"/bridges/"+string(bridgeID)+"/revoke", nil)
	if err != nil {
		t.Fatalf("POST revoke: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST revoke: status %d", resp.StatusCode)
	}

	resp, err = client.Get(srv.URL + swarmPath)
	if err != nil {
		t.Fatalf("GET %s: %v", swarmPath, err)
	}
	body, _ = readAll(resp)
	if !strings.Contains(body, "revoked") {
		t.Fatalf("Swarm page did not show the Bridge as revoked: %s", body)
	}

	// Re-enroll it: the owner-authorised escape from a revoked family.
	resp, err = client.PostForm(srv.URL+swarmPath+"/bridges/"+string(bridgeID)+"/reenroll", nil)
	if err != nil {
		t.Fatalf("POST reenroll: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST reenroll: status %d", resp.StatusCode)
	}
	body, _ = readAll(resp)
	if !strings.Contains(body, "will not be shown again") {
		t.Fatalf("reenroll page missing one-time warning: %s", body)
	}
}

func extractCodeDisplay(t *testing.T, body string) string {
	t.Helper()
	const marker = `class="code-display">`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("no code-display element found: %s", body)
	}
	rest := body[i+len(marker):]
	j := strings.Index(rest, "<")
	if j < 0 {
		t.Fatalf("malformed code-display element: %s", body)
	}
	return rest[:j]
}

func readAll(resp *http.Response) (string, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}
