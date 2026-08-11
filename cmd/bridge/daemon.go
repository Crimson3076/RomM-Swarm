package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/bridgeconfig"
	"github.com/Crimson3076/RomM-Swarm/bridge/destination"
	"github.com/Crimson3076/RomM-Swarm/bridge/ingest"
	"github.com/Crimson3076/RomM-Swarm/bridge/scan"
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
type Daemon struct {
	ConfigStore *bridgeconfig.FileStore
	Journal     *ingest.FileJournal
	Staging     *destination.Staging

	conn atomic.Pointer[Connection]
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

	return &Daemon{ConfigStore: store, Journal: journal, Staging: staging}, nil
}

// Connection returns the currently live RomM connection, or nil if the
// Bridge hasn't connected yet — unconfigured, or the last attempt failed.
func (d *Daemon) Connection() *Connection { return d.conn.Load() }

// Reconnect loads the persisted config, connects to RomM, and swaps in the
// result atomically. Safe to call repeatedly, e.g. after settings change
// through the admin UI: a transfer already in flight keeps using whatever
// Connection was live when it started (see StartImport), and only the next
// one picks up the new settings.
func (d *Daemon) Reconnect(ctx context.Context) error {
	cfg, err := d.ConfigStore.Load()
	if err != nil {
		return err
	}
	if !cfg.Configured() {
		return bridgeconfig.ErrNotConfigured
	}
	conn, err := connect(ctx, cfg.RommURL, cfg.RommToken)
	if err != nil {
		return err
	}
	d.conn.Store(conn)
	return nil
}

// StartImport validates a local file against a platform and, if it checks
// out, launches the real receiving flow (stage, verify, upload through
// RomM's API, wait for RomM to index and match it) in the background,
// returning the TransferID immediately so a caller (the admin UI) doesn't
// block on RomM's confirmed multi-minute ingestion debounce. Progress is
// visible through d.Journal from that point on; StartImport itself only
// reports the synchronous, fast-to-detect problems (no connection, no such
// file, wrong platform).
func (d *Daemon) StartImport(localPath, platformSlug string, timeout time.Duration) (protocol.TransferID, error) {
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
	relativeDest := filepath.Join(platformSlug, filepath.Base(localPath))

	library := &ingest.RommLibrary{Source: &scan.RommSource{Client: conn.Client, Report: conn.Report}}
	flow := &ingest.Flow{
		Mode:       protocol.ModeAPIOnly,
		Staging:    d.Staging,
		Journal:    d.Journal,
		Uploader:   &ingest.RommUploader{Client: conn.Client, Report: conn.Report, Platforms: conn.Platforms},
		Reconciler: &ingest.Reconciler{Library: library, Timeout: timeout},
	}

	go func() {
		defer f.Close()
		// A background import must not be bound to any request's lifetime;
		// it gets its own timeout instead, generous enough to cover the
		// upload itself plus the full reconciliation wait.
		ctx, cancel := context.WithTimeout(context.Background(), timeout+2*time.Minute)
		defer cancel()
		flow.Receive(ctx, id, f, expected, relativeDest)
		// Errors and the terminal state are already recorded in d.Journal by
		// Flow itself; there is nothing further to do with the return value.
	}()

	return id, nil
}
