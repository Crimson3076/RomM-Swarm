package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/adminui"
	"github.com/Crimson3076/RomM-Swarm/bridge/bridgeconfig"
	"github.com/Crimson3076/RomM-Swarm/bridge/hostclient"
	"github.com/Crimson3076/RomM-Swarm/bridge/publish"
	"github.com/Crimson3076/RomM-Swarm/bridge/scan"
	"github.com/Crimson3076/RomM-Swarm/bridge/swarmconn"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// scanHoldings scans every protocol.InitialPlatforms() platform the
// connected RomM server actually has, using whatever reference Selection
// is loaded for that platform. A platform with no Selection isn't skipped
// outright — Scanner.ScanPlatform still runs it and reports every item on
// it as Skipped with a clear reason, so the caller can tell "RomM has
// nothing here" from "nothing here verified" (ADR 0019).
//
// Reports progress into d.status as it goes (platform N of the fixed,
// known platform list, plus a running items-scanned count) — the only
// part of a publish attempt slow enough that an operator watching the
// Swarm page needs to see it moving, not just "started" and "finished".
func (d *Daemon) scanHoldings(ctx context.Context) ([]protocol.Item, []scan.Skipped, error) {
	conn := d.Connection()
	if conn == nil {
		return nil, nil, errors.New("bridge: not connected to RomM")
	}

	platforms := protocol.InitialPlatforms()
	var items []protocol.Item
	var skipped []scan.Skipped
	scannedSoFar := 0
	for i, platform := range platforms {
		if _, err := conn.Platforms.Lookup(string(platform)); err != nil {
			// RomM doesn't have this platform at all — nothing to scan,
			// not a reason to skip items that were never there.
			continue
		}
		d.updateScanProgress(platform, i+1, len(platforms), scannedSoFar)

		platformIndex, platformTotal, baseline := i+1, len(platforms), scannedSoFar
		scanner := &scan.Scanner{
			Source: &scan.RommSource{Client: conn.Client, Report: conn.Report},
			// Each record here can mean a real download and four-identity
			// analysis — a large platform can take a long time, and
			// without per-record progress the status page only ever shows
			// "platform N of M" for the whole duration, indistinguishable
			// from having hung (the operator report that prompted this).
			OnItemScanned: func(scannedThisPlatform int) {
				d.updateScanProgress(platform, platformIndex, platformTotal, baseline+scannedThisPlatform)
			},
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
		scannedSoFar += result.Scanned
		d.updateScanProgress(platform, i+1, len(platforms), scannedSoFar)
	}
	return items, skipped, nil
}

// PublishInventory implements bridge/adminui.Backend: scans local
// holdings, builds this Swarm's manifest, and sends it to the Host. Called
// both manually (the admin UI's button) and automatically (autopublish.go,
// ADR 0019/0020) — publishMu serializes every call, manual or automatic,
// so two overlapping publishes can never race the Load-modify-Save of
// swarmStore's LastPublishedRevision/LastPublishedAt (the same class of
// lost-update hazard auth.Store's bridgeLocks closes for credential
// rotation; here a single mutex is enough, since a Bridge only ever has
// one Swarm connection to publish to at a time).
//
// Updates d.status throughout, so a caller with no other way to tell
// "still working" from "hung" — see PublishStatus — can check back without
// blocking on this call's return.
func (d *Daemon) PublishInventory(ctx context.Context) (adminui.InventoryPublishResult, error) {
	d.publishMu.Lock()
	defer d.publishMu.Unlock()

	d.startPublishStatus()

	swarmCfg, err := d.swarmStore.Load()
	if err != nil {
		d.finishPublishStatusError(err)
		return adminui.InventoryPublishResult{}, err
	}
	if !swarmCfg.Configured() {
		d.finishPublishStatusError(swarmconn.ErrNotJoined)
		return adminui.InventoryPublishResult{}, swarmconn.ErrNotJoined
	}

	cred, err := d.credentialStore.Load()
	if err != nil {
		werr := fmt.Errorf("bridge: loading the Host credential: %w", err)
		d.finishPublishStatusError(werr)
		return adminui.InventoryPublishResult{}, werr
	}

	// The RomM-connection config is where a Bridge owner sets its own
	// display name (ADR 0022, bridge/adminui's Settings page) — a separate
	// store from swarmCfg, which only knows about the Swarm connection
	// itself. A load failure here isn't fatal to publishing: it just means
	// no name is sent, same as if the field were left blank.
	cfg, err := d.ConfigStore().Load()
	if err != nil {
		cfg = bridgeconfig.Config{}
	}

	d.refreshReferenceCatalogues(ctx, swarmCfg.HostURL, swarmCfg.BridgeID(), cred.Refresh, swarmCfg.SwarmID)

	holdings, skipped, err := d.scanHoldings(ctx)
	if err != nil {
		d.finishPublishStatusError(err)
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
		// publish.Snapshot refuses to build an empty manifest, so the
		// ordinary path below — which carries cfg.DisplayName alongside a
		// real manifest — never runs on this branch. Without this, a
		// Bridge with nothing yet verified (no reference catalogue
		// loaded, or nothing on it verifies) could set a display name in
		// Settings and have it never reach the Host at all. Best-effort:
		// a failure here doesn't turn an otherwise-informative "nothing
		// published" result into an error.
		if cfg.DisplayName != "" {
			if err := hostclient.New(swarmCfg.HostURL).SetDisplayName(ctx, swarmCfg.BridgeID(), cred.Refresh, swarmCfg.SwarmID, cfg.DisplayName); err != nil {
				log.Printf("bridge: sending the display name to the Host: %v", err)
			}
		}
		reasons := map[string]int{}
		for _, s := range skipped {
			reasons[s.Reason]++
		}
		result := adminui.InventoryPublishResult{SkippedCount: len(skipped), SkipReasons: reasons}
		d.finishPublishStatusDone(result)
		return result, nil
	}
	if err != nil {
		d.finishPublishStatusError(err)
		return adminui.InventoryPublishResult{}, err
	}

	d.setPublishPhase(adminui.PublishPhasePublishing)

	result, err := hostclient.New(swarmCfg.HostURL).PublishInventory(ctx, swarmCfg.BridgeID(), cred.Refresh, manifest, cfg.DisplayName)
	if err != nil {
		d.finishPublishStatusError(err)
		return adminui.InventoryPublishResult{}, err
	}

	swarmCfg.LastPublishedRevision = manifest.Revision
	swarmCfg.LastPublishedAt = time.Now()
	if err := d.swarmStore.Save(swarmCfg); err != nil {
		werr := fmt.Errorf("bridge: persisting the new publish revision: %w", err)
		d.finishPublishStatusError(werr)
		return adminui.InventoryPublishResult{}, werr
	}

	out := adminui.InventoryPublishResult{
		Published:     true,
		ItemCount:     result.ItemCount,
		SkippedCount:  len(skipped),
		DistinctFiles: result.DistinctFiles,
		Revision:      result.Revision,
	}
	d.finishPublishStatusDone(out)
	return out, nil
}

// PublishStatus implements bridge/adminui.Backend.
func (d *Daemon) PublishStatus() adminui.PublishStatus {
	d.statusMu.Lock()
	defer d.statusMu.Unlock()
	return d.status
}

// TriggerPublishInventory implements bridge/adminui.Backend: starts
// PublishInventory in the background if nothing is already running, and
// returns the resulting status immediately either way.
//
// publishTriggering (a CompareAndSwap, not a plain check-then-act) closes
// two races a naive "if !Running() { go ... }" in the HTTP handler itself
// would leave open: (1) the response to the very call that started a
// publish must already show running:true, which means marking the status
// synchronously, here, before returning — not waiting for the spawned
// goroutine to get scheduled and reach PublishInventory's own
// startPublishStatus call, an unbounded race an operator could otherwise
// observe as "I clicked Publish and it still says idle"; (2) two calls
// arriving close together (an impatient second click) must not each
// decide independently that nothing is running and both spawn a
// redundant full rescan back to back.
func (d *Daemon) TriggerPublishInventory() adminui.PublishStatus {
	if d.publishTriggering.CompareAndSwap(false, true) {
		d.startPublishStatus()
		go func() {
			defer d.publishTriggering.Store(false)
			ctx, cancel := context.WithTimeout(context.Background(), publishTimeoutFromEnv())
			defer cancel()
			d.PublishInventory(ctx)
		}()
	}
	return d.PublishStatus()
}

// startPublishStatus marks a new attempt beginning. Result/Err are
// deliberately left untouched — see PublishStatus's own doc comment for
// why.
func (d *Daemon) startPublishStatus() {
	d.statusMu.Lock()
	defer d.statusMu.Unlock()
	d.status.Phase = adminui.PublishPhaseScanning
	d.status.Platform = ""
	d.status.PlatformIndex = 0
	d.status.PlatformTotal = 0
	d.status.ItemsScanned = 0
	d.status.StartedAt = time.Now()
	d.status.FinishedAt = time.Time{}
}

func (d *Daemon) updateScanProgress(platform protocol.PlatformID, index, total, itemsScanned int) {
	d.statusMu.Lock()
	defer d.statusMu.Unlock()
	d.status.Platform = platform
	d.status.PlatformIndex = index
	d.status.PlatformTotal = total
	d.status.ItemsScanned = itemsScanned
}

func (d *Daemon) setPublishPhase(phase adminui.PublishPhase) {
	d.statusMu.Lock()
	defer d.statusMu.Unlock()
	d.status.Phase = phase
}

func (d *Daemon) finishPublishStatusDone(result adminui.InventoryPublishResult) {
	d.statusMu.Lock()
	defer d.statusMu.Unlock()
	d.status.Phase = adminui.PublishPhaseDone
	d.status.Result = result
	d.status.Err = ""
	d.status.FinishedAt = time.Now()
}

func (d *Daemon) finishPublishStatusError(err error) {
	d.statusMu.Lock()
	defer d.statusMu.Unlock()
	d.status.Phase = adminui.PublishPhaseError
	d.status.Err = err.Error()
	d.status.FinishedAt = time.Now()
}
