package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/destination"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// The receiving flow.
//
// Scope of Work Phase 6 specifies it step by step; this is that specification
// executed, with every transition validated against protocol.Transition so the
// implementation cannot quietly diverge from the state machine it claims to
// follow.

// Journal records destination state transitions durably.
//
// Phase 6 requires a "Transfer and destination state machine with durable
// transitions and timeouts". Durability matters because the states that need an
// operator's attention — ingestion_timeout, romm_unmatched — are exactly the ones
// a Bridge is likely to be restarted in the middle of.
type Journal interface {
	// Append records a transition. It must return an error rather than record an
	// illegal one.
	Append(protocol.TransferID, Transition) error

	// Current returns the transfer's state, and whether it is known.
	Current(protocol.TransferID) (protocol.DestinationState, bool)

	// History returns every transition for a transfer, oldest first.
	History(protocol.TransferID) []Transition
}

// Transition is one recorded state change.
type Transition struct {
	From protocol.DestinationState
	To   protocol.DestinationState
	At   time.Time
	// Detail is operator-facing. It never contains a credential or a path
	// outside the Bridge's own configuration.
	Detail string
}

// Uploader hands a verified payload to RomM through its own upload API.
//
// Phase 6's API-only flow: "send the verified staged payload through RomM's
// supported chunked upload API using the local scoped standard-user token" and
// "Let RomM control its own assembly, publication, indexing, and matching
// process. The Bridge does not write into the RomM library."
//
// filename is what the uploaded item should be called — RomM's chunked upload
// session requires a name up front, and Expected carries no filename of its
// own (a verified identity is not a name). Callers pass the same value they
// would give a filesystem-mode publish.
type Uploader interface {
	Upload(ctx context.Context, stagedPath, filename string, expected Expected) error
}

// ErrUploadUnprobed is returned by UnprobedUploader.
var ErrUploadUnprobed = errors.New(
	"ingest: RomM's upload API has not been probed, so API-only import cannot be performed")

// UnprobedUploader is the API-only uploader used until a real RomM instance has
// been probed.
//
// It fails, deliberately and with an explanation. RomM's upload endpoint, its
// chunking scheme, and its completion signal are not known to this codebase: the
// environment it was written in could not reach a RomM server. Writing a
// plausible-looking implementation against a guessed protocol would produce code
// that appears finished, passes its own tests against a fake built from the same
// guess, and fails against every real server.
//
// Failing loudly at the exact point the gap bites is more useful than that. The
// filesystem publication path is unaffected and fully implemented; run
// `swarm-probe` against a real instance to close this.
type UnprobedUploader struct{}

// Upload implements Uploader.
func (UnprobedUploader) Upload(context.Context, string, string, Expected) error {
	return fmt.Errorf("%w: run swarm-probe against a RomM instance and record its upload operation (see ADR 0003)",
		ErrUploadUnprobed)
}

// Publisher republishes the Bridge's manifest after a successful import, which
// is what makes the item visible to a Swarm.
type Publisher interface {
	// Republish advertises the newly matched item. Only called after
	// romm_matched.
	Republish(context.Context, Expected) error
}

// Flow runs the receiving flow for one destination mode.
type Flow struct {
	Mode    protocol.DestinationMode
	Staging *destination.Staging
	Journal Journal

	// Publisher is used for filesystem publication mode.
	Publisher *destination.Publisher

	// Uploader is used for API-only mode.
	Uploader Uploader

	// Reconciler waits for RomM to ingest and match.
	Reconciler *Reconciler

	// Manifest republishes after a match.
	Manifest Publisher

	// Now is injectable for tests.
	Now func() time.Time
}

func (f *Flow) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// advance validates and records a transition.
func (f *Flow) advance(id protocol.TransferID, to protocol.DestinationState, detail string) error {
	from, ok := f.Journal.Current(id)
	if !ok {
		from = ""
	}
	if from != "" {
		if err := protocol.Transition(from, to); err != nil {
			return err
		}
	}
	return f.Journal.Append(id, Transition{From: from, To: to, At: f.now(), Detail: detail})
}

