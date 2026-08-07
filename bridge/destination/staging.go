// Package destination implements the receiving Bridge's staging and
// publication paths.
//
// Scope of Work Phase 6 fixes a "Required common receiving flow" that both
// destination modes share:
//
//  1. Download into a transfer-specific .part file in a Bridge-controlled
//     staging directory that RomM does not watch.
//  2. Verify size, structure, canonical payload identity, and approved reference
//     identity while the file remains staged.
//  3. Flush and fsync the staged file before handing it to either destination.
//  4. Persist the transfer as verified_in_staging.
//  5. Continue through exactly one destination-mode flow.
//
// The ordering is the substance. A file that reaches RomM's watched tree before
// it has been verified is a file RomM will index, and an operator will then have
// to work out why their library contains something that does not match anything.
package destination

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// PartSuffix marks an incomplete staged download.
const PartSuffix = ".part"

// DefaultAbandonedAfter is how long a staged file may sit untouched before it is
// treated as abandoned and removed.
//
// Phase 6 requires "Crash recovery and abandoned-staging cleanup". The window is
// generous because the cost of deleting a live transfer's staging file is
// restarting a large download, while the cost of keeping a dead one an extra day
// is some disk.
const DefaultAbandonedAfter = 48 * time.Hour

// SpaceHeadroom is the multiple of the expected payload size that must be free
// before a transfer starts.
//
// 2 rather than 1 because filesystem publication may need a second copy on the
// destination filesystem, and because filling a member's disk exactly to zero is
// a hostile thing for a background service to do.
const SpaceHeadroom = 2

// ErrInsufficientSpace means the staging filesystem cannot hold the payload.
var ErrInsufficientSpace = errors.New("destination: not enough free space to stage this transfer")

// Staging owns the Bridge-controlled directory that downloads land in.
type Staging struct {
	// Dir is the staging directory. It must not be inside RomM's watched library
	// tree; NewStaging cannot verify that on its own, so the Bridge's
	// configuration preflight checks it against the configured library path.
	Dir string
}

// NewStaging prepares a staging directory, creating it if necessary.
func NewStaging(dir string) (*Staging, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("destination: no staging directory was configured")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("destination: creating the staging directory: %w", err)
	}
	return &Staging{Dir: dir}, nil
}

// PathFor returns the staging path for a transfer.
//
// Keyed by transfer id rather than by filename, so two transfers of the same
// title cannot collide, and so a staged file carries no information about what
// it is until it is published.
func (s *Staging) PathFor(id protocol.TransferID) string {
	return filepath.Join(s.Dir, string(id)+PartSuffix)
}

// CheckSpace verifies there is room for a payload before the transfer begins.
func (s *Staging) CheckSpace(expectedSize int64) error {
	if expectedSize <= 0 {
		return nil
	}
	free, err := freeSpace(s.Dir)
	if err != nil {
		return err
	}
	need := expectedSize * SpaceHeadroom
	if free < need {
		return fmt.Errorf("%w: %d bytes free, %d needed for a %d-byte payload",
			ErrInsufficientSpace, free, need, expectedSize)
	}
	return nil
}

// Receive writes a payload into the transfer's staging file, then flushes and
// fsyncs it.
//
// Phase 6 step 3 requires the fsync before hand-off. Without it the bytes may
// still be in the page cache when the atomic rename publishes the name, and a
// power loss can leave a correctly named file with the wrong contents — the
// exact outcome the staging flow exists to prevent.
func (s *Staging) Receive(id protocol.TransferID, src io.Reader) (int64, error) {
	path := s.PathFor(id)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, fmt.Errorf("destination: opening the staging file: %w", err)
	}
	defer f.Close()

	n, err := io.Copy(f, src)
	if err != nil {
		return n, fmt.Errorf("destination: writing to staging: %w", err)
	}
	if err := f.Sync(); err != nil {
		return n, fmt.Errorf("destination: flushing the staging file: %w", err)
	}
	return n, nil
}

// Abandon removes a transfer's staging file.
func (s *Staging) Abandon(id protocol.TransferID) error {
	err := os.Remove(s.PathFor(id))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("destination: removing the staging file: %w", err)
	}
	return nil
}

// CleanupAbandoned removes staged files that have not been touched within the
// window, and reports how many it removed.
//
// Only files carrying the .part suffix are considered. A staging directory that
// somehow contains something else is left alone: a cleanup routine that deletes
// unrecognised files is a cleanup routine that will one day delete something
// that mattered.
func (s *Staging) CleanupAbandoned(olderThan time.Duration, now time.Time) (int, error) {
	if olderThan <= 0 {
		olderThan = DefaultAbandonedAfter
	}

	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return 0, fmt.Errorf("destination: reading the staging directory: %w", err)
	}

	removed := 0
	var firstErr error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), PartSuffix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if now.Sub(info.ModTime()) < olderThan {
			continue
		}
		if err := os.Remove(filepath.Join(s.Dir, e.Name())); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}

// Verify checks a staged file against its expected identity, while it is still
// staged.
//
// Returns the computed digest so the caller can record what was actually
// received when it does not match. A mismatch is not an error condition to be
// retried; it is a verification_conflict, which is a state an operator has to
// see.
func (s *Staging) Verify(id protocol.TransferID, expected protocol.Digest) (protocol.Digest, error) {
	path := s.PathFor(id)

	f, err := os.Open(path)
	if err != nil {
		return protocol.Digest{}, fmt.Errorf("destination: opening the staged file for verification: %w", err)
	}
	defer f.Close()

	got, err := protocol.DigestReader(f)
	if err != nil {
		return protocol.Digest{}, err
	}

	strength, ok := got.Compare(expected)
	if !ok {
		return got, fmt.Errorf("destination: the staged payload does not match the expected identity")
	}
	// Scope of Work: content arriving from a partially trusted peer is accepted
	// only on SHA-256. A catalogue-strength match is enough to say two records
	// describe one file; it is not enough to accept bytes.
	if strength < protocol.StrengthStrong {
		return got, fmt.Errorf(
			"destination: the staged payload matched only at %s strength; a transfer requires SHA-256 agreement",
			strength)
	}
	return got, nil
}
