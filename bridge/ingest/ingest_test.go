package ingest

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/destination"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// fakeLibrary is a stand-in RomM whose indexing behaviour a test can script.
type fakeLibrary struct {
	// appearsAfter is how many observations pass before the item shows up.
	appearsAfter int
	// observation is what is reported once it does.
	observation Observation
	// err is returned from every call, to simulate an unreachable RomM.
	err error

	calls int
}

func (f *fakeLibrary) Observe(context.Context, Expected) (Observation, error) {
	f.calls++
	if f.err != nil {
		return Observation{}, f.err
	}
	if f.calls <= f.appearsAfter {
		return Observation{Present: false}, nil
	}
	return f.observation, nil
}

// clock drives both the reconciler's deadline and its polling without real time
// passing, so the timeout paths are testable in microseconds.
type clock struct{ t time.Time }

func (c *clock) Now() time.Time { return c.t }

func (c *clock) After(d time.Duration) <-chan time.Time {
	c.t = c.t.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.t
	return ch
}

func expectationFor(payload []byte) Expected {
	digest := protocol.DigestBytes(payload)
	return Expected{
		Platform:  protocol.PlatformGB,
		FileID:    protocol.FileIDFromCanonicalDigest(digest.SHA256),
		Canonical: digest,
	}
}

// setupFlow builds a filesystem-publication flow against temporary directories.
func setupFlow(t *testing.T, lib Library) (*Flow, destination.Config) {
	t.Helper()
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

	c := &clock{t: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)}
	return &Flow{
		Mode:      protocol.ModeFilesystemPublication,
		Staging:   staging,
		Journal:   NewMemoryJournal(),
		Publisher: pub,
		Reconciler: &Reconciler{
			Library: lib,
			Poll:    time.Second,
			Timeout: time.Minute,
			Now:     c.Now,
			After:   c.After,
		},
		Now: c.Now,
	}, cfg
}

// TestPhase0_FilesystemFlowReachesSourceActiveOnlyAfterRommMatches walks the
// whole Phase 6 filesystem publication flow and asserts it took exactly the
// specified path.
func TestPhase0_FilesystemFlowReachesSourceActiveOnlyAfterRommMatches(t *testing.T) {
	payload := bytes.Repeat([]byte("a verified game "), 1024)
	expected := expectationFor(payload)

	lib := &fakeLibrary{
		appearsAfter: 2, // RomM takes a couple of polls to index it
		observation: Observation{
			Present:   true,
			Platform:  protocol.PlatformGB,
			Canonical: expected.Canonical,
			Name:      "Kirby (USA)",
		},
	}
	flow, cfg := setupFlow(t, lib)

	id := protocol.NewTransferID()
	state, err := flow.Receive(context.Background(), id, bytes.NewReader(payload),
		expected, filepath.Join("gb", "Kirby (USA).gb"))
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if state != protocol.StateSourceActive {
		t.Fatalf("final state is %s, want source_active", state)
	}

	// The exact path Scope of Work Phase 6 requires.
	want := []protocol.DestinationState{
		protocol.StateReceiving,
		protocol.StateVerifiedInStaging,
		protocol.StatePublishedToFilesystem,
		protocol.StateAwaitingRommIngestion,
		protocol.StateRommMatched,
		protocol.StateSourceActive,
	}
	got := Path(flow.Journal, id)
	if len(got) != len(want) {
		t.Fatalf("path was %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("path was %v, want %v", got, want)
		}
	}

	// The file is in the library with its verified contents.
	onDisk, err := os.ReadFile(filepath.Join(cfg.LibraryRoot, "gb", "Kirby (USA).gb"))
	if err != nil {
		t.Fatalf("reading the published file: %v", err)
	}
	if !bytes.Equal(onDisk, payload) {
		t.Fatal("the published file does not match the payload")
	}

	// Nothing before the last state counted as a source.
	for _, s := range got[:len(got)-1] {
		if s.IsSource() || s.CountsTowardCoverage() {
			t.Errorf("state %s counted as a source before the flow completed", s)
		}
	}
}

