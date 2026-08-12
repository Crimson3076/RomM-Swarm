package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/adminui"
	"github.com/Crimson3076/RomM-Swarm/bridge/hostclient"
	"github.com/Crimson3076/RomM-Swarm/bridge/publish"
	"github.com/Crimson3076/RomM-Swarm/bridge/scan"
	"github.com/Crimson3076/RomM-Swarm/bridge/swarmconn"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// autoPublishTimeout bounds one automatic (startup, post-join, or
// scheduled) publish attempt — see autopublish.go. Generous, since a
// full-catalogue scan-and-publish legitimately takes a while, but bounded
// so a hung Host or RomM connection can never wedge a scheduled tick
// forever.
const autoPublishTimeout = 5 * time.Minute

// scanHoldings scans every protocol.InitialPlatforms() platform the
// connected RomM server actually has, using whatever reference Selection
// is loaded for that platform. A platform with no Selection isn't skipped
// outright — Scanner.ScanPlatform still runs it and reports every item on
// it as Skipped with a clear reason, so the caller can tell "RomM has
// nothing here" from "nothing here verified" (ADR 0019).
func (d *Daemon) scanHoldings(ctx context.Context) ([]protocol.Item, []scan.Skipped, error) {
	conn := d.Connection()
	if conn == nil {
		return nil, nil, errors.New("bridge: not connected to RomM")
	}

	scanner := &scan.Scanner{Source: &scan.RommSource{Client: conn.Client, Report: conn.Report}}

	var items []protocol.Item
	var skipped []scan.Skipped
	for _, platform := range protocol.InitialPlatforms() {
		if _, err := conn.Platforms.Lookup(string(platform)); err != nil {
			// RomM doesn't have this platform at all — nothing to scan,
			// not a reason to skip items that were never there.
			continue
		}
		result, err := scanner.ScanPlatform(ctx, scan.PlatformSource{
			RomMSlug:  string(platform),
			Platform:  platform,
			Selection: d.referenceSelections[platform],
		})
		if err != nil {
			return nil, nil, fmt.Errorf("bridge: scanning %s: %w", platform, err)
		}
		items = append(items, result.Items...)
		skipped = append(skipped, result.Skipped...)
	}
	return items, skipped, nil
}

// PublishInventory implements bridge/adminui.Backend: scans local
// holdings, builds this Swarm's manifest, and sends it to the Host. Called
// both manually (the admin UI's button) and automatically (autopublish.go,
// ADR 0019's follow-on) — publishMu serializes every call, manual or
// automatic, so two overlapping publishes can never race the
// Load-modify-Save of swarmStore's LastPublishedRevision/LastPublishedAt
// (the same class of lost-update hazard auth.Store's bridgeLocks closes
// for credential rotation; here a single mutex is enough, since a Bridge
// only ever has one Swarm connection to publish to at a time).
func (d *Daemon) PublishInventory(ctx context.Context) (adminui.InventoryPublishResult, error) {
	d.publishMu.Lock()
	defer d.publishMu.Unlock()

	swarmCfg, err := d.swarmStore.Load()
	if err != nil {
		return adminui.InventoryPublishResult{}, err
	}
	if !swarmCfg.Configured() {
		return adminui.InventoryPublishResult{}, swarmconn.ErrNotJoined
	}

	holdings, skipped, err := d.scanHoldings(ctx)
	if err != nil {
		return adminui.InventoryPublishResult{}, err
	}

	membership := publish.Membership{
		Swarm:    swarmCfg.SwarmID,
		Alias:    swarmCfg.Alias,
		Policy:   publish.Policy{Swarm: swarmCfg.SwarmID, ShareAll: true},
		Revision: swarmCfg.LastPublishedRevision,
	}
	manifest, err := publish.Snapshot(membership, holdings, time.Now())
	if errors.Is(err, publish.ErrNothingToPublish) {
		reasons := map[string]int{}
		for _, s := range skipped {
			reasons[s.Reason]++
		}
		return adminui.InventoryPublishResult{SkippedCount: len(skipped), SkipReasons: reasons}, nil
	}
	if err != nil {
		return adminui.InventoryPublishResult{}, err
	}

	cred, err := d.credentialStore.Load()
	if err != nil {
		return adminui.InventoryPublishResult{}, fmt.Errorf("bridge: loading the Host credential: %w", err)
	}

	result, err := hostclient.New(swarmCfg.HostURL).PublishInventory(ctx, swarmCfg.BridgeID(), cred.Refresh, manifest)
	if err != nil {
		return adminui.InventoryPublishResult{}, err
	}

	swarmCfg.LastPublishedRevision = manifest.Revision
	swarmCfg.LastPublishedAt = time.Now()
	if err := d.swarmStore.Save(swarmCfg); err != nil {
		return adminui.InventoryPublishResult{}, fmt.Errorf("bridge: persisting the new publish revision: %w", err)
	}

	return adminui.InventoryPublishResult{
		Published:     true,
		ItemCount:     result.ItemCount,
		SkippedCount:  len(skipped),
		DistinctFiles: result.DistinctFiles,
		Revision:      result.Revision,
	}, nil
}
