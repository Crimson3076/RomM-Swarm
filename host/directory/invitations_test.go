package directory_test

import (
	"context"
	"testing"
	"time"
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
