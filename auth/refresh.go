// Package auth implements the rotating refresh credential protocol shared by
// the Network Host and the Bridge.
//
// Scope of Work Phase 0 deliverable: "Define the rotating-refresh recovery
// protocol, including current and previous token hashes, a bounded grace
// window, transactional persistence, and owner re-enrollment." The decision log
// gives the reason: "Prevent routine Bridge crashes from becoming
// credential-reuse incidents."
//
// # The problem
//
// Refresh-token rotation with reuse detection is the standard defence against a
// stolen refresh token: each use issues a new token and invalidates the old one,
// so a thief and the legitimate holder cannot both keep using it — the second
// one to present the old token reveals the theft.
//
// The trouble is that a Bridge runs on someone's home server. It gets power-cut,
// OOM-killed, and restarted mid-upgrade. Rotation has an unavoidable window: the
// Host has issued a new token and forgotten the old one, and the Bridge has not
// yet received or persisted the new one. A crash there leaves the Bridge holding
// a token the Host considers spent.
//
// Without a recovery path, every such crash looks exactly like a stolen token,
// revokes the Bridge, and requires the owner to re-enrol. That is a system that
// punishes people for having unreliable electricity, and one whose alerts
// everyone learns to ignore.
//
// # The protocol
//
// The Host keeps two hashes per token family: current and previous.
//
//   - Presenting the current token is a normal rotation.
//   - Presenting the previous token, inside a bounded grace window, from the same
//     Bridge identity, and only once, is the recovery path. It succeeds and
//     issues a fresh token.
//   - Presenting the previous token outside the window, or a second time, or from
//     a different Bridge identity, is reuse. The whole family is revoked and the
//     owner must re-enrol.
//
// The three qualifiers on the recovery path are what keep it from being a hole:
//
//   - **Bounded window.** A stolen token is useful for at most the grace period
//     after the legitimate rotation, not indefinitely.
//   - **One use.** A thief who races the legitimate Bridge wins once; the
//     Bridge's next attempt then trips reuse detection and revokes everything.
//   - **Same Bridge identity.** The grace path is bound to the identity key, so a
//     captured token cannot be replayed from anywhere else at all. This is the
//     qualifier that matters most, and Scope of Work Phase 2 calls for it
//     explicitly: recovery must never accept "replay from another Bridge
//     identity".
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// GraceWindow is how long after a rotation the previous token remains usable on
// the recovery path.
//
// Scope of Work Phase 2 specifies 30 to 60 seconds. 45 is taken as the middle of
// that range. The window only has to cover the gap between the Host committing a
// rotation and the Bridge persisting the result — a network round trip and one
// fsync. Seconds are generous; minutes would widen the theft window for no
// operational gain.
const GraceWindow = 45 * time.Second

// AccessTokenLifetime bounds how long a Bridge may act on one access token.
//
// Phase 2 acceptance: "Disabling a Bridge prevents new API activity within the
// access-token lifetime." That criterion turns this constant into the Host's
// worst-case revocation delay, so it is deliberately short.
const AccessTokenLifetime = 5 * time.Minute

// tokenBytes is the entropy in a refresh token.
const tokenBytes = 32

// Errors returned by the Host-side verifier. They are distinguished because the
// Host reacts differently to each: a revoked family is a security event, an
// unknown token is noise, and a wrong Bridge is an attack.
var (
	// ErrUnknownToken means the presented token matches no family. Not
	// necessarily an attack; an old backup being restored looks like this.
	ErrUnknownToken = errors.New("auth: refresh token is not recognised")

	// ErrFamilyRevoked means the family was already revoked. The Bridge owner
	// must re-enrol.
	ErrFamilyRevoked = errors.New("auth: this credential family has been revoked; the Bridge owner must re-enrol")

	// ErrReuseDetected means a spent token was presented. The family has just
	// been revoked as a result.
	ErrReuseDetected = errors.New("auth: a spent refresh token was presented; the credential family has been revoked")

	// ErrWrongBridge means the token belongs to a different Bridge identity than
	// the one presenting it.
	ErrWrongBridge = errors.New("auth: refresh token does not belong to the presenting Bridge identity")

	// ErrGraceExpired means the previous token was presented after the grace
	// window closed.
	ErrGraceExpired = errors.New("auth: the recovery window for the previous credential has closed; the Bridge owner must re-enrol")
)

// Token is a refresh credential in the form handed to a Bridge. It exists only
// in transit and in the Bridge's own store; the Host keeps a hash.
type Token string

// NewToken mints a refresh credential.
func NewToken() Token {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		panic("auth: crypto/rand unavailable: " + err.Error())
	}
	return Token(base64.RawURLEncoding.EncodeToString(b))
}

