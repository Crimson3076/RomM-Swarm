package ingest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// FileJournal is the persistent Journal ADR 0015 calls for — the "Bridge's
// local database" MemoryJournal's own doc comment names as its Phase 1
// replacement. It implements Journal with exactly the same transition
// legality checks MemoryJournal enforces, so either can be substituted for
// the other without changing Flow's behaviour.
//
// One JSON file per transfer, holding that transfer's full ordered
// []Transition, written with the same atomic recipe auth.FileStore and
// bridgeconfig.FileStore use: temp file in the same directory, fsync,
// rename, fsync the directory. A reader never observes a torn write.
//
// There is no separate index for "list recent transfers" (see Recent):
// protocol.TransferID is crypto/rand-derived, not time-ordered, so the
// filename itself can't be sorted chronologically, but the file's own mtime
// — which changes on every Append — can, and directory listing plus mtime
// is enough for a single Bridge's transfer volume without adding a database.
type FileJournal struct {
	Dir string

	mu sync.Mutex
}

// NewFileJournal returns a journal backed by dir, creating it if necessary.
func NewFileJournal(dir string) (*FileJournal, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("ingest: creating journal directory: %w", err)
	}
	return &FileJournal{Dir: dir}, nil
}

func (j *FileJournal) pathFor(id protocol.TransferID) string {
	return filepath.Join(j.Dir, string(id)+".json")
}

// Append implements Journal, refusing illegal transitions on exactly the
// same terms as MemoryJournal.Append.
func (j *FileJournal) Append(id protocol.TransferID, t Transition) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	existing, err := j.readLocked(id)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		from := existing[len(existing)-1].To
		if err := protocol.Transition(from, t.To); err != nil {
			return err
		}
	} else if t.To != protocol.StateReceiving {
		return fmt.Errorf("ingest: a transfer must begin at %s, not %s", protocol.StateReceiving, t.To)
	}

	return j.writeLocked(id, append(existing, t))
}

// Current implements Journal.
func (j *FileJournal) Current(id protocol.TransferID) (protocol.DestinationState, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()

	h, err := j.readLocked(id)
	if err != nil || len(h) == 0 {
		return "", false
	}
	return h[len(h)-1].To, true
}

// History implements Journal.
func (j *FileJournal) History(id protocol.TransferID) []Transition {
	j.mu.Lock()
	defer j.mu.Unlock()

	h, err := j.readLocked(id)
	if err != nil {
		return nil
	}
	out := make([]Transition, len(h))
	copy(out, h)
	return out
}

// Recent returns up to limit transfer IDs, most recently active first, for
// the admin UI's activity view. limit <= 0 means no limit.
func (j *FileJournal) Recent(limit int) ([]protocol.TransferID, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	entries, err := os.ReadDir(j.Dir)
	if err != nil {
		return nil, fmt.Errorf("ingest: listing journal directory: %w", err)
	}

	type item struct {
		id  protocol.TransferID
		mod time.Time
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // a file that vanished or errored between ReadDir and Info is not fatal to the listing
		}
		items = append(items, item{
			id:  protocol.TransferID(strings.TrimSuffix(e.Name(), ".json")),
			mod: info.ModTime(),
		})
	}
	sort.Slice(items, func(a, b int) bool { return items[a].mod.After(items[b].mod) })
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}

	out := make([]protocol.TransferID, len(items))
	for i, it := range items {
		out[i] = it.id
	}
	return out, nil
}

func (j *FileJournal) readLocked(id protocol.TransferID) ([]Transition, error) {
	raw, err := os.ReadFile(j.pathFor(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ingest: reading journal for %s: %w", id, err)
	}
	var t []Transition
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("ingest: journal for %s is corrupt: %w", id, err)
	}
	return t, nil
}

func (j *FileJournal) writeLocked(id protocol.TransferID, history []Transition) error {
	raw, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return fmt.Errorf("ingest: encoding journal for %s: %w", id, err)
	}

	tmp, err := os.CreateTemp(j.Dir, ".journal-*.tmp")
	if err != nil {
		return fmt.Errorf("ingest: creating temporary journal file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("ingest: securing temporary journal file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		cleanup()
		return fmt.Errorf("ingest: writing journal: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("ingest: flushing journal: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("ingest: closing journal: %w", err)
	}
	if err := os.Rename(tmpName, j.pathFor(id)); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("ingest: publishing journal: %w", err)
	}
	return syncJournalDir(j.Dir)
}

// syncJournalDir flushes a directory entry, so a rename survives power loss.
func syncJournalDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("ingest: opening journal directory: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		// Some filesystems refuse to fsync a directory. Not a reason to fail
		// a write that has otherwise succeeded.
		return nil
	}
	return nil
}
