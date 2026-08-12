package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

func bridgeID(seed string) protocol.BridgeID {
	return protocol.BridgeIDFromPublicKey([]byte(seed))
}

// harness wires a Host verifier to a Bridge file store with a controllable
// clock, which is what the crash simulations need.
type harness struct {
	t        *testing.T
	verifier *Verifier
	store    *FileStore
	client   *Client
	bridge   protocol.BridgeID
	clock    time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	h := &harness{
		t:      t,
		bridge: bridgeID("crash-test bridge"),
		clock:  time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
	}
	h.verifier = &Verifier{
		Store: NewMemoryStore(),
		Now:   func() time.Time { return h.clock },
	}
	h.store = NewFileStore(filepath.Join(t.TempDir(), "credential.json"))
	h.client = &Client{Store: h.store, Rotate: h.verifier.Rotate}

	token, err := h.verifier.Enroll(h.bridge)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if err := h.store.Save(Credential{BridgeID: h.bridge, Refresh: token, Generation: 1}); err != nil {
		t.Fatalf("persisting the first credential: %v", err)
	}
	return h
}

// advance moves the shared clock.
func (h *harness) advance(d time.Duration) { h.clock = h.clock.Add(d) }

// storedToken is what the Bridge currently holds on disk.
func (h *harness) storedToken() Token {
	h.t.Helper()
	c, err := h.store.Load()
	if err != nil {
		h.t.Fatalf("loading the stored credential: %v", err)
	}
	return c.Refresh
}

// TestPhase0_RefreshRotationSurvivesACrashAtEveryStep is Phase 2 acceptance,
// brought forward because Phase 0 must "Define the rotating-refresh recovery
// protocol": "A simulated crash at each refresh-rotation step either recovers
// safely or requires explicit re-enrollment without accepting replay from
// another Bridge identity."
//
// The steps below are every point at which a Bridge can lose power during one
// rotation. Each is exercised, and after each the Bridge must either carry on or
// be plainly told to re-enrol — never end up silently unable to authenticate,
// and never leave a credential usable from elsewhere.
func TestPhase0_RefreshRotationSurvivesACrashAtEveryStep(t *testing.T) {
	steps := []struct {
		name string
		// crashAfter names the point inside the Bridge's atomic write, or is
		// empty for crashes outside it.
		crashAfter string
		// beforeSend crashes the Bridge before it ever contacts the Host.
		beforeSend bool
		// dropResponse lets the Host rotate but loses the reply.
		dropResponse bool
	}{
		{name: "before the request is sent", beforeSend: true},
		{name: "after the Host rotated but the reply was lost", dropResponse: true},
		{name: "after writing the temporary file", crashAfter: "write"},
		{name: "after flushing the temporary file", crashAfter: "fsync"},
		{name: "after the rename, before the directory flush", crashAfter: "rename"},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			h := newHarness(t)
			before := h.storedToken()

			switch {
			case step.beforeSend:
				// Nothing happened at all. The stored credential is still
				// current, so the next attempt is an ordinary rotation.

			case step.dropResponse:
				// The Host rotated; the Bridge never learned the new token.
				if _, err := h.verifier.Rotate(h.bridge, before); err != nil {
					t.Fatalf("the Host rotation itself failed: %v", err)
				}

			default:
				h.store.crashAfter = step.crashAfter
				_, err := h.client.Refresh()
				if !errors.Is(err, errSimulatedCrash) {
					t.Fatalf("expected a simulated crash, got %v", err)
				}
				h.store.crashAfter = ""
			}

			// The Bridge restarts a moment later and retries.
			h.advance(2 * time.Second)

			result, err := h.client.Refresh()
			if err != nil {
				t.Fatalf("the Bridge could not recover after a crash %s: %v", step.name, err)
			}

			// It must now hold a working credential, and the recovery must have
			// been either an ordinary rotation or the bounded grace path.
			switch result.Outcome {
			case OutcomeRotated, OutcomeRecovered:
			default:
				t.Fatalf("unexpected outcome %q", result.Outcome)
			}
			if h.storedToken() != result.Refresh {
				t.Fatal("the Bridge did not persist the credential it was issued")
			}

			// And the recovered credential must keep working.
			if _, err := h.client.Refresh(); err != nil {
				t.Fatalf("the recovered credential did not survive a further rotation: %v", err)
			}
		})
	}
}

