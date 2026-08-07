package destination

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// Filesystem publication mode.
//
// Scope of Work §3 makes this a separate, explicit operator mode requiring a
// scoped writable mount, and Phase 6 spells out the flow. Two of its rules are
// the ones that make it safe, and both are easy to get subtly wrong:
//
//   - "Filesystem-mode publication uses an atomic rename when staging and
//     destination share a filesystem."
//   - "A normal move across filesystems is a copy followed by deletion and is
//     not atomic. The Bridge must never describe it as atomic. It must use the
//     destination-filesystem temporary copy and final rename above, or refuse the
//     transfer until a safe path is configured."
//
// So the code never moves a file across a filesystem boundary and calls it
// publication. When the receiving staging area is on a different filesystem from
// the library, the verified payload is copied to a temporary file *on the
// destination filesystem*, re-verified there, flushed, and only then linked into
// place.
//
// Publication itself uses a hard link rather than a rename. os.Rename overwrites
// an existing destination silently; os.Link fails with EEXIST. Phase 6 requires
// that "Existing library files are never silently overwritten", and a link
// followed by unlinking the temporary file gives both atomicity and
// no-overwrite in one operation, with no check-then-act race in between.

// ErrDestinationExists means the final path is already occupied.
var ErrDestinationExists = errors.New("destination: a file already exists at the destination and was not overwritten")

// ErrUnsafeConfiguration means the preflight refused the configuration.
var ErrUnsafeConfiguration = errors.New("destination: the filesystem publication configuration is unsafe")

// Config describes a Bridge's filesystem publication setup.
type Config struct {
	// LibraryRoot is the root of the tree RomM watches. Publication targets live
	// beneath it; nothing the Bridge stages may.
	LibraryRoot string

	// ReceiveStagingDir is where downloads land. It must be outside LibraryRoot.
	// It may be on a different filesystem, which is handled rather than
	// forbidden.
	ReceiveStagingDir string

	// PublishStagingDir is where the destination-filesystem temporary copy is
	// made. It must be on the same filesystem as LibraryRoot and outside it.
	//
	// Empty means a sibling of LibraryRoot. Scope of Work Phase 6 prefers "a
	// sibling directory outside the watched tree" over the operating system's
	// general temporary directory, because the general temporary directory is
	// very often a different filesystem — or a tmpfs, where a large publication
	// would land in RAM.
	PublishStagingDir string
}

// DefaultPublishStagingDir returns the sibling directory used when none is
// configured.
func DefaultPublishStagingDir(libraryRoot string) string {
	clean := filepath.Clean(libraryRoot)
	return filepath.Join(filepath.Dir(clean), "."+filepath.Base(clean)+"-romm-swarm-publish")
}

// Check is one preflight result.
type Check struct {
	Name string
	OK   bool
	// Detail explains the outcome in terms an operator can act on.
	Detail string
	// Fatal marks a failure that prevents filesystem publication entirely.
	Fatal bool
}

// PreflightReport is the outcome of validating a configuration.
type PreflightReport struct {
	Checks []Check

	// SameFilesystem records whether the receiving staging area shares a
	// filesystem with the library. When false, publication takes the
	// destination-filesystem copy path. This is reported rather than hidden,
	// because it changes how much disk a transfer needs.
	SameFilesystem bool

	// PublishStagingDir is the resolved publication staging directory.
	PublishStagingDir string
}

// OK reports whether the configuration may be used.
func (r PreflightReport) OK() bool {
	for _, c := range r.Checks {
		if !c.OK && c.Fatal {
			return false
		}
	}
	return true
}

// Err returns an error describing every fatal failure, or nil.
func (r PreflightReport) Err() error {
	var reasons []string
	for _, c := range r.Checks {
		if !c.OK && c.Fatal {
			reasons = append(reasons, c.Name+": "+c.Detail)
		}
	}
	if len(reasons) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrUnsafeConfiguration, strings.Join(reasons, "; "))
}

