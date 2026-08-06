package protocol

import (
	"testing"
	"time"
)

// TestPhase0_EveryEventKindDeclaresRetention is the mechanism behind the
// privacy requirement: an event kind cannot exist without an answer to "how
// long may this be kept, and does it name a title?".
func TestPhase0_EveryEventKindDeclaresRetention(t *testing.T) {
	kinds := AllEventKinds()
	if len(kinds) == 0 {
		t.Fatal("the event vocabulary is empty")
	}
	valid := map[Retention]bool{
		RetentionOperational: true,
		RetentionSecurity:    true,
		RetentionAggregate:   true,
		RetentionAudit:       true,
	}
	for _, k := range kinds {
		r, err := k.Retention()
		if err != nil {
			t.Errorf("%s: %v", k, err)
			continue
		}
		if !valid[r] {
			t.Errorf("%s: retention %q is not one of the four defined classes", k, r)
		}
		if k.Summary() == "" {
			t.Errorf("%s: no summary; an undocumented event kind cannot be reviewed", k)
		}
	}
}

// TestPhase0_LongTermMetricsAreTitleFree is Scope of Work §7: "Long-term
// metrics are aggregate and title-free."
func TestPhase0_LongTermMetricsAreTitleFree(t *testing.T) {
	for _, k := range AllEventKinds() {
		r, err := k.Retention()
		if err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		if r == RetentionAggregate && k.NamesTitle() {
			t.Errorf("%s is kept long term as an aggregate but is marked as naming an exact title", k)
		}
		if r == RetentionAudit && k.NamesTitle() {
			t.Errorf("%s is immutable audit history but is marked as naming an exact title", k)
		}
	}
}

// Every event that can name a title must be short-retention, so the sweeper has
// something to sweep.
func TestTitleBearingEventsAreShortRetention(t *testing.T) {
	for _, k := range AllEventKinds() {
		if !k.NamesTitle() {
			continue
		}
		r, _ := k.Retention()
		if r != RetentionOperational && r != RetentionSecurity {
			t.Errorf("%s names a title but has retention %q, which is not a short window", k, r)
		}
	}
}

func TestUnknownEventKindsAreRejected(t *testing.T) {
	if EventKind("something.invented").Known() {
		t.Fatal("an invented event kind reported itself as known")
	}
	if _, err := EventKind("something.invented").Retention(); err == nil {
		t.Fatal("an invented event kind returned a retention class")
	}

	e := Event{Kind: "something.invented", At: time.Now()}
	if err := e.Validate(); err == nil {
		t.Fatal("Validate accepted an event with an unknown kind")
	}
}

func TestEventValidation(t *testing.T) {
	good := Event{
		Kind:        EventTransferCompleted,
		At:          time.Now(),
		SwarmID:     NewSwarmID(),
		ActorUser:   NewUserID(),
		ActorBridge: BridgeIDFromPublicKey([]byte("k")),
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("a well-formed event was rejected: %v", err)
	}

	noTime := good
	noTime.At = time.Time{}
	if err := noTime.Validate(); err == nil {
		t.Error("Validate accepted an event with no timestamp")
	}

	badActor := good
	badActor.ActorUser = "usr_not-a-real-id"
	if err := badActor.Validate(); err == nil {
		t.Error("Validate accepted an event with a malformed actor id")
	}

	// A Swarm id in the wrong field must be caught: this is the mistake that
	// would leak a Bridge's global identity into a member-facing record.
	swapped := good
	swapped.ActorBridge = BridgeID(NewSwarmID())
	if err := swapped.Validate(); err == nil {
		t.Error("Validate accepted a swarm id in the actor bridge field")
	}
}

func TestVocabularyCoversEveryDestinationTransition(t *testing.T) {
	// The destination state machine must be reportable, or Phase 6's durable
	// transitions cannot be audited.
	if !EventDestinationState.Known() {
		t.Fatal("there is no event kind for a destination state change")
	}
}