// TestPhase0_RecoveryIsBoundToTheBridgeIdentity is the qualifier that keeps the
// grace path from being a hole: a captured credential must not be usable from
// anywhere else, even inside the window.
func TestPhase0_RecoveryIsBoundToTheBridgeIdentity(t *testing.T) {
	h := newHarness(t)
	captured := h.storedToken()

	// The legitimate rotation happens; the reply is lost. The captured token is
	// now the "previous" token and the grace window is open.
	if _, err := h.verifier.Rotate(h.bridge, captured); err != nil {
		t.Fatalf("the legitimate rotation failed: %v", err)
	}
	h.advance(time.Second)

	// An attacker presents it from a different Bridge identity, well inside the
	// window.
	attacker := bridgeID("attacker bridge")
	if _, err := h.verifier.Rotate(attacker, captured); err == nil {
		t.Fatal("a captured credential was accepted from a different Bridge identity")
	}

	// The legitimate Bridge must be unaffected by the attempt.
	if _, err := h.client.Refresh(); err != nil {
		t.Fatalf("the attacker's attempt broke the legitimate Bridge: %v", err)
	}
}

// TestPhase0_ReuseOutsideTheGracePathRevokesTheFamily is the reuse detection the
// rotation scheme exists for.
func TestPhase0_ReuseOutsideTheGracePathRevokesTheFamily(t *testing.T) {
	t.Run("a second use of the previous token", func(t *testing.T) {
		h := newHarness(t)
		spent := h.storedToken()

		// Legitimate rotation, reply lost, Bridge recovers through the grace
		// path. That consumes the previous token.
		if _, err := h.verifier.Rotate(h.bridge, spent); err != nil {
			t.Fatalf("rotation: %v", err)
		}
		h.advance(time.Second)
		if _, err := h.client.Refresh(); err != nil {
			t.Fatalf("recovery: %v", err)
		}

		// Someone else still holds the spent token and tries it.
		_, err := h.verifier.Rotate(h.bridge, spent)
		if !errors.Is(err, ErrReuseDetected) {
			t.Fatalf("a twice-used credential returned %v, want reuse detection", err)
		}

		// The whole family is now dead, including the Bridge's good credential.
		// That is the intended, expensive consequence of a detected compromise.
		if _, err := h.client.Refresh(); !errors.Is(err, ErrFamilyRevoked) {
			t.Fatalf("after reuse detection the Bridge got %v, want the family revoked", err)
		}
	})

	t.Run("the previous token after the window closes", func(t *testing.T) {
		h := newHarness(t)
		spent := h.storedToken()

		if _, err := h.verifier.Rotate(h.bridge, spent); err != nil {
			t.Fatalf("rotation: %v", err)
		}
		h.advance(GraceWindow + time.Second)

		_, err := h.verifier.Rotate(h.bridge, spent)
		if !errors.Is(err, ErrReuseDetected) {
			t.Fatalf("a credential presented after the window returned %v, want reuse detection", err)
		}
	})
}

// TestPhase0_OwnerReEnrolmentIsTheOnlyEscapeFromRevocation records that nothing
// automatic can resurrect a revoked family, because an automatic escape from
// revocation is the same thing as not revoking.
func TestPhase0_OwnerReEnrolmentIsTheOnlyEscapeFromRevocation(t *testing.T) {
	h := newHarness(t)
	spent := h.storedToken()

	if _, err := h.verifier.Rotate(h.bridge, spent); err != nil {
		t.Fatalf("rotation: %v", err)
	}
	h.advance(GraceWindow + time.Second)
	if _, err := h.verifier.Rotate(h.bridge, spent); !errors.Is(err, ErrReuseDetected) {
		t.Fatalf("expected reuse detection, got %v", err)
	}

	// Every automatic route is closed.
	if _, err := h.client.Refresh(); !errors.Is(err, ErrFamilyRevoked) {
		t.Errorf("a revoked family still answered a refresh: %v", err)
	}
	if _, err := h.verifier.Rotate(h.bridge, NewToken()); !errors.Is(err, ErrFamilyRevoked) {
		t.Errorf("a revoked family answered an arbitrary token: %v", err)
	}

	// The owner re-enrols. Only now does the Bridge work again.
	fresh, err := h.verifier.ReEnroll(h.bridge)
	if err != nil {
		t.Fatalf("ReEnroll: %v", err)
	}
	if err := h.store.Save(Credential{BridgeID: h.bridge, Refresh: fresh, Generation: 1}); err != nil {
		t.Fatalf("persisting the re-enrolled credential: %v", err)
	}
	if _, err := h.client.Refresh(); err != nil {
		t.Fatalf("the Bridge did not work after re-enrolment: %v", err)
	}

	// And the old credentials stay dead.
	if _, err := h.verifier.Rotate(h.bridge, spent); err == nil {
		t.Error("a credential from before re-enrolment was accepted")
	}
}

