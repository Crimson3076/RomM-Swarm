package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/bridge/adminui"
	"github.com/Crimson3076/RomM-Swarm/bridge/hostclient"
	"github.com/Crimson3076/RomM-Swarm/bridge/swarmconn"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// SwarmStatus implements bridge/adminui.Backend.
func (d *Daemon) SwarmStatus() (adminui.SwarmStatus, error) {
	swarmCfg, err := d.swarmStore.Load()
	if errors.Is(err, swarmconn.ErrNotJoined) {
		return adminui.SwarmStatus{}, nil
	}
	if err != nil {
		return adminui.SwarmStatus{}, err
	}
	if !swarmCfg.Configured() {
		return adminui.SwarmStatus{}, nil
	}

	cred, err := d.credentialStore.Load()
	if err != nil {
		return adminui.SwarmStatus{}, fmt.Errorf("bridge: swarm connection is stored but its credential is not: %w", err)
	}

	return adminui.SwarmStatus{
		Joined:                true,
		HostURL:               swarmCfg.HostURL,
		BridgeID:              swarmCfg.BridgeID(),
		Generation:            cred.Generation,
		LastRotated:           cred.PersistedAt,
		LastPublishedRevision: swarmCfg.LastPublishedRevision,
		LastPublishedAt:       swarmCfg.LastPublishedAt,
	}, nil
}

// JoinSwarm redeems an invitation code against a Network Host: generating
// (or reusing) this Bridge's identity keypair, enrolling it, and persisting
// both the Host connection and the resulting rotating credential.
//
// Reuses an existing identity key rather than generating a new one on every
// join attempt — re-enrolling with a different key would derive a different
// protocol.BridgeID, and the Host would see a second Bridge, not the same
// one recognised again (protocol.BridgeIDFromPublicKey's own doc comment).
func (d *Daemon) JoinSwarm(ctx context.Context, hostURL, code string) (protocol.BridgeID, error) {
	hostURL = strings.TrimRight(strings.TrimSpace(hostURL), "/")
	if hostURL == "" {
		return "", errors.New("bridge: a Host URL is required")
	}
	if strings.TrimSpace(code) == "" {
		return "", errors.New("bridge: an invitation code is required")
	}

	swarmCfg, err := d.swarmStore.Load()
	if err != nil && !errors.Is(err, swarmconn.ErrNotJoined) {
		return "", err
	}
	if !swarmCfg.HasIdentity() {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return "", fmt.Errorf("bridge: generating an identity key: %w", err)
		}
		swarmCfg.IdentityPrivateKey = priv
	}

	client := hostclient.New(hostURL)
	enrolled, err := client.Enroll(ctx, code, swarmCfg.PublicKey())
	if err != nil {
		return "", fmt.Errorf("bridge: enrolling with %s: %w", hostURL, err)
	}

	if err := d.credentialStore.Save(auth.Credential{
		BridgeID:   enrolled.BridgeID,
		Refresh:    enrolled.Refresh,
		Generation: 1, // auth.Verifier.Enroll always sets Generation 1.
	}); err != nil {
		return "", fmt.Errorf("bridge: persisting the Host credential: %w", err)
	}

	swarmCfg.HostURL = hostURL
	swarmCfg.SwarmID = enrolled.SwarmID
	swarmCfg.Alias = enrolled.Alias
	if err := d.swarmStore.Save(swarmCfg); err != nil {
		return "", fmt.Errorf("bridge: persisting the Host connection: %w", err)
	}

	// Publish once immediately, so a newly-enrolled Bridge shows real
	// stats on the Host right away rather than waiting for the next
	// scheduled tick (ADR 0019's follow-on). Backgrounded on its own
	// context, not ctx: JoinSwarm's caller (an HTTP handler) cancels ctx
	// as soon as it returns, well before a scan-and-publish could finish —
	// the same reasoning StartImport's own background goroutine documents.
	// Best-effort: a failure here is logged, not returned, since the join
	// itself already succeeded and must not be reported as failed over it.
	go func() {
		publishCtx, cancel := context.WithTimeout(context.Background(), autoPublishTimeout)
		defer cancel()
		if _, err := d.PublishInventory(publishCtx); err != nil {
			fmt.Fprintf(os.Stderr, "bridge: initial inventory publish after joining failed: %v\n", err)
		}
	}()

	return enrolled.BridgeID, nil
}

// TestSwarmConnection rotates the stored credential once, proving the
// round trip to the Host still works. No automatic, scheduled rotation
// exists yet — see ADR 0017; this is a manual, operator-triggered check,
// mirroring the existing RomM /api/connection/test pattern.
func (d *Daemon) TestSwarmConnection(ctx context.Context) (auth.Result, error) {
	swarmCfg, err := d.swarmStore.Load()
	if err != nil {
		return auth.Result{}, err
	}
	if !swarmCfg.Configured() {
		return auth.Result{}, swarmconn.ErrNotJoined
	}

	client := &auth.Client{
		Store:  d.credentialStore,
		Rotate: hostclient.New(swarmCfg.HostURL).RotateFunc(d.credentialStore),
	}
	return client.Refresh()
}

// BootstrapSwarm seeds a first Swarm join from SWARM_HOST_URL/
// SWARM_INVITATION_CODE, mirroring Bootstrap's ROMM_URL/ROMM_TOKEN
// convenience exactly: only once, only if no Host connection is stored
// yet, never reconsulted on a later boot.
func (d *Daemon) BootstrapSwarm(ctx context.Context) error {
	_, err := d.swarmStore.Load()
	if err == nil {
		return nil // already joined (or an identity key exists mid-join)
	}
	if !errors.Is(err, swarmconn.ErrNotJoined) {
		return err
	}

	hostURL := strings.TrimSpace(os.Getenv("SWARM_HOST_URL"))
	code := strings.TrimSpace(os.Getenv("SWARM_INVITATION_CODE"))
	if hostURL == "" || code == "" {
		return nil
	}

	if _, err := d.JoinSwarm(ctx, hostURL, code); err != nil {
		return fmt.Errorf("seeding Swarm connection from SWARM_HOST_URL/SWARM_INVITATION_CODE: %w", err)
	}
	return nil
}
