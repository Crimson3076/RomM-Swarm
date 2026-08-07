package destination

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// setup builds a valid configuration: a library root, a receive staging
// directory outside it, and a publication staging directory beside it.
func setup(t *testing.T) (Config, string) {
	t.Helper()
	base := t.TempDir()

	lib := filepath.Join(base, "library")
	recv := filepath.Join(base, "staging")
	for _, d := range []string{lib, recv} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("creating %s: %v", d, err)
		}
	}
	return Config{LibraryRoot: lib, ReceiveStagingDir: recv}, base
}

// otherFilesystem returns a directory on a filesystem other than dir, or skips.
//
// Cross-filesystem behaviour is the one thing here that cannot be faked: the
// whole point is that a real device boundary changes what the code must do.
func otherFilesystem(t *testing.T, dir string) string {
	t.Helper()

	here, err := deviceOf(dir)
	if err != nil {
		t.Skipf("cannot determine the filesystem of %s: %v", dir, err)
	}
	for _, candidate := range []string{"/dev/shm", os.TempDir(), "/var/tmp"} {
		info, err := os.Stat(candidate)
		if err != nil || !info.IsDir() {
			continue
		}
		dev, err := deviceOf(candidate)
		if err != nil || dev == here {
			continue
		}
		scratch, err := os.MkdirTemp(candidate, "romm-swarm-xfs-*")
		if err != nil {
			continue
		}
		t.Cleanup(func() { os.RemoveAll(scratch) })
		return scratch
	}
	t.Skip("no second filesystem is available on this machine, so the cross-filesystem path cannot be exercised")
	return ""
}

func stage(t *testing.T, s *Staging, id protocol.TransferID, payload []byte) protocol.Digest {
	t.Helper()
	if _, err := s.Receive(id, bytes.NewReader(payload)); err != nil {
		t.Fatalf("Receive: %v", err)
	}
	return protocol.DigestBytes(payload)
}

// TestPhase0_FilesystemPublicationIsAtomicAndNeverOverwrites covers three Phase 6
// acceptance criteria at once: a final filename never appears before the payload
// is complete and verified, an existing library file is never silently
// overwritten, and a crash cannot leave a partial file under the final name.
func TestPhase0_FilesystemPublicationIsAtomicAndNeverOverwrites(t *testing.T) {
	cfg, _ := setup(t)
	pub, err := NewPublisher(cfg)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	staging, err := NewStaging(cfg.ReceiveStagingDir)
	if err != nil {
		t.Fatalf("NewStaging: %v", err)
	}

	payload := bytes.Repeat([]byte("verified payload "), 4096)
	id := protocol.NewTransferID()
	expected := stage(t, staging, id, payload)

	finalRel := filepath.Join("gb", "Kirby (USA).gb")
	finalAbs := filepath.Join(cfg.LibraryRoot, finalRel)

	// Before publication the final name must not exist, even though the complete
	// payload is already staged.
	if _, err := os.Lstat(finalAbs); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the final name existed before publication")
	}

	if _, err := staging.Verify(id, expected); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	published, err := pub.Publish(staging.PathFor(id), finalRel, expected)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if published != finalAbs {
		t.Fatalf("published to %s, want %s", published, finalAbs)
	}

	got, err := os.ReadFile(finalAbs)
	if err != nil {
		t.Fatalf("reading the published file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("the published file does not match the payload")
	}

	// The staging file is gone, so one name points at the data.
	if _, err := os.Lstat(staging.PathFor(id)); !errors.Is(err, os.ErrNotExist) {
		t.Error("the staging file survived publication")
	}

	// A second publication to the same name must be refused, not silently win.
	second := protocol.NewTransferID()
	otherPayload := bytes.Repeat([]byte("different content "), 100)
	otherExpected := stage(t, staging, second, otherPayload)

	_, err = pub.Publish(staging.PathFor(second), finalRel, otherExpected)
	if !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("publishing over an existing file returned %v, want a refusal", err)
	}

	// And the original must be untouched.
	got, err = os.ReadFile(finalAbs)
	if err != nil {
		t.Fatalf("re-reading the published file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("the existing library file was modified by the refused publication")
	}
}

