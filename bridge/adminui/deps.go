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
	"github.com/Crimson3076/RomM-Swarm/bridge/scan"
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
	// and sends it to the Host (ADR 0019), called automatically (ADR 0020)
	// as well as via TriggerPublishInventory below. Blocks until finished
	// — callers that want to observe progress rather than wait should use
	// TriggerPublishInventory instead of calling this directly.
	PublishInventory(ctx context.Context) (InventoryPublishResult, error)

	// TriggerPublishInventory starts PublishInventory in the background if
	// nothing is already running, and returns the resulting PublishStatus
	// immediately either way — the manual, operator-triggered action
	// behind the Swarm page's "Publish Inventory" button. Never blocks on
	// the publish itself finishing.
	TriggerPublishInventory() PublishStatus

	// PublishStatus reports the live progress of whatever PublishInventory
	// call is currently running (scanning/publishing), plus the outcome of
	// the most recently finished one — persists in memory across page
	// reloads, since an operator with no other way to tell a publish apart
	// from a hang needs to be able to check back without losing state.
	PublishStatus() PublishStatus

	// Library returns RomM's full inventory listing, from an internal
	// cache when forceRefresh is false and the cache is still fresh — the
	// Library page (and its infinite-scroll chunks) call this on every
	// request, and without caching that meant re-listing RomM's entire
	// inventory from scratch each time.
	Library(ctx context.Context, forceRefresh bool) ([]scan.ROMRecord, error)
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

// PublishPhase is where a publish attempt currently stands.
type PublishPhase string

const (
	// PublishPhaseIdle means no publish has ever run in this process.
	PublishPhaseIdle PublishPhase = "idle"
	// PublishPhaseScanning means Daemon.scanHoldings is running — the slow
	// part, since it downloads and hashes every held item.
	PublishPhaseScanning PublishPhase = "scanning"
	// PublishPhasePublishing means the scan finished and the manifest is
	// in flight to the Host.
	PublishPhasePublishing PublishPhase = "publishing"
	// PublishPhaseDone means the most recent attempt finished without
	// error — Result holds its outcome, including the "nothing to
	// publish" case (Result.Published false is not an error).
	PublishPhaseDone PublishPhase = "done"
	// PublishPhaseError means the most recent attempt failed — Err holds
	// why.
	PublishPhaseError PublishPhase = "error"
)

// PublishStatus is a live snapshot of Bridge inventory publishing:
// whichever attempt is currently running, if any, plus the outcome of the
// last one that finished. Defined here, not in cmd/bridge, for the same
// reason SwarmStatus is: a Backend method's return type must be visible to
// this package, and this package cannot import package main.
type PublishStatus struct {
	Phase PublishPhase

	// Platform, PlatformIndex, and PlatformTotal describe scan progress:
	// which of the Bridge's fixed, known platform list is currently being
	// scanned, out of how many. ItemsScanned is a running total across
	// every platform scanned so far in this attempt. All four are only
	// meaningful while Phase is PublishPhaseScanning; they hold their last
	// values afterward, not reset to zero, since "platform 5 of 5, 812
	// items scanned" is still useful context once a run finishes.
	Platform      protocol.PlatformID
	PlatformIndex int
	PlatformTotal int
	ItemsScanned  int

	// StartedAt is when the current (or, once finished, the most recent)
	// attempt began. FinishedAt is zero while Phase is scanning or
	// publishing.
	StartedAt  time.Time
	FinishedAt time.Time

	// Result and Err hold the most recently finished attempt's outcome —
	// left untouched (not cleared) while a new attempt is in progress, so
	// the UI keeps showing the last known outcome instead of flashing back
	// to empty the moment a new scan starts.
	Result InventoryPublishResult
	Err    string
}

// Running reports whether a publish attempt is currently in progress.
func (s PublishStatus) Running() bool {
	return s.Phase == PublishPhaseScanning || s.Phase == PublishPhasePublishing
}
