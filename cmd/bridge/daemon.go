package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/bridge/bridgeconfig"
	"github.com/Crimson3076/RomM-Swarm/bridge/destination"
	"github.com/Crimson3076/RomM-Swarm/bridge/ingest"
	"github.com/Crimson3076/RomM-Swarm/bridge/romm"
	"github.com/Crimson3076/RomM-Swarm/bridge/scan"
	"github.com/Crimson3076/RomM-Swarm/bridge/swarmconn"
	"github.com/Crimson3076/RomM-Swarm/protocol"
	"github.com/Crimson3076/RomM-Swarm/verify"
)

// Daemon holds one running Bridge's wired dependencies: persistent config,
// journal, and staging, plus whatever RomM connection is currently live.
//
// This reuses cmd/swarm-bridge's proven ingest.Flow wiring
// (RommUploader/RommLibrary/Reconciler/scan.RommSource, all unmodified) —
// the only things that change from the one-shot CLI are the staging
// directory (fixed and persistent here, not os.MkdirTemp+RemoveAll) and the
// Journal (FileJournal, not an in-memory one).
//
// Fields are private with accessor methods rather than exported directly,
// so Daemon can satisfy bridge/adminui.Backend (an interface, which only
// sees methods) without a naming collision between a field and a method of
// the same name.
type Daemon struct {
	configStore *bridgeconfig.FileStore
	journal     *ingest.FileJournal
	staging     *destination.Staging

	// swarmStore and credentialStore back ADR 0017's Host-enrollment
	// wiring — see swarm.go. Two separate stores, not one: swarmStore
	// holds the Host URL and this Bridge's Ed25519 identity key
	// (bridge/swarmconn.Config); credentialStore holds the rotating
	// refresh credential (auth.Credential), reusing auth.FileStore
	// verbatim since auth.Client.Store is a concrete *auth.FileStore
	// field, not an interface.
	swarmStore      *swarmconn.FileStore
	credentialStore *auth.FileStore

	conn atomic.Pointer[romm.Connection]
}

// NewDaemon prepares persistent state rooted at configDir: the config file
// itself, a receive-staging directory, and a journal directory. It does not
// load the config or connect to RomM — call Bootstrap and/or Reconnect for
// that, since a fresh install has nothing to connect with yet.
func NewDaemon(configDir string) (*Daemon, error) {
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return nil, fmt.Errorf("bridge: creating config directory: %w", err)
	}
	store := bridgeconfig.NewFileStore(filepath.Join(configDir, "config.json"))

	stagingDir := filepath.Join(configDir, "receive-staging")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return nil, fmt.Errorf("bridge: creating staging directory: %w", err)
	}
	staging, err := destination.NewStaging(stagingDir)
	if err != nil {
		return nil, fmt.Errorf("bridge: preparing staging: %w", err)
	}

	journal, err := ingest.NewFileJournal(filepath.Join(configDir, "journal"))
	if err != nil {
		return nil, err
	}

	swarmStore := swarmconn.NewFileStore(filepath.Join(configDir, "swarm-connection.json"))
	credentialStore := auth.NewFileStore(filepath.Join(configDir, "swarm-credential.json"))

	return &Daemon{
		configStore:     store,
		journal:         journal,
		staging:         staging,
		swarmStore:      swarmStore,
		credentialStore: credentialStore,
	}, nil
}

// ConfigStore implements bridge/adminui.Backend.
func (d *Daemon) ConfigStore() *bridgeconfig.FileStore { return d.configStore }

// Journal implements bridge/adminui.Backend.
func (d *Daemon) Journal() *ingest.FileJournal { return d.journal }

// Connection implements bridge/adminui.Backend. Returns nil if the Bridge
// hasn't connected yet — unconfigured, or the last attempt failed.
func (d *Daemon) Connection() *romm.Connection { return d.conn.Load() }

