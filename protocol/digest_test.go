package protocol

import (
	"bytes"
	"strings"
	"testing"
)

func TestDigestKnownVector(t *testing.T) {
	// "abc" through all four algorithms. Hard-coded so a future refactor of the
	// hashing pipeline cannot quietly change what a digest means.
	d := DigestBytes([]byte("abc"))
	want := Digest{
		Size:   3,
		CRC32:  "352441c2",
		MD5:    "900150983cd24fb0d6963f7d28e17f72",
		SHA1:   "a9993e364706816aba3e25717850c26c9cd0d89d",
		SHA256: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
	}
	if d != want {
		t.Fatalf("digest mismatch:\n got %+v\nwant %+v", d, want)
	}
}

func TestDigestReaderMatchesDigestBytes(t *testing.T) {
	payload := bytes.Repeat([]byte("romm-swarm"), 5000)
	streamed, err := DigestReader(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("DigestReader: %v", err)
	}
	if streamed != DigestBytes(payload) {
		t.Fatal("streaming and in-memory hashing disagreed")
	}
}

func TestNormalizedMakesCatalogueCaseIrrelevant(t *testing.T) {
	// DAT files conventionally store uppercase hex. A digest read from a DAT
	// must compare equal to one we computed.
	d := DigestBytes([]byte("abc"))
	upper := Digest{
		Size:   d.Size,
		CRC32:  strings.ToUpper(d.CRC32),
		MD5:    strings.ToUpper(d.MD5),
		SHA1:   strings.ToUpper(d.SHA1),
		SHA256: strings.ToUpper(d.SHA256),
	}
	if d.Normalized() != upper.Normalized() {
		t.Fatal("case affected digest comparison")
	}
}

// TestPhase0_TransferAcceptanceRequiresStrongAgreement records the rule that a
// catalogue match and a transfer acceptance are different bars. A DAT can only
// support MD5 or SHA-1; accepting bytes from a partially trusted peer requires
// SHA-256.
func TestPhase0_TransferAcceptanceRequiresStrongAgreement(t *testing.T) {
	payload := []byte("a game")
	full := DigestBytes(payload)

	// What a No-Intro entry can offer: size, CRC32, MD5, SHA-1, no SHA-256.
	catalogue := Digest{Size: full.Size, CRC32: full.CRC32, MD5: full.MD5, SHA1: full.SHA1}
	level, ok := full.Compare(catalogue)
	if !ok {
		t.Fatal("a genuine payload failed to compare against its catalogue entry")
	}
	if level != StrengthCatalogue {
		t.Fatalf("catalogue comparison reached %s, want %s", level, StrengthCatalogue)
	}
	if level >= StrengthStrong {
		t.Fatal("a catalogue entry with no SHA-256 must not reach strong agreement")
	}

	// What an arriving transfer must clear.
	level, ok = full.Compare(full)
	if !ok || level != StrengthStrong {
		t.Fatalf("a byte-identical payload reached %s (ok=%v), want %s", level, ok, StrengthStrong)
	}
}

func TestCompareRejectsAnyActiveDisagreement(t *testing.T) {
	a := DigestBytes([]byte("payload one"))
	b := DigestBytes([]byte("payload two"))

	if _, ok := a.Compare(b); ok {
		t.Fatal("two different payloads compared as agreeing")
	}

	// A single disagreeing field is fatal even when stronger fields agree.
	// Anything else lets a corrupted or crafted record paper over a mismatch.
	tampered := a
	tampered.CRC32 = b.CRC32
	if _, ok := a.Compare(tampered); ok {
		t.Fatal("a CRC32 disagreement was tolerated because SHA-256 agreed")
	}
}

func TestCompareTreatsSizeDisagreementAsFatal(t *testing.T) {
	a := DigestBytes([]byte("payload"))
	b := a
	b.Size = a.Size + 1
	if _, ok := a.Compare(b); ok {
		t.Fatal("a size disagreement was tolerated")
	}
}

func TestCompareOfEmptyDigestsProvesNothing(t *testing.T) {
	// Two records that share only a size must not be reported as agreeing about
	// content. StrengthNone with ok=true means "no contradiction, no evidence".
	a := Digest{Size: 100}
	b := Digest{Size: 100}
	level, ok := a.Compare(b)
	if !ok {
		t.Fatal("two non-contradictory records were reported as contradicting")
	}
	if level != StrengthNone {
		t.Fatalf("size-only agreement reported strength %s, want none", level)
	}
}

func TestStrengthOrdering(t *testing.T) {
	if !(StrengthNone < StrengthWeak && StrengthWeak < StrengthCatalogue && StrengthCatalogue < StrengthStrong) {
		t.Fatal("strength levels are not ordered from weakest to strongest")
	}
}
