package protocol

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"strings"
)

// Canonical identifiers (Scope of Work, Phase 0: "Define canonical identifiers
// for Bridge, user, Swarm, game, exact file, transfer, and inventory revision").
//
// Every identifier is a short prefixed string. The prefix makes an identifier
// self-describing in logs, URLs, and support conversations, and makes it
// impossible to pass a Swarm ID where a Bridge ID was meant without the type
// system and the parser both objecting.
//
// Identifiers come in three flavours, and which flavour an identifier uses is a
// security property rather than a style choice:
//
//   - Random: minted from crypto/rand. Used where the identifier must carry no
//     information at all (users, Swarms, transfers).
//   - Key-derived: a digest of a Bridge's public identity key. Two enrolments of
//     the same key produce the same Bridge ID, so a re-enrolling Bridge is
//     recognisable, and the identifier cannot be claimed by a Bridge that does
//     not hold the private key.
//   - Content-derived: a digest of verified content identity. Two Bridges that
//     independently hold the same file compute the same FileID without talking
//     to each other, which is what lets the Host index replicas at all.
//
// Content-derived identifiers are deliberately *not* used for anything
// member-facing that could correlate a Bridge across Swarms; that is what
// BridgeAlias is for (see alias.go).

// crockford is Crockford's base32 alphabet: no I, L, O, or U, so an identifier
// read aloud or copied by hand does not turn into a different identifier.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var idEncoding = base32.NewEncoding(crockford).WithPadding(base32.NoPadding)

// idBytes is the number of digest or random bytes in an identifier body. 16
// bytes encode to 26 characters and leave collision probability negligible at
// any plausible network size.
const idBytes = 16

// Identifier prefixes.
const (
	prefixBridge   = "brg"
	prefixUser     = "usr"
	prefixSwarm    = "swm"
	prefixTransfer = "xfr"
	prefixGame     = "gam"
	prefixFile     = "fil"
	prefixAlias    = "sba"
	prefixGrant    = "grt"
)

// Domain separation tags. A digest computed for one identifier kind must never
// be reusable as another kind, even when the same input bytes are involved.
const (
	domainBridge = "romm-swarm/id/bridge/v1"
	domainGame   = "romm-swarm/id/game/v1"
	domainFile   = "romm-swarm/id/file/v1"
	domainAlias  = "romm-swarm/id/swarm-bridge-alias/v1"
)

type (
	// BridgeID is a Bridge's global identity, derived from its public identity
	// key. It is used by the Host and by the Bridge's own owner. It is never
	// exposed to ordinary members of a Swarm; they see a BridgeAlias.
	BridgeID string

	// UserID identifies a human account on the Network Host.
	UserID string

	// SwarmID identifies a trust group.
	SwarmID string

	// TransferID identifies one transfer attempt end to end, across direct and
	// relayed routes, and keys the receiving Bridge's staging file.
	TransferID string

	// GrantID identifies a short-lived access grant.
	GrantID string

	// GameID identifies a canonical game: the work itself, independent of which
	// exact dump, region, or revision a Bridge happens to hold.
	GameID string

	// FileID identifies one exact file: a specific verified canonical payload.
	// Derived from content, so it is identical on every Bridge holding that
	// payload.
	FileID string

	// BridgeAlias is a Swarm-scoped pseudonym for a Bridge. See alias.go.
	BridgeAlias string
)

// Revision is a Bridge's monotonic inventory revision within one Swarm. It
// starts at 1 for the first published snapshot and increments for every
// published delta. Revision 0 means "no inventory published yet".
type Revision uint64

// IsZero reports whether no inventory has been published.
func (r Revision) IsZero() bool { return r == 0 }

// Next returns the revision that must follow r.
func (r Revision) Next() Revision { return r + 1 }

// encode renders a body as a prefixed identifier string.
func encode(prefix string, body []byte) string {
	return prefix + "_" + strings.ToLower(idEncoding.EncodeToString(body))
}

