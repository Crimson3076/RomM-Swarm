package hostui_test

import (
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

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	dsn := hoststoretest.SkipWithoutPostgres(t)
	db := hoststoretest.OpenDB(t, dsn)
	srv := httptest.NewServer(hostui.New(directory.New(db)))
	t.Cleanup(srv.Close)
	return srv
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
	srv := newTestServer(t)
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
	srv := newTestServer(t)
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
	srv := newTestServer(t)
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

func readAll(resp *http.Response) (string, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}
