package protocol

import (
	"strings"
	"testing"
)

func TestRandomIDsAreWellFormedAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		u := NewUserID()
		if err := u.Validate(); err != nil {
			t.Fatalf("minted user id failed validation: %v", err)
		}
		if seen[string(u)] {
			t.Fatalf("minted a duplicate user id at iteration %d", i)
		}
		seen[string(u)] = true
	}
}

func TestIDPrefixesAreNotInterchangeable(t *testing.T) {
	u := NewUserID()
	// A user id must not validate as any other kind. Without this the type
	// system is the only thing stopping a Swarm id being accepted where a
	// Bridge id was meant, and JSON decoding bypasses the type system.
	if err := SwarmID(u).Validate(); err == nil {
		t.Fatal("a user id validated as a swarm id")
	}
	if err := BridgeID(u).Validate(); err == nil {
		t.Fatal("a user id validated as a bridge id")
	}
}

func TestValidateRejectsMalformedIdentifiers(t *testing.T) {
	good := string(NewUserID())
	body := strings.TrimPrefix(good, "usr_")

	cases := map[string]string{
		"no prefix":         body,
		"wrong prefix":      "swm_" + body,
		"truncated body":    "usr_" + body[:10],
		"overlong body":     "usr_" + body + "aa",
		"uppercase body":    "usr_" + strings.ToUpper(body),
		"excluded letter u": "usr_" + strings.Repeat("u", 26),
		"empty":             "",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if err := UserID(in).Validate(); err == nil {
				t.Fatalf("Validate accepted malformed id %q", in)
			}
		})
	}
}

func TestBridgeIDIsDerivedFromKey(t *testing.T) {
	keyA := []byte("public-identity-key-a")
	keyB := []byte("public-identity-key-b")

	// Stable: re-enrolment with the same key must produce the same identity,
	// otherwise owner recovery silently creates a second Bridge.
	if BridgeIDFromPublicKey(keyA) != BridgeIDFromPublicKey(keyA) {
		t.Fatal("the same public key produced two different bridge ids")
	}
	if BridgeIDFromPublicKey(keyA) == BridgeIDFromPublicKey(keyB) {
		t.Fatal("two different public keys produced the same bridge id")
	}
	if err := BridgeIDFromPublicKey(keyA).Validate(); err != nil {
		t.Fatalf("derived bridge id failed validation: %v", err)
	}
}

func TestFileIDIsContentDerivedAndStable(t *testing.T) {
	const sha = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

	// Two Bridges that never speak to each other must compute the same FileID
	// for the same canonical payload, or replica counting cannot work at all.
	a := FileIDFromCanonicalDigest(sha)
	b := FileIDFromCanonicalDigest(strings.ToUpper(sha))
	if a != b {
		t.Fatalf("case affected the derived file id: %s != %s", a, b)
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("derived file id failed validation: %v", err)
	}
	if a == FileIDFromCanonicalDigest(strings.Repeat("0", 64)) {
		t.Fatal("different payloads produced the same file id")
	}
}

func TestDomainSeparationBetweenDerivedKinds(t *testing.T) {
	// The same input bytes fed to two derivations must not produce the same
	// body. If they did, a file digest could be replayed as a bridge identity.
	shared := "1111111111111111111111111111111111111111111111111111111111111111"
	file := strings.TrimPrefix(string(FileIDFromCanonicalDigest(shared)), "fil_")
	bridge := strings.TrimPrefix(string(BridgeIDFromPublicKey([]byte(shared))), "brg_")
	if file == bridge {
		t.Fatal("file and bridge derivations collided on the same input")
	}
}

func TestGameIDIgnoresReferenceSetVersion(t *testing.T) {
	// Importing a newer DAT must not renumber every game in the index.
	a := GameIDFromReference("no-intro", "Super Mario Land")
	b := GameIDFromReference("No-Intro", "super mario land")
	if a != b {
		t.Fatalf("game id was not case stable: %s != %s", a, b)
	}
	if a == GameIDFromReference("redump", "Super Mario Land") {
		t.Fatal("two catalogue families produced the same game id")
	}
}

func TestDeriveIsUnambiguousAcrossPartBoundaries(t *testing.T) {
	// ("ab","c") and ("a","bc") must not collide; the length prefix is what
	// prevents it.
	x := derive("test", []byte("ab"), []byte("c"))
	y := derive("test", []byte("a"), []byte("bc"))
	if string(x) == string(y) {
		t.Fatal("derive collided across different part boundaries")
	}
}

func TestRevisionSequencing(t *testing.T) {
	var r Revision
	if !r.IsZero() {
		t.Fatal("zero revision did not report IsZero")
	}
	if r.Next() != 1 {
		t.Fatalf("first revision is %d, want 1", r.Next())
	}
}
