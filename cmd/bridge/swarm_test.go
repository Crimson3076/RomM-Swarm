package main

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
	"github.com/Crimson3076/RomM-Swarm/host/hostapi"
	"github.com/Crimson3076/RomM-Swarm/host/hoststore/hoststoretest"
)

// TestPhase2_JoinSwarmAndRotateOverRealHTTP is the real two-process proof
// ADR 0017 calls for: a real Daemon, talking real net/http (httptest.Server,
// not an in-process function call) to a real host/hostapi.Server backed by
// real Postgres. Not just unit tests on either side alone.
func TestPhase2_JoinSwarmAndRotateOverRealHTTP(t *testing.T) {
	dsn := hoststoretest.SkipWithoutPostgres(t)
	db := hoststoretest.OpenDB(t, dsn)
	dir := directory.New(db)

	srv := httptest.NewServer(hostapi.New(dir))
	defer srv.Close()

	ctx := context.Background()
	owner, err := dir.BootstrapOwner(ctx, "owner", "The Owner", "", "the-password")
	if err != nil {
		t.Fatalf("BootstrapOwner: %v", err)
	}
	swarmID, err := dir.CreateSwarm(ctx, owner, "Test Swarm")
	if err != nil {
		t.Fatalf("CreateSwarm: %v", err)
	}
	code, _, err := dir.IssueInvitation(ctx, swarmID, owner, 1, 0)
	if err != nil {
		t.Fatalf("IssueInvitation: %v", err)
	}

	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}

	if status, err := d.SwarmStatus(); err != nil || status.Joined {
		t.Fatalf("SwarmStatus before joining: %+v, %v — want Joined false, no error", status, err)
	}

	bridgeID, err := d.JoinSwarm(ctx, srv.URL, string(code))
	if err != nil {
		t.Fatalf("JoinSwarm: %v", err)
	}
	if err := bridgeID.Validate(); err != nil {
		t.Fatalf("JoinSwarm returned an invalid BridgeID: %v", err)
	}

	status, err := d.SwarmStatus()
	if err != nil {
		t.Fatalf("SwarmStatus after joining: %v", err)
	}
	if !status.Joined || status.HostURL != srv.URL || status.BridgeID != bridgeID || status.Generation != 1 {
		t.Fatalf("SwarmStatus after joining = %+v, want Joined with HostURL %s, BridgeID %s, Generation 1", status, srv.URL, bridgeID)
	}

	result, err := d.TestSwarmConnection(ctx)
	if err != nil {
		t.Fatalf("first TestSwarmConnection: %v", err)
	}
	if result.Generation != 2 {
		t.Fatalf("first TestSwarmConnection: Generation = %d, want 2", result.Generation)
	}

	result2, err := d.TestSwarmConnection(ctx)
	if err != nil {
		t.Fatalf("second TestSwarmConnection: %v", err)
	}
	if result2.Generation != 3 {
		t.Fatalf("second TestSwarmConnection: Generation = %d, want 3", result2.Generation)
	}

	finalStatus, err := d.SwarmStatus()
	if err != nil {
		t.Fatalf("SwarmStatus after rotating: %v", err)
	}
	if finalStatus.Generation != 3 {
		t.Fatalf("SwarmStatus after rotating: Generation = %d, want 3", finalStatus.Generation)
	}
}

// TestPhase2_JoinSwarmRejectsAnInvalidCode confirms JoinSwarm surfaces the
// Host's rejection rather than silently persisting a half-formed connection.
func TestPhase2_JoinSwarmRejectsAnInvalidCode(t *testing.T) {
	dsn := hoststoretest.SkipWithoutPostgres(t)
	db := hoststoretest.OpenDB(t, dsn)
	dir := directory.New(db)
	srv := httptest.NewServer(hostapi.New(dir))
	defer srv.Close()

	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}

	if _, err := d.JoinSwarm(context.Background(), srv.URL, "not-a-real-code"); err == nil {
		t.Fatal("JoinSwarm accepted a code that was never issued")
	}
	if status, err := d.SwarmStatus(); err != nil || status.Joined {
		t.Fatalf("SwarmStatus after a rejected join: %+v, %v — want Joined false, no error", status, err)
	}
}

// TestPhase2_BootstrapSwarmSeedsFromEnvOnceOnly mirrors Bootstrap's own
// ROMM_URL/ROMM_TOKEN test pattern.
func TestPhase2_BootstrapSwarmSeedsFromEnvOnceOnly(t *testing.T) {
	dsn := hoststoretest.SkipWithoutPostgres(t)
	db := hoststoretest.OpenDB(t, dsn)
	dir := directory.New(db)
	srv := httptest.NewServer(hostapi.New(dir))
	defer srv.Close()

	ctx := context.Background()
	owner, err := dir.BootstrapOwner(ctx, "owner", "The Owner", "", "the-password")
	if err != nil {
		t.Fatalf("BootstrapOwner: %v", err)
	}
	swarmID, err := dir.CreateSwarm(ctx, owner, "Test Swarm")
	if err != nil {
		t.Fatalf("CreateSwarm: %v", err)
	}
	code, _, err := dir.IssueInvitation(ctx, swarmID, owner, 1, 0)
	if err != nil {
		t.Fatalf("IssueInvitation: %v", err)
	}

	t.Setenv("SWARM_HOST_URL", srv.URL)
	t.Setenv("SWARM_INVITATION_CODE", string(code))

	d, err := NewDaemon(t.TempDir())
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	if err := d.BootstrapSwarm(ctx); err != nil {
		t.Fatalf("BootstrapSwarm: %v", err)
	}
	status, err := d.SwarmStatus()
	if err != nil || !status.Joined {
		t.Fatalf("SwarmStatus after BootstrapSwarm: %+v, %v — want Joined true", status, err)
	}

	// Changing the env after the fact must not cause a second join attempt.
	t.Setenv("SWARM_INVITATION_CODE", "a-different-code-that-was-never-issued")
	if err := d.BootstrapSwarm(ctx); err != nil {
		t.Fatalf("second BootstrapSwarm call: %v", err)
	}
	statusAfter, err := d.SwarmStatus()
	if err != nil {
		t.Fatalf("SwarmStatus after the second BootstrapSwarm call: %v", err)
	}
	if statusAfter.BridgeID != status.BridgeID {
		t.Fatalf("BootstrapSwarm re-joined on a later call: BridgeID changed from %s to %s", status.BridgeID, statusAfter.BridgeID)
	}
}
