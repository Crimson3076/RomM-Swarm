package directory

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id password hashing for Host account credentials.
//
// docs/phase0/privacy-data-map.md specifies Argon2id for the Host's
// password verifier specifically — unlike bridge/adminui's deliberate
// hand-rolled PBKDF2, which exists to keep Bridge dependency-free. Host was
// never held to that bar (go.mod's own comment anticipated dependencies
// arriving with Phase 2 Host work), and Argon2id is the data map's actual
// requirement here, not a free reimplementation choice — so this uses
// golang.org/x/crypto/argon2 directly rather than hand-rolling it. See
// ADR 0016, resolved sub-decision 5.

// Parameters follow OWASP's 2023 Argon2id guidance for the "less memory"
// profile: 19 MiB, 2 iterations, one thread per core up to 4, 32-byte key.
const (
	argon2Time    = 2
	argon2Memory  = 19 * 1024 // KiB
	argon2Threads = 4
	argon2KeyLen  = 32
	argon2SaltLen = 16
)

// hashPassword returns a self-describing encoded hash:
// "argon2id$v=19$m=<mem>,t=<time>,p=<threads>$<salt-b64>$<hash-b64>".
func hashPassword(password string) (string, error) {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("directory: generating a password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	return fmt.Sprintf("argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argon2Memory, argon2Time, argon2Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// verifyPassword checks password against a hash produced by hashPassword. A
// malformed encoded hash is treated as a non-match rather than an error, so
// a corrupt row never becomes a way to bypass the check.
func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[1], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var mem uint32
	var timeCost uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &mem, &timeCost, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, timeCost, mem, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
