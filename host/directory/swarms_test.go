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

func TestPhase2_DeleteSwarmRemovesEverythingScopedToItButNotTheBridge(t *testing.T) {
	d := newTestDirectory(t)
	ctx := context.Background()

	owner, err := d.BootstrapOwner(ctx, "owner", "The Owner", "", "the-password")
	if err != nil {
		t.Fatalf("BootstrapOwner: %v", err)
	}
	swarmID, err := d.CreateSwarm(ctx, owner, "Doomed Swarm")
	if err != nil {
		t.Fatalf("CreateSwarm: %v", err)
	}
	code, _, err := d.IssueInvitation(ctx, swarmID, owner, 1, 0)
	if err != nil {
		t.Fatalf("IssueInvitation: %v", err)
	}
	bridgeID, _, _, _, err := d.RedeemInvitation(ctx, code, randomKey(t))
	if err != nil {
		t.Fatalf("RedeemInvitation: %v", err)
	}

	if err := d.DeleteSwarm(ctx, owner, swarmID); err != nil {
		t.Fatalf("DeleteSwarm: %v", err)
	}

	if _, err := d.GetSwarm(ctx, owner, swarmID); err != directory.ErrSwarmNotFound {
		t.Fatalf("GetSwarm after delete: err = %v, want ErrSwarmNotFound", err)
	}
	if invitations, err := d.ListInvitations(ctx, swarmID); err != nil || len(invitations) != 0 {
		t.Fatalf("ListInvitations after delete = %+v (err %v), want none", invitations, err)
	}

	var swarmCount, invitationCount, membershipCount int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM swarms WHERE id = $1`, string(swarmID)).Scan(&swarmCount); err != nil {
		t.Fatalf("counting swarms: %v", err)
	}
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM invitations WHERE swarm_id = $1`, string(swarmID)).Scan(&invitationCount); err != nil {
		t.Fatalf("counting invitations: %v", err)
	}
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM bridge_swarm_memberships WHERE swarm_id = $1`, string(swarmID)).Scan(&membershipCount); err != nil {
		t.Fatalf("counting bridge_swarm_memberships: %v", err)
	}
	if swarmCount != 0 || invitationCount != 0 || membershipCount != 0 {
		t.Fatalf("rows survived deletion: swarms=%d invitations=%d bridge_swarm_memberships=%d", swarmCount, invitationCount, membershipCount)
	}

	// The Bridge's own global identity must survive — DeleteSwarm never
	// touches the bridges table.
	var bridgeCount int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM bridges WHERE id = $1`, string(bridgeID)).Scan(&bridgeCount); err != nil {
		t.Fatalf("counting bridges: %v", err)
	}
	if bridgeCount != 1 {
		t.Fatalf("the Bridge's own identity row was deleted along with the Swarm (count=%d)", bridgeCount)
	}
}

func TestPhase2_DeleteSwarmIsScopedPerAccount(t *testing.T) {
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

	other := protocol.NewUserID()
	if err := d.DeleteSwarm(ctx, other, swarmID); err != directory.ErrSwarmNotFound {
		t.Fatalf("DeleteSwarm by an unrelated account: err = %v, want ErrSwarmNotFound", err)
	}

	// Untouched by the rejected attempt.
	if _, err := d.GetSwarm(ctx, owner, swarmID); err != nil {
		t.Fatalf("GetSwarm after a rejected delete attempt: %v", err)
	}
}
