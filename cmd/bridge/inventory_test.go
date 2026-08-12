package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/bridge/bridgeconfig"
	"github.com/Crimson3076/RomM-Swarm/bridge/swarmconn"
	"github.com/Crimson3076/RomM-Swarm/internal/romfixture"
	"github.com/Crimson3076/RomM-Swarm/protocol"
	"github.com/Crimson3076/RomM-Swarm/reference"
)

// seedROM injects a rom directly into fakeBridgeServer's state, bypassing
// the chunked upload sequence — inventory tests only need content already
// present on the fake RomM server, not proof that uploading works (that's
// daemon_test.go's job). The upload-shaped bookkeeping (fakeUpload with a
// single whole-file chunk) is what the existing /content/{name} handler
// already knows how to serve, so no new route is needed.
func seedROM(fb *fakeBridgeServer, platformSlug, fsName string, payload []byte) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.nextRom++
	id := fb.nextRom
	sum := sha1.Sum(payload)
	uploadID := fmt.Sprintf("seed-%d", id)
	fb.uploads[uploadID] = &fakeUpload{
		filename:   fsName,
		platformID: fb.platform[platformSlug],
		chunks:     map[int64][]byte{0: payload},
	}
	fb.roms = append(fb.roms, map[string]any{
		"id": id, "platform_id": fb.platform[platformSlug], "platform_slug": platformSlug,
		"fs_name": fsName, "fs_size_bytes": len(payload), "sha1_hash": hex.EncodeToString(sum[:]),
	})
}

// buildInventoryTestDAT renders a minimal Logiqx catalogue with real
// hashes, matching the pattern used by bridge/scan's own tests and by
// reference/reference_test.go.
func buildInventoryTestDAT(entries map[string][]byte) []byte {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\"?>\n<datafile>\n")
	b.WriteString("  <header>\n    <name>Nintendo - Game Boy</name>\n" +
		"    <version>20260812-000000</version>\n  </header>\n")
	for name, payload := range entries {
		d := protocol.DigestBytes(payload)
		fmt.Fprintf(&b, "  <game name=%q>\n    <rom name=\"%s.gb\" size=\"%d\" crc=\"%s\" md5=\"%s\" sha1=\"%s\"/>\n  </game>\n",
			name, name, d.Size, strings.ToUpper(d.CRC32), strings.ToUpper(d.MD5), strings.ToUpper(d.SHA1))
	}
	b.WriteString("</datafile>\n")
	return []byte(b.String())
}

// fakeInventoryHost is a minimal stand-in for host/hostapi's
// POST /api/bridges/{bridgeID}/inventory route: enough to prove
// Daemon.PublishInventory builds the right request and handles the
// response, without spinning up a real Host.
type fakeInventoryHost struct {
	mu            sync.Mutex
	receivedCount int
	lastManifest  protocol.Manifest
	lastToken     string
}