// Reconnect implements bridge/adminui.Backend: loads the persisted config,
// connects to RomM, and swaps in the result atomically. Safe to call
// repeatedly, e.g. after settings change through the admin UI: a transfer
// already in flight keeps using whatever Connection was live when it
// started (see StartImport), and only the next one picks up new settings.
func (d *Daemon) Reconnect(ctx context.Context) error {
	cfg, err := d.configStore.Load()
	if err != nil {
		return err
	}
	if !cfg.Configured() {
		return bridgeconfig.ErrNotConfigured
	}
	conn, err := romm.ConnectAndResolvePlatforms(ctx, cfg.RommURL, cfg.RommToken)
	if err != nil {
		return err
	}
	d.conn.Store(conn)
	return nil
}

// StartImport implements bridge/adminui.Backend: validates a local file
// against a platform and, if it checks out, launches the real receiving
// flow (stage, verify, upload through RomM's API, wait for RomM to index
// and match it) in the background, returning the TransferID immediately so
// a caller doesn't block on RomM's confirmed multi-minute ingestion
// debounce. Progress is visible through Journal() from that point on;
// StartImport itself only reports the synchronous, fast-to-detect problems
// (no connection, no such file, wrong platform).
//
// cleanup, if non-nil, runs once the import goroutine is done with
// localPath (success or failure) — for a file the Bridge itself staged
// temporarily (a browser upload), never for a file the operator owns (an
// inbox file), which must never be deleted out from under them.
//
// displayName is the filename RomM will see. It defaults to
// filepath.Base(localPath) when empty, which is correct for an inbox file
// (localPath is the real file) but wrong for a browser upload, whose
// localPath is a Bridge-generated temp path — callers staging their own
// temp file must pass the original filename explicitly so RomM receives a
// name it can parse rather than a temp-file name.
func (d *Daemon) StartImport(localPath, platformSlug, displayName string, timeout time.Duration, cleanup func()) (protocol.TransferID, error) {
	conn := d.Connection()
	if conn == nil {
		return "", errors.New("bridge: not connected to a RomM server yet")
	}

	res, err := (&verify.Analyzer{}).AnalyzeFile(localPath, protocol.PlatformID(platformSlug))
	if err != nil {
		return "", fmt.Errorf("analysing %s: %w", localPath, err)
	}
	if res.Canonical.SHA256 == "" {
		return "", fmt.Errorf("%s was not recognised as a %s cartridge image", localPath, platformSlug)
	}
	if res.Platform != protocol.PlatformID(platformSlug) {
		return "", fmt.Errorf("%s looks like %s, not %s", localPath, res.Platform, platformSlug)
	}
	if _, err := conn.Platforms.Lookup(platformSlug); err != nil {
		return "", err
	}

	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", localPath, err)
	}

	id := protocol.NewTransferID()
	expected := ingest.Expected{
		Platform:  res.Platform,
		FileID:    protocol.FileIDFromCanonicalDigest(res.Canonical.SHA256),
		Canonical: res.Canonical,
	}
	name := displayName
	if name == "" {
		name = filepath.Base(localPath)
	}
	relativeDest := filepath.Join(platformSlug, filepath.Base(name))

	library := &ingest.RommLibrary{Source: &scan.RommSource{Client: conn.Client, Report: conn.Report}}
	flow := &ingest.Flow{
		Mode:       protocol.ModeAPIOnly,
		Staging:    d.staging,
		Journal:    d.journal,
		Uploader:   &ingest.RommUploader{Client: conn.Client, Report: conn.Report, Platforms: conn.Platforms},
		Reconciler: &ingest.Reconciler{Library: library, Timeout: timeout},
	}

	go func() {
		defer f.Close()
		if cleanup != nil {
			defer cleanup()
		}
		// A background import must not be bound to any request's lifetime;
		// it gets its own timeout instead, generous enough to cover the
		// upload itself plus the full reconciliation wait.
		ctx, cancel := context.WithTimeout(context.Background(), timeout+2*time.Minute)
		defer cancel()
		flow.Receive(ctx, id, f, expected, relativeDest)
		// Errors and the terminal state are already recorded in d.journal by
		// Flow itself; there is nothing further to do with the return value.
	}()

	return id, nil
}
