// Package swarmconn persists one Bridge's connection to a Network Host: the
// Host's URL and the Bridge's own Ed25519 identity keypair — ADR 0017.
//
// This is deliberately a separate store from both bridge/bridgeconfig.Config
// (local RomM/admin settings) and auth.Credential/auth.FileStore (the
// Host-enrollment refresh token, Phase 0, already tested). auth.Client.Store
// is a concrete *auth.FileStore field, not an interface, so the refresh
// credential has to keep living in a real auth.FileStore file; the Host URL
// and identity key don't fit that type at all and get their own store here.
// The three happen to share a persistence recipe, not a type — the same
// distinction bridgeconfig's own package doc comment already draws between
// itself and auth.FileStore.
package swarmconn

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// Config is what a Bridge persists about its Host connection.
type Config struct {
	SchemaVersion int `json:"schema_version"`

	// HostURL is the Network Host's base URL.
	HostURL string `json:"host_url"`

	// IdentityPrivateKey is the Bridge's Ed25519 private key (64 bytes,
	// seed+public per crypto/ed25519's convention), generated once on
	// first join and never regenerated — re-enrolling with a different key
	// would produce a different protocol.BridgeID, silently creating a
	// second Bridge identity rather than being recognised as the same one.
	IdentityPrivateKey []byte `json:"identity_private_key,omitempty"`

	PersistedAt time.Time `json:"persisted_at"`
}

// Configured reports whether a Host connection has been established.
func (c Config) Configured() bool {
	return c.HostURL != "" && c.HasIdentity()
}

// HasIdentity reports whether an identity keypair has been generated yet,
// independent of whether a Host connection exists — JoinSwarm generates the
// key first, then enrolls it, so there is a moment where an identity exists
// without a completed connection.
func (c Config) HasIdentity() bool {
	return len(c.IdentityPrivateKey) == ed25519.PrivateKeySize
}

// PublicKey derives the public half of the stored identity key. Panics if
// HasIdentity is false — callers must check first, the same convention
// protocol's other derived-value accessors use.
func (c Config) PublicKey() ed25519.PublicKey {
	if !c.HasIdentity() {
		panic("swarmconn: PublicKey called on a Config with no identity key")
	}
	return ed25519.PrivateKey(c.IdentityPrivateKey).Public().(ed25519.PublicKey)
}

// BridgeID derives this Bridge's global identity from its stored public
// key. Never stored redundantly — always recomputed from the key itself.
func (c Config) BridgeID() protocol.BridgeID {
	return protocol.BridgeIDFromPublicKey(c.PublicKey())
}

// Validate checks a loaded or about-to-be-saved config. An identity key
// with no Host URL yet is valid (mid-join); a Host URL with no identity key
// is not.
func (c Config) Validate() error {
	if c.SchemaVersion != protocol.SchemaVersion {
		return fmt.Errorf("swarmconn: schema version %d, want %d", c.SchemaVersion, protocol.SchemaVersion)
	}
	if len(c.IdentityPrivateKey) != 0 && len(c.IdentityPrivateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("swarmconn: identity private key is %d bytes, want %d", len(c.IdentityPrivateKey), ed25519.PrivateKeySize)
	}
	if c.HostURL != "" && !c.HasIdentity() {
		return errors.New("swarmconn: a Host URL is stored without an identity key")
	}
	return nil
}

// ErrNotJoined means the store has never been saved to.
var ErrNotJoined = errors.New("swarmconn: no Host connection is stored yet")

// FileStore persists a Config to one file, using the same atomic-write
// recipe as auth.FileStore and bridgeconfig.FileStore: temp file in the
// same directory, fsync, rename, fsync the directory. A reader never
// observes a torn write.
type FileStore struct {
	// Path is the config file. Its directory must already exist.
	Path string

	// crashAfter, when set, aborts Save after the named step. Tests only.
	crashAfter string
}

// NewFileStore returns a store for a path.
func NewFileStore(path string) *FileStore { return &FileStore{Path: path} }

// errSimulatedCrash is what a simulated interruption returns.
var errSimulatedCrash = errors.New("swarmconn: simulated crash")

// Load reads the stored config.
func (f *FileStore) Load() (Config, error) {
	raw, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, ErrNotJoined
	}
	if err != nil {
		return Config{}, fmt.Errorf("swarmconn: reading config: %w", err)
	}

	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("swarmconn: the stored connection is unreadable (%v); "+
			"move or remove %s and rejoin the Swarm", err, f.Path)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Save writes the config atomically.
func (f *FileStore) Save(c Config) error {
	c.SchemaVersion = protocol.SchemaVersion
	c.PersistedAt = time.Now().UTC()
	if err := c.Validate(); err != nil {
		return err
	}

	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("swarmconn: encoding config: %w", err)
	}

	dir := filepath.Dir(f.Path)
	tmp, err := os.CreateTemp(dir, ".swarm-connection-*.tmp")
	if err != nil {
		return fmt.Errorf("swarmconn: creating temporary file: %w", err)
	}
	tmpName := tmp.Name()

	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("swarmconn: securing temporary file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		cleanup()
		return fmt.Errorf("swarmconn: writing config: %w", err)
	}
	if f.crashAfter == "write" {
		cleanup()
		return errSimulatedCrash
	}

	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("swarmconn: flushing config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("swarmconn: closing config: %w", err)
	}
	if f.crashAfter == "fsync" {
		os.Remove(tmpName)
		return errSimulatedCrash
	}

	if err := os.Rename(tmpName, f.Path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("swarmconn: publishing config: %w", err)
	}
	if f.crashAfter == "rename" {
		// The rename has happened; the new config is live even though the
		// caller is about to see an error. Callers must not assume an
		// error here means the old config is still in effect.
		return errSimulatedCrash
	}

	return syncDir(dir)
}

// syncDir flushes a directory entry, so a rename survives power loss.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("swarmconn: opening config directory: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		// Some filesystems refuse to fsync a directory. Not a reason to
		// fail a save that has otherwise succeeded, but the durability
		// guarantee is weaker there.
		return nil
	}
	return nil
}
