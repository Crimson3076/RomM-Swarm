package directory_test

import (
	"context"
	"testing"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

func TestPhase2_CreateSwarmCreatesOwnerMembership(t *testing.T) {
	d := newTestDirectory(t)
	ctx := context.Background()

	owner, err := d.BootstrapOwner(ctx, "owner", "The Owner", "", "the-password")
	if err != nil {
		t.Fatalf("BootstrapOwner: %v", err)
	}

	swarmID, err := d.CreateSwarm(ctx, owner, "Test Swarm")
	if err != nil {
		t.Fatalf("CreateSwarm: %v", err)
	}
	if err := swarmID.Validate(); err != nil {
		t.Fatalf("CreateSwarm returned an invalid Swarm id: %v", err)
	}

	swarms, err := d.ListSwarms(ctx, owner)
	if err != nil {
		t.Fatalf("ListSwarms: %v", err)
	}
	if len(swarms) != 1 || swarms[0].ID != swarmID || swarms[0].Name != "Test Swarm" {
		t.Fatalf("ListSwarms = %+v, want exactly one entry for %s", swarms, swarmID)
	}
}

func TestPhase2_ListSwarmsIsScopedPerAccount(t *testing.T) {
	d := newTestDirectory(t)
	ctx := context.Background()

	owner, err := d.BootstrapOwner(ctx, "owner", "The Owner", "", "the-password")
	if err != nil {
		t.Fatalf("BootstrapOwner: %v", err)
	}
	if _, err := d.CreateSwarm(ctx, owner, "Owner's Swarm"); err != nil {
		t.Fatalf("CreateSwarm: %v", err)
	}

	other := protocol.NewUserID()
	swarms, err := d.ListSwarms(ctx, other)
	if err != nil {
		t.Fatalf("ListSwarms: %v", err)
	}
	if len(swarms) != 0 {
		t.Fatalf("ListSwarms for an unrelated account returned %d Swarms, want 0", len(swarms))
	}
}

func TestPhase2_GetSwarmIsScopedPerAccount(t *testing.T) {
	d := newTestDirectory(t)
	ctx := context.Background()

	owner, err := d.BootstrapOwner(ctx, "owner", "The Owner", "", "the-password")
	if err != nil {
		t.Fatalf("BootstrapOwner: %v", err)
	}
	swarmID, err := d.CreateSwarm(ctx, owner, "Owner's Swarm")
	if err != nil {
		t.Fatalf("CreateSwarm: %v", err)
	}

	got, err := d.GetSwarm(ctx, owner, swarmID)
	if err != nil {
		t.Fatalf("GetSwarm: %v", err)
	}
	if got.ID != swarmID || got.Name != "Owner's Swarm" {
		t.Fatalf("GetSwarm = %+v, want id %s", got, swarmID)
	}

	other := protocol.NewUserID()
	if _, err := d.GetSwarm(ctx, other, swarmID); err != directory.ErrSwarmNotFound {
		t.Fatalf("GetSwarm for an unrelated account: err = %v, want ErrSwarmNotFound", err)
	}

	if _, err := d.GetSwarm(ctx, owner, protocol.NewSwarmID()); err != directory.ErrSwarmNotFound {
		t.Fatalf("GetSwarm for a nonexistent Swarm: err = %v, want ErrSwarmNotFound", err)
	}
}
