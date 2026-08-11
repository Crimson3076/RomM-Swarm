// Package bridgeconfig persists one Bridge's own local configuration: its
// RomM connection, its destination mode and staging paths, and its admin UI
// password — the settings ADR 0015's daemon and admin UI need to survive a
// restart instead of being re-supplied as environment variables on every
// invocation, the way cmd/swarm-bridge's one-shot CLI requires today.
//
// This is deliberately a separate store from auth.FileStore/auth.Credential.
// That one persists the Bridge's own Host-enrollment refresh token (Phase 2)
// — a different credential, with different validation and a different
// lifecycle. The two happen to share a persistence recipe, not a type.
package bridgeconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// Config is what a Bridge persists about itself.
type Config struct {
	SchemaVersion int `json:"schema_version"`

	// RommURL and RommToken are the Bridge's connection to its own RomM
	// instance. RommToken is stored in the clear, for the same reason
	// auth.Credential's refresh token is: an unattended process must be able
	// to read it after a restart, and there is nowhere to hide a key from a
	// process that has to use it anyway. File permissions are what protect
	// it — see Save.
	RommURL   string `json:"romm_url"`
	RommToken string `json:"romm_token"`

	// DestinationMode, LibraryRoot, ReceiveStagingDir, and PublishStagingDir
	// mirror protocol.DestinationMode and destination.Config's field names
	// exactly, so a loaded Config can be handed straight to
	// bridge/destination without renaming anything.
	DestinationMode   protocol.DestinationMode `json:"destination_mode,omitempty"`
	LibraryRoot       string                   `json:"library_root,omitempty"`
	ReceiveStagingDir string                   `json:"receive_staging_dir,omitempty"`
	PublishStagingDir string                   `json:"publish_staging_dir,omitempty"`

	// AdminPasswordHash is a PBKDF2-HMAC-SHA256 hash (see bridge/adminui),
	// never the password itself.
	AdminPasswordHash string `json:"admin_password_hash,omitempty"`

	// ListenAddr is the admin UI's bind address. Empty means the daemon's
	// own default.
	ListenAddr string `json:"listen_addr,omitempty"`

	PersistedAt time.Time `json:"persisted_at"`
}

// Configured reports whether enough is present for the Bridge to actually
// connect to a RomM instance. False is what tells cmd/bridge to show the
// first-run setup flow instead of the normal dashboard.
func (c Config) Configured() bool {
	return c.RommURL != "" && c.RommToken != ""
}

// Validate checks a loaded or about-to-be-saved config. It does not require
// RommURL/RommToken to be set — an unconfigured Config is a valid state
// (freshly created, awaiting first-run setup), distinct from a corrupt one.
func (c Config) Validate() error {
	if c.SchemaVersion != protocol.SchemaVersion {
		return fmt.Errorf("bridgeconfig: schema version %d, want %d", c.SchemaVersion, protocol.SchemaVersion)
	}
	if c.DestinationMode != "" && !c.DestinationMode.Valid() {
		return fmt.Errorf("bridgeconfig: destination mode %q is not supported", c.DestinationMode)
	}
	return nil
}

// ErrNotConfigured means the store has never been saved to.
var ErrNotConfigured = errors.New("bridgeconfig: no configuration is stored yet")

// FileStore persists a Config to one file, using the same atomic-write
// recipe as auth.FileStore: temp file in the same directory, fsync, rename,
// fsync the directory. A reader never observes a torn write.
type FileStore struct {
	// Path is the config file. Its directory must already exist.
	Path string

	// crashAfter, when set, aborts Save after the named step. Tests only.
	crashAfter string
}

// NewFileStore returns a store for a path.
func NewFileStore(path string) *FileStore { return &FileStore{Path: path} }

// errSimulatedCrash is what a simulated interruption returns.
var errSimulatedCrash = errors.New("bridgeconfig: simulated crash")

// Load reads the stored config.
func (f *FileStore) Load() (Config, error) {
	raw, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, ErrNotConfigured
	}
	if err != nil {
		return Config{}, fmt.Errorf("bridgeconfig: reading config: %w", err)
	}

	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		// A corrupt config file is not recoverable by guessing at its
		// contents. Saying so plainly beats a bare JSON parse error reaching
		// an operator who has no way to act on it.
		return Config{}, fmt.Errorf("bridgeconfig: the stored configuration is unreadable (%v); "+
			"move or remove %s and reconfigure through the admin UI", err, f.Path)
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
		return fmt.Errorf("bridgeconfig: encoding config: %w", err)
	}

	dir := filepath.Dir(f.Path)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("bridgeconfig: creating temporary config file: %w", err)
	}
	tmpName := tmp.Name()

	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("bridgeconfig: securing temporary config file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		cleanup()
		return fmt.Errorf("bridgeconfig: writing config: %w", err)
	}
	if f.crashAfter == "write" {
		cleanup()
		return errSimulatedCrash
	}

	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("bridgeconfig: flushing config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("bridgeconfig: closing config: %w", err)
	}
	if f.crashAfter == "fsync" {
		os.Remove(tmpName)
		return errSimulatedCrash
	}

	if err := os.Rename(tmpName, f.Path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("bridgeconfig: publishing config: %w", err)
	}
	if f.crashAfter == "rename" {
		// The rename has happened; the new config is live even though the
		// caller is about to see an error. Callers must not assume an error
		// here means the old config is still in effect.
		return errSimulatedCrash
	}

	return syncDir(dir)
}

// syncDir flushes a directory entry, so a rename survives power loss.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("bridgeconfig: opening config directory: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		// Some filesystems refuse to fsync a directory. Not a reason to fail
		// a save that has otherwise succeeded, but the durability guarantee
		// is weaker there.
		return nil
	}
	return nil
}
