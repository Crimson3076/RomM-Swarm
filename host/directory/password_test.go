package directory

import "testing"

func TestPasswordHashRoundTrips(t *testing.T) {
	hash, err := hashPassword("a-correct-password")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if !verifyPassword("a-correct-password", hash) {
		t.Fatal("verifyPassword rejected the password it was hashed from")
	}
	if verifyPassword("a-wrong-password", hash) {
		t.Fatal("verifyPassword accepted the wrong password")
	}
}

func TestPasswordHashUsesADistinctSaltEachTime(t *testing.T) {
	a, err := hashPassword("same-password")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	b, err := hashPassword("same-password")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if a == b {
		t.Fatal("two hashes of the same password were identical")
	}
}

func TestVerifyPasswordRejectsMalformedInputWithoutPanicking(t *testing.T) {
	cases := []string{
		"",
		"not-a-hash-at-all",
		"argon2id$",
		"argon2id$v=19$m=not-a-number,t=2,p=4$c2FsdA$aGFzaA",
		"pbkdf2-sha256$210000$abcd$abcd", // a different scheme's format
	}
	for _, c := range cases {
		if verifyPassword("anything", c) {
			t.Fatalf("verifyPassword accepted malformed input %q", c)
		}
	}
}
