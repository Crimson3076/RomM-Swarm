package hostui_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
	"github.com/Crimson3076/RomM-Swarm/host/hoststore/hoststoretest"
	"github.com/Crimson3076/RomM-Swarm/host/hostui"
	"github.com/Crimson3076/RomM-Swarm/protocol"
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

// testInventoryItem builds a minimal, verified-eligible Item — the same
// shape host/hostapi's own inventory tests use — so a manifest built from
// it passes protocol.Manifest.Validate().
func testInventoryItem(seed string, platform protocol.PlatformID, size int64) protocol.Item {
	sha := fmt.Sprintf("%064x", []byte(seed))
	return protocol.Item{
		FileID:         protocol.FileIDFromCanonicalDigest(sha),
		Platform:       platform,
		Canonical:      protocol.Digest{SHA256: sha, Size: size},
		Classification: protocol.ClassVerifiedEligible,
		Reference: &protocol.ReferenceMatch{
			Family: "test-family", SetName: "test-set", SetVersion: "1",
			EntryName: "Test Entry", CanonicalKey: "test-entry", Strength: protocol.StrengthStrong,
		},
		Adapter: protocol.AdapterRef{ID: "test-adapter", Version: "1"},
	}
}

// TestPhase2_SwarmViewShowsInventoryTotalsAfterAPublish proves the Swarm
// page's inventory stats (ADR 0019 Step 10) render real numbers once a
// Bridge has actually published, and say so plainly beforehand.
func TestPhase2_SwarmViewShowsInventoryTotalsAfterAPublish(t *testing.T) {
	srv, dir := newTestServer(t)
	client := newClient(t)

	form := url.Values{
		"username": {"owner"}, "display_name": {"The Owner"},
		"password": {"a-long-enough-password"}, "password_confirm": {"a-long-enough-password"},
	}
	if _, err := client.PostForm(srv.URL+"/setup", form); err != nil {
		t.Fatalf("POST /setup: %v", err)
	}

	resp, err := client.PostForm(srv.URL+"/swarms", url.Values{"name": {"Inventory Swarm"}})
	if err != nil {
		t.Fatalf("POST /swarms: %v", err)
	}
	swarmPath := resp.Header.Get("Location")

	// Before any publish, the page says so rather than showing zeros that
	// could be mistaken for "nothing was ever shared".
	resp, err = client.Get(srv.URL + swarmPath)
	if err != nil {
		t.Fatalf("GET %s: %v", swarmPath, err)
	}
	body, _ := readAll(resp)
	if !strings.Contains(body, "No Bridge has published its inventory") {
		t.Fatalf("Swarm page before any publish did not say so: %s", body)
	}

	inviteResp, err := client.PostForm(srv.URL+swarmPath+"/invitations", nil)
	if err != nil {
		t.Fatalf("POST invitations: %v", err)
	}
	inviteBody, _ := readAll(inviteResp)
	code := extractCodeDisplay(t, inviteBody)

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	bridgeID, swarmID, alias, token, err := dir.RedeemInvitation(context.Background(), directory.InvitationCode(code), pub)
	if err != nil {
		t.Fatalf("RedeemInvitation: %v", err)
	}

	manifest := protocol.Manifest{
		SchemaVersion: protocol.SchemaVersion,
		Swarm:         swarmID,
		Alias:         alias,
		Revision:      1,
		GeneratedAt:   time.Now().UTC(),
		Items:         []protocol.Item{testInventoryItem("swarm-view-item", protocol.PlatformGB, 100)},
	}
	if _, err := dir.PublishInventory(context.Background(), bridgeID, token, manifest, ""); err != nil {
		t.Fatalf("PublishInventory: %v", err)
	}

	resp, err = client.Get(srv.URL + swarmPath)
	if err != nil {
		t.Fatalf("GET %s: %v", swarmPath, err)
	}
	body, _ = readAll(resp)
	if strings.Contains(body, "No Bridge has published its inventory") {
		t.Fatalf("Swarm page still says nothing was published, after a real publish: %s", body)
	}
	if !strings.Contains(body, "1 distinct file(s) across 1 published holding(s), 0 held by more than one Bridge") {
		t.Fatalf("Swarm page did not show the correct inventory totals: %s", body)
	}
	if strings.Contains(body, "never") {
		t.Fatalf("Swarm page's Bridge row still says never published: %s", body)
	}
}

