package protocol

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"strings"
)

// Digest is the set of hashes describing one byte stream.
//
// All four algorithms are carried because the ecosystem forces it, not because
// redundancy is elegant:
//
//   - CRC32 and MD5 and SHA1 are what No-Intro and Redump publish. Matching a
//     reference entry means matching what the reference actually contains.
//   - SHA-256 is what RomM Swarm uses for its own content-derived identifiers
//     and transfer verification, because CRC32 and MD5 are not collision
//     resistant and SHA-1 is no longer a defensible integrity boundary for
//     content arriving from a partially trusted peer.
//
// The split matters: a reference *match* may rest on the catalogue's weaker
// hashes, but a transfer is only ever accepted on SHA-256. See
// docs/adr/0011-verification-identity-model.md.
type Digest struct {
	Size   int64  `json:"size"`
	CRC32  string `json:"crc32"`
	MD5    string `json:"md5"`
	SHA1   string `json:"sha1"`
	SHA256 string `json:"sha256"`
}

// Hasher computes all four digests in a single pass over the data.
type Hasher struct {
	size   int64
	crc    hash.Hash32
	md5    hash.Hash
	sha1   hash.Hash
	sha256 hash.Hash
	w      io.Writer
}

// NewHasher returns a Hasher ready to accept writes.
func NewHasher() *Hasher {
	h := &Hasher{
		crc:    crc32.NewIEEE(),
		md5:    md5.New(),
		sha1:   sha1.New(),
		sha256: sha256.New(),
	}
	h.w = io.MultiWriter(h.crc, h.md5, h.sha1, h.sha256)
	return h
}

// Write feeds bytes into every hash.
func (h *Hasher) Write(p []byte) (int, error) {
	n, err := h.w.Write(p)
	h.size += int64(n)
	return n, err
}

// Digest finalises and returns the accumulated hashes.
func (h *Hasher) Digest() Digest {
	return Digest{
		Size:   h.size,
		CRC32:  hex.EncodeToString(h.crc.Sum(nil)),
		MD5:    hex.EncodeToString(h.md5.Sum(nil)),
		SHA1:   hex.EncodeToString(h.sha1.Sum(nil)),
		SHA256: hex.EncodeToString(h.sha256.Sum(nil)),
	}
}

// DigestBytes hashes an in-memory payload.
func DigestBytes(b []byte) Digest {
	h := NewHasher()
	_, _ = h.Write(b)
	return h.Digest()
}

// DigestReader hashes a stream without buffering it.
func DigestReader(r io.Reader) (Digest, error) {
	h := NewHasher()
	if _, err := io.Copy(h, r); err != nil {
		return Digest{}, fmt.Errorf("hashing stream: %w", err)
	}
	return h.Digest(), nil
}

// Normalized returns a copy with every hash lowercased, so that a digest read
// from a DAT file (conventionally uppercase) compares equal to one we computed.
func (d Digest) Normalized() Digest {
	d.CRC32 = strings.ToLower(d.CRC32)
	d.MD5 = strings.ToLower(d.MD5)
	d.SHA1 = strings.ToLower(d.SHA1)
	d.SHA256 = strings.ToLower(d.SHA256)
	return d
}

// String renders a digest compactly for logs. Deliberately short: full digests
// in every log line make logs unreadable and are rarely what an operator needs.
func (d Digest) String() string {
	s := d.Normalized()
	short := s.SHA256
	if len(short) > 12 {
		short = short[:12]
	}
	return fmt.Sprintf("%d bytes sha256:%s…", d.Size, short)
}

// StrengthLevel describes how strongly two digests were shown to agree.
type StrengthLevel int

const (
	// StrengthNone means nothing comparable was present on both sides.
	StrengthNone StrengthLevel = iota
	// StrengthWeak means only size and CRC32 agreed. Adequate for a catalogue
	// hint, never adequate to accept transferred content.
	StrengthWeak
	// StrengthCatalogue means MD5 or SHA-1 agreed, alongside size. This is the
	// strongest statement most reference catalogues can support.
	StrengthCatalogue
	// StrengthStrong means SHA-256 agreed. Required to accept a transfer.
	StrengthStrong
)

func (s StrengthLevel) String() string {
	switch s {
	case StrengthWeak:
		return "weak"
	case StrengthCatalogue:
		return "catalogue"
	case StrengthStrong:
		return "strong"
	default:
		return "none"
	}
}

// Compare reports the strongest agreement between two digests, and whether any
// populated field actively disagreed.
//
// A disagreement is always fatal regardless of what else matched: if SHA-1
// agrees but CRC32 does not, something is wrong in a way that no amount of
// other agreement should paper over.
func (d Digest) Compare(other Digest) (StrengthLevel, bool) {
	a, b := d.Normalized(), other.Normalized()

	if a.Size != 0 && b.Size != 0 && a.Size != b.Size {
		return StrengthNone, false
	}

	best := StrengthNone
	cmp := func(x, y string, level StrengthLevel) bool {
		if x == "" || y == "" {
			return true
		}
		if x != y {
			return false
		}
		if level > best {
			best = level
		}
		return true
	}

	if !cmp(a.CRC32, b.CRC32, StrengthWeak) {
		return StrengthNone, false
	}
	if !cmp(a.MD5, b.MD5, StrengthCatalogue) {
		return StrengthNone, false
	}
	if !cmp(a.SHA1, b.SHA1, StrengthCatalogue) {
		return StrengthNone, false
	}
	if !cmp(a.SHA256, b.SHA256, StrengthStrong) {
		return StrengthNone, false
	}

	// Size agreement alone is not evidence of anything.
	if best == StrengthNone {
		return StrengthNone, true
	}
	return best, true
}
