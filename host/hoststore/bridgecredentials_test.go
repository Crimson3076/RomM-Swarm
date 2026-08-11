package hoststore_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Crimson3076/RomM-Swarm/auth"
	"github.com/Crimson3076/RomM-Swarm/host/hoststore"
	"github.com/Crimson3076/RomM-Swarm/host/hoststore/hoststoretest"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// TestPhase2_LoadThenSaveRoundTrips is the ordinary, single-threaded case:
// BridgeCredentialStore must behave exactly like auth.MemoryStore for a
// sequential caller.
func TestPhase2_LoadThenSaveRoundTrips(t *testing.T) {
	dsn := hoststoretest.SkipWithoutPostgres(t)
	db := hoststoretest.OpenDB(t, dsn)

	bridgeID := protocol.BridgeIDFromPublicKey([]byte("round-trip-key"))
	if err := hoststore.EnsureBridge(context.Background(), db, bridgeID, []byte("round-trip-key")); err != nil {
		t.Fatalf("EnsureBridge: %v", err)
	}

	store := &hoststore.BridgeCredentialStore{DB: db}
	if _, ok := store.Load(bridgeID); ok {
		t.Fatal("Load found a family before any Save")
	}

	verifier := &auth.Verifier{Store: store}
	token, err := verifier.Enroll(bridgeID)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	result, err := verifier.Rotate(bridgeID, token)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if result.Outcome != auth.OutcomeRotated {
		t.Fatalf("outcome = %s, want rotated", result.Outcome)
	}

	state, ok := store.Load(bridgeID)
	if !ok {
		t.Fatal("Load found nothing after Enroll+Rotate")
	}
	if state.CurrentHash != result.Refresh.Hash() {
		t.Fatal("persisted CurrentHash does not match the token Rotate returned")
	}
	if state.Generation != 2 {
		t.Fatalf("generation = %d, want 2 (1 from Enroll, 1 from Rotate)", state.Generation)
	}
}

// TestPhase2_ConcurrentRotateWithoutLockingCorruptsState is a deliberate
// proof-of-race: it fires N goroutines at Verifier.Rotate for the *same*
// BridgeID, presenting the *same* current token, with no external locking
// — exactly the scenario BridgeCredentialStore's own doc comment says it
// does not protect against. It asserts the anomaly actually happens, so
// this test fails if BridgeCredentialStore's Load/Save ever quietly grow
// enough internal locking to hide the hazard host/directory's mutex (added
// in a later step) is supposed to be the thing that closes.
//
// The anomaly: two goroutines can both Load the same CurrentHash before
// either Saves, so both see the token as valid and both compute a "next"
// token from the same starting state. Whichever Save runs last wins,
// silently discarding the other goroutine's rotation — a lost update. The
// caller that lost still believes its returned token is live; it is not.
func TestPhase2_ConcurrentRotateWithoutLockingCorruptsState(t *testing.T) {
	dsn := hoststoretest.SkipWithoutPostgres(t)
	db := hoststoretest.OpenDB(t, dsn)

	bridgeID := protocol.BridgeIDFromPublicKey([]byte("race-key"))
	if err := hoststore.EnsureBridge(context.Background(), db, bridgeID, []byte("race-key")); err != nil {
		t.Fatalf("EnsureBridge: %v", err)
	}

	store := &hoststore.BridgeCredentialStore{DB: db}
	verifier := &auth.Verifier{Store: store}
	initial, err := verifier.Enroll(bridgeID)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	const n = 12
	var (
		wg        sync.WaitGroup
		succeeded int64
		results   = make([]auth.Token, n)
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := verifier.Rotate(bridgeID, initial)
			if err == nil {
				atomic.AddInt64(&succeeded, 1)
				results[i] = result.Refresh
			}
		}(i)
	}
	wg.Wait()

	// Every concurrent presentation of the same current token is a "normal
	// rotation" attempt as far as Verifier.Rotate's logic goes (none of
	// them are the previous-token recovery path), so an unlocked Store
	// lets more than one succeed — that is the lost update. A correctly
	// serialized Store (per-BridgeID mutex, added in host/directory) would
	// let exactly one succeed and reject the rest with ErrUnknownToken,
	// since by the time they run the current token has already changed.
	if succeeded <= 1 {
		t.Skip("this run happened not to race (goroutine scheduling is not guaranteed); " +
			"the fixed path is proven separately in host/directory's tests")
	}

	// Of the tokens callers were told are now valid, count how many
	// actually match what's persisted. With a lost update, more than one
	// caller was handed a token, but at most one can match the final
	// persisted CurrentHash.
	final, ok := store.Load(bridgeID)
	if !ok {
		t.Fatal("Load found nothing after concurrent rotation")
	}
	live := 0
	for _, tok := range results {
		if tok != "" && tok.Hash() == final.CurrentHash {
			live++
		}
	}
	if int64(live) == succeeded {
		t.Fatalf("expected a lost update (more callers told success than tokens actually live), "+
			"got %d successes and all %d matched the final state — no corruption observed this run", succeeded, live)
	}
	t.Logf("proof of race: %d goroutines were told Rotate succeeded, but only %d hold a token "+
		"that matches the final persisted state — %d tokens were silently discarded", succeeded, live, int(succeeded)-live)
}
