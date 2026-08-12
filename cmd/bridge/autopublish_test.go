package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/bridgeconfig"
	"github.com/Crimson3076/RomM-Swarm/internal/romfixture"
	"github.com/Crimson3076/RomM-Swarm/protocol"
	"github.com/Crimson3076/RomM-Swarm/reference"
)

func TestPublishIntervalFromEnvDefaultsWhenUnset(t *testing.T) {
	if got := publishIntervalFromEnv(); got != DefaultPublishInterval {
		t.Fatalf("publishIntervalFromEnv() = %v, want the default %v", got, DefaultPublishInterval)
	}
}

func TestPublishIntervalFromEnvUsesACustomValue(t *testing.T) {
	t.Setenv("BRIDGE_PUBLISH_INTERVAL_MINUTES", "5")
	if got := publishIntervalFromEnv(); got != 5*time.Minute {
		t.Fatalf("publishIntervalFromEnv() = %v, want 5m", got)
	}
}

func TestPublishIntervalFromEnvFallsBackOnAnInvalidValue(t *testing.T) {
	t.Setenv("BRIDGE_PUBLISH_INTERVAL_MINUTES", "not-a-number")
	if got := publishIntervalFromEnv(); got != DefaultPublishInterval {
		t.Fatalf("publishIntervalFromEnv() = %v, want the default on a non-numeric value", got)
	}

	t.Setenv("BRIDGE_PUBLISH_INTERVAL_MINUTES", "-5")
	if got := publishIntervalFromEnv(); got != DefaultPublishInterval {
		t.Fatalf("publishIntervalFromEnv() = %v, want the default on a non-positive value", got)
	}
}

// setUpPublishableDaemon wires a Daemon that's already joined (via
// joinedDaemon, inventory_test.go) with a real scan target and a loaded
// reference.Selection, so PublishInventory has real, publishable content
// to work with -- the shared setup every autopublish test below needs.
func setUpPublishableDaemon(t *testing.T) (*Daemon, *fakeInventoryHost) {
	t.Helper()

	fb := newFakeBridgeServer()
	payload := romfixture.GameBoy("AUTOPUBLISH FIXTURE", 65536, false)
	seedROM(fb, "gb", "Autopublish Fixture.gb", payload)
	rommSrv := fb.start(t)

	host := &fakeInventoryHost{}
	hostSrv := host.start(t)

	d, _, _ := joinedDaemon(t, rommSrv.URL, hostSrv.URL)

	set, err := reference.ImportDAT(bytes.NewReader(buildInventoryTestDAT(map[string][]byte{
		"Autopublish Fixture": payload,
	})), reference.ImportOptions{Platform: protocol.PlatformGB})
	if err != nil {
		t.Fatalf("ImportDAT: %v", err)
	}
	d.referenceSelections[protocol.PlatformGB] = reference.DefaultProfile().Apply(set)

	return d, host
}

func waitForPublishCount(t *testing.T, host *fakeInventoryHost, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		host.mu.Lock()
		n := host.receivedCount
		host.mu.Unlock()
		if n >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	t.Fatalf("timed out waiting for %d publish call(s); the fake Host received %d", want, host.receivedCount)
}

// TestPhase2_StartAutoPublishFiresImmediatelyWhenAlreadyJoined proves the
// "docker start" case: an interval long enough that no tick could
// plausibly fire during the test, yet a publish still reaches the Host —
// only the pre-loop immediate call can have produced it.
func TestPhase2_StartAutoPublishFiresImmediatelyWhenAlreadyJoined(t *testing.T) {
	d, host := setUpPublishableDaemon(t)

	stop := d.StartAutoPublish(context.Background(), time.Hour)
	defer stop()

	waitForPublishCount(t, host, 1)

	host.mu.Lock()
	defer host.mu.Unlock()
	if host.receivedCount != 1 {
		t.Fatalf("the fake Host received %d publish call(s) within the wait window, want exactly 1", host.receivedCount)
	}
}

// TestPhase2_StartAutoPublishSkipsSilentlyWhenNotJoined proves an
// unconfigured Daemon's loop neither panics nor blocks shutdown — the
// normal state for every Bridge before its first Swarm join.
func TestPhase2_StartAutoPublishSkipsSilentlyWhenNotJoined(t *testing.T) {
	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}

	stop := d.StartAutoPublish(context.Background(), time.Hour)
	time.Sleep(50 * time.Millisecond) // let the immediate attempt run and return
	stop()                            // must not hang
}

