// Package adminui is the local, single-instance Bridge admin web UI —
// ADR 0015. It is not the future cross-Swarm member portal (that's web/,
// deferred to Phase 5 behind ADR 0013); this package never talks to a
// Network Host, because none exists yet. It only ever talks to the RomM
// server the operator configures, and to the Backend that wraps it.
package adminui

import (
	"context"
	"time"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/bridge/bridgeconfig"
	"github.com/Crimson3076/RomM-Swarm/bridge/ingest"
	"github.com/Crimson3076/RomM-Swarm/bridge/romm"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// Backend is the narrow view of a running Bridge this package needs.
// cmd/bridge's Daemon satisfies it; tests supply a fake.
type Backend interface {
	// ConfigStore is the persisted Bridge configuration: RomM connection,
	// destination mode, staging paths, admin password hash.
	ConfigStore() *bridgeconfig.FileStore

	// Connection is the currently live RomM connection, or nil if the
	// Bridge hasn't connected yet.
	Connection() *romm.Connection

	// Reconnect loads the persisted config and establishes a fresh
	// Connection from it.
	Reconnect(ctx context.Context) error

	// StartImport validates localPath against platformSlug and, if it
	// checks out, runs the real receiving flow in the background,
	// returning the TransferID immediately. displayName is the filename
	// RomM will see; pass "" to derive it from localPath (correct for an
	// inbox file, wrong for a Bridge-generated upload temp path — see
	// Daemon.StartImport). cleanup, if non-nil, runs once the background
	// import is done with localPath — see Daemon.StartImport for why this
	// must never be used for a file the operator owns.
	StartImport(localPath, platformSlug, displayName string, timeout time.Duration, cleanup func()) (protocol.TransferID, error)

	// Journal is where import progress is recorded — used for the activity
	// view.
	Journal() *ingest.FileJournal

	// SwarmStatus reports this Bridge's Network Host connection, or the
	// zero value with Joined false if it has never joined a Swarm.
	SwarmStatus() (SwarmStatus, error)

	// JoinSwarm redeems an invitation code against a Network Host at
	// hostURL, generating (or reusing) this Bridge's identity and
	// persisting the resulting Host connection and credential.
	JoinSwarm(ctx context.Context, hostURL, code string) (protocol.BridgeID, error)

	// TestSwarmConnection rotates the stored Host credential once, proving
	// the round trip still works — a manual, operator-triggered check,
	// mirroring Reconnect/Connection's relationship to the RomM side.
	TestSwarmConnection(ctx context.Context) (auth.Result, error)

	// PublishInventory scans local holdings, builds this Swarm's manifest,
	// and sends it to the Host — the manual, operator-triggered action
	// behind the Swarm page's "Publish Inventory" button (ADR 0019).
	PublishInventory(ctx context.Context) (InventoryPublishResult, error)
}

// SwarmStatus is this Bridge's view of its own Network Host connection, as
// cmd/bridge's Daemon reports it. Defined here rather than in cmd/bridge
// because a Backend method's return type must be visible to this package,
// and this package cannot import package main.
type SwarmStatus struct {
	Joined      bool
	HostURL     string
	BridgeID    protocol.BridgeID
	Generation  uint64
	LastRotated time.Time

	// LastPublishedRevision and LastPublishedAt report this Bridge's most
	// recent successful inventory publish (ADR 0019). Zero
	// LastPublishedRevision means never published.
	LastPublishedRevision protocol.Revision
	LastPublishedAt       time.Time
}

// InventoryPublishResult is what a "Publish Inventory" click returns.
// Defined here, not in cmd/bridge, for the same reason SwarmStatus is: a
// Backend method's return type must be visible to this package, and this
// package cannot import package main.
type InventoryPublishResult struct {
	// Published is false when the policy permitted nothing to publish —
	// most likely because no reference catalogue is loaded for any
	// platform RomM actually has. Not an error: an owner who hasn't loaded
	// a DAT yet should see why, not a failure.
	Published bool

	ItemCount     int
	SkippedCount  int
	DistinctFiles int // Swarm-wide, from the Host's response
	Revision      protocol.Revision

	// SkipReasons counts why holdings weren't published, one entry per
	// distinct reason — populated only when Published is false.
	SkipReasons map[string]int
}