// TestPhase2_DeleteSwarmRequiresTheTypedNameToMatch proves the confirm-by-
// typing-the-name safety net actually blocks a mismatched submission
// server-side, not just via the page's disabled-button JS a raw POST
// bypasses entirely.
func TestPhase2_DeleteSwarmRequiresTheTypedNameToMatch(t *testing.T) {
	srv, _ := newTestServer(t)
	client := newClient(t)

	form := url.Values{
		"username": {"owner"}, "display_name": {"The Owner"},
		"password": {"a-long-enough-password"}, "password_confirm": {"a-long-enough-password"},
	}
	if _, err := client.PostForm(srv.URL+"/setup", form); err != nil {
		t.Fatalf("POST /setup: %v", err)
	}

	resp, err := client.PostForm(srv.URL+"/swarms", url.Values{"name": {"Precious Swarm"}})
	if err != nil {
		t.Fatalf("POST /swarms: %v", err)
	}
	swarmPath := resp.Header.Get("Location")

	// Wrong name: rejected, and the Swarm survives.
	resp, err = client.PostForm(srv.URL+swarmPath+"/delete", url.Values{"confirm_name": {"Not The Right Name"}})
	if err != nil {
		t.Fatalf("POST delete with a wrong name: %v", err)
	}
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST delete with a wrong name: status %d, want 422", resp.StatusCode)
	}
	body, _ := readAll(resp)
	if !strings.Contains(body, "did not match") {
		t.Fatalf("delete-with-wrong-name response did not explain why: %s", body)
	}

	stillThere, err := client.Get(srv.URL + swarmPath)
	if err != nil {
		t.Fatalf("GET %s: %v", swarmPath, err)
	}
	if stillThere.StatusCode != http.StatusOK {
		t.Fatalf("the Swarm was deleted despite a mismatched confirmation name: GET %s returned %d", swarmPath, stillThere.StatusCode)
	}

	// Correct name: deleted, redirected home, and gone for good.
	resp, err = client.PostForm(srv.URL+swarmPath+"/delete", url.Values{"confirm_name": {"Precious Swarm"}})
	if err != nil {
		t.Fatalf("POST delete with the right name: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("POST delete with the right name: status %d, location %q, want a redirect to /", resp.StatusCode, resp.Header.Get("Location"))
	}

	gone, err := client.Get(srv.URL + swarmPath)
	if err != nil {
		t.Fatalf("GET %s after deletion: %v", swarmPath, err)
	}
	if gone.StatusCode != http.StatusNotFound {
		t.Fatalf("GET %s after deletion: status %d, want 404", swarmPath, gone.StatusCode)
	}
}

// TestPhase2_DeleteInvitationRemovesItFromTheSwarmPage proves the delete
// button on an invitation row actually removes it.
func TestPhase2_DeleteInvitationRemovesItFromTheSwarmPage(t *testing.T) {
	srv, _ := newTestServer(t)
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
	swarmPath := resp.Header.Get("Location")

	inviteResp, err := client.PostForm(srv.URL+swarmPath+"/invitations", nil)
	if err != nil {
		t.Fatalf("POST invitations: %v", err)
	}
	inviteBody, _ := readAll(inviteResp)
	code := extractCodeDisplay(t, inviteBody)

	page, err := client.Get(srv.URL + swarmPath)
	if err != nil {
		t.Fatalf("GET %s: %v", swarmPath, err)
	}
	body, _ := readAll(page)
	invitationID := extractInvitationID(t, body)
	if !strings.Contains(body, invitationID) {
		t.Fatalf("Swarm page did not list the issued invitation: %s", body)
	}

	resp, err = client.PostForm(srv.URL+swarmPath+"/invitations/"+invitationID+"/delete", nil)
	if err != nil {
		t.Fatalf("POST delete invitation: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST delete invitation: status %d", resp.StatusCode)
	}

	after, err := client.Get(srv.URL + swarmPath)
	if err != nil {
		t.Fatalf("GET %s: %v", swarmPath, err)
	}
	afterBody, _ := readAll(after)
	if strings.Contains(afterBody, invitationID) {
		t.Fatalf("Swarm page still lists the deleted invitation: %s", afterBody)
	}
	if !strings.Contains(afterBody, "No invitations issued yet.") {
		t.Fatalf("Swarm page did not fall back to the empty-invitations message: %s", afterBody)
	}
	_ = code // the code itself is irrelevant once deletion is what's under test
}