// TestPhase2_StartAutoPublishRepublishesOnEachTick drives the loop with an
// injectable tick channel instead of racing a real time.Ticker, proving
// each tick produces one more real publish to the Host with an advancing
// revision.
func TestPhase2_StartAutoPublishRepublishesOnEachTick(t *testing.T) {
	d, host := setUpPublishableDaemon(t)

	ticks := make(chan time.Time)
	stop := d.startAutoPublishFromTicks(context.Background(), ticks, func() {})
	defer stop()

	waitForPublishCount(t, host, 1) // the immediate pre-loop publish

	ticks <- time.Now()
	waitForPublishCount(t, host, 2)

	ticks <- time.Now()
	waitForPublishCount(t, host, 3)

	host.mu.Lock()
	defer host.mu.Unlock()
	if host.lastManifest.Revision != 3 {
		t.Fatalf("after 3 publishes, the last manifest's revision = %d, want 3", host.lastManifest.Revision)
	}
}

// TestPhase2_PublishInventorySerializesConcurrentCalls proves publishMu
// does its job: N goroutines calling PublishInventory concurrently (the
// scheduled loop racing a manual "Publish Inventory" click, or the
// post-join publish) must all succeed and land N distinct, sequential
// revisions -- not silently lose one to a Load-then-Save race on
// swarmStore.
func TestPhase2_PublishInventorySerializesConcurrentCalls(t *testing.T) {
	d, host := setUpPublishableDaemon(t)

	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := d.PublishInventory(context.Background()); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent PublishInventory: %v", err)
	}

	saved, err := d.swarmStore.Load()
	if err != nil {
		t.Fatalf("loading the swarm connection: %v", err)
	}
	if saved.LastPublishedRevision != n {
		t.Fatalf("LastPublishedRevision = %d after %d concurrent publishes, want %d — a lost update means publishMu isn't serializing", saved.LastPublishedRevision, n, n)
	}

	host.mu.Lock()
	defer host.mu.Unlock()
	if host.receivedCount != n {
		t.Fatalf("the fake Host received %d publish call(s), want %d", host.receivedCount, n)
	}
}

// fakeJoinHost is a minimal stand-in for host/hostapi's enroll and
// inventory routes together — enough to prove JoinSwarm's own background
// publish actually fires and reaches the Host, without a real Host.
type fakeJoinHost struct {
	mu                sync.Mutex
	inventoryReceived int
}

func (h *fakeJoinHost) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/bridges/enroll", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code      string `json:"code"`
			PublicKey string `json:"public_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		pub, err := base64.StdEncoding.DecodeString(req.PublicKey)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		swarmID := protocol.NewSwarmID()
		bridgeID := protocol.BridgeIDFromPublicKey(pub)
		alias := protocol.MustAliasFor(protocol.NewSwarmAliasKey(), swarmID, bridgeID)

		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{
			"bridge_id":     string(bridgeID),
			"swarm_id":      string(swarmID),
			"bridge_alias":  string(alias),
			"refresh_token": "fake-refresh-token",
		})
	})

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
		h.inventoryReceived++
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

// TestPhase2_JoinSwarmTriggersAnImmediatePublish proves the "adding a
// bridge" trigger: right after a real JoinSwarm call succeeds, a publish
// reaches the Host on its own, without a manual click or waiting for the
// next scheduled tick.
func TestPhase2_JoinSwarmTriggersAnImmediatePublish(t *testing.T) {
	fb := newFakeBridgeServer()
	payload := romfixture.GameBoy("JOIN TRIGGER GAME", 65536, false)
	seedROM(fb, "gb", "Join Trigger Game.gb", payload)
	rommSrv := fb.start(t)

	host := &fakeJoinHost{}
	hostSrv := host.start(t)

	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	if err := d.ConfigStore().Save(bridgeconfig.Config{RommURL: rommSrv.URL, RommToken: "t"}); err != nil {
		t.Fatalf("saving the RomM config: %v", err)
	}
	if err := d.Reconnect(context.Background()); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}

	set, err := reference.ImportDAT(bytes.NewReader(buildInventoryTestDAT(map[string][]byte{
		"Join Trigger Game": payload,
	})), reference.ImportOptions{Platform: protocol.PlatformGB})
	if err != nil {
		t.Fatalf("ImportDAT: %v", err)
	}
	d.referenceSelections[protocol.PlatformGB] = reference.DefaultProfile().Apply(set)

	if _, err := d.JoinSwarm(context.Background(), hostSrv.URL, "the-code"); err != nil {
		t.Fatalf("JoinSwarm: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		host.mu.Lock()
		n := host.inventoryReceived
		host.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	host.mu.Lock()
	defer host.mu.Unlock()
	if host.inventoryReceived != 1 {
		t.Fatalf("post-join background publish did not reach the Host (received %d), want 1", host.inventoryReceived)
	}
}
