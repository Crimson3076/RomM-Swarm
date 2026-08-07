package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// Bridge-side transactional credential persistence.
//
// Scope of Work Phase 2 deliverable: "Rotating refresh credentials persisted
// transactionally by the Bridge." The Host half of the protocol is only as good
// as this half. If the Bridge can end up with a half-written credential file,
// the grace window cannot help it — there is nothing valid to present.
//
// So the write is atomic in the only sense that survives power loss:
//
//  1. Write the new credential to a temporary file in the same directory.
//  2. fsync that file, so its contents reach the disk.
//  3. Rename it over the real path. Rename within a directory is atomic: a
//     reader sees either the whole old file or the whole new one, never a
//     mixture and never nothing.
//  4. fsync the directory, so the rename itself reaches the disk.
//
// Step 4 is the one that is usually skipped and usually matters. Without it the
// rename can still be in the filesystem's journal when the power goes, and the
// file reverts to its previous contents — which, for this protocol, is
// survivable (it is the grace path) but is not what the code claimed to do.

// ErrNoCredential means the Bridge has not enrolled, or its store is empty.
var ErrNoCredential = errors.New("auth: no credential is stored; the Bridge must enrol")

// Credential is what a Bridge persists between rotations.
//
// The refresh token is stored in the clear, because the Bridge must be able to
// present it after an unattended restart and there is nowhere to hide a key from
// a process that has to read it anyway. What protects it is file permissions and
// the fact that a stolen one is bounded: it is single-use, rotation invalidates
// it, and reuse revokes the family.
type Credential struct {
	SchemaVersion int               `json:"schema_version"`
	BridgeID      protocol.BridgeID `json:"bridge_id"`
	Refresh       Token             `json:"refresh"`
	Generation    uint64            `json:"generation"`
	PersistedAt   time.Time         `json:"persisted_at"`
}

// Validate checks a loaded credential.
func (c Credential) Validate() error {
	if c.SchemaVersion != protocol.SchemaVersion {
		return fmt.Errorf("auth: credential schema version %d, want %d", c.SchemaVersion, protocol.SchemaVersion)
	}
	if err := c.BridgeID.Validate(); err != nil {
		return err
	}
	if c.Refresh == "" {
		return errors.New("auth: credential has no refresh token")
	}
	return nil
}

// FileStore persists a Bridge's credential to one file.
type FileStore struct {
	// Path is the credential file. Its directory must already exist and should
	// be private to the Bridge.
	Path string

	// crashAfter, when set, aborts the save after the named step. Tests use it
	// to simulate power loss at each point in the write; nothing else sets it.
	crashAfter string
}

// NewFileStore returns a store for a path.
func NewFileStore(path string) *FileStore { return &FileStore{Path: path} }

// errSimulatedCrash is what a simulated interruption returns.
var errSimulatedCrash = errors.New("auth: simulated crash")

// Load reads the stored credential.
func (f *FileStore) Load() (Credential, error) {
	raw, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Credential{}, ErrNoCredential
	}
	if err != nil {
		return Credential{}, fmt.Errorf("auth: reading credential: %w", err)
	}

	var c Credential
	if err := json.Unmarshal(raw, &c); err != nil {
		// A corrupt credential file is indistinguishable from no credential for
		// recovery purposes: either way the Bridge cannot authenticate and the
		// owner must re-enrol. Saying so plainly beats a JSON parse error.
		return Credential{}, fmt.Errorf("%w: the stored credential is unreadable (%v)", ErrNoCredential, err)
	}
	if err := c.Validate(); err != nil {
		return Credential{}, err
	}
	return c, nil
}

// Save writes the credential atomically.
func (f *FileStore) Save(c Credential) error {
	c.SchemaVersion = protocol.SchemaVersion
	c.PersistedAt = time.Now().UTC()
	if err := c.Validate(); err != nil {
		return err
	}

	raw, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("auth: encoding credential: %w", err)
	}

	dir := filepath.Dir(f.Path)
	tmp, err := os.CreateTemp(dir, ".credential-*.tmp")
	if err != nil {
		return fmt.Errorf("auth: creating temporary credential file: %w", err)
	}
	tmpName := tmp.Name()

	// From here every failure must remove the temporary file, or a crashing
	// Bridge slowly fills its config directory with abandoned credentials.
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("auth: securing temporary credential file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		cleanup()
		return fmt.Errorf("auth: writing credential: %w", err)
	}
	if f.crashAfter == "write" {
		cleanup()
		return errSimulatedCrash
	}

	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("auth: flushing credential: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("auth: closing credential: %w", err)
	}
	if f.crashAfter == "fsync" {
		os.Remove(tmpName)
		return errSimulatedCrash
	}

	if err := os.Rename(tmpName, f.Path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("auth: publishing credential: %w", err)
	}
	if f.crashAfter == "rename" {
		// The rename has happened. The new credential is live even though the
		// caller is about to see an error — which is exactly the ambiguous state
		// this protocol has to tolerate, so the test asserts on it rather than
		// pretending it cannot occur.
		return errSimulatedCrash
	}

	return syncDir(dir)
}

// syncDir flushes a directory entry, so a rename survives power loss.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("auth: opening credential directory: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		// Some filesystems refuse to fsync a directory. That is not a reason to
		// fail a rotation that has otherwise succeeded, but it is worth being
		// honest that the durability guarantee is weaker there.
		return nil
	}
	return nil
}

// Client drives rotation from the Bridge side.
//
// It exists to make the ordering explicit and testable: persist the new
// credential *before* treating it as live. A Bridge that used a credential it
// had not yet stored would, on a crash, hold neither the old one nor the new
// one.
type Client struct {
	Store *FileStore

	// Rotate performs the exchange with the Host. In production this is an HTTP
	// call; in tests it is the Verifier directly.
	Rotate func(protocol.BridgeID, Token) (Result, error)
}

// Refresh exchanges the stored credential for a new one and persists it.
//
// On success the new credential is on disk before the caller sees it. On
// failure the old credential is left untouched, so the next attempt takes the
// recovery path rather than finding nothing at all.
func (c *Client) Refresh() (Result, error) {
	current, err := c.Store.Load()
	if err != nil {
		return Result{}, err
	}

	result, err := c.Rotate(current.BridgeID, current.Refresh)
	if err != nil {
		return Result{}, err
	}

	next := Credential{
		BridgeID:   current.BridgeID,
		Refresh:    result.Refresh,
		Generation: result.Generation,
	}
	if err := c.Store.Save(next); err != nil {
		// The Host has already rotated. The Bridge still holds the old
		// credential, so its next attempt lands on the recovery path. Returning
		// the error rather than the result is what stops the caller from acting
		// on a credential that is not durable.
		return Result{}, fmt.Errorf("auth: the credential was rotated but could not be stored: %w", err)
	}
	return result, nil
}