// derive builds a domain-separated identifier body from arbitrary inputs.
func derive(domain string, parts ...[]byte) []byte {
	h := sha256.New()
	h.Write([]byte(domain))
	for _, p := range parts {
		// Length-prefix every part so that ("ab","c") and ("a","bc") cannot
		// produce the same digest.
		var l [8]byte
		n := uint64(len(p))
		for i := 0; i < 8; i++ {
			l[i] = byte(n >> (8 * (7 - i)))
		}
		h.Write(l[:])
		h.Write(p)
	}
	return h.Sum(nil)[:idBytes]
}

// randomBody returns idBytes of cryptographically secure randomness.
func randomBody() []byte {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail on any platform we support; if it does, the
		// process cannot safely mint identities and must not continue.
		panic("romm-swarm: crypto/rand unavailable: " + err.Error())
	}
	return b
}

// NewUserID mints a random user identifier.
func NewUserID() UserID { return UserID(encode(prefixUser, randomBody())) }

// NewSwarmID mints a random Swarm identifier.
func NewSwarmID() SwarmID { return SwarmID(encode(prefixSwarm, randomBody())) }

// NewTransferID mints a random transfer identifier.
func NewTransferID() TransferID { return TransferID(encode(prefixTransfer, randomBody())) }

// NewGrantID mints a random grant identifier.
func NewGrantID() GrantID { return GrantID(encode(prefixGrant, randomBody())) }

// BridgeIDFromPublicKey derives a Bridge identity from its public identity key.
// The same key always yields the same BridgeID, which is what makes owner
// re-enrolment (Phase 2) recognisable rather than a silent second Bridge.
func BridgeIDFromPublicKey(pub []byte) BridgeID {
	return BridgeID(encode(prefixBridge, derive(domainBridge, pub)))
}

// FileIDFromCanonicalDigest derives the exact-file identifier from the SHA-256
// of the canonical payload.
//
// Deliberately keyed on the *canonical payload*, not the stored file: two
// Bridges holding the same game as a bare ROM and as a zip must agree that they
// hold the same exact file, or replica counting is meaningless.
func FileIDFromCanonicalDigest(sha256Hex string) FileID {
	return FileID(encode(prefixFile, derive(domainFile, []byte(strings.ToLower(sha256Hex)))))
}

// GameIDFromReference derives a canonical game identifier from the reference
// catalogue family (for example "no-intro") and the profile-independent
// canonical game key (the No-Intro title with region, revision, and language
// tags stripped).
//
// Keyed on family plus key rather than on a reference-set version, so importing
// a newer DAT does not renumber every game in the index.
func GameIDFromReference(family, canonicalKey string) GameID {
	return GameID(encode(prefixGame, derive(domainGame,
		[]byte(strings.ToLower(family)),
		[]byte(strings.ToLower(canonicalKey)),
	)))
}

// validate checks that s is a well-formed identifier with the expected prefix.
func validate(kind, prefix, s string) error {
	want := prefix + "_"
	if !strings.HasPrefix(s, want) {
		return fmt.Errorf("%s: %q does not start with %q", kind, s, want)
	}
	body := s[len(want):]
	if len(body) != 26 {
		return fmt.Errorf("%s: body is %d characters, want 26", kind, len(body))
	}
	if body != strings.ToLower(body) {
		return fmt.Errorf("%s: body must be lowercase", kind)
	}
	if _, err := idEncoding.DecodeString(strings.ToUpper(body)); err != nil {
		return fmt.Errorf("%s: body is not valid base32: %w", kind, err)
	}
	return nil
}

// Validate reports whether the identifier is well formed.
func (id BridgeID) Validate() error    { return validate("bridge id", prefixBridge, string(id)) }
func (id UserID) Validate() error      { return validate("user id", prefixUser, string(id)) }
func (id SwarmID) Validate() error     { return validate("swarm id", prefixSwarm, string(id)) }
func (id TransferID) Validate() error  { return validate("transfer id", prefixTransfer, string(id)) }
func (id GrantID) Validate() error     { return validate("grant id", prefixGrant, string(id)) }
func (id GameID) Validate() error      { return validate("game id", prefixGame, string(id)) }
func (id FileID) Validate() error      { return validate("file id", prefixFile, string(id)) }
func (id BridgeAlias) Validate() error { return validate("bridge alias", prefixAlias, string(id)) }
