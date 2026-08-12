package directory_test

import (
	"context"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

func TestPhase2_IssueInvitationStoresOnlyTheHash(t *testing.T) {
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

	code, id, err := d.IssueInvitation(ctx, swarmID, owner, 0, 0)
	if err != nil {
		t.Fatalf("IssueInvitation: %v", err)
	}
	if code == "" {
		t.Fatal("IssueInvitation returned an empty code")
	}
	if err := id.Validate(); err != nil {
		t.Fatalf("IssueInvitation returned an invalid InvitationID: %v", err)
	}

	var storedHash string
	if err := d.DB.QueryRowContext(ctx, `SELECT code_hash FROM invitations WHERE id = $1`, string(id)).Scan(&storedHash); err != nil {
		t.Fatalf("reading stored invitation: %v", err)
	}
	if storedHash == string(code) {
		t.Fatal("the plaintext invitation code was stored instead of its hash")
	}
}

func TestPhase2_IssueInvitationDefaultsMaxUsesAndExpiry(t *testing.T) {
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

	_, id, err := d.IssueInvitation(ctx, swarmID, owner, 0, 0)
	if err != nil {
		t.Fatalf("IssueInvitation: %v", err)
	}

	var (
		maxUses   int
		expiresAt time.Time
	)
	if err := d.DB.QueryRowContext(ctx, `SELECT max_uses, expires_at FROM invitations WHERE id = $1`, string(id)).
		Scan(&maxUses, &expiresAt); err != nil {
		t.Fatalf("reading stored invitation: %v", err)
	}
	if maxUses != 1 {
		t.Fatalf("default max_uses = %d, want 1", maxUses)
	}
	if !expiresAt.After(time.Now().Add(24 * time.Hour)) {
		t.Fatalf("default expiry %s is not comfortably in the future", expiresAt)
	}
}

func TestPhase2_ListInvitationsNeverIncludesTheCode(t *testing.T) {
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

	_, id, err := d.IssueInvitation(ctx, swarmID, owner, 3, time.Hour)
	if err != nil {
		t.Fatalf("IssueInvitation: %v", err)
	}

	invitations, err := d.ListInvitations(ctx, swarmID)
	if err != nil {
		t.Fatalf("ListInvitations: %v", err)
	}
	if len(invitations) != 1 {
		t.Fatalf("ListInvitations returned %d entries, want 1", len(invitations))
	}
	got := invitations[0]
	if got.ID != id {
		t.Fatalf("ListInvitations returned id %s, want %s", got.ID, id)
	}
	if got.MaxUses != 3 || got.UseCount != 0 {
		t.Fatalf("ListInvitations = %+v, want MaxUses 3, UseCount 0", got)
	}
	if !got.RevokedAt.IsZero() {
		t.Fatalf("a fresh invitation reports a revocation time: %+v", got)
	}
}

func TestPhase2_DeleteInvitationRemovesItWithoutTouchingTheBridgeItEnrolled(t *testing.T) {
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
	code, invitationID, err := d.IssueInvitation(ctx, swarmID, owner, 1, 0)
	if err != nil {
		t.Fatalf("IssueInvitation: %v", err)
	}
	bridgeID, _, _, _, err := d.RedeemInvitation(ctx, code, randomKey(t))
	if err != nil {
		t.Fatalf("RedeemInvitation: %v", err)
	}

	if err := d.DeleteInvitation(ctx, swarmID, invitationID); err != nil {
		t.Fatalf("DeleteInvitation: %v", err)
	}

	invitations, err := d.ListInvitations(ctx, swarmID)
	if err != nil {
		t.Fatalf("ListInvitations: %v", err)
	}
	if len(invitations) != 0 {
		t.Fatalf("ListInvitations after delete = %+v, want none", invitations)
	}

	// The Bridge it enrolled is still a member — deleting the invitation
	// only forgets which one it came in through.
	bridges, err := d.ListBridgesForSwarm(ctx, swarmID)
	if err != nil {
		t.Fatalf("ListBridgesForSwarm: %v", err)
	}
	if len(bridges) != 1 || bridges[0].BridgeID != bridgeID {
		t.Fatalf("ListBridgesForSwarm after deleting its invitation = %+v, want the Bridge still listed", bridges)
	}
}

func TestPhase2_DeleteInvitationRejectsAnUnknownID(t *testing.T) {
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

	if err := d.DeleteInvitation(ctx, swarmID, protocol.NewInvitationID()); err != directory.ErrInvitationNotFound {
		t.Fatalf("DeleteInvitation for an unknown id: err = %v, want ErrInvitationNotFound", err)
	}
}
