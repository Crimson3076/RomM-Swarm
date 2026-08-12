package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/swarmconn"
)

// DefaultPublishInterval is how often StartAutoPublish republishes
// inventory when BRIDGE_PUBLISH_INTERVAL_MINUTES isn't set.
const DefaultPublishInterval = 15 * time.Minute

// publishIntervalFromEnv resolves BRIDGE_PUBLISH_INTERVAL_MINUTES, falling
// back to DefaultPublishInterval for an unset, non-numeric, or
// non-positive value — mirroring HOST_MAX_INVENTORY_BYTES's own
// non-fatal-fallback convention (host/hostapi's Server.MaxInventoryBytes).
func publishIntervalFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("BRIDGE_PUBLISH_INTERVAL_MINUTES"))
	if raw == "" {
		return DefaultPublishInterval
	}
	minutes, err := strconv.Atoi(raw)
	if err != nil || minutes <= 0 {
		fmt.Fprintf(os.Stderr, "bridge: BRIDGE_PUBLISH_INTERVAL_MINUTES=%q is not a positive integer; using the %s default\n", raw, DefaultPublishInterval)
		return DefaultPublishInterval
	}
	return time.Duration(minutes) * time.Minute
}

// DefaultPublishTimeout bounds one whole publish attempt — every platform's
// scan plus the final upload — when BRIDGE_PUBLISH_TIMEOUT_MINUTES isn't
// set. Raised from an original 5 minutes after an operator report: a
// Bridge whose RomM server isn't on the same local network (so every list
// and download request is markedly slower) legitimately needs much longer
// to get through all of protocol.InitialPlatforms(), especially with large
// Nintendo DS dumps (up to 512MiB, ADR 0004) in the mix. Still bounded, so
// a genuinely hung Host or RomM connection can't wedge a scheduled tick
// forever.
const DefaultPublishTimeout = 30 * time.Minute

// publishTimeoutFromEnv resolves BRIDGE_PUBLISH_TIMEOUT_MINUTES, the same
// fallback shape as publishIntervalFromEnv.
func publishTimeoutFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("BRIDGE_PUBLISH_TIMEOUT_MINUTES"))
	if raw == "" {
		return DefaultPublishTimeout
	}
	minutes, err := strconv.Atoi(raw)
	if err != nil || minutes <= 0 {
		fmt.Fprintf(os.Stderr, "bridge: BRIDGE_PUBLISH_TIMEOUT_MINUTES=%q is not a positive integer; using the %s default\n", raw, DefaultPublishTimeout)
		return DefaultPublishTimeout
	}
	return time.Duration(minutes) * time.Minute
}

// StartAutoPublish runs PublishInventory once immediately — covering a
// container restart, so the Host isn't left showing stale data for a full
// interval — and then again on every tick of interval, for as long as the
// daemon runs.
//
// Not joined to a Swarm yet, or not currently connected to RomM, is not an
// error here: each attempt is silently skipped (not-joined) or logged
// (any other failure) and simply retried on the next tick, since a Bridge
// can join a Swarm or reconnect to RomM at any point in its lifetime
// through the admin UI, independent of when this loop started.
//
// Overlapping publishes — this loop's tick racing a manual "Publish
// Inventory" click, or JoinSwarm's own post-join publish — are made safe
// by PublishInventory's own publishMu, not by anything here.
//
// Returns a stop func that ends the loop; safe to call at most once.
func (d *Daemon) StartAutoPublish(ctx context.Context, interval time.Duration) func() {
	ticker := time.NewTicker(interval)
	return d.startAutoPublishFromTicks(ctx, ticker.C, ticker.Stop)
}

// startAutoPublishFromTicks is StartAutoPublish's implementation, taking
// the tick source as a parameter so tests can drive it deterministically
// instead of racing a real time.Ticker.
func (d *Daemon) startAutoPublishFromTicks(ctx context.Context, ticks <-chan time.Time, stopTicker func()) func() {
	done := make(chan struct{})

	go func() {
		d.attemptAutoPublish(ctx)
		for {
			select {
			case <-ticks:
				d.attemptAutoPublish(ctx)
			case <-done:
				stopTicker()
				return
			}
		}
	}()

	return func() { close(done) }
}

// attemptAutoPublish runs one PublishInventory call, logging but never
// propagating a failure — a scheduled or startup publish attempt must
// never take the daemon down with it, and "nothing joined yet" is the
// normal steady state before a first Swarm join, not worth logging on
// every tick.
func (d *Daemon) attemptAutoPublish(ctx context.Context) {
	publishCtx, cancel := context.WithTimeout(ctx, publishTimeoutFromEnv())
	defer cancel()

	result, err := d.PublishInventory(publishCtx)
	switch {
	case errors.Is(err, swarmconn.ErrNotJoined):
		return
	case err != nil:
		fmt.Fprintf(os.Stderr, "bridge: automatic inventory publish failed: %v\n", err)
	case result.Published:
		fmt.Printf("bridge: automatically published inventory: revision %d, %d item(s)\n", result.Revision, result.ItemCount)
	}
}
