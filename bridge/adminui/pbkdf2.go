package adminui

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"strconv"
	"strings"
)

// PBKDF2-HMAC-SHA256 password hashing, hand-rolled from stdlib primitives.
//
// ADR 0015 records why: the standard library has no PBKDF2 implementation,
// and this project has deliberately stayed dependency-free everywhere until
// Phase 1 persistence specifically required otherwise. PBKDF2 (RFC 2898) is
// a well-defined, standard construction — not novel cryptography — so
// implementing it directly keeps that streak intact for the one place a
// choice actually had to be made, rather than reaching for
// golang.org/x/crypto's bcrypt.

// pbkdf2Iterations follows OWASP's 2023 guidance for PBKDF2-HMAC-SHA256.
const pbkdf2Iterations = 210_000

const (
	pbkdf2SaltLen = 16
	pbkdf2KeyLen  = 32
)

// hashPassword returns a self-describing encoded hash:
// "pbkdf2-sha256$<iterations>$<salt-hex>$<hash-hex>".
func hashPassword(password string) (string, error) {
	salt := make([]byte, pbkdf2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("adminui: generating a password salt: %w", err)
	}
	key := pbkdf2(password, salt, pbkdf2Iterations, pbkdf2KeyLen)
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iterations, hex.EncodeToString(salt), hex.EncodeToString(key)), nil
}

// verifyPassword checks password against a hash produced by hashPassword. A
// malformed encoded hash is treated as a non-match rather than an error, so
// a corrupt config never becomes a way to bypass the check.
func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false
	}
	salt, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got := pbkdf2(password, salt, iterations, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// pbkdf2 implements RFC 2898's PBKDF2 with HMAC-SHA256 as the pseudorandom
// function.
func pbkdf2(password string, salt []byte, iterations, keyLen int) []byte {
	prf := func() hash.Hash { return hmac.New(sha256.New, []byte(password)) }
	hLen := sha256.Size
	numBlocks := (keyLen + hLen - 1) / hLen

	dk := make([]byte, 0, numBlocks*hLen)
	for block := 1; block <= numBlocks; block++ {
		dk = append(dk, pbkdf2Block(prf, salt, iterations, uint32(block))...)
	}
	return dk[:keyLen]
}

func pbkdf2Block(prf func() hash.Hash, salt []byte, iterations int, blockIndex uint32) []byte {
	var idx [4]byte
	binary.BigEndian.PutUint32(idx[:], blockIndex)

	h := prf()
	h.Write(salt)
	h.Write(idx[:])
	u := h.Sum(nil)
	result := append([]byte(nil), u...)

	for i := 1; i < iterations; i++ {
		h := prf()
		h.Write(u)
		u = h.Sum(nil)
		for j := range result {
			result[j] ^= u[j]
		}
	}
	return result
}
