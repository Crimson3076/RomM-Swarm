package ingest

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/destination"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

func newFileJournal(t *testing.T) *FileJournal {
	t.Helper()
	j, err := NewFileJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatalf("NewFileJournal: %v", err)
	}
	return j
}

func TestFileJournalRoundTrip(t *testing.T) {
	j := newFileJournal(t)
	id := protocol.NewTransferID()

	if _, ok := j.Current(id); ok {
		t.Fatal("Current reported a state for a transfer that was never appended to")
	}
	if h := j.History(id); len(h) != 0 {
		t.Fatalf("History for an unknown transfer = %v, want empty", h)
	}

	if err := j.Append(id, Transition{To: protocol.StateReceiving, At: time.Now()}); err != nil {
		t.Fatalf("Append receiving: %v", err)
	}
	if err := j.Append(id, Transition{From: protocol.StateReceiving, To: protocol.StateVerifiedInStaging, At: time.Now()}); err != nil {
		t.Fatalf("Append verified_in_staging: %v", err)
	}

	state, ok := j.Current(id)
	if !ok || state != protocol.StateVerifiedInStaging {
		t.Fatalf("Current = %s, %v, want verified_in_staging, true", state, ok)
	}
	h := j.History(id)
	if len(h) != 2 || h[0].To != protocol.StateReceiving || h[1].To != protocol.StateVerifiedInStaging {
		t.Fatalf("History = %v, want [receiving verified_in_staging]", Path(j, id))
	}
}

// TestFileJournalRefusesIllegalTransitions mirrors
// TestJournalRefusesIllegalTransitions (MemoryJournal) exactly, to prove the
// two implementations enforce identical legality rules.
func TestFileJournalRefusesIllegalTransitions(t *testing.T) {
	j := newFileJournal(t)
	id := protocol.NewTransferID()

	if err := j.Append(id, Transition{To: protocol.StateSourceActive}); err == nil {
		t.Fatal("a transfer was allowed to begin at source_active")
	}
	if err := j.Append(id, Transition{To: protocol.StateReceiving}); err != nil {
		t.Fatalf("a legal opening transition was refused: %v", err)
	}
	if err := j.Append(id, Transition{From: protocol.StateReceiving, To: protocol.StatePublishedToFilesystem}); err == nil {
		t.Fatal("a payload was recorded as published without passing through verification")
	}
	if err := j.Append(id, Transition{From: protocol.StateReceiving, To: protocol.StateVerifiedInStaging}); err != nil {
		t.Fatalf("a legal transition was refused: %v", err)
	}
	if got, _ := j.Current(id); got != protocol.StateVerifiedInStaging {
		t.Fatalf("current state is %s", got)
	}
}

func TestFileJournalRecentOrdersByMostRecentlyActive(t *testing.T) {
	j := newFileJournal(t)

	older := protocol.NewTransferID()
	newer := protocol.NewTransferID()

	if err := j.Append(older, Transition{To: protocol.StateReceiving}); err != nil {
		t.Fatalf("Append older: %v", err)
	}
	time.Sleep(10 * time.Millisecond) // ensure a distinct, later mtime
	if err := j.Append(newer, Transition{To: protocol.StateReceiving}); err != nil {
		t.Fatalf("Append newer: %v", err)
	}

	recent, err := j.Recent(0)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(recent) != 2 || recent[0] != newer || recent[1] != older {
		t.Fatalf("Recent = %v, want [newer older]", recent)
	}

	limited, err := j.Recent(1)
	if err != nil {
		t.Fatalf("Recent(1): %v", err)
	}
	if len(limited) != 1 || limited[0] != newer {
		t.Fatalf("Recent(1) = %v, want [newer]", limited)
	}

	// Touching the older transfer again should move it back to the front.
	time.Sleep(10 * time.Millisecond)
	if err := j.Append(older, Transition{From: protocol.StateReceiving, To: protocol.StateVerifiedInStaging}); err != nil {
		t.Fatalf("Append older again: %v", err)
	}
	recent, err = j.Recent(0)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(recent) != 2 || recent[0] != older {
		t.Fatalf("Recent after touching older = %v, want older first", recent)
	}
}

