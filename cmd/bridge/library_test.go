package main

import (
	"context"
	"testing"

	"github.com/Crimson3076/RomM-Swarm/bridge/bridgeconfig"
)

// TestPhase2_LibraryCachesBetweenCalls proves the actual bug the operator
// hit: without a cache, every Library page load re-lists RomM's entire
// inventory from scratch. Two plain (non-forced) calls must reach RomM
// exactly once between them.
func TestPhase2_LibraryCachesBetweenCalls(t *testing.T) {
	fb := newFakeBridgeServer()
	fb.roms = []map[string]any{
		{"id": 1, "platform_slug": "gb", "fs_name": "A.gb", "fs_size_bytes": 100, "sha1_hash": "aa"},
		{"id": 2, "platform_slug": "gba", "fs_name": "B.gba", "fs_size_bytes": 200, "sha1_hash": "bb"},
	}
	srv := fb.start(t)

	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	if err := d.ConfigStore().Save(bridgeconfig.Config{RommURL: srv.URL, RommToken: "t"}); err != nil {
		t.Fatalf("saving the RomM config: %v", err)
	}
	if err := d.Reconnect(context.Background()); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	// Reconnect's own capability probe hits /api/roms once, to detect
	// field names (romm.Probe) — unrelated to Library's cache, so the
	// baseline is taken after Reconnect rather than assuming 0.
	fb.mu.Lock()
	baseline := fb.romsListRequests
	fb.mu.Unlock()

	first, err := d.Library(context.Background(), false)
	if err != nil {
		t.Fatalf("Library (first call): %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("Library returned %d record(s), want 2", len(first))
	}

	second, err := d.Library(context.Background(), false)
	if err != nil {
		t.Fatalf("Library (second call): %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("Library (cached) returned %d record(s), want 2", len(second))
	}

	fb.mu.Lock()
	requests := fb.romsListRequests - baseline
	fb.mu.Unlock()
	if requests != 1 {
		t.Fatalf("RomM's /api/roms was hit %d time(s) across two plain Library calls, want exactly 1 — the cache isn't working", requests)
	}
}

// TestPhase2_LibraryForceRefreshBypassesTheCache proves the "Refresh"
// control's escape hatch actually reaches RomM again rather than quietly
// returning stale cached data.
func TestPhase2_LibraryForceRefreshBypassesTheCache(t *testing.T) {
	fb := newFakeBridgeServer()
	fb.roms = []map[string]any{
		{"id": 1, "platform_slug": "gb", "fs_name": "A.gb", "fs_size_bytes": 100, "sha1_hash": "aa"},
	}
	srv := fb.start(t)

	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	if err := d.ConfigStore().Save(bridgeconfig.Config{RommURL: srv.URL, RommToken: "t"}); err != nil {
		t.Fatalf("saving the RomM config: %v", err)
	}
	if err := d.Reconnect(context.Background()); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	fb.mu.Lock()
	baseline := fb.romsListRequests
	fb.mu.Unlock()

	if _, err := d.Library(context.Background(), false); err != nil {
		t.Fatalf("Library (first call): %v", err)
	}

	// RomM now reports a second item, as if a new import landed outside
	// this Bridge's own knowledge.
	fb.mu.Lock()
	fb.roms = append(fb.roms, map[string]any{"id": 2, "platform_slug": "gba", "fs_name": "B.gba", "fs_size_bytes": 200, "sha1_hash": "bb"})
	fb.mu.Unlock()

	stillCached, err := d.Library(context.Background(), false)
	if err != nil {
		t.Fatalf("Library (still cached): %v", err)
	}
	if len(stillCached) != 1 {
		t.Fatalf("Library before refresh returned %d record(s), want 1 (the stale cached count)", len(stillCached))
	}

	refreshed, err := d.Library(context.Background(), true)
	if err != nil {
		t.Fatalf("Library (forced refresh): %v", err)
	}
	if len(refreshed) != 2 {
		t.Fatalf("Library after a forced refresh returned %d record(s), want 2", len(refreshed))
	}

	fb.mu.Lock()
	requests := fb.romsListRequests - baseline
	fb.mu.Unlock()
	if requests != 2 {
		t.Fatalf("RomM's /api/roms was hit %d time(s), want exactly 2 (one cached fill, one forced refresh)", requests)
	}
}

// TestPhase2_ReconnectInvalidatesTheLibraryCache proves a new RomM
// connection (settings changed through the admin UI, say) never keeps
// serving the previous connection's cached inventory.
func TestPhase2_ReconnectInvalidatesTheLibraryCache(t *testing.T) {
	fbA := newFakeBridgeServer()
	fbA.roms = []map[string]any{{"id": 1, "platform_slug": "gb", "fs_name": "A.gb", "fs_size_bytes": 100, "sha1_hash": "aa"}}
	srvA := fbA.start(t)

	fbB := newFakeBridgeServer()
	fbB.roms = []map[string]any{
		{"id": 1, "platform_slug": "gb", "fs_name": "X.gb", "fs_size_bytes": 100, "sha1_hash": "cc"},
		{"id": 2, "platform_slug": "gba", "fs_name": "Y.gba", "fs_size_bytes": 200, "sha1_hash": "dd"},
	}
	srvB := fbB.start(t)

	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	if err := d.ConfigStore().Save(bridgeconfig.Config{RommURL: srvA.URL, RommToken: "t"}); err != nil {
		t.Fatalf("saving the RomM config: %v", err)
	}
	if err := d.Reconnect(context.Background()); err != nil {
		t.Fatalf("Reconnect (A): %v", err)
	}
	if _, err := d.Library(context.Background(), false); err != nil {
		t.Fatalf("Library (A): %v", err)
	}

	if err := d.ConfigStore().Save(bridgeconfig.Config{RommURL: srvB.URL, RommToken: "t"}); err != nil {
		t.Fatalf("saving the RomM config (B): %v", err)
	}
	if err := d.Reconnect(context.Background()); err != nil {
		t.Fatalf("Reconnect (B): %v", err)
	}

	items, err := d.Library(context.Background(), false)
	if err != nil {
		t.Fatalf("Library (B): %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("Library after switching RomM connections returned %d record(s), want 2 (server B's, not A's stale cache)", len(items))
	}
}

func TestPhase2_LibraryFailsFastWithoutAConnection(t *testing.T) {
	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	if _, err := d.Library(context.Background(), false); err == nil {
		t.Fatal("Library succeeded with no RomM connection established")
	}
}
