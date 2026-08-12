package hostapi_test

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
	"github.com/Crimson3076/RomM-Swarm/host/hostapi"
	"github.com/Crimson3076/RomM-Swarm/host/hoststore/hoststoretest"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

func validInventoryItem(seed string, platform protocol.PlatformID, size int64) protocol.Item {
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

// enrollTestBridge walks setup -> login -> create Swarm -> issue
// invitation -> enroll, the same sequence TestPhase2_FullHappyPath already
// proves works, and hands back everything a /inventory call needs.
func enrollTestBridge(t *testing.T, s *hostapi.Server) (swarmID, bridgeID, alias, refreshToken string) {
	t.Helper()

	setup := doJSON(t, s, http.MethodPost, "/api/setup", "", map[string]string{
		"username": "owner", "display_name": "The Owner", "password": "a-long-enough-password",
	})
	if setup.Code != http.StatusCreated {
		t.Fatalf("POST /api/setup: status %d, body %s", setup.Code, setup.Body.String())
	}
	login := doJSON(t, s, http.MethodPost, "/api/login", "", map[string]string{
		"username": "owner", "password": "a-long-enough-password",
	})
	token, _ := decodeBody(t, login)["token"].(string)

	createSwarm := doJSON(t, s, http.MethodPost, "/api/swarms", token, map[string]string{"name": "Test Swarm"})
	swarmID, _ = decodeBody(t, createSwarm)["swarm_id"].(string)

	issue := doJSON(t, s, http.MethodPost, "/api/swarms/"+swarmID+"/invitations", token, nil)
	code, _ := decodeBody(t, issue)["code"].(string)

	publicKey := base64.StdEncoding.EncodeToString([]byte("inventory-test-bridge-key"))
	enroll := doJSON(t, s, http.MethodPost, "/api/bridges/enroll", "", map[string]string{
		"code": code, "public_key": publicKey,
	})
	if enroll.Code != http.StatusCreated {
		t.Fatalf("POST /api/bridges/enroll: status %d, body %s", enroll.Code, enroll.Body.String())
	}
	body := decodeBody(t, enroll)
	bridgeID, _ = body["bridge_id"].(string)
	alias, _ = body["bridge_alias"].(string)
	refreshToken, _ = body["refresh_token"].(string)
	if swarmID == "" || bridgeID == "" || alias == "" || refreshToken == "" {
		t.Fatalf("enroll response missing a field: %+v", body)
	}
	return swarmID, bridgeID, alias, refreshToken
}

func TestPhase2_PublishInventoryHappyPathOverHTTP(t *testing.T) {
	s := newTestServer(t)
	swarmID, bridgeID, alias, refreshToken := enrollTestBridge(t, s)

	manifest := protocol.Manifest{
		SchemaVersion: protocol.SchemaVersion,
		Swarm:         protocol.SwarmID(swarmID),
		Alias:         protocol.BridgeAlias(alias),
		Revision:      1,
		GeneratedAt:   time.Now().UTC(),
		Items: []protocol.Item{
			validInventoryItem("item-a", protocol.PlatformGB, 100),
			validInventoryItem("item-b", protocol.PlatformGBA, 200),
		},
	}

	resp := doJSON(t, s, http.MethodPost, "/api/bridges/"+bridgeID+"/inventory", "", map[string]any{
		"refresh_token": refreshToken,
		"manifest":      manifest,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("POST /api/bridges/{id}/inventory: status %d, body %s", resp.Code, resp.Body.String())
	}
	body := decodeBody(t, resp)
	if body["item_count"] != float64(2) {
		t.Errorf("item_count = %v, want 2", body["item_count"])
	}
	if body["distinct_files"] != float64(2) {
		t.Errorf("distinct_files = %v, want 2", body["distinct_files"])
	}
	if body["revision"] != float64(1) {
		t.Errorf("revision = %v, want 1", body["revision"])
	}
}

// TestPhase2_PublishInventoryOverHTTPAppliesTheBridgePublishedName is the
// wire-level proof for ADR 0022's display_name field: it actually reaches
// Directory.PublishInventory and gets persisted, not just accepted and
// silently dropped by a JSON tag mismatch.
func TestPhase2_PublishInventoryOverHTTPAppliesTheBridgePublishedName(t *testing.T) {
	dsn := hoststoretest.SkipWithoutPostgres(t)
	db := hoststoretest.OpenDB(t, dsn)
	dir := directory.New(db)
	s := hostapi.New(dir)

	swarmID, bridgeID, alias, refreshToken := enrollTestBridge(t, s)

	manifest := protocol.Manifest{
		SchemaVersion: protocol.SchemaVersion,
		Swarm:         protocol.SwarmID(swarmID),
		Alias:         protocol.BridgeAlias(alias),
		Revision:      1,
		GeneratedAt:   time.Now().UTC(),
		Items:         []protocol.Item{validInventoryItem("item-a", protocol.PlatformGB, 100)},
	}

	resp := doJSON(t, s, http.MethodPost, "/api/bridges/"+bridgeID+"/inventory", "", map[string]any{
		"refresh_token": refreshToken,
		"manifest":      manifest,
		"display_name":  "Bridge's Own Name",
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("POST /api/bridges/{id}/inventory: status %d, body %s", resp.Code, resp.Body.String())
	}

	var name string
	if err := db.QueryRow(`SELECT display_name FROM bridge_swarm_memberships WHERE bridge_id = $1 AND swarm_id = $2`,
		bridgeID, swarmID).Scan(&name); err != nil {
		t.Fatalf("reading the persisted display name: %v", err)
	}
	if name != "Bridge's Own Name" {
		t.Fatalf("persisted display_name = %q, want %q", name, "Bridge's Own Name")
	}
}

// TestPhase2_SetBridgeDisplayNameOverHTTP is the wire-level proof for the
// new /display-name route: a Bridge that has never published any
// inventory (so /inventory alone could never carry a name — an empty
// manifest is refused before it ever leaves the Bridge) can still set its
// own name.
func TestPhase2_SetBridgeDisplayNameOverHTTP(t *testing.T) {
	dsn := hoststoretest.SkipWithoutPostgres(t)
	db := hoststoretest.OpenDB(t, dsn)
	dir := directory.New(db)
	s := hostapi.New(dir)

	swarmID, bridgeID, _, refreshToken := enrollTestBridge(t, s)

	resp := doJSON(t, s, http.MethodPost, "/api/bridges/"+bridgeID+"/display-name", "", map[string]any{
		"refresh_token": refreshToken,
		"swarm_id":      swarmID,
		"display_name":  "Dallas's RomM Bridge",
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("POST /api/bridges/{id}/display-name: status %d, body %s", resp.Code, resp.Body.String())
	}

	var name string
	if err := db.QueryRow(`SELECT display_name FROM bridge_swarm_memberships WHERE bridge_id = $1 AND swarm_id = $2`,
		bridgeID, swarmID).Scan(&name); err != nil {
		t.Fatalf("reading the persisted display name: %v", err)
	}
	if name != "Dallas's RomM Bridge" {
		t.Fatalf("persisted display_name = %q, want %q", name, "Dallas's RomM Bridge")
	}
}

func TestPhase2_SetBridgeDisplayNameOverHTTPRejectsABadToken(t *testing.T) {
	s := newTestServer(t)
	swarmID, bridgeID, _, _ := enrollTestBridge(t, s)

	resp := doJSON(t, s, http.MethodPost, "/api/bridges/"+bridgeID+"/display-name", "", map[string]any{
		"refresh_token": "not-the-real-token",
		"swarm_id":      swarmID,
		"display_name":  "Anyone's Bridge",
	})
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("POST /api/bridges/{id}/display-name with a bad token: status %d, body %s", resp.Code, resp.Body.String())
	}
}

func TestPhase2_PublishInventoryRejectsABadRefreshToken(t *testing.T) {
	s := newTestServer(t)
	swarmID, bridgeID, alias, _ := enrollTestBridge(t, s)

	manifest := protocol.Manifest{
		SchemaVersion: protocol.SchemaVersion,
		Swarm:         protocol.SwarmID(swarmID),
		Alias:         protocol.BridgeAlias(alias),
		Revision:      1,
		GeneratedAt:   time.Now().UTC(),
		Items:         []protocol.Item{validInventoryItem("item-a", protocol.PlatformGB, 100)},
	}

	resp := doJSON(t, s, http.MethodPost, "/api/bridges/"+bridgeID+"/inventory", "", map[string]any{
		"refresh_token": "not-the-real-token",
		"manifest":      manifest,
	})
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("POST /api/bridges/{id}/inventory with a bad token: status %d, body %s", resp.Code, resp.Body.String())
	}
}

func TestPhase2_PublishInventoryRejectsAnOversizedBody(t *testing.T) {
	s := newTestServer(t)
	s.MaxInventoryBytes = 256 // deliberately tiny, to make the limit reachable in a test
	swarmID, bridgeID, alias, refreshToken := enrollTestBridge(t, s)

	// Enough items that the encoded body clears the 256-byte cap.
	var items []protocol.Item
	for i := 0; i < 20; i++ {
		items = append(items, validInventoryItem(fmt.Sprintf("item-%d", i), protocol.PlatformGB, int64(i)))
	}
	manifest := protocol.Manifest{
		SchemaVersion: protocol.SchemaVersion,
		Swarm:         protocol.SwarmID(swarmID),
		Alias:         protocol.BridgeAlias(alias),
		Revision:      1,
		GeneratedAt:   time.Now().UTC(),
		Items:         items,
	}

	resp := doJSON(t, s, http.MethodPost, "/api/bridges/"+bridgeID+"/inventory", "", map[string]any{
		"refresh_token": refreshToken,
		"manifest":      manifest,
	})
	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST /api/bridges/{id}/inventory over the size cap: status %d, body %s", resp.Code, resp.Body.String())
	}
}

// TestPhase2_MaxBytesReaderRejectsARawOversizedBodyOnAnOrdinaryRoute proves
// the size cap isn't specific to /inventory — it protects every route,
// closing the gap that existed before ADR 0019.
func TestPhase2_MaxBytesReaderRejectsARawOversizedBodyOnAnOrdinaryRoute(t *testing.T) {
	s := newTestServer(t)

	// Must be syntactically valid JSON, or json.Decoder fails on the first
	// bad byte long before MaxBytesReader's limit is ever reached — that
	// would prove nothing about the size cap specifically.
	huge := `{"username": "owner", "display_name": "` + string(bytes.Repeat([]byte("x"), 2<<20)) + `", "password": "a-long-enough-password"}`
	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader([]byte(huge)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST /api/setup with an oversized raw body: status %d, body %s, want 413", rec.Code, rec.Body.String())
	}
}
