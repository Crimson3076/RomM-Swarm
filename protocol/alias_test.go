package protocol

import "testing"

// TestPhase0_SwarmAliasesDoNotCorrelateAcrossSwarms is Phase 2 acceptance
// evidence: "Membership in two Swarms does not expose one Swarm's Bridge alias
// to the other."
func TestPhase0_SwarmAliasesDoNotCorrelateAcrossSwarms(t *testing.T) {
	bridge := BridgeIDFromPublicKey([]byte("one bridge, two swarms"))
	swarmA, swarmB := NewSwarmID(), NewSwarmID()
	keyA, keyB := NewSwarmAliasKey(), NewSwarmAliasKey()

	aliasA := MustAliasFor(keyA, swarmA, bridge)
	aliasB := MustAliasFor(keyB, swarmB, bridge)

	if aliasA == aliasB {
		t.Fatal("the same bridge presented an identical alias in two swarms")
	}
	if err := aliasA.Validate(); err != nil {
		t.Fatalf("alias failed validation: %v", err)
	}

	// A member of Swarm A holds aliasA and the public knowledge of Swarm B's
	// id. Without Swarm B's alias key they cannot derive aliasB.
	if got := MustAliasFor(keyA, swarmB, bridge); got == aliasB {
		t.Fatal("swarm B's alias was derivable using swarm A's key")
	}
}

func TestAliasIsStableWithinASwarm(t *testing.T) {
	bridge := BridgeIDFromPublicKey([]byte("stable bridge"))
	swarm := NewSwarmID()
	key := NewSwarmAliasKey()

	first := MustAliasFor(key, swarm, bridge)
	for i := 0; i < 10; i++ {
		if got := MustAliasFor(key, swarm, bridge); got != first {
			t.Fatalf("alias changed between calls: %s then %s", first, got)
		}
	}
}

func TestDistinctBridgesGetDistinctAliases(t *testing.T) {
	swarm := NewSwarmID()
	key := NewSwarmAliasKey()
	a := MustAliasFor(key, swarm, BridgeIDFromPublicKey([]byte("bridge a")))
	b := MustAliasFor(key, swarm, BridgeIDFromPublicKey([]byte("bridge b")))
	if a == b {
		t.Fatal("two bridges collided on the same alias within one swarm")
	}
}

// A reused alias key across two Swarms is an operational mistake, not a
// cryptographic one. Mixing the Swarm id into the MAC keeps that mistake from
// becoming a cross-Swarm correlation.
func TestSwarmIDIsMixedInSoAReusedKeyStillSeparates(t *testing.T) {
	bridge := BridgeIDFromPublicKey([]byte("bridge"))
	key := NewSwarmAliasKey()
	a := MustAliasFor(key, NewSwarmID(), bridge)
	b := MustAliasFor(key, NewSwarmID(), bridge)
	if a == b {
		t.Fatal("a reused alias key produced the same alias in two swarms")
	}
}

func TestAliasKeyMustBeCorrectSize(t *testing.T) {
	if _, err := AliasFor([]byte("short"), NewSwarmID(), "brg_x"); err == nil {
		t.Fatal("AliasFor accepted an undersized key")
	}
	if len(NewSwarmAliasKey()) != AliasKeySize {
		t.Fatalf("NewSwarmAliasKey returned %d bytes, want %d", len(NewSwarmAliasKey()), AliasKeySize)
	}
}