// Receive runs the whole flow: stage, verify, hand off, wait for ingestion, and
// activate.
//
// The returned state is the terminal state reached. An error is returned only
// for conditions the Bridge could not classify; a wrong payload, a failed
// ingestion, and a timeout are all *outcomes*, recorded in the journal as review
// states, not errors.
func (f *Flow) Receive(
	ctx context.Context,
	id protocol.TransferID,
	src io.Reader,
	expected Expected,
	relativeDest string,
) (protocol.DestinationState, error) {
	if !f.Mode.Valid() {
		return "", fmt.Errorf("ingest: destination mode %q is not supported", f.Mode)
	}
	if err := expected.Validate(); err != nil {
		return "", err
	}

	// Step 1: download into a transfer-specific .part file outside RomM's
	// watched tree.
	if err := f.advance(id, protocol.StateReceiving, "receiving into staging"); err != nil {
		return "", err
	}
	if expected.Canonical.Size > 0 {
		if err := f.Staging.CheckSpace(expected.Canonical.Size); err != nil {
			// No space is a cancellation, not a verification failure: nothing is
			// wrong with the payload.
			_ = f.advance(id, protocol.StateCancelled, err.Error())
			return protocol.StateCancelled, nil
		}
	}
	if _, err := f.Staging.Receive(id, src); err != nil {
		_ = f.advance(id, protocol.StateCancelled, err.Error())
		return protocol.StateCancelled, nil
	}

	// Steps 2 and 3: verify while staged, then flush. Staging.Receive has
	// already fsynced; Verify reads the file back, which is the check that the
	// bytes on disk are the bytes that were verified.
	if _, err := f.Staging.Verify(id, expected.Canonical); err != nil {
		_ = f.Staging.Abandon(id)
		_ = f.advance(id, protocol.StateVerificationConflict, err.Error())
		return protocol.StateVerificationConflict, nil
	}

	// Step 4: persist verified_in_staging before any hand-off. This is the
	// checkpoint that makes the rest recoverable.
	if err := f.advance(id, protocol.StateVerifiedInStaging,
		"the payload matched its expected identity while staged"); err != nil {
		return "", err
	}

	// Step 5: exactly one destination-mode flow.
	handOff, err := protocol.HandOffState(f.Mode)
	if err != nil {
		return "", err
	}

	switch f.Mode {
	case protocol.ModeAPIOnly:
		if f.Uploader == nil {
			return "", errors.New("ingest: API-only mode requires an uploader")
		}
		if err := f.advance(id, handOff, "handing the verified payload to RomM's upload API"); err != nil {
			return "", err
		}
		if err := f.Uploader.Upload(ctx, f.Staging.PathFor(id), filepath.Base(relativeDest), expected); err != nil {
			// The upload failed. The payload is still staged and still verified,
			// so this is not a verification conflict — but the transfer cannot
			// proceed, and the state machine has no path back from
			// uploading_to_romm except onward or to review.
			_ = f.advance(id, protocol.StateVerificationConflict, "the upload to RomM failed: "+err.Error())
			return protocol.StateVerificationConflict, err
		}

	case protocol.ModeFilesystemPublication:
		if f.Publisher == nil {
			return "", errors.New("ingest: filesystem publication mode requires a configured publisher")
		}
		published, err := f.Publisher.Publish(f.Staging.PathFor(id), relativeDest, expected.Canonical)
		if err != nil {
			// Publication refused. Nothing reached the library, and the staged
			// file is untouched, so the transfer stops before hand-off.
			_ = f.advance(id, protocol.StateCancelled, err.Error())
			return protocol.StateCancelled, err
		}
		if err := f.advance(id, handOff, "published to "+published); err != nil {
			return "", err
		}
	}

	// Wait for RomM to ingest and match.
	if err := f.advance(id, protocol.StateAwaitingRommIngestion, "waiting for RomM to index the item"); err != nil {
		return "", err
	}
	result, err := f.Reconciler.Await(ctx, expected)
	if err != nil && result.State == "" {
		return "", err
	}
	if err := f.advance(id, result.State, result.Detail); err != nil {
		return "", err
	}
	if result.State != protocol.StateRommMatched {
		// ingestion_timeout and romm_unmatched are terminal review states. The
		// Bridge does not become a source, and coverage does not move.
		return result.State, nil
	}

	// Republish the manifest, then activate. The order matters: a source
	// advertised before the manifest names it is a source nobody can find.
	if f.Manifest != nil {
		if err := f.Manifest.Republish(ctx, expected); err != nil {
			_ = f.advance(id, protocol.StateQuarantined,
				"RomM matched the item but the manifest could not be republished: "+err.Error())
			return protocol.StateQuarantined, err
		}
	}
	if err := f.advance(id, protocol.StateSourceActive, "the item is now an active source"); err != nil {
		return "", err
	}
	return protocol.StateSourceActive, nil
}

// MemoryJournal is an in-memory Journal for tests and for the Phase 0 reference
// implementation. Phase 1 replaces it with the Bridge's local database.
type MemoryJournal struct {
	mu      sync.Mutex
	history map[protocol.TransferID][]Transition
}

// NewMemoryJournal returns an empty journal.
func NewMemoryJournal() *MemoryJournal {
	return &MemoryJournal{history: map[protocol.TransferID][]Transition{}}
}

// Append implements Journal, refusing illegal transitions.
func (j *MemoryJournal) Append(id protocol.TransferID, t Transition) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if existing := j.history[id]; len(existing) > 0 {
		from := existing[len(existing)-1].To
		if err := protocol.Transition(from, t.To); err != nil {
			return err
		}
	} else if t.To != protocol.StateReceiving {
		return fmt.Errorf("ingest: a transfer must begin at %s, not %s", protocol.StateReceiving, t.To)
	}

	j.history[id] = append(j.history[id], t)
	return nil
}

// Current implements Journal.
func (j *MemoryJournal) Current(id protocol.TransferID) (protocol.DestinationState, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()

	h := j.history[id]
	if len(h) == 0 {
		return "", false
	}
	return h[len(h)-1].To, true
}

// History implements Journal.
func (j *MemoryJournal) History(id protocol.TransferID) []Transition {
	j.mu.Lock()
	defer j.mu.Unlock()

	out := make([]Transition, len(j.history[id]))
	copy(out, j.history[id])
	return out
}

// Path returns the states a transfer passed through, for assertions and for
// operator-facing history.
func Path(j Journal, id protocol.TransferID) []protocol.DestinationState {
	history := j.History(id)
	out := make([]protocol.DestinationState, 0, len(history))
	for _, t := range history {
		out = append(out, t.To)
	}
	return out
}