// TestPhase0_IngestionTimeoutIsAVisibleReviewStateNotASource is Phase 6
// acceptance: "Ingestion timeout or mismatch creates a visible review state and
// never increases coverage or resilience."
func TestPhase0_IngestionTimeoutIsAVisibleReviewStateNotASource(t *testing.T) {
	payload := []byte("a game RomM never indexes")
	expected := expectationFor(payload)

	// RomM never reports the item.
	flow, cfg := setupFlow(t, &fakeLibrary{appearsAfter: 1 << 30})

	id := protocol.NewTransferID()
	state, err := flow.Receive(context.Background(), id, bytes.NewReader(payload),
		expected, filepath.Join("gb", "Ghost (USA).gb"))
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}

	if state != protocol.StateIngestionTimeout {
		t.Fatalf("final state is %s, want ingestion_timeout", state)
	}
	if state.IsSource() || state.CountsTowardCoverage() {
		t.Fatal("a timed-out transfer became a source")
	}
	if !state.NeedsReview() {
		t.Fatal("a timed-out transfer is not marked for review")
	}

	// The file is still in the library — it was published successfully, RomM
	// simply has not indexed it. The operator needs to be told that, not told
	// the transfer failed.
	if _, err := os.Stat(filepath.Join(cfg.LibraryRoot, "gb", "Ghost (USA).gb")); err != nil {
		t.Errorf("the published file is missing after an ingestion timeout: %v", err)
	}

	history := flow.Journal.History(id)
	last := history[len(history)-1]
	if last.Detail == "" {
		t.Error("the review state carries no explanation for the operator")
	}
}

// A payload that RomM matches to something else must not activate.
func TestPhase0_MismatchedIngestionIsAReviewState(t *testing.T) {
	payload := []byte("the payload that was sent")
	expected := expectationFor(payload)

	lib := &fakeLibrary{
		observation: Observation{
			Present:   true,
			Platform:  protocol.PlatformGB,
			Canonical: protocol.DigestBytes([]byte("something else entirely")),
			Name:      "Not What Was Sent",
		},
	}
	flow, _ := setupFlow(t, lib)

	id := protocol.NewTransferID()
	state, err := flow.Receive(context.Background(), id, bytes.NewReader(payload),
		expected, filepath.Join("gb", "Mismatch (USA).gb"))
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if state != protocol.StateRommUnmatched {
		t.Fatalf("final state is %s, want romm_unmatched", state)
	}
	if state.CountsTowardCoverage() {
		t.Fatal("a mismatched import counted toward coverage")
	}
}

// The bytes are right but RomM filed them under the wrong platform: a source
// nobody would find. Not corruption, but not something to activate silently.
func TestIngestionUnderTheWrongPlatformIsAReviewState(t *testing.T) {
	payload := []byte("right bytes, wrong shelf")
	expected := expectationFor(payload)

	lib := &fakeLibrary{
		observation: Observation{
			Present:   true,
			Platform:  protocol.PlatformGBA,
			Canonical: expected.Canonical,
			Name:      "Misfiled",
		},
	}
	flow, _ := setupFlow(t, lib)

	state, err := flow.Receive(context.Background(), protocol.NewTransferID(),
		bytes.NewReader(payload), expected, filepath.Join("gb", "Misfiled (USA).gb"))
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if state != protocol.StateRommUnmatched {
		t.Fatalf("final state is %s, want romm_unmatched", state)
	}
}

// TestPhase0_CorruptedPayloadIsRejectedBeforeItReachesTheLibrary is Phase 6
// acceptance: "A wrong or corrupted payload is rejected before import."
func TestPhase0_CorruptedPayloadIsRejectedBeforeItReachesTheLibrary(t *testing.T) {
	intended := bytes.Repeat([]byte("what was requested "), 512)
	expected := expectationFor(intended)

	// The peer sends something else.
	substituted := bytes.Repeat([]byte("what actually arrived "), 512)

	flow, cfg := setupFlow(t, &fakeLibrary{observation: Observation{Present: true, Canonical: expected.Canonical}})

	id := protocol.NewTransferID()
	state, err := flow.Receive(context.Background(), id, bytes.NewReader(substituted),
		expected, filepath.Join("gb", "Requested (USA).gb"))
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if state != protocol.StateVerificationConflict {
		t.Fatalf("final state is %s, want verification_conflict", state)
	}

	// Nothing reached the library, and RomM was never contacted.
	entries, err := os.ReadDir(cfg.LibraryRoot)
	if err != nil {
		t.Fatalf("reading the library: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a rejected payload created %d entries in the library", len(entries))
	}

	// The staging file is cleaned up rather than left to be retried.
	if _, err := os.Stat(flow.Staging.PathFor(id)); !errors.Is(err, os.ErrNotExist) {
		t.Error("the rejected payload was left in staging")
	}

	// The flow stopped at verification: it never reached a hand-off state.
	for _, s := range Path(flow.Journal, id) {
		if s == protocol.StatePublishedToFilesystem || s == protocol.StateUploadingToRomm {
			t.Fatalf("a rejected payload reached the hand-off state %s", s)
		}
	}
}