// TestPhase2_RemoveBridgeOnlyOfferedAfterRevokeAndRemovesItFromTheSwarm
// proves the Remove button is gated on the Bridge's credential already
// being revoked, and that using it actually clears the Bridge off this
// Swarm's roster.
func TestPhase2_RemoveBridgeOnlyOfferedAfterRevokeAndRemovesItFromTheSwarm(t *testing.T) {
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
	swarmPath := resp.Header.Get("Location")

	inviteResp, err := client.PostForm(srv.URL+swarmPath+"/invitations", nil)
	if err != nil {
		t.Fatalf("POST invitations: %v", err)
	}
	inviteBody, _ := readAll(inviteResp)
	code := extractCodeDisplay(t, inviteBody)

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	bridgeID, _, _, _, err := dir.RedeemInvitation(context.Background(), directory.InvitationCode(code), pub)
	if err != nil {
		t.Fatalf("RedeemInvitation: %v", err)
	}

	removePath := swarmPath + "/bridges/" + string(bridgeID) + "/remove"

	// Not yet revoked: the button (and therefore the route's normal
	// entry point) isn't offered, but proving the gate is real means
	// checking the page doesn't render the form at all.
	beforeRevoke, err := client.Get(srv.URL + swarmPath)
	if err != nil {
		t.Fatalf("GET %s: %v", swarmPath, err)
	}
	beforeBody, _ := readAll(beforeRevoke)
	if strings.Contains(beforeBody, removePath) {
		t.Fatalf("Swarm page offered Remove before the Bridge was revoked: %s", beforeBody)
	}

	resp, err = client.PostForm(srv.URL+swarmPath+"/bridges/"+string(bridgeID)+"/revoke", nil)
	if err != nil {
		t.Fatalf("POST revoke: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST revoke: status %d", resp.StatusCode)
	}

	afterRevoke, err := client.Get(srv.URL + swarmPath)
	if err != nil {
		t.Fatalf("GET %s: %v", swarmPath, err)
	}
	afterRevokeBody, _ := readAll(afterRevoke)
	if !strings.Contains(afterRevokeBody, removePath) {
		t.Fatalf("Swarm page did not offer Remove once the Bridge was revoked: %s", afterRevokeBody)
	}

	resp, err = client.PostForm(srv.URL+removePath, nil)
	if err != nil {
		t.Fatalf("POST remove: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST remove: status %d", resp.StatusCode)
	}

	final, err := client.Get(srv.URL + swarmPath)
	if err != nil {
		t.Fatalf("GET %s: %v", swarmPath, err)
	}
	finalBody, _ := readAll(final)
	if strings.Contains(finalBody, string(bridgeID)) {
		t.Fatalf("Swarm page still lists the removed Bridge: %s", finalBody)
	}
	if !strings.Contains(finalBody, "No Bridges enrolled in this Swarm yet.") {
		t.Fatalf("Swarm page did not fall back to the empty-Bridges message: %s", finalBody)
	}
}

// extractInvitationID pulls the invitation ID out of the Swarm page's
// invitations table — the first table cell of the first data row,
// identified by its "inv_" prefix (protocol.InvitationID's own prefix).
func extractInvitationID(t *testing.T, body string) string {
	t.Helper()
	const marker = "<td>inv_"
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("no invitation ID found in the Swarm page: %s", body)
	}
	rest := body[i+len("<td>"):]
	j := strings.Index(rest, "<")
	if j < 0 {
		t.Fatalf("malformed invitation ID cell: %s", body)
	}
	return rest[:j]
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