func TestGraceWindowMatchesTheScopeOfWork(t *testing.T) {
	// Scope of Work Phase 2: "A 30 to 60 second, one-use previous-token grace
	// path".
	if GraceWindow < 30*time.Second || GraceWindow > 60*time.Second {
		t.Fatalf("grace window is %v, outside the specified 30 to 60 seconds", GraceWindow)
	}
}

func TestRotationRejectsUnknownAndForgedTokens(t *testing.T) {
	h := newHarness(t)

	// An unrecognised token must not revoke the family: otherwise anyone who
	// learns a Bridge id can revoke it by sending garbage.
	if _, err := h.verifier.Rotate(h.bridge, NewToken()); !errors.Is(err, ErrUnknownToken) {
		t.Fatalf("a forged token returned %v, want it to be unrecognised", err)
	}
	if _, err := h.client.Refresh(); err != nil {
		t.Fatalf("a forged token attempt broke the legitimate Bridge: %v", err)
	}

	// A token for a Bridge that never enrolled.
	if _, err := h.verifier.Rotate(bridgeID("never enrolled"), NewToken()); !errors.Is(err, ErrUnknownToken) {
		t.Error("an unenrolled Bridge was not reported as unknown")
	}
}

func TestAdministrativeRevocationStopsTheBridge(t *testing.T) {
	h := newHarness(t)

	if err := h.verifier.Revoke(h.bridge, "the owner disabled this Bridge"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := h.client.Refresh(); !errors.Is(err, ErrFamilyRevoked) {
		t.Fatalf("a revoked Bridge still refreshed: %v", err)
	}
}

// TestAuthenticateAcceptsOnlyTheCurrentTokenAndNeverMutates is ADR 0019's
// proof for the new read-only check: it accepts the live token exactly
// like Rotate's normal path would, rejects the previous token even inside
// the grace window (unlike Rotate, which would recover through it), and —
// the property that actually matters — never calls Store.Save, so the
// FamilyState is byte-identical before and after every call, successful or
// not.
func TestAuthenticateAcceptsOnlyTheCurrentTokenAndNeverMutates(t *testing.T) {
	h := newHarness(t)
	current := h.storedToken()

	before, ok := h.verifier.Store.Load(h.bridge)
	if !ok {
		t.Fatalf("the harness's own Bridge is not enrolled")
	}

	if err := h.verifier.Authenticate(h.bridge, current); err != nil {
		t.Fatalf("Authenticate rejected the live current token: %v", err)
	}
	after, _ := h.verifier.Store.Load(h.bridge)
	if after != before {
		t.Fatalf("Authenticate mutated FamilyState: before %+v, after %+v", before, after)
	}

	// Rotate for real, so `current` is now the previous token, still
	// inside the grace window Rotate itself would recover through.
	if _, err := h.verifier.Rotate(h.bridge, current); err != nil {
		t.Fatalf("rotation: %v", err)
	}
	stateAfterRotate, _ := h.verifier.Store.Load(h.bridge)

	if err := h.verifier.Authenticate(h.bridge, current); !errors.Is(err, ErrUnknownToken) {
		t.Fatalf("Authenticate accepted a previous/spent token: %v, want ErrUnknownToken (Authenticate has no grace-window recovery)", err)
	}
	stateAfterFailedAuth, _ := h.verifier.Store.Load(h.bridge)
	if stateAfterFailedAuth != stateAfterRotate {
		t.Fatalf("a failed Authenticate call mutated FamilyState: before %+v, after %+v", stateAfterRotate, stateAfterFailedAuth)
	}

	// A forged/unknown token.
	if err := h.verifier.Authenticate(h.bridge, NewToken()); !errors.Is(err, ErrUnknownToken) {
		t.Fatalf("Authenticate accepted a forged token: %v", err)
	}

	// A Bridge that never enrolled.
	if err := h.verifier.Authenticate(bridgeID("never enrolled"), NewToken()); !errors.Is(err, ErrUnknownToken) {
		t.Fatalf("Authenticate accepted a token for an unenrolled Bridge: %v", err)
	}

	// A revoked family.
	if err := h.verifier.Revoke(h.bridge, "testing Authenticate against a revoked family"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := h.verifier.Authenticate(h.bridge, current); !errors.Is(err, ErrFamilyRevoked) {
		t.Fatalf("Authenticate accepted a token from a revoked family: %v, want ErrFamilyRevoked", err)
	}
}

func TestRotationEventsAreDistinguishable(t *testing.T) {
	h := newHarness(t)

	normal, err := h.client.Refresh()
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if normal.Event != protocol.EventTokenRotated {
		t.Errorf("a normal rotation recorded %s, want %s", normal.Event, protocol.EventTokenRotated)
	}

	// Force the grace path and check it is recorded separately: an operator
	// seeing a rise in grace-path use is seeing unstable Bridges, which is a
	// different problem from seeing reuse detection.
	stored := h.storedToken()
	if _, err := h.verifier.Rotate(h.bridge, stored); err != nil {
		t.Fatalf("rotation: %v", err)
	}
	h.advance(time.Second)
	recovered, err := h.client.Refresh()
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if recovered.Event != protocol.EventTokenGraceUsed {
		t.Errorf("a recovery recorded %s, want %s", recovered.Event, protocol.EventTokenGraceUsed)
	}
	if recovered.Outcome != OutcomeRecovered {
		t.Errorf("outcome %s, want %s", recovered.Outcome, OutcomeRecovered)
	}
}

func TestAccessTokenLifetimeBoundsRevocationDelay(t *testing.T) {
	// Phase 2 acceptance: "Disabling a Bridge prevents new API activity within
	// the access-token lifetime." This constant is that worst case.
	if AccessTokenLifetime > 15*time.Minute {
		t.Fatalf("access token lifetime is %v, too long to be a credible revocation bound", AccessTokenLifetime)
	}

	h := newHarness(t)
	result, err := h.client.Refresh()
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !result.AccessExpiresAt.Equal(h.clock.Add(AccessTokenLifetime)) {
		t.Errorf("access expiry is %v, want %v", result.AccessExpiresAt, h.clock.Add(AccessTokenLifetime))
	}
}

func TestTokenHashesDoNotRevealTheToken(t *testing.T) {
	token := NewToken()
	if string(token) == token.Hash() {
		t.Fatal("the stored hash is the token itself")
	}
	if len(token.Hash()) != 64 {
		t.Fatalf("hash is %d characters, want a 64-character SHA-256", len(token.Hash()))
	}
	// Two tokens must not collide, and hashing must be stable.
	if token.Hash() != token.Hash() {
		t.Fatal("hashing is not deterministic")
	}
	if token.Hash() == NewToken().Hash() {
		t.Fatal("two distinct tokens hashed identically")
	}
}

func TestCorruptCredentialFileReadsAsNoCredential(t *testing.T) {
	h := newHarness(t)

	if err := os.WriteFile(h.store.Path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupting the credential: %v", err)
	}
	_, err := h.store.Load()
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("a corrupt credential returned %v, want it to read as no credential", err)
	}
}

func TestSaveLeavesNoTemporaryFilesBehind(t *testing.T) {
	h := newHarness(t)
	dir := filepath.Dir(h.store.Path)

	for _, step := range []string{"write", "fsync", ""} {
		h.store.crashAfter = step
		_ = h.store.Save(Credential{BridgeID: h.bridge, Refresh: NewToken(), Generation: 9})
	}
	h.store.crashAfter = ""

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the credential directory: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "credential.json" {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}
