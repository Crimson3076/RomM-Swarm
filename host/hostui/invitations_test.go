package hostui

import (
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/host/directory"
)

func TestInvitationStatuses(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		inv  directory.Invitation
		want string
	}{
		{"active", directory.Invitation{MaxUses: 2, UseCount: 1, ExpiresAt: now.Add(time.Hour)}, "active"},
		{"expired", directory.Invitation{MaxUses: 2, ExpiresAt: now}, "expired"},
		{"exhausted", directory.Invitation{MaxUses: 2, UseCount: 2, ExpiresAt: now.Add(time.Hour)}, "exhausted"},
		{"revoked", directory.Invitation{MaxUses: 2, ExpiresAt: now.Add(time.Hour), RevokedAt: now}, "revoked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := invitationStatus(tc.inv, now); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
