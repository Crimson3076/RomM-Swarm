// Package ingest drives the receiving Bridge from a verified staged payload to
// an active Swarm source.
//
// Scope of Work Phase 6, and the decision log entry that explains it: "Require
// RomM ingestion and identity reconciliation before source activation — a
// verified file on disk is not yet a matched and usable RomM source."
//
// That distinction is the whole package. A Bridge that announced a new source
// the moment the bytes landed would advertise items its own RomM had not
// indexed, could not serve by name, and might have matched to something else
// entirely. Every such claim inflates coverage and, worse, inflates resilience —
// the figure members would rely on when deciding what still needs preserving.
//
// So the flow ends in a poll: hand the payload to the destination, then wait for
// RomM to report the expected platform, canonical identity, and reference
// identity. Only then does the Bridge republish its manifest and become a
// source.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// Expected describes the item RomM should end up holding.
type Expected struct {
	Platform  protocol.PlatformID
	FileID    protocol.FileID
	Canonical protocol.Digest
	Reference *protocol.ReferenceMatch
}

// Validate checks that an expectation is complete enough to reconcile against.
func (e Expected) Validate() error {
	if e.Platform == "" {
		return errors.New("ingest: no expected platform")
	}
	if e.Canonical.SHA256 == "" {
		return errors.New("ingest: no expected canonical digest")
	}
	if err := e.FileID.Validate(); err != nil {
		return err
	}
	return nil
}

// Observation is what the local RomM currently reports about an expected item.
type Observation struct {
	// Present reports whether anything plausibly matching has appeared yet.
	Present bool

	// Platform is where RomM filed it.
	Platform protocol.PlatformID

	// Canonical is the identity the Bridge computed from what RomM now holds.
	// Empty when RomM has indexed the file but the Bridge has not yet been able
	// to compute an identity for it.
	Canonical protocol.Digest

	// Name is RomM's name for the item, for operator-facing messages.
	Name string
}

// Library is the narrow view of a RomM server that reconciliation needs.
//
// An interface, and a deliberately small one. Scope of Work §7 requires adapter
// boundaries around RomM-specific endpoints; this is where the boundary sits for
// ingestion. It also keeps the polling logic testable without a RomM server,
// which matters because that is the part with the timing behaviour.
type Library interface {
	// Observe reports what RomM currently holds for an expectation. It must not
	// trigger a full library rescan: Scope of Work §7 requires avoiding
	// unnecessary load on participating RomM servers.
	Observe(context.Context, Expected) (Observation, error)
}

// Reconciler waits for RomM to ingest and match a handed-off payload.
type Reconciler struct {
	Library Library

	// Poll is the interval between observations. Zero uses the protocol default,
	// which is deliberately unhurried.
	Poll time.Duration

	// Timeout bounds the wait. Zero uses the protocol default.
	Timeout time.Duration

	// Now and After are injectable for tests. After returns a channel that fires
	// once the duration elapses.
	Now   func() time.Time
	After func(time.Duration) <-chan time.Time
}

func (r *Reconciler) poll() time.Duration {
	if r.Poll > 0 {
		return r.Poll
	}
	return protocol.IngestionPollInterval
}

func (r *Reconciler) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return protocol.DefaultIngestionTimeout
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reconciler) after(d time.Duration) <-chan time.Time {
	if r.After != nil {
		return r.After(d)
	}
	return time.After(d)
}

// Result is the outcome of waiting for ingestion.
type Result struct {
	// State is the destination state reached: romm_matched, romm_unmatched, or
	// ingestion_timeout.
	State protocol.DestinationState

	// Attempts is how many times RomM was observed.
	Attempts int

	// Elapsed is how long the wait took.
	Elapsed time.Duration

	// Detail explains a non-matching outcome in operator-facing language.
	Detail string
}

// Await polls the local RomM until the expected item appears, the expectation is
// contradicted, or the deadline passes.
//
// It never returns romm_matched on partial evidence. An item that has appeared
// but whose identity does not match is romm_unmatched, which is a review state:
// Phase 6 acceptance requires that mismatch "creates a visible review state and
// never increases coverage or resilience".
func (r *Reconciler) Await(ctx context.Context, expected Expected) (Result, error) {
	if err := expected.Validate(); err != nil {
		return Result{}, err
	}
	if r.Library == nil {
		return Result{}, errors.New("ingest: no library was configured")
	}

	start := r.now()
	deadline := start.Add(r.timeout())
	attempts := 0

	for {
		attempts++

		obs, err := r.Library.Observe(ctx, expected)
		if err != nil {
			// A transient RomM failure is not a mismatch. Scope of Work §7
			// requires backoff during RomM failures rather than treating them as
			// verdicts, so the loop keeps waiting until the deadline.
			if r.now().After(deadline) {
				return Result{
					State:    protocol.StateIngestionTimeout,
					Attempts: attempts,
					Elapsed:  r.now().Sub(start),
					Detail: fmt.Sprintf(
						"RomM could not be reached while waiting for ingestion, and the deadline passed: %v", err),
				}, nil
			}
		} else if obs.Present {
			if state, detail, decided := classify(expected, obs); decided {
				return Result{
					State:    state,
					Attempts: attempts,
					Elapsed:  r.now().Sub(start),
					Detail:   detail,
				}, nil
			}
		}

		if !r.now().Before(deadline) {
			return Result{
				State:    protocol.StateIngestionTimeout,
				Attempts: attempts,
				Elapsed:  r.now().Sub(start),
				Detail: fmt.Sprintf(
					"RomM did not report the expected item within %s. The file is in place; it may need a library scan",
					r.timeout()),
			}, nil
		}

		select {
		case <-ctx.Done():
			return Result{
				State:    protocol.StateIngestionTimeout,
				Attempts: attempts,
				Elapsed:  r.now().Sub(start),
				Detail:   "the wait for ingestion was cancelled",
			}, ctx.Err()
		case <-r.after(r.poll()):
		}
	}
}

// classify decides whether an observation settles the question.
//
// Returns decided=false when RomM has indexed something but has not yet produced
// enough information to judge it — an in-progress scan looks like this, and
// treating it as a mismatch would fail transfers that were about to succeed.
func classify(expected Expected, obs Observation) (protocol.DestinationState, string, bool) {
	if obs.Canonical.SHA256 == "" {
		return "", "", false
	}

	strength, ok := obs.Canonical.Compare(expected.Canonical)
	if !ok || strength < protocol.StrengthStrong {
		return protocol.StateRommUnmatched, fmt.Sprintf(
			"RomM indexed %q, but its content does not match the payload that was verified and imported. This needs review",
			obs.Name), true
	}

	if obs.Platform != "" && obs.Platform != expected.Platform {
		// The bytes are right but RomM filed them somewhere unexpected. Not a
		// corruption, but not something to activate silently either: a source
		// advertised under the wrong platform is a source nobody will find.
		return protocol.StateRommUnmatched, fmt.Sprintf(
			"RomM indexed the expected content under platform %s rather than %s. This needs review",
			obs.Platform, expected.Platform), true
	}

	return protocol.StateRommMatched, fmt.Sprintf("RomM indexed and matched %q", obs.Name), true
}
