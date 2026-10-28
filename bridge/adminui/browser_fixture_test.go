package adminui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/bridgeconfig"
	"github.com/Crimson3076/RomM-Swarm/bridge/ingest"
	"github.com/Crimson3076/RomM-Swarm/bridge/romm"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// This fixture is compiled into the test binary only. It never ships in the
// Bridge image. Browser tests exercise real handlers with synthetic holdings
// and a scripted import backend, without requiring or altering a live RomM.
type browserBackend struct {
	*fakeBackend
}

func (b *browserBackend) StartImport(path, platform, name string, timeout time.Duration, cleanup func()) (protocol.TransferID, error) {
	if filepath.Ext(name) != ".gb" {
		return "", errors.New("Fixture verifier rejected this file; choose a .gb cartridge")
	}
	id := protocol.NewTransferID()
	for _, state := range []protocol.DestinationState{
		protocol.StateReceiving, protocol.StateVerifiedInStaging,
		protocol.StateUploadingToRomm, protocol.StateAwaitingRommIngestion,
		protocol.StateRommMatched, protocol.StateSourceActive,
	} {
		if err := b.journal.Append(id, ingest.Transition{To: state, At: time.Now(), Detail: name}); err != nil {
			return "", err
		}
	}
	if cleanup != nil {
		cleanup()
	}
	return id, nil
}

func TestBrowserFixture(t *testing.T) {
	addr := os.Getenv("SWARM_UI_FIXTURE_ADDR")
	if addr == "" {
		t.Skip("only started by the browser CI job")
	}
	fakeRomm := newMinimalFakeRomm(t)
	backend := &browserBackend{newFakeBackend(t)}
	hash, err := hashPassword("browser-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.store.Save(bridgeconfig.Config{
		RommURL: fakeRomm.URL, RommToken: "fixture-token", AdminPasswordHash: hash, DisplayName: "Test library",
	}); err != nil {
		t.Fatal(err)
	}
	backend.conn, err = romm.ConnectAndResolvePlatforms(context.Background(), fakeRomm.URL, "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	backend.library = fakeROMRecords(205, "gb")
	for i := range backend.library {
		backend.library[i].Name = fmt.Sprintf("Fixture Game %03d", i)
	}
	backend.library[0].Name = "Smoke Quest (USA)"
	backend.swarmStatus = SwarmStatus{Joined: true, HostURL: "https://host.example", Generation: 1}
	backend.publishInventoryResult = InventoryPublishResult{Published: true, ItemCount: 205, DistinctFiles: 205, Revision: 1}
	t.Fatal(http.ListenAndServe(addr, New(backend, "")))
}
