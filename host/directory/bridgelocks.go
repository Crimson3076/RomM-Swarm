package directory

import (
	"sync"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// bridgeLocks serializes every Directory operation that touches one
// BridgeID's credential family, closing the race
// TestPhase2_ConcurrentRotateWithoutLockingCorruptsState (host/hoststore)
// proves BridgeCredentialStore alone does not close. See ADR 0016,
// resolved sub-decision 1: the atomicity auth.Store's doc comment asks for
// lives here, as a per-BridgeID in-process mutex held for the whole
// duration of one auth.Verifier call, not inside the Store.
//
// Correct for a single Host process, which is all that's required today.
// A multi-process Host would need a Postgres session-held advisory lock
// acquired at this same call-wrapping layer instead — recorded as an open
// question in ADR 0016, not built here.
// The lock map is never pruned — one *sync.Mutex per distinct BridgeID
// ever seen lives for the process lifetime. Acceptable at this slice's
// scale (a private, invitation-only federation); worth revisiting if the
// Bridge count ever gets large enough for that to matter.
type bridgeLocks struct {
	mu    sync.Mutex
	locks map[protocol.BridgeID]*sync.Mutex
}

// lock acquires the per-id mutex, creating it on first use, and returns a
// func to release it. Call with defer immediately so a panic still
// releases the lock.
func (b *bridgeLocks) lock(id protocol.BridgeID) (unlock func()) {
	b.mu.Lock()
	if b.locks == nil {
		b.locks = map[protocol.BridgeID]*sync.Mutex{}
	}
	l, ok := b.locks[id]
	if !ok {
		l = &sync.Mutex{}
		b.locks[id] = l
	}
	b.mu.Unlock()

	l.Lock()
	return l.Unlock
}
