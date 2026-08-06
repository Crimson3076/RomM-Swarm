package protocol

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
)

// Swarm-scoped Bridge aliases.
//
// Scope of Work §3: "Each Swarm sees a Swarm-scoped Bridge alias rather than a
// globally correlatable Bridge identifier", and Phase 2 acceptance: "Membership
// in two Swarms does not expose one Swarm's Bridge alias to the other."
//
// The alias is an HMAC of the Bridge identity under a per-Swarm alias key. The
// properties this buys, and which the tests assert:
//
//   - Stable: the same Bridge in the same Swarm always presents the same alias,
//     so members can recognise a source over time and across restarts.
//   - Uncorrelatable: the same Bridge in two Swarms presents two unrelated
//     aliases. A member of both Swarms cannot tell they are the same Bridge.
//   - One-way: an alias cannot be reversed to a BridgeID without the Swarm's
//     alias key, which lives only on the Host.
//
// The Host necessarily *can* correlate: it holds every alias key. That is a
// known and documented limit — see docs/phase0/threat-model.md, "The Host is a
// trusted-but-sensitive observer". Removing that ability is the job of the
// later encrypted-catalog mode, not of this mechanism.

// AliasKeySize is the required length of a Swarm alias key.
const AliasKeySize = 32

// ErrBadAliasKey is returned when a Swarm alias key is the wrong size.
var ErrBadAliasKey = errors.New("swarm alias key must be exactly 32 bytes")

// NewSwarmAliasKey mints a fresh per-Swarm alias key. It is generated when the
// Swarm is created and never leaves the Host.
func NewSwarmAliasKey() []byte {
	k := make([]byte, AliasKeySize)
	if _, err := rand.Read(k); err != nil {
		panic("romm-swarm: crypto/rand unavailable: " + err.Error())
	}
	return k
}

// AliasFor computes the Swarm-scoped alias a Bridge presents inside one Swarm.
//
// The swarm ID is mixed in alongside the key so that an alias key accidentally
// reused across two Swarms still yields distinct aliases: defence in depth
// against an operational mistake rather than a cryptographic one.
func AliasFor(aliasKey []byte, swarm SwarmID, bridge BridgeID) (BridgeAlias, error) {
	if len(aliasKey) != AliasKeySize {
		return "", ErrBadAliasKey
	}
	mac := hmac.New(sha256.New, aliasKey)
	mac.Write([]byte(domainAlias))
	mac.Write([]byte{0})
	mac.Write([]byte(swarm))
	mac.Write([]byte{0})
	mac.Write([]byte(bridge))
	return BridgeAlias(encode(prefixAlias, mac.Sum(nil)[:idBytes])), nil
}

// MustAliasFor is AliasFor for call sites that have already validated the key.
func MustAliasFor(aliasKey []byte, swarm SwarmID, bridge BridgeID) BridgeAlias {
	a, err := AliasFor(aliasKey, swarm, bridge)
	if err != nil {
		panic(err)
	}
	return a
}
