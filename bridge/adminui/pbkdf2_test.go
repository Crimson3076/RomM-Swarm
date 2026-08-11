package adminui

import "testing"

func TestPasswordHashRoundTrips(t *testing.T) {
	encoded, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if !verifyPassword("correct horse battery staple", encoded) {
		t.Fatal("verifyPassword rejected the exact password that was hashed")
	}
}

func TestPasswordHashRejectsAWrongPassword(t *testing.T) {
	encoded, err := hashPassword("the real password")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if verifyPassword("a guess", encoded) {
		t.Fatal("verifyPassword accepted the wrong password")
	}
	if verifyPassword("", encoded) {
		t.Fatal("verifyPassword accepted an empty password")
	}
}

func TestPasswordHashUsesADistinctSaltEachTime(t *testing.T) {
	a, err := hashPassword("same password")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	b, err := hashPassword("same password")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if a == b {
		t.Fatal("two hashes of the same password were identical — the salt is not actually random")
	}
	if !verifyPassword("same password", a) || !verifyPassword("same password", b) {
		t.Fatal("one of the two distinctly-salted hashes did not verify")
	}
}

func TestPasswordHashDetectsTampering(t *testing.T) {
	encoded, err := hashPassword("tamper me")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	tampered := []byte(encoded)
	// Flip a character deep in the hash portion.
	tampered[len(tampered)-1] = tampered[len(tampered)-1] ^ 1
	if verifyPassword("tamper me", string(tampered)) {
		t.Fatal("verifyPassword accepted a tampered hash")
	}
}

func TestVerifyPasswordRejectsMalformedInputWithoutPanicking(t *testing.T) {
	cases := []string{
		"",
		"not-the-right-format",
		"pbkdf2-sha256$notanumber$abcd$abcd",
		"pbkdf2-sha256$1000$not-hex$abcd",
		"pbkdf2-sha256$1000$abcd$not-hex",
		"wrong-algo$1000$abcd$abcd",
	}
	for _, c := range cases {
		if verifyPassword("anything", c) {
			t.Errorf("verifyPassword(%q) unexpectedly succeeded", c)
		}
	}
}
