package directory_test

import (
	"context"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
	"github.com/Crimson3076/RomM-Swarm/host/hoststore/hoststoretest"
)

func newTestDirectory(t *testing.T) *directory.Directory {
	t.Helper()
	dsn := hoststoretest.SkipWithoutPostgres(t)
	db := hoststoretest.OpenDB(t, dsn)
	return directory.New(db)
}

func TestPhase2_OwnerExists(t *testing.T) {
	d := newTestDirectory(t)
	ctx := context.Background()

	if exists, err := d.OwnerExists(ctx); err != nil || exists {
		t.Fatalf("OwnerExists before bootstrap = %v, %v — want false, no error", exists, err)
	}
	if _, err := d.BootstrapOwner(ctx, "owner", "The Owner", "", "a-long-enough-password"); err != nil {
		t.Fatalf("BootstrapOwner: %v", err)
	}
	if exists, err := d.OwnerExists(ctx); err != nil || !exists {
		t.Fatalf("OwnerExists after bootstrap = %v, %v — want true, no error", exists, err)
	}
}

func TestPhase2_BootstrapOwnerSucceedsOnceThenFails(t *testing.T) {
	d := newTestDirectory(t)
	ctx := context.Background()

	id, err := d.BootstrapOwner(ctx, "owner", "The Owner", "", "a-long-enough-password")
	if err != nil {
		t.Fatalf("BootstrapOwner: %v", err)
	}
	if id == "" {
		t.Fatal("BootstrapOwner returned an empty user id")
	}

	if _, err := d.BootstrapOwner(ctx, "someone-else", "Someone Else", "", "another-password"); err != directory.ErrOwnerAlreadyExists {
		t.Fatalf("second BootstrapOwner: err = %v, want ErrOwnerAlreadyExists", err)
	}
}

func TestPhase2_AuthenticateAcceptsCorrectRejectsWrongPassword(t *testing.T) {
	d := newTestDirectory(t)
	ctx := context.Background()

	if _, err := d.BootstrapOwner(ctx, "owner", "The Owner", "owner@example.com", "correct-password"); err != nil {
		t.Fatalf("BootstrapOwner: %v", err)
	}

	if _, _, err := d.Authenticate(ctx, "owner", "wrong-password"); err != directory.ErrInvalidCredentials {
		t.Fatalf("wrong password: err = %v, want ErrInvalidCredentials", err)
	}
	if _, _, err := d.Authenticate(ctx, "nobody", "correct-password"); err != directory.ErrInvalidCredentials {
		t.Fatalf("unknown username: err = %v, want ErrInvalidCredentials (not a distinct error)", err)
	}

	token, expiresAt, err := d.Authenticate(ctx, "owner", "correct-password")
	if err != nil {
		t.Fatalf("correct password: %v", err)
	}
	if token == "" {
		t.Fatal("Authenticate returned an empty token")
	}
	if !expiresAt.After(time.Now()) {
		t.Fatal("Authenticate returned an already-expired session")
	}
}

func TestPhase2_SessionValidationAndLogout(t *testing.T) {
	d := newTestDirectory(t)
	ctx := context.Background()

	ownerID, err := d.BootstrapOwner(ctx, "owner", "The Owner", "", "the-password")
	if err != nil {
		t.Fatalf("BootstrapOwner: %v", err)
	}
	token, _, err := d.Authenticate(ctx, "owner", "the-password")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	got, err := d.ValidateSession(ctx, token)
	if err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}
	if got != ownerID {
		t.Fatalf("ValidateSession returned %s, want %s", got, ownerID)
	}

	if err := d.Logout(ctx, token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := d.ValidateSession(ctx, token); err != directory.ErrSessionInvalid {
		t.Fatalf("ValidateSession after logout: err = %v, want ErrSessionInvalid", err)
	}
}

func TestPhase2_ExpiredSessionIsRejected(t *testing.T) {
	d := newTestDirectory(t)
	now := time.Now()
	d.Now = func() time.Time { return now }
	ctx := context.Background()

	if _, err := d.BootstrapOwner(ctx, "owner", "The Owner", "", "the-password"); err != nil {
		t.Fatalf("BootstrapOwner: %v", err)
	}
	token, _, err := d.Authenticate(ctx, "owner", "the-password")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	// Advance the clock past the session lifetime.
	d.Now = func() time.Time { return now.Add(25 * time.Hour) }
	if _, err := d.ValidateSession(ctx, token); err != directory.ErrSessionInvalid {
		t.Fatalf("ValidateSession after expiry: err = %v, want ErrSessionInvalid", err)
	}
}
