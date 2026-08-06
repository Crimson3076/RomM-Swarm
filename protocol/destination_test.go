package protocol

import "testing"

// TestPhase0_RequiredDestinationPaths walks the two destination flows exactly as
// Scope of Work Phase 6 specifies them, and asserts the governing invariant at
// every step: the item is not a source until source_active.
func TestPhase0_RequiredDestinationPaths(t *testing.T) {
	paths := map[DestinationMode][]DestinationState{
		ModeAPIOnly: {
			StateReceiving,
			StateVerifiedInStaging,
			StateUploadingToRomm,
			StateAwaitingRommIngestion,
			StateRommMatched,
			StateSourceActive,
		},
		ModeFilesystemPublication: {
			StateReceiving,
			StateVerifiedInStaging,
			StatePublishedToFilesystem,
			StateAwaitingRommIngestion,
			StateRommMatched,
			StateSourceActive,
		},
	}

	for mode, path := range paths {
		t.Run(string(mode), func(t *testing.T) {
			// The hand-off branch must be chosen by the configured mode.
			handOff, err := HandOffState(mode)
			if err != nil {
				t.Fatalf("HandOffState(%s): %v", mode, err)
			}
			if handOff != path[2] {
				t.Fatalf("mode %s hands off to %s, want %s", mode, handOff, path[2])
			}

			for i := 0; i < len(path)-1; i++ {
				from, to := path[i], path[i+1]
				if err := Transition(from, to); err != nil {
					t.Fatalf("required transition %s -> %s rejected: %v", from, to, err)
				}
				if from.IsSource() {
					t.Fatalf("state %s reported itself as a source before source_active", from)
				}
				if from.CountsTowardCoverage() {
					t.Fatalf("state %s counted toward coverage before source_active", from)
				}
			}

			final := path[len(path)-1]
			if !final.IsSource() {
				t.Fatalf("terminal state %s is not a source", final)
			}
			if !final.CountsTowardCoverage() {
				t.Fatalf("terminal state %s does not count toward coverage", final)
			}
		})
	}
}

// TestPhase0_ReviewStatesNeverBecomeSources is Phase 6 acceptance: "Ingestion
// timeout or mismatch creates a visible review state and never increases
// coverage or resilience."
func TestPhase0_ReviewStatesNeverBecomeSources(t *testing.T) {
	review := []DestinationState{
		StateVerificationConflict,
		StateRommUnmatched,
		StateIngestionTimeout,
		StateQuarantined,
	}
	for _, s := range review {
		if !s.NeedsReview() {
			t.Errorf("%s does not report NeedsReview", s)
		}
		if s.IsSource() {
			t.Errorf("review state %s reported itself as a source", s)
		}
		if s.CountsTowardCoverage() {
			t.Errorf("review state %s counted toward coverage", s)
		}
		if !s.Terminal() {
			t.Errorf("review state %s is not terminal; it could transition onward", s)
		}
		// The path into source_active must not be reachable from a review state.
		if CanTransition(s, StateSourceActive) {
			t.Errorf("review state %s can transition directly to source_active", s)
		}
	}
}

func TestSourceActiveIsOnlyReachableFromRommMatched(t *testing.T) {
	for from := range transitions {
		if from == StateRommMatched {
			continue
		}
		if CanTransition(from, StateSourceActive) {
			t.Errorf("source_active is reachable from %s", from)
		}
	}
	if !CanTransition(StateRommMatched, StateSourceActive) {
		t.Error("source_active is not reachable from romm_matched")
	}
}

// Once bytes have been handed to RomM or renamed into the library, "cancelled"
// would be a false statement about the world. Such a transfer must resolve
// through ingestion or land in a review state where an operator can see it.
func TestCancellationIsImpossibleAfterHandOff(t *testing.T) {
	for _, from := range []DestinationState{StateUploadingToRomm, StatePublishedToFilesystem} {
		if CanTransition(from, StateCancelled) {
			t.Errorf("%s can transition to cancelled, which would misdescribe bytes already handed off", from)
		}
	}
	for _, from := range []DestinationState{StateReceiving, StateVerifiedInStaging} {
		if !CanTransition(from, StateCancelled) {
			t.Errorf("%s cannot be cancelled, but nothing has been handed off yet", from)
		}
	}
}

func TestQuarantineIsAvailableFromEveryLiveState(t *testing.T) {
	live := []DestinationState{
		StateReceiving, StateVerifiedInStaging, StateUploadingToRomm,
		StatePublishedToFilesystem, StateAwaitingRommIngestion,
		StateRommMatched, StateSourceActive,
	}
	for _, s := range live {
		if !CanTransition(s, StateQuarantined) {
			t.Errorf("a moderator cannot quarantine from %s", s)
		}
	}
	// Quarantine is an override, not a step on the happy path: it must not be
	// reachable from an already-terminal state.
	if CanTransition(StateCancelled, StateQuarantined) {
		t.Error("a cancelled transfer can be quarantined")
	}
}

func TestTransitionRejectsUnknownAndIllegalMoves(t *testing.T) {
	if err := Transition("made_up", StateReceiving); err == nil {
		t.Error("Transition accepted an unknown source state")
	}
	if err := Transition(StateReceiving, "made_up"); err == nil {
		t.Error("Transition accepted an unknown target state")
	}
	// Skipping verification is the transition that must never be possible.
	if err := Transition(StateReceiving, StateUploadingToRomm); err == nil {
		t.Error("a payload could be uploaded to RomM without passing through verified_in_staging")
	}
	if err := Transition(StateReceiving, StatePublishedToFilesystem); err == nil {
		t.Error("a payload could be published to the filesystem without passing through verified_in_staging")
	}
}

func TestHandOffStateRejectsUnknownMode(t *testing.T) {
	if _, err := HandOffState("both"); err == nil {
		t.Error("HandOffState accepted an unknown mode")
	}
	if !ModeAPIOnly.Valid() || !ModeFilesystemPublication.Valid() {
		t.Error("a supported destination mode reported itself invalid")
	}
	if DestinationMode("").Valid() {
		t.Error("the empty destination mode reported itself valid")
	}
}
