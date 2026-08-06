package protocol

import (
	"fmt"
	"time"
)

// The receiving-side destination state machine.
//
// Scope of Work Phase 6 fixes the required path exactly:
//
//	receiving -> verified_in_staging -> (uploading_to_romm | published_to_filesystem)
//	          -> awaiting_romm_ingestion -> romm_matched -> source_active
//
// with terminal or review states cancelled, verification_conflict,
// romm_unmatched, ingestion_timeout, and quarantined. The governing rule, and
// the reason this machine exists as an enforced type rather than a diagram in a
// document: "A file on disk is not a Swarm source until it reaches
// source_active."
//
// The machine is defined in Phase 0 (deliverable: "Define the RomM ingestion
// state machine and timeout behavior") so that Phase 6 implements against a
// frozen contract instead of inventing one under delivery pressure.

// DestinationState is the receiving Bridge's state for one transfer.
type DestinationState string

const (
	// StateReceiving means bytes are landing in a transfer-specific .part file
	// inside a staging directory RomM does not watch.
	StateReceiving DestinationState = "receiving"

	// StateVerifiedInStaging means size, structure, canonical payload identity,
	// and approved reference identity have all been confirmed while the file is
	// still staged, and the file has been flushed and fsynced.
	StateVerifiedInStaging DestinationState = "verified_in_staging"

	// StateUploadingToRomm is the API-only destination branch: handing the
	// verified payload to RomM's own upload API. The Bridge never writes into
	// the RomM library.
	StateUploadingToRomm DestinationState = "uploading_to_romm"

	// StatePublishedToFilesystem is the filesystem-publication branch: the
	// verified payload has been atomically renamed into the library from a
	// temporary file on the same filesystem, and the destination directory has
	// been fsynced.
	StatePublishedToFilesystem DestinationState = "published_to_filesystem"

	// StateAwaitingRommIngestion means the payload has been handed off and the
	// Bridge is waiting for RomM to ingest and match it.
	StateAwaitingRommIngestion DestinationState = "awaiting_romm_ingestion"

	// StateRommMatched means RomM reports the expected platform, canonical
	// identity, and reference identity.
	StateRommMatched DestinationState = "romm_matched"

	// StateSourceActive means the item has been republished in the Bridge's
	// manifest and may now be advertised as a source. This is the only state in
	// which the Bridge is a source for this item.
	StateSourceActive DestinationState = "source_active"

	// StateCancelled means the transfer was abandoned before hand-off.
	StateCancelled DestinationState = "cancelled"

	// StateVerificationConflict means the received payload did not match the
	// expected identity. The payload is never handed to a destination.
	StateVerificationConflict DestinationState = "verification_conflict"

	// StateRommUnmatched means RomM ingested the file but matched it to
	// something other than the expected item, or failed to match it at all.
	StateRommUnmatched DestinationState = "romm_unmatched"

	// StateIngestionTimeout means RomM did not report the expected item within
	// the ingestion deadline.
	StateIngestionTimeout DestinationState = "ingestion_timeout"

	// StateQuarantined means a moderator or an automatic safety rule withdrew
	// the item from service pending review.
	StateQuarantined DestinationState = "quarantined"
)

// DestinationMode is the Bridge's configured import path. Scope of Work §3
// makes these two mutually exclusive and requires filesystem publication to be
// an explicit operator opt-in with a scoped writable mount.
type DestinationMode string

const (
	// ModeAPIOnly imports through RomM's upload API using the local scoped
	// standard-user token. Requires no library filesystem access at all.
	ModeAPIOnly DestinationMode = "api_only"

	// ModeFilesystemPublication writes into a narrowly scoped writable mount by
	// atomic rename after verification.
	ModeFilesystemPublication DestinationMode = "filesystem_publication"
)

// Valid reports whether the mode is one of the two supported modes.
func (m DestinationMode) Valid() bool {
	return m == ModeAPIOnly || m == ModeFilesystemPublication
}