// Preflight validates a filesystem publication configuration.
//
// Phase 1 acceptance: "Filesystem publication mode refuses to start with an
// unsafe, overly broad, or incompatible staging configuration." Every check
// below is one way that has gone wrong in practice, and each reports what to
// change rather than only that something is wrong.
func Preflight(cfg Config) PreflightReport {
	rep := PreflightReport{}
	add := func(name string, ok bool, fatal bool, format string, args ...any) {
		rep.Checks = append(rep.Checks, Check{
			Name: name, OK: ok, Fatal: fatal, Detail: fmt.Sprintf(format, args...),
		})
	}

	if !filesystemSupported {
		add("platform", false, true,
			"this platform cannot report filesystem devices, so the same-filesystem guarantee cannot be made; use API-only mode")
		return rep
	}

	if strings.TrimSpace(cfg.LibraryRoot) == "" {
		add("library root", false, true, "no library root was configured")
		return rep
	}

	libRoot, err := resolve(cfg.LibraryRoot)
	if err != nil {
		add("library root", false, true, "%v", err)
		return rep
	}
	info, err := os.Stat(libRoot)
	if err != nil || !info.IsDir() {
		add("library root", false, true, "%s is not a directory the Bridge can read", cfg.LibraryRoot)
		return rep
	}
	add("library root", true, true, "%s", libRoot)

	if err := probeWritable(libRoot); err != nil {
		add("library writable", false, true,
			"the library root is not writable by the Bridge, which filesystem publication requires: %v", err)
	} else {
		add("library writable", true, true, "the Bridge can write into the library root")
	}

	// The receiving staging area must not be inside the watched tree, or RomM
	// will index partial downloads.
	recvDir, err := resolve(cfg.ReceiveStagingDir)
	if err != nil {
		add("receive staging", false, true, "%v", err)
		return rep
	}
	if within(recvDir, libRoot) {
		add("receive staging", false, true,
			"the download staging directory %s is inside the watched library tree %s; RomM would index incomplete files. Move it outside the library",
			recvDir, libRoot)
	} else {
		add("receive staging", true, true, "%s is outside the watched tree", recvDir)
	}

	// The publication staging area must be on the library's filesystem, and also
	// outside the watched tree.
	pubDir := cfg.PublishStagingDir
	if strings.TrimSpace(pubDir) == "" {
		pubDir = DefaultPublishStagingDir(libRoot)
	}
	if err := os.MkdirAll(pubDir, 0o700); err != nil {
		add("publish staging", false, true, "cannot create the publication staging directory %s: %v", pubDir, err)
		return rep
	}
	pubDir, err = resolve(pubDir)
	if err != nil {
		add("publish staging", false, true, "%v", err)
		return rep
	}
	rep.PublishStagingDir = pubDir

	if within(pubDir, libRoot) {
		add("publish staging", false, true,
			"the publication staging directory %s is inside the watched library tree; RomM would index files before they are published",
			pubDir)
	} else {
		add("publish staging", true, true, "%s is outside the watched tree", pubDir)
	}

	libDev, err1 := deviceOf(libRoot)
	pubDev, err2 := deviceOf(pubDir)
	switch {
	case err1 != nil || err2 != nil:
		add("publish staging filesystem", false, true,
			"the filesystem of the library or publication staging directory could not be determined")
	case libDev != pubDev:
		add("publish staging filesystem", false, true,
			"the publication staging directory %s is on a different filesystem from the library %s. "+
				"Publication would not be atomic. Configure a publication staging directory on the library's own filesystem",
			pubDir, libRoot)
	default:
		add("publish staging filesystem", true, true,
			"the publication staging directory shares the library's filesystem, so publication is atomic")
	}

	// Whether the receiving staging area shares the library's filesystem is not
	// a failure either way; it decides which publication path is taken, and how
	// much disk a transfer needs.
	if recvDev, err := deviceOf(recvDir); err == nil && err1 == nil {
		rep.SameFilesystem = recvDev == libDev
		if rep.SameFilesystem {
			add("receive staging filesystem", true, false,
				"downloads land on the library's own filesystem, so publication needs no second copy")
		} else {
			add("receive staging filesystem", true, false,
				"downloads land on a different filesystem from the library, so each publication copies the verified payload onto the library's filesystem first. This is not a move and is never treated as atomic")
		}
	}

	return rep
}

// Publisher publishes verified payloads into a RomM library.
type Publisher struct {
	Config Config

	// report is the accepted preflight, held so publication cannot run against a
	// configuration that was never validated.
	report PreflightReport
}

// NewPublisher validates a configuration and returns a publisher.
func NewPublisher(cfg Config) (*Publisher, error) {
	rep := Preflight(cfg)
	if err := rep.Err(); err != nil {
		return nil, err
	}
	return &Publisher{Config: cfg, report: rep}, nil
}

// Report returns the accepted preflight report.
func (p *Publisher) Report() PreflightReport { return p.report }

