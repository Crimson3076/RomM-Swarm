package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/scan"
)

// libraryWarmTimeout bounds the background warm-up fetch main.go kicks off
// right after connecting to RomM at startup — generous, since a full
// listing of a large library legitimately takes a while, but bounded so a
// hung RomM connection can never leak a goroutine forever.
const libraryWarmTimeout = 5 * time.Minute

// libraryCacheTTL bounds how long a cached RomM listing is served before a
// plain (non-forced) Library call re-fetches it. Long enough that browsing
// the Library page — paging, filtering by platform — never re-lists
// RomM's entire inventory per click; short enough that an operator who
// imports something new sees it show up on its own within a few minutes
// without having to know to hit Refresh.
const libraryCacheTTL = 5 * time.Minute

// libraryPageSize bounds each RomM listing request while filling or
// refreshing the cache. Independent of adminui's own per-page chunk size
// for the browser (library.go, bridge/adminui) — this one only affects how
// many round trips a full RomM listing takes.
const libraryPageSize = 200

// Library returns RomM's full inventory listing, from cache when
// forceRefresh is false and the cache is younger than libraryCacheTTL —
// which is the common case, since bridge/adminui's Library page re-calls
// this on every page load, every platform-filter change, and every
// infinite-scroll chunk. Without the cache each of those would re-list
// RomM's entire inventory from scratch, which is what made a large
// library's Library page slow to the point of feeling broken.
//
// forceRefresh always re-lists regardless of cache age — the admin UI's
// "Refresh" control uses this after an operator imports something new
// through another path (directly on RomM, say) and wants to see it show
// up without waiting for the TTL.
func (d *Daemon) Library(ctx context.Context, forceRefresh bool) ([]scan.ROMRecord, error) {
	d.libraryMu.Lock()
	defer d.libraryMu.Unlock()

	if !forceRefresh && d.libraryCache != nil && time.Since(d.libraryCachedAt) < libraryCacheTTL {
		return d.libraryCache, nil
	}

	conn := d.Connection()
	if conn == nil {
		return nil, errors.New("bridge: not connected to RomM")
	}

	source := &scan.RommSource{Client: conn.Client, Report: conn.Report}
	var all []scan.ROMRecord
	offset := 0
	for {
		page, hasMore, err := source.ListROMs(ctx, scan.Page{Limit: libraryPageSize, Offset: offset})
		if err != nil {
			return nil, fmt.Errorf("bridge: listing RomM's inventory: %w", err)
		}
		all = append(all, page...)
		if !hasMore || len(page) == 0 {
			break
		}
		offset += len(page)
		if offset > 200_000 {
			// A hard safety cap: an unbounded pagination loop must never
			// hang a request forever, however large a real library gets.
			break
		}
	}

	d.libraryCache = all
	d.libraryCachedAt = time.Now()
	return all, nil
}

// WarmLibraryCache fetches and caches RomM's full listing in the
// background, without blocking its caller. Called once from main.go right
// after a successful startup connection to RomM, so the first operator to
// open the Library page after a container restart finds a warm (or
// warming) cache instead of the daemon starting that fetch from a click —
// the in-memory cache (see Library's own doc comment) is always empty
// immediately after a restart, since it holds process memory, not
// anything persisted to disk.
//
// Errors are logged, not surfaced anywhere else: a failed warm-up isn't
// fatal to the daemon starting, and Library's own normal fetch-on-demand
// path still runs the next time anything actually asks for the listing.
func (d *Daemon) WarmLibraryCache() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), libraryWarmTimeout)
		defer cancel()
		if _, err := d.Library(ctx, false); err != nil {
			log.Printf("bridge: warming the Library cache: %v", err)
		}
	}()
}

// invalidateLibraryCache drops the cached listing — called on Reconnect,
// since a new RomM connection (a different server, or the same server
// after settings changed) must never keep serving the previous
// connection's cached inventory.
func (d *Daemon) invalidateLibraryCache() {
	d.libraryMu.Lock()
	defer d.libraryMu.Unlock()
	d.libraryCache = nil
	d.libraryCachedAt = time.Time{}
}