// transitions is the complete, closed set of legal moves.
//
// Two absences are deliberate:
//
//   - There is no transition out of StateUploadingToRomm or
//     StatePublishedToFilesystem back to StateCancelled. Once the payload has
//     been handed to RomM or renamed into the library, "cancel" is a lie: the
//     bytes are already there. Such a transfer must resolve through ingestion
//     or land in a visible review state, so an operator sees it.
//   - There is no transition into StateSourceActive from anywhere except
//     StateRommMatched. That is the whole point of the machine.
var transitions = map[DestinationState][]DestinationState{
	StateReceiving: {
		StateVerifiedInStaging,
		StateVerificationConflict,
		StateCancelled,
	},
	StateVerifiedInStaging: {
		StateUploadingToRomm,
		StatePublishedToFilesystem,
		StateCancelled,
	},
	StateUploadingToRomm: {
		StateAwaitingRommIngestion,
		StateVerificationConflict,
	},
	StatePublishedToFilesystem: {
		StateAwaitingRommIngestion,
	},
	StateAwaitingRommIngestion: {
		StateRommMatched,
		StateRommUnmatched,
		StateIngestionTimeout,
	},
	StateRommMatched: {
		StateSourceActive,
	},
	StateSourceActive: {},

	// Review and terminal states.
	StateCancelled:            {},
	StateVerificationConflict: {},
	StateRommUnmatched:        {},
	StateIngestionTimeout:     {},
	StateQuarantined:          {},
}

// quarantinableFrom lists the states a moderator may quarantine out of.
// Quarantine is an override rather than a normal step, so it is kept out of the
// main table to stop it being mistaken for part of the happy path.
var quarantinableFrom = map[DestinationState]bool{
	StateReceiving:             true,
	StateVerifiedInStaging:     true,
	StateUploadingToRomm:       true,
	StatePublishedToFilesystem: true,
	StateAwaitingRommIngestion: true,
	StateRommMatched:           true,
	StateSourceActive:          true,
}

// Known reports whether s is a defined state.
func (s DestinationState) Known() bool {
	_, ok := transitions[s]
	return ok
}

// Terminal reports whether no further transition is possible.
func (s DestinationState) Terminal() bool {
	next, ok := transitions[s]
	return ok && len(next) == 0
}

// NeedsReview reports whether the state requires a human to look at it. These
// states must be surfaced in the interface and must never silently disappear.
func (s DestinationState) NeedsReview() bool {
	switch s {
	case StateVerificationConflict, StateRommUnmatched, StateIngestionTimeout, StateQuarantined:
		return true
	}
	return false
}

// IsSource reports whether the Bridge may advertise this item as a source.
//
// Phase 6 acceptance: "A file on disk is not a Swarm source until it reaches
// source_active."
func (s DestinationState) IsSource() bool {
	return s == StateSourceActive
}

// CountsTowardCoverage reports whether the item may contribute to content
// coverage or resilience metrics.
//
// Phase 6 acceptance: "Ingestion timeout or mismatch creates a visible review
// state and never increases coverage or resilience." Identical to IsSource
// today, kept separate because the two questions are asked by different
// subsystems and may legitimately diverge later (an item can be withdrawn from
// serving while still counting as preserved).
func (s DestinationState) CountsTowardCoverage() bool {
	return s == StateSourceActive
}

// CanTransition reports whether from -> to is legal.
func CanTransition(from, to DestinationState) bool {
	if to == StateQuarantined {
		return quarantinableFrom[from]
	}
	for _, allowed := range transitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// Transition validates a state change, returning an error describing the
// illegal move rather than silently allowing it.
func Transition(from, to DestinationState) error {
	if !from.Known() {
		return fmt.Errorf("unknown source state %q", from)
	}
	if !to.Known() {
		return fmt.Errorf("unknown target state %q", to)
	}
	if !CanTransition(from, to) {
		return fmt.Errorf("illegal destination transition %s -> %s", from, to)
	}
	return nil
}

// HandOffState returns the state a verified transfer moves into for the given
// destination mode.
func HandOffState(mode DestinationMode) (DestinationState, error) {
	switch mode {
	case ModeAPIOnly:
		return StateUploadingToRomm, nil
	case ModeFilesystemPublication:
		return StatePublishedToFilesystem, nil
	default:
		return "", fmt.Errorf("unknown destination mode %q", mode)
	}
}

// DefaultIngestionTimeout bounds how long a Bridge waits for RomM to ingest and
// match a handed-off payload before moving to StateIngestionTimeout.
//
// Chosen long rather than short: RomM's scan of a large library can be slow on
// the modest hardware these Bridges run on, and a premature timeout produces a
// review state for a transfer that was about to succeed. The failure mode of
// waiting too long is a stale row in an operator's queue; the failure mode of
// timing out too early is an operator learning to ignore review states.
const DefaultIngestionTimeout = 30 * time.Minute

// IngestionPollInterval is how often the Bridge asks RomM whether the expected
// item has appeared. Deliberately unhurried: Scope of Work §7 requires avoiding
// unnecessary load on participating RomM servers.
const IngestionPollInterval = 15 * time.Second