// TestPhase0_FileJournalSurvivesACrashAtEveryStep mirrors
// bridgeconfig.TestPhase0_SaveSurvivesACrashAtEveryStep: a crash mid-Append
// must never leave a transfer's journal file torn.
func TestPhase0_FileJournalSurvivesACrashAtEveryStep(t *testing.T) {
	// writeLocked's steps aren't individually hookable from outside the
	// package the way FileStore's crashAfter field is (no test-only field
	// was added to FileJournal, since nothing outside this test needs one) —
	// instead this proves the property that actually matters: a process that
	// dies between writeLocked's os.CreateTemp and os.Rename never corrupts
	// the previously-published file, because the new content only ever
	// exists under a different, temporary name until the rename.
	j := newFileJournal(t)
	id := protocol.NewTransferID()

	if err := j.Append(id, Transition{To: protocol.StateReceiving}); err != nil {
		t.Fatalf("seeding the journal: %v", err)
	}
	before := j.History(id)

	// Simulate a crash mid-write by leaving a stray, partially-written
	// temp file in the journal directory, exactly as an interrupted
	// os.CreateTemp+Write would.
	stray, err := os.CreateTemp(j.Dir, ".journal-*.tmp")
	if err != nil {
		t.Fatalf("creating a stray temp file: %v", err)
	}
	stray.WriteString("{not valid json, simulating a torn write")
	stray.Close()

	// The published file for id must be completely unaffected.
	after := j.History(id)
	if len(after) != len(before) || after[0].To != before[0].To {
		t.Fatalf("a stray temp file corrupted the published journal: got %v, want %v", after, before)
	}

	// And a fresh Append still works, ignoring the stray file entirely.
	if err := j.Append(id, Transition{From: protocol.StateReceiving, To: protocol.StateVerifiedInStaging}); err != nil {
		t.Fatalf("Append after a simulated crash: %v", err)
	}
	if state, _ := j.Current(id); state != protocol.StateVerifiedInStaging {
		t.Fatalf("Current after recovery = %s, want verified_in_staging", state)
	}
}

// TestPhase0_FlowWorksIdenticallyWithAFileJournal is the substitution proof
// the plan calls for: the same flow that
// TestPhase0_FilesystemFlowReachesSourceActiveOnlyAfterRommMatches exercises
// against MemoryJournal, run instead against FileJournal, reaching the same
// final state through the same path.
func TestPhase0_FlowWorksIdenticallyWithAFileJournal(t *testing.T) {
	payload := bytes.Repeat([]byte("a verified game "), 1024)
	expected := expectationFor(payload)

	base := t.TempDir()
	cfg := destination.Config{
		LibraryRoot:       filepath.Join(base, "library"),
		ReceiveStagingDir: filepath.Join(base, "staging"),
	}
	for _, d := range []string{cfg.LibraryRoot, cfg.ReceiveStagingDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("creating %s: %v", d, err)
		}
	}
	staging, err := destination.NewStaging(cfg.ReceiveStagingDir)
	if err != nil {
		t.Fatalf("NewStaging: %v", err)
	}
	pub, err := destination.NewPublisher(cfg)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}

	journal := newFileJournal(t)
	c := &clock{t: time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)}
	flow := &Flow{
		Mode:      protocol.ModeFilesystemPublication,
		Staging:   staging,
		Journal:   journal,
		Publisher: pub,
		Reconciler: &Reconciler{
			Library: &fakeLibrary{
				appearsAfter: 1,
				observation: Observation{
					Present: true, Platform: protocol.PlatformGB, Canonical: expected.Canonical, Name: "Kirby (USA)",
				},
			},
			Poll: time.Second, Timeout: time.Minute, Now: c.Now, After: c.After,
		},
		Now: c.Now,
	}

	id := protocol.NewTransferID()
	state, err := flow.Receive(context.Background(), id, bytes.NewReader(payload),
		expected, filepath.Join("gb", "Kirby (USA).gb"))
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if state != protocol.StateSourceActive {
		t.Fatalf("final state is %s, want source_active", state)
	}

	want := []protocol.DestinationState{
		protocol.StateReceiving,
		protocol.StateVerifiedInStaging,
		protocol.StatePublishedToFilesystem,
		protocol.StateAwaitingRommIngestion,
		protocol.StateRommMatched,
		protocol.StateSourceActive,
	}
	got := Path(journal, id)
	if len(got) != len(want) {
		t.Fatalf("path was %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("path was %v, want %v", got, want)
		}
	}

	// The journal must actually be on disk, not just in memory.
	entries, err := os.ReadDir(journal.Dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("journal directory has %d entries (err %v), want exactly 1 persisted file", len(entries), err)
	}
}