// TestPhase0_CrashLeavesNoPartialFileUnderTheFinalName is Phase 6 acceptance
// stated directly. The staging file is abandoned mid-transfer, which is what a
// crash leaves behind, and the library must be untouched.
func TestPhase0_CrashLeavesNoPartialFileUnderTheFinalName(t *testing.T) {
	cfg, _ := setup(t)
	staging, err := NewStaging(cfg.ReceiveStagingDir)
	if err != nil {
		t.Fatalf("NewStaging: %v", err)
	}
	if _, err := NewPublisher(cfg); err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}

	// A transfer that got half way and then died.
	id := protocol.NewTransferID()
	partial := bytes.Repeat([]byte("half a payload "), 100)
	if _, err := staging.Receive(id, bytes.NewReader(partial)); err != nil {
		t.Fatalf("Receive: %v", err)
	}

	// The library must contain nothing at all.
	entries, err := os.ReadDir(cfg.LibraryRoot)
	if err != nil {
		t.Fatalf("reading the library: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("the library contains %d entries after an interrupted transfer", len(entries))
	}

	// The partial file lives under a transfer-keyed .part name, which carries no
	// hint of what it was going to be.
	staged := staging.PathFor(id)
	if !strings.HasSuffix(staged, PartSuffix) {
		t.Errorf("the staging file %s does not carry the .part suffix", staged)
	}
	if _, err := os.Lstat(staged); err != nil {
		t.Fatalf("the staging file is missing: %v", err)
	}

	// Verification against the intended payload must fail, so the partial file
	// can never be published.
	full := bytes.Repeat([]byte("half a payload "), 200)
	if _, err := staging.Verify(id, protocol.DigestBytes(full)); err == nil {
		t.Fatal("a partial payload passed verification")
	}

	// Abandoned-staging cleanup collects it once it has aged out.
	future := time.Now().Add(DefaultAbandonedAfter + time.Hour)
	removed, err := staging.CleanupAbandoned(DefaultAbandonedAfter, future)
	if err != nil {
		t.Fatalf("CleanupAbandoned: %v", err)
	}
	if removed != 1 {
		t.Fatalf("cleanup removed %d files, want 1", removed)
	}
	if _, err := os.Lstat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Error("the abandoned staging file survived cleanup")
	}
}