// Publish moves a verified staged payload into its final library location.
//
// relativeDest is the path beneath the library root, for example
// "gb/Kirby (USA).gb". It is validated: a destination that escapes the library
// root is refused, because the Bridge's writable mount is scoped and publication
// must stay inside it.
//
// On success the file exists at its final name with its complete verified
// contents, and never existed there in any other state.
func (p *Publisher) Publish(stagedPath, relativeDest string, expected protocol.Digest) (string, error) {
	libRoot, err := resolve(p.Config.LibraryRoot)
	if err != nil {
		return "", err
	}

	finalPath, err := safeJoin(libRoot, relativeDest)
	if err != nil {
		return "", err
	}
	finalDir := filepath.Dir(finalPath)
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		return "", fmt.Errorf("destination: creating the destination directory: %w", err)
	}

	// Phase 6 step 4: recheck that the final destination contains no conflicting
	// file. The link below is what actually enforces it; this check exists to
	// fail early with a clear message rather than with a bare EEXIST.
	if _, err := os.Lstat(finalPath); err == nil {
		return "", fmt.Errorf("%w: %s", ErrDestinationExists, finalPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("destination: checking the destination: %w", err)
	}

	// Decide whether the staged file is already on the destination filesystem.
	stagedDev, err := deviceOf(stagedPath)
	if err != nil {
		return "", err
	}
	destDev, err := deviceOf(finalDir)
	if err != nil {
		return "", err
	}

	source := stagedPath
	var temporary string

	if stagedDev != destDev {
		// Cross-filesystem. Copy onto the destination filesystem, verify the
		// copy, and flush it. Copying and deleting would not be atomic, and the
		// Bridge must never describe it as though it were.
		temporary, err = p.copyToDestinationFilesystem(stagedPath, expected)
		if err != nil {
			return "", err
		}
		source = temporary
		defer os.Remove(temporary)
	}

	// Atomic, no-overwrite publication. os.Link fails if the destination exists,
	// which is what makes this safe without a check-then-act window.
	if err := os.Link(source, finalPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("%w: %s", ErrDestinationExists, finalPath)
		}
		return "", fmt.Errorf("destination: publishing to %s: %w", finalPath, err)
	}

	// Flush the directory entry, so the publication survives power loss.
	if err := syncDir(finalDir); err != nil {
		return "", err
	}

	// The staged file is now linked into the library. Removing the staging copy
	// leaves exactly one name pointing at the data.
	if temporary == "" {
		if err := os.Remove(stagedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			// The publication succeeded; a leftover staging file is untidy, not
			// a failure, and abandoned-staging cleanup will collect it.
			return finalPath, nil
		}
	}
	return finalPath, nil
}

// copyToDestinationFilesystem writes the payload onto the library's filesystem,
// re-verifies it there, and flushes it.
//
// Re-verifying the copy rather than trusting the source is deliberate: the copy
// is new bytes on a different device, and the whole point of this path is that
// the two filesystems are not the same one.
func (p *Publisher) copyToDestinationFilesystem(stagedPath string, expected protocol.Digest) (string, error) {
	src, err := os.Open(stagedPath)
	if err != nil {
		return "", fmt.Errorf("destination: opening the staged payload: %w", err)
	}
	defer src.Close()

	tmp, err := os.CreateTemp(p.report.PublishStagingDir, "publish-*.tmp")
	if err != nil {
		return "", fmt.Errorf("destination: creating a temporary file on the library filesystem: %w", err)
	}
	tmpName := tmp.Name()

	fail := func(err error) (string, error) {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}

	hasher := protocol.NewHasher()
	if _, err := io.Copy(io.MultiWriter(tmp, hasher), src); err != nil {
		return fail(fmt.Errorf("destination: copying onto the library filesystem: %w", err))
	}
	if err := tmp.Sync(); err != nil {
		return fail(fmt.Errorf("destination: flushing the copy: %w", err))
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("destination: closing the copy: %w", err)
	}

	got := hasher.Digest()
	strength, ok := got.Compare(expected)
	if !ok || strength < protocol.StrengthStrong {
		os.Remove(tmpName)
		return "", fmt.Errorf(
			"destination: the copy on the library filesystem does not match the verified payload; it was discarded")
	}

	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("destination: setting permissions on the copy: %w", err)
	}
	return tmpName, nil
}

// safeJoin resolves a relative destination beneath a root, refusing anything
// that escapes it.
func safeJoin(root, rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", errors.New("destination: no destination path was given")
	}
	if strings.ContainsRune(rel, 0) {
		return "", errors.New("destination: the destination path contains a null byte")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("destination: the destination path %q must be relative to the library root", rel)
	}

	joined := filepath.Join(root, rel)
	if !within(joined, root) {
		return "", fmt.Errorf("destination: the destination path %q escapes the library root", rel)
	}
	return joined, nil
}

// within reports whether path is root or lies beneath it.
func within(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolve cleans a path and follows symlinks, so that containment checks cannot
// be defeated by a link.
func resolve(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("destination: an empty path was configured")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("destination: resolving %s: %w", path, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// A path that does not exist yet cannot be resolved. Fall back to the
		// cleaned absolute form; callers that require existence check separately.
		if errors.Is(err, os.ErrNotExist) {
			return abs, nil
		}
		return "", fmt.Errorf("destination: resolving %s: %w", path, err)
	}
	return resolved, nil
}

// probeWritable confirms the Bridge can create and remove a file in a directory.
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".romm-swarm-writable-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// syncDir flushes a directory entry so a publication survives power loss.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("destination: opening %s to flush it: %w", dir, err)
	}
	defer d.Close()
	// Some filesystems refuse to fsync a directory. That weakens durability but
	// does not invalidate a publication that has otherwise succeeded.
	_ = d.Sync()
	return nil
}
