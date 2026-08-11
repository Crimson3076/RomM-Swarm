// Package adminui is the local, single-instance Bridge admin web UI —
// ADR 0015. It is not the future cross-Swarm member portal (that's web/,
// deferred to Phase 5 behind ADR 0013); this package never talks to a
// Network Host, because none exists yet. It only ever talks to the RomM
// server the operator configures, and to the Backend that wraps it.
package adminui

import (
	"context"
	"time"

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
	// returning the TransferID immediately. cleanup, if non-nil, runs once
	// the background import is done with localPath — see Daemon.StartImport
	// for why this must never be used for a file the operator owns.
	StartImport(localPath, platformSlug string, timeout time.Duration, cleanup func()) (protocol.TransferID, error)

	// Journal is where import progress is recorded — used for the activity
	// view.
	Journal() *ingest.FileJournal
}