// TestPhase0_APIOnlyModeIsBlockedOnAnUnprobedUploadAPI records the outstanding
// blocker honestly, at the point it bites.
func TestPhase0_APIOnlyModeIsBlockedOnAnUnprobedUploadAPI(t *testing.T) {
	payload := []byte("a game to upload")
	expected := expectationFor(payload)

	flow, cfg := setupFlow(t, &fakeLibrary{observation: Observation{Present: true, Canonical: expected.Canonical}})
	flow.Mode = protocol.ModeAPIOnly
	flow.Uploader = UnprobedUploader{}
	flow.Publisher = nil

	id := protocol.NewTransferID()
	state, err := flow.Receive(context.Background(), id, bytes.NewReader(payload), expected, "")

	if !errors.Is(err, ErrUploadUnprobed) {
		t.Fatalf("got %v, want the unprobed-upload blocker", err)
	}
	if state != protocol.StateVerificationConflict {
		t.Fatalf("final state is %s, want a review state", state)
	}

	// The payload was verified before the upload was attempted, so the blocker
	// is genuinely the only thing missing on this path.
	path := Path(flow.Journal, id)
	var verified, attempted bool
	for _, s := range path {
		if s == protocol.StateVerifiedInStaging {
			verified = true
		}
		if s == protocol.StateUploadingToRomm {
			attempted = true
		}
	}
	if !verified {
		t.Error("the payload was not verified before the upload was attempted")
	}
	if !attempted {
		t.Error("the upload was never attempted")
	}

	// API-only mode must not write into the library. That is its whole point.
	entries, err := os.ReadDir(cfg.LibraryRoot)
	if err != nil {
		t.Fatalf("reading the library: %v", err)
	}
	if len(entries) != 0 {
		t.Fatal("API-only mode wrote into the RomM library")
	}
}

// A transient RomM failure is not a verdict. Phase 7 requires backoff during
// RomM failures rather than treating them as mismatches.
func TestUnreachableRommTimesOutRatherThanReportingAMismatch(t *testing.T) {
	c := &clock{t: time.Now()}
	r := &Reconciler{
		Library: &fakeLibrary{err: errors.New("connection refused")},
		Poll:    time.Second,
		Timeout: 5 * time.Second,
		Now:     c.Now,
		After:   c.After,
	}

	result, err := r.Await(context.Background(), expectationFor([]byte("x")))
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if result.State != protocol.StateIngestionTimeout {
		t.Fatalf("state is %s, want ingestion_timeout rather than a mismatch verdict", result.State)
	}
	if result.Attempts < 2 {
		t.Errorf("gave up after %d attempts; it should have retried until the deadline", result.Attempts)
	}
}

// An item RomM has indexed but not yet hashed must not be judged early.
func TestPartialObservationsDoNotDecideTheOutcome(t *testing.T) {
	payload := []byte("indexed but not yet hashed")
	expected := expectationFor(payload)

	// Present, but with no identity yet — an in-progress scan looks like this.
	state, detail, decided := classify(expected, Observation{Present: true, Name: "Pending"})
	if decided {
		t.Fatalf("an incomplete observation decided the outcome as %s (%s)", state, detail)
	}

	// Once the identity arrives, it decides.
	_, _, decided = classify(expected, Observation{
		Present: true, Canonical: expected.Canonical, Platform: expected.Platform,
	})
	if !decided {
		t.Fatal("a complete matching observation did not decide the outcome")
	}
}

func TestJournalRefusesIllegalTransitions(t *testing.T) {
	j := NewMemoryJournal()
	id := protocol.NewTransferID()

	// A transfer must begin at receiving.
	if err := j.Append(id, Transition{To: protocol.StateSourceActive}); err == nil {
		t.Fatal("a transfer was allowed to begin at source_active")
	}
	if err := j.Append(id, Transition{To: protocol.StateReceiving}); err != nil {
		t.Fatalf("a legal opening transition was refused: %v", err)
	}

	// Skipping verification is the transition that must never be recordable.
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

func TestReconcilerRequiresACompleteExpectation(t *testing.T) {
	r := &Reconciler{Library: &fakeLibrary{}}
	if _, err := r.Await(context.Background(), Expected{}); err == nil {
		t.Fatal("Await accepted an empty expectation")
	}

	incomplete := expectationFor([]byte("x"))
	incomplete.Platform = ""
	if _, err := r.Await(context.Background(), incomplete); err == nil {
		t.Fatal("Await accepted an expectation with no platform")
	}
}

func TestCancellationStopsTheWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := &Reconciler{
		Library: &fakeLibrary{appearsAfter: 1 << 30},
		Poll:    time.Millisecond,
		Timeout: time.Hour,
	}
	result, err := r.Await(ctx, expectationFor([]byte("x")))
	if err == nil {
		t.Fatal("a cancelled wait returned no error")
	}
	if result.State != protocol.StateIngestionTimeout {
		t.Errorf("a cancelled wait produced state %s", result.State)
	}
}