// TestPhase0_CrossFilesystemStagingIsDetectedAndHandled is Phase 6 acceptance:
// "Cross-filesystem staging is detected before a transfer starts and cannot be
// mistaken for an atomic move."
func TestPhase0_CrossFilesystemStagingIsDetectedAndHandled(t *testing.T) {
	cfg, _ := setup(t)
	cfg.ReceiveStagingDir = otherFilesystem(t, cfg.LibraryRoot)

	pub, err := NewPublisher(cfg)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}

	// The boundary is detected, and reported to the operator rather than hidden.
	if pub.Report().SameFilesystem {
		t.Fatal("a cross-filesystem configuration was reported as same-filesystem")
	}
	var explained bool
	for _, c := range pub.Report().Checks {
		if strings.Contains(c.Detail, "never treated as atomic") {
			explained = true
		}
	}
	if !explained {
		t.Errorf("the cross-filesystem path was not explained to the operator: %+v", pub.Report().Checks)
	}

	// And publication still works, through the destination-filesystem copy.
	staging, err := NewStaging(cfg.ReceiveStagingDir)
	if err != nil {
		t.Fatalf("NewStaging: %v", err)
	}
	payload := bytes.Repeat([]byte("across a boundary "), 2048)
	id := protocol.NewTransferID()
	expected := stage(t, staging, id, payload)

	finalRel := filepath.Join("gba", "Metroid (USA).gba")
	published, err := pub.Publish(staging.PathFor(id), finalRel, expected)
	if err != nil {
		t.Fatalf("Publish across filesystems: %v", err)
	}

	got, err := os.ReadFile(published)
	if err != nil {
		t.Fatalf("reading the published file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("the cross-filesystem publication produced different bytes")
	}

	// No temporary file may be left on the library's filesystem.
	entries, err := os.ReadDir(pub.Report().PublishStagingDir)
	if err != nil {
		t.Fatalf("reading the publication staging directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("publication left %d temporary files behind", len(entries))
	}
}

// A publication staging directory on the wrong filesystem must be refused, not
// worked around: it is the configuration that would make publication non-atomic.
func TestPreflightRefusesAPublishStagingDirectoryOnAnotherFilesystem(t *testing.T) {
	cfg, _ := setup(t)
	cfg.PublishStagingDir = otherFilesystem(t, cfg.LibraryRoot)

	rep := Preflight(cfg)
	if rep.OK() {
		t.Fatal("a publication staging directory on another filesystem was accepted")
	}
	err := rep.Err()
	if !errors.Is(err, ErrUnsafeConfiguration) {
		t.Fatalf("error is %v, want an unsafe-configuration refusal", err)
	}
	if !strings.Contains(err.Error(), "different filesystem") {
		t.Errorf("the refusal does not explain the problem: %v", err)
	}

	if _, err := NewPublisher(cfg); err == nil {
		t.Fatal("NewPublisher accepted an unsafe configuration")
	}
}

// TestPhase0_PreflightRefusesStagingInsideTheWatchedTree is Phase 1 acceptance:
// filesystem publication mode refuses to start with an unsafe configuration.
func TestPhase0_PreflightRefusesStagingInsideTheWatchedTree(t *testing.T) {
	t.Run("receive staging inside the library", func(t *testing.T) {
		cfg, _ := setup(t)
		cfg.ReceiveStagingDir = filepath.Join(cfg.LibraryRoot, "incoming")
		if err := os.MkdirAll(cfg.ReceiveStagingDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		rep := Preflight(cfg)
		if rep.OK() {
			t.Fatal("staging inside the watched library tree was accepted; RomM would index partial downloads")
		}
		if !strings.Contains(rep.Err().Error(), "inside the watched library tree") {
			t.Errorf("the refusal does not explain the problem: %v", rep.Err())
		}
	})

	t.Run("publish staging inside the library", func(t *testing.T) {
		cfg, _ := setup(t)
		cfg.PublishStagingDir = filepath.Join(cfg.LibraryRoot, ".publish")

		rep := Preflight(cfg)
		if rep.OK() {
			t.Fatal("publication staging inside the watched library tree was accepted")
		}
	})

	t.Run("a library root that does not exist", func(t *testing.T) {
		cfg, base := setup(t)
		cfg.LibraryRoot = filepath.Join(base, "nowhere")
		if Preflight(cfg).OK() {
			t.Fatal("a missing library root was accepted")
		}
	})

	t.Run("no library root at all", func(t *testing.T) {
		if Preflight(Config{ReceiveStagingDir: t.TempDir()}).OK() {
			t.Fatal("an empty configuration was accepted")
		}
	})
}

// A scoped writable mount means publication must stay inside it.
func TestPublishRefusesDestinationsThatEscapeTheLibrary(t *testing.T) {
	cfg, _ := setup(t)
	pub, err := NewPublisher(cfg)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	staging, err := NewStaging(cfg.ReceiveStagingDir)
	if err != nil {
		t.Fatalf("NewStaging: %v", err)
	}

	id := protocol.NewTransferID()
	payload := []byte("payload")
	expected := stage(t, staging, id, payload)

	for _, rel := range []string{
		"../escaped.gb",
		"gb/../../escaped.gb",
		"/etc/cron.d/escaped",
		"",
		"gb/\x00.gb",
	} {
		if _, err := pub.Publish(staging.PathFor(id), rel, expected); err == nil {
			t.Errorf("publishing to %q was allowed", rel)
		}
	}

	// Nothing may have been created outside the library.
	if _, err := os.Lstat(filepath.Join(filepath.Dir(cfg.LibraryRoot), "escaped.gb")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a publication escaped the library root")
	}
}

// TestPhase0_VerificationHappensWhileStaged is Phase 6's required common
// receiving flow: verification precedes any hand-off, and rests on SHA-256.
func TestPhase0_VerificationHappensWhileStaged(t *testing.T) {
	cfg, _ := setup(t)
	staging, err := NewStaging(cfg.ReceiveStagingDir)
	if err != nil {
		t.Fatalf("NewStaging: %v", err)
	}

	payload := bytes.Repeat([]byte("genuine "), 1000)
	id := protocol.NewTransferID()
	expected := stage(t, staging, id, payload)

	if _, err := staging.Verify(id, expected); err != nil {
		t.Fatalf("a genuine payload failed verification: %v", err)
	}

	// A payload that is not what was asked for.
	wrong := protocol.NewTransferID()
	stage(t, staging, wrong, bytes.Repeat([]byte("substitute "), 1000))
	if _, err := staging.Verify(wrong, expected); err == nil {
		t.Fatal("a substituted payload passed verification")
	}

	// A record carrying only catalogue-strength hashes is not enough to accept
	// bytes from a peer, however well it matches.
	catalogueOnly := protocol.Digest{
		Size: expected.Size, CRC32: expected.CRC32, MD5: expected.MD5, SHA1: expected.SHA1,
	}
	_, err = staging.Verify(id, catalogueOnly)
	if err == nil {
		t.Fatal("a payload was accepted on catalogue-strength agreement alone")
	}
	if !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("the refusal does not explain that SHA-256 is required: %v", err)
	}
}

func TestStagingPathsAreKeyedByTransferNotByTitle(t *testing.T) {
	cfg, _ := setup(t)
	staging, err := NewStaging(cfg.ReceiveStagingDir)
	if err != nil {
		t.Fatalf("NewStaging: %v", err)
	}

	a, b := protocol.NewTransferID(), protocol.NewTransferID()
	if staging.PathFor(a) == staging.PathFor(b) {
		t.Fatal("two transfers share a staging path")
	}
	// A staged file must not name what it is going to be: the staging directory
	// is on disk for hours and should not be a readable list of pending titles.
	if strings.Contains(filepath.Base(staging.PathFor(a)), ".gb") {
		t.Error("the staging filename leaks the payload's identity")
	}
}

func TestCleanupLeavesLiveTransfersAndForeignFilesAlone(t *testing.T) {
	cfg, _ := setup(t)
	staging, err := NewStaging(cfg.ReceiveStagingDir)
	if err != nil {
		t.Fatalf("NewStaging: %v", err)
	}

	live := protocol.NewTransferID()
	stage(t, staging, live, []byte("in progress"))

	// Something the Bridge did not create. A cleanup routine that deletes
	// unrecognised files will one day delete something that mattered.
	foreign := filepath.Join(cfg.ReceiveStagingDir, "operator-notes.txt")
	if err := os.WriteFile(foreign, []byte("do not delete"), 0o644); err != nil {
		t.Fatalf("writing the foreign file: %v", err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(foreign, old, old); err != nil {
		t.Fatalf("ageing the foreign file: %v", err)
	}

	removed, err := staging.CleanupAbandoned(time.Hour, time.Now())
	if err != nil {
		t.Fatalf("CleanupAbandoned: %v", err)
	}
	if removed != 0 {
		t.Errorf("cleanup removed %d files, want 0", removed)
	}
	if _, err := os.Lstat(foreign); err != nil {
		t.Error("cleanup removed a file it did not create")
	}
	if _, err := os.Lstat(staging.PathFor(live)); err != nil {
		t.Error("cleanup removed a live transfer's staging file")
	}
}

func TestCheckSpaceRefusesAnImpossibleTransfer(t *testing.T) {
	cfg, _ := setup(t)
	staging, err := NewStaging(cfg.ReceiveStagingDir)
	if err != nil {
		t.Fatalf("NewStaging: %v", err)
	}

	if err := staging.CheckSpace(1024); err != nil {
		t.Fatalf("a 1 KiB transfer was refused: %v", err)
	}

	// An exabyte will not fit on any machine this runs on.
	err = staging.CheckSpace(1 << 60)
	if !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("an impossible transfer returned %v, want an insufficient-space refusal", err)
	}
}

func TestDefaultPublishStagingDirSitsBesideTheLibrary(t *testing.T) {
	got := DefaultPublishStagingDir("/mnt/roms")
	want := filepath.Join("/mnt", ".roms-romm-swarm-publish")
	if got != want {
		t.Fatalf("default publication staging is %s, want %s", got, want)
	}
	if within(got, "/mnt/roms") {
		t.Fatal("the default publication staging directory is inside the watched tree")
	}
}

func TestWithinContainment(t *testing.T) {
	cases := []struct {
		path, root string
		want       bool
	}{
		{"/a/b/c", "/a/b", true},
		{"/a/b", "/a/b", true},
		{"/a/bc", "/a/b", false},
		{"/a", "/a/b", false},
		{"/a/b/../c", "/a/b", false},
	}
	for _, tc := range cases {
		if got := within(tc.path, tc.root); got != tc.want {
			t.Errorf("within(%q, %q) = %v, want %v", tc.path, tc.root, got, tc.want)
		}
	}
}

// A symlink must not be usable to place staging inside the watched tree while
// appearing to be outside it.
func TestContainmentChecksFollowSymlinks(t *testing.T) {
	cfg, base := setup(t)

	inside := filepath.Join(cfg.LibraryRoot, "incoming")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(base, "looks-outside")
	if err := os.Symlink(inside, link); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	cfg.ReceiveStagingDir = link
	if Preflight(cfg).OK() {
		t.Fatal("a symlink pointing into the watched tree was accepted as outside it")
	}
}