// Hash returns the stored form of a token. The Host never holds the token
// itself, so a Host database leak does not yield usable credentials.
func (t Token) Hash() string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// equalHash compares two hashes without leaking timing information.
func equalHash(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// FamilyState is the Host's record for one Bridge's credential family.
//
// The name "family" is the point: every token descended from one enrolment
// belongs to one family, and reuse anywhere in it revokes the whole lineage. A
// thief cannot keep a branch alive.
type FamilyState struct {
	BridgeID protocol.BridgeID

	// CurrentHash is the token the Bridge is expected to hold.
	CurrentHash string

	// PreviousHash is the token it held before the last rotation, retained for
	// the grace window. Empty once consumed or expired.
	PreviousHash string

	// PreviousIssuedAt is when PreviousHash stopped being current. The grace
	// window is measured from here.
	PreviousIssuedAt time.Time

	// PreviousConsumed records that the recovery path has already been taken for
	// this previous token. It is what makes the path one-use.
	PreviousConsumed bool

	// Generation counts rotations, for operator diagnostics.
	Generation uint64

	// Revoked ends the family permanently.
	Revoked bool

	// RevokedReason explains why, for the audit record.
	RevokedReason string
}

// Outcome describes how a rotation was satisfied.
type Outcome string

const (
	// OutcomeRotated is the normal path: the current token was presented.
	OutcomeRotated Outcome = "rotated"

	// OutcomeRecovered is the grace path: the previous token was presented
	// inside the window by the same Bridge, and a fresh credential was issued.
	// This is the outcome that turns a crash into a non-event.
	OutcomeRecovered Outcome = "recovered"
)

// Result is a successful rotation.
type Result struct {
	Outcome Outcome

	// Refresh is the new refresh credential. The Bridge must persist it
	// transactionally before using it.
	Refresh Token

	// AccessExpiresAt bounds the access token issued alongside it.
	AccessExpiresAt time.Time

	Generation uint64

	// Event is the event kind the Host should record for this rotation.
	Event protocol.EventKind
}

// Store is the Host's persistence for credential families.
//
// An interface rather than a concrete type because the Phase 2 implementation is
// PostgreSQL and this package must not depend on it. The contract that matters:
// Load and Save must be callable inside one transaction, because a rotation that
// is not atomic reintroduces exactly the race this protocol exists to close.
type Store interface {
	// Load returns the family for a Bridge, or false if none exists.
	Load(protocol.BridgeID) (FamilyState, bool)

	// Save writes the family back.
	Save(FamilyState) error
}

// Verifier performs rotations against a Store.
type Verifier struct {
	Store Store

	// Grace overrides GraceWindow. Zero means the default.
	Grace time.Duration

	// Now overrides the clock, for tests.
	Now func() time.Time
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

func (v *Verifier) grace() time.Duration {
	if v.Grace > 0 {
		return v.Grace
	}
	return GraceWindow
}

// Enroll creates a fresh credential family for a Bridge and returns its first
// refresh token.
func (v *Verifier) Enroll(bridge protocol.BridgeID) (Token, error) {
	if err := bridge.Validate(); err != nil {
		return "", err
	}
	token := NewToken()
	state := FamilyState{
		BridgeID:    bridge,
		CurrentHash: token.Hash(),
		Generation:  1,
	}
	if err := v.Store.Save(state); err != nil {
		return "", fmt.Errorf("auth: persisting enrolment: %w", err)
	}
	return token, nil
}

// Rotate exchanges a refresh credential for a new one.
//
// The presenting Bridge identity is required and checked against the family.
// Scope of Work Phase 2 requires recovery to be "bound to the same Bridge
// identity key", and the same binding is applied on the normal path: a token
// presented by the wrong Bridge is never honoured, whichever hash it matches.
func (v *Verifier) Rotate(bridge protocol.BridgeID, presented Token) (Result, error) {
	state, ok := v.Store.Load(bridge)
	if !ok {
		return Result{}, ErrUnknownToken
	}
	if state.Revoked {
		return Result{}, ErrFamilyRevoked
	}
	if state.BridgeID != bridge {
		// Defensive: a Store keyed by Bridge should make this impossible, but a
		// mismatch here would be a confused-deputy bug and must not be silent.
		return Result{}, ErrWrongBridge
	}

	hash := presented.Hash()
	now := v.now()

	// Normal path.
	if equalHash(hash, state.CurrentHash) {
		next := NewToken()
		state.PreviousHash = state.CurrentHash
		state.PreviousIssuedAt = now
		state.PreviousConsumed = false
		state.CurrentHash = next.Hash()
		state.Generation++
		if err := v.Store.Save(state); err != nil {
			return Result{}, fmt.Errorf("auth: persisting rotation: %w", err)
		}
		return Result{
			Outcome:         OutcomeRotated,
			Refresh:         next,
			AccessExpiresAt: now.Add(AccessTokenLifetime),
			Generation:      state.Generation,
			Event:           protocol.EventTokenRotated,
		}, nil
	}

	// Recovery path, and its three failure modes.
	if state.PreviousHash != "" && equalHash(hash, state.PreviousHash) {
		switch {
		case state.PreviousConsumed:
			// The recovery path was already taken for this token. A second
			// presentation means two parties hold it.
			return Result{}, v.revoke(state, "the previous credential was presented twice")

		case now.Sub(state.PreviousIssuedAt) > v.grace():
			// Outside the window. This is not a crash recovering; a crashed
			// Bridge retries in seconds. Revoking rather than merely refusing is
			// deliberate: a token surfacing minutes or days later is far more
			// likely to be a restored backup or a captured credential, and
			// leaving the family alive would leave that credential live too.
			return Result{}, v.revoke(state, "the previous credential was presented after the recovery window closed")

		default:
			// A genuine interrupted rotation. Issue a fresh credential and
			// discard the one that was never delivered.
			//
			// Note what happens to the undelivered token: it is dropped, not
			// re-sent. Re-sending it would mean the Host cannot distinguish a
			// Bridge that never received it from one that did, and the family
			// would carry two live credentials.
			next := NewToken()
			state.PreviousConsumed = true
			state.CurrentHash = next.Hash()
			state.Generation++
			if err := v.Store.Save(state); err != nil {
				return Result{}, fmt.Errorf("auth: persisting recovery: %w", err)
			}
			return Result{
				Outcome:         OutcomeRecovered,
				Refresh:         next,
				AccessExpiresAt: now.Add(AccessTokenLifetime),
				Generation:      state.Generation,
				Event:           protocol.EventTokenGraceUsed,
			}, nil
		}
	}

	// A token for this Bridge that matches neither hash. Either very old, or
	// forged. Not treated as reuse, because revoking on any unrecognised string
	// would let an attacker who knows a Bridge id revoke it at will by sending
	// garbage.
	return Result{}, ErrUnknownToken
}

// Authenticate verifies that presented is the Bridge's current, live
// refresh token, without consuming or rotating it. Unlike Rotate, this
// never calls Store.Save and never mutates FamilyState — it exists for
// calls that need to prove "this caller currently holds Bridge X's live
// credential" without the rotation side effects Rotate's own protocol
// requires. See ADR 0019, resolved sub-decision 1: reusing Rotate itself
// for this would silently rotate the credential as an unrelated side
// effect of every such call.
//
// Deliberately does not accept the previous-token grace-window recovery
// path Rotate does — that path exists specifically to handle token
// *consumption* (a Bridge that crashed before persisting a freshly-rotated
// token), which has no meaning for a read-only check that never issues a
// new token in the first place.
func (v *Verifier) Authenticate(bridge protocol.BridgeID, presented Token) error {
	state, ok := v.Store.Load(bridge)
	if !ok {
		return ErrUnknownToken
	}
	if state.Revoked {
		return ErrFamilyRevoked
	}
	if state.BridgeID != bridge {
		return ErrWrongBridge
	}
	if !equalHash(presented.Hash(), state.CurrentHash) {
		return ErrUnknownToken
	}
	return nil
}

// revoke ends a family and returns the error to report.
func (v *Verifier) revoke(state FamilyState, reason string) error {
	state.Revoked = true
	state.RevokedReason = reason
	state.PreviousHash = ""
	state.CurrentHash = ""
	if err := v.Store.Save(state); err != nil {
		return fmt.Errorf("auth: persisting revocation: %w", err)
	}
	return ErrReuseDetected
}

// Revoke ends a family administratively, for Bridge disable or user suspension.
func (v *Verifier) Revoke(bridge protocol.BridgeID, reason string) error {
	state, ok := v.Store.Load(bridge)
	if !ok {
		return ErrUnknownToken
	}
	state.Revoked = true
	state.RevokedReason = reason
	state.CurrentHash = ""
	state.PreviousHash = ""
	return v.Store.Save(state)
}

// ReEnroll replaces a revoked family with a fresh one.
//
// Scope of Work Phase 2: "Owner-triggered Bridge re-enrollment recovery." This
// is the deliberate, owner-authorised escape from a revoked family, and it is
// the only one. Nothing on the automatic path can resurrect a revoked family,
// because an automatic escape from revocation is the same thing as not revoking.
func (v *Verifier) ReEnroll(bridge protocol.BridgeID) (Token, error) {
	state, ok := v.Store.Load(bridge)
	if !ok {
		return "", ErrUnknownToken
	}
	token := NewToken()
	state.Revoked = false
	state.RevokedReason = ""
	state.CurrentHash = token.Hash()
	state.PreviousHash = ""
	state.PreviousConsumed = false
	state.PreviousIssuedAt = time.Time{}
	state.Generation++
	if err := v.Store.Save(state); err != nil {
		return "", fmt.Errorf("auth: persisting re-enrolment: %w", err)
	}
	return token, nil
}

// MemoryStore is an in-memory Store for tests and for the Phase 0 reference
// implementation. Phase 2 replaces it with PostgreSQL.
type MemoryStore struct {
	families map[protocol.BridgeID]FamilyState

	// SaveHook lets a test fail a save, to simulate a Host crash mid-rotation.
	SaveHook func(FamilyState) error
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{families: map[protocol.BridgeID]FamilyState{}}
}

// Load implements Store.
func (m *MemoryStore) Load(id protocol.BridgeID) (FamilyState, bool) {
	s, ok := m.families[id]
	return s, ok
}

// Save implements Store.
func (m *MemoryStore) Save(s FamilyState) error {
	if m.SaveHook != nil {
		if err := m.SaveHook(s); err != nil {
			return err
		}
	}
	m.families[s.BridgeID] = s
	return nil
}