func (h *fakeInventoryHost) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/bridges/{bridgeID}/inventory", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RefreshToken string            `json:"refresh_token"`
			Manifest     protocol.Manifest `json:"manifest"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		h.mu.Lock()
		h.receivedCount++
		h.lastManifest = req.Manifest
		h.lastToken = req.RefreshToken
		h.mu.Unlock()

		writeJSON(w, map[string]any{
			"item_count":     len(req.Manifest.Items),
			"revision":       uint64(req.Manifest.Revision),
			"published_at":   time.Now().UTC(),
			"distinct_files": len(req.Manifest.Items),
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// joinedDaemon builds a Daemon already connected to a fake RomM server and
// already holding a swarm-connection + credential, as if JoinSwarm had
// already run — the pieces PublishInventory needs but doesn't itself
// create.
func joinedDaemon(t *testing.T, rommURL, hostURL string) (*Daemon, protocol.SwarmID, protocol.BridgeID) {
	t.Helper()

	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	if err := d.ConfigStore().Save(bridgeconfig.Config{RommURL: rommURL, RommToken: "t"}); err != nil {
		t.Fatalf("saving the RomM config: %v", err)
	}
	if err := d.Reconnect(context.Background()); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating an identity key: %v", err)
	}
	swarmID := protocol.NewSwarmID()
	bridgeID := protocol.BridgeIDFromPublicKey(pub)
	alias := protocol.MustAliasFor(protocol.NewSwarmAliasKey(), swarmID, bridgeID)

	if err := d.swarmStore.Save(swarmconn.Config{
		HostURL:            hostURL,
		IdentityPrivateKey: priv,
		SwarmID:            swarmID,
		Alias:              alias,
	}); err != nil {
		t.Fatalf("saving the swarm connection: %v", err)
	}
	if err := d.credentialStore.Save(auth.Credential{
		SchemaVersion: protocol.SchemaVersion,
		BridgeID:      bridgeID,
		Refresh:       auth.Token("test-refresh-token"),
		Generation:    1,
	}); err != nil {
		t.Fatalf("saving the credential: %v", err)
	}

	return d, swarmID, bridgeID
}

// TestPhase2_PublishInventoryScansClassifiesAndPublishes is the full local
// path (ADR 0019 Step 7): a real scan against a fake RomM server, real
// classification against a loaded reference.Selection, real
// publish.Snapshot, and a real (fake) hostclient call — proving Daemon
// wiring, not re-proving bridge/scan or bridge/publish's own logic.
func TestPhase2_PublishInventoryScansClassifiesAndPublishes(t *testing.T) {
	fb := newFakeBridgeServer()
	payload := romfixture.GameBoy("PUBLISH TEST GAME", 65536, false)
	seedROM(fb, "gb", "Publish Test Game.gb", payload)
	rommSrv := fb.start(t)

	host := &fakeInventoryHost{}
	hostSrv := host.start(t)

	d, swarmID, bridgeID := joinedDaemon(t, rommSrv.URL, hostSrv.URL)
	if got, err := d.swarmStore.Load(); err != nil || got.BridgeID() != bridgeID {
		t.Fatalf("joinedDaemon's stored identity does not derive bridgeID %s (err %v)", bridgeID, err)
	}

	set, err := reference.ImportDAT(bytes.NewReader(buildInventoryTestDAT(map[string][]byte{
		"Publish Test Game": payload,
	})), reference.ImportOptions{Platform: protocol.PlatformGB})
	if err != nil {
		t.Fatalf("ImportDAT: %v", err)
	}
	d.referenceSelections[protocol.PlatformGB] = reference.DefaultProfile().Apply(set)

	result, err := d.PublishInventory(context.Background())
	if err != nil {
		t.Fatalf("PublishInventory: %v", err)
	}
	if !result.Published {
		t.Fatalf("Published = false, want true; SkipReasons: %+v", result.SkipReasons)
	}
	if result.ItemCount != 1 {
		t.Errorf("ItemCount = %d, want 1", result.ItemCount)
	}
	if result.DistinctFiles != 1 {
		t.Errorf("DistinctFiles = %d, want 1", result.DistinctFiles)
	}
	if result.Revision != 1 {
		t.Errorf("Revision = %d, want 1 (first publish)", result.Revision)
	}

	host.mu.Lock()
	defer host.mu.Unlock()
	if host.receivedCount != 1 {
		t.Fatalf("the fake Host received %d publish calls, want 1", host.receivedCount)
	}
	if host.lastToken != "test-refresh-token" {
		t.Errorf("the Host saw refresh token %q, want the stored credential", host.lastToken)
	}
	if host.lastManifest.Swarm != swarmID {
		t.Errorf("published manifest's Swarm = %s, want %s", host.lastManifest.Swarm, swarmID)
	}
	if len(host.lastManifest.Items) != 1 || host.lastManifest.Items[0].Canonical.SHA256 != protocol.DigestBytes(payload).SHA256 {
		t.Fatalf("published manifest does not carry the scanned item: %+v", host.lastManifest.Items)
	}

	// The new revision is persisted, so the next publish continues the
	// sequence rather than starting over.
	saved, err := d.swarmStore.Load()
	if err != nil {
		t.Fatalf("loading the swarm connection after publish: %v", err)
	}
	if saved.LastPublishedRevision != 1 {
		t.Errorf("persisted LastPublishedRevision = %d, want 1", saved.LastPublishedRevision)
	}
	if saved.LastPublishedAt.IsZero() {
		t.Error("persisted LastPublishedAt was not set")
	}
}

// TestPhase2_PublishInventoryWithNoReferenceCatalogueIsInformativeNotAnError
// proves the real default production state — a Bridge that has not loaded
// any DAT yet — surfaces as a clear, non-error result rather than either a
// hard failure or a silent no-op.
func TestPhase2_PublishInventoryWithNoReferenceCatalogueIsInformativeNotAnError(t *testing.T) {
	fb := newFakeBridgeServer()
	payload := romfixture.GameBoy("NO CATALOGUE GAME", 65536, false)
	seedROM(fb, "gb", "No Catalogue Game.gb", payload)
	rommSrv := fb.start(t)

	host := &fakeInventoryHost{}
	hostSrv := host.start(t)

	d, _, _ := joinedDaemon(t, rommSrv.URL, hostSrv.URL)
	// Deliberately no referenceSelections loaded for any platform.

	result, err := d.PublishInventory(context.Background())
	if err != nil {
		t.Fatalf("PublishInventory: %v", err)
	}
	if result.Published {
		t.Fatal("Published = true, want false: nothing was ever verified")
	}
	if result.SkippedCount == 0 {
		t.Error("SkippedCount = 0, want at least the one scanned-but-unverifiable holding")
	}
	if len(result.SkipReasons) == 0 {
		t.Error("SkipReasons is empty, want the reason no items were published")
	}

	host.mu.Lock()
	defer host.mu.Unlock()
	if host.receivedCount != 0 {
		t.Errorf("the fake Host received %d publish calls, want 0: nothing should have been sent", host.receivedCount)
	}

	saved, err := d.swarmStore.Load()
	if err != nil {
		t.Fatalf("loading the swarm connection: %v", err)
	}
	if saved.LastPublishedRevision != 0 {
		t.Errorf("LastPublishedRevision = %d, want 0: nothing was published", saved.LastPublishedRevision)
	}
}

// TestPhase2_PublishInventoryFailsFastWithoutJoiningASwarm mirrors
// TestStartImportFailsFastWithoutAConnection's shape for the new action.
func TestPhase2_PublishInventoryFailsFastWithoutJoiningASwarm(t *testing.T) {
	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	if _, err := d.PublishInventory(context.Background()); err == nil {
		t.Fatal("PublishInventory succeeded despite never having joined a Swarm")
	}
}
