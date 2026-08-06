package verify

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// Result is the complete four-identity analysis of one stored file.
type Result struct {
	// Name is the stored file's name, for reporting only.
	Name string

	// Platform is the platform the structural evidence identified, empty when
	// nothing claimed the payload.
	Platform protocol.PlatformID

	// Stored is the identity of the bytes exactly as they sit on disk.
	Stored protocol.Digest

	// Container describes the archive envelope, nil for a bare file.
	Container *protocol.ContainerInfo

	// Canonical is the identity of the canonical payload. Zero when no adapter
	// claimed the payload.
	Canonical protocol.Digest

	// Adapter names the rules that produced Canonical.
	Adapter protocol.AdapterRef

	// Confidence is how strongly the adapter claimed the payload.
	Confidence Confidence

	// Canonicalized reports whether a canonical payload was produced at all.
	Canonicalized bool

	// Notes explain, in operator-facing language, everything that was observed
	// or transformed.
	Notes []string
}

// CanonicalPayload returns a reader over the canonical payload, recomputing the
// canonicalization from the source. Used by the transfer path, which must serve
// the canonical bytes rather than the stored ones.
type CanonicalPayload struct {
	Reader io.Reader
	Size   int64
	Close  func() error
}

// DefaultMaxInMemory bounds how large an archive member may be before it is
// spilled to a temporary file rather than decompressed into memory. 64 MiB
// comfortably covers the initial platform set's cartridges except the largest
// DS titles, and keeps a Bridge on modest hardware from being pushed into swap
// by a library scan.
const DefaultMaxInMemory = 64 << 20

// DefaultMaxArchiveMember bounds a single decompressed archive member. A zip
// declaring a member far larger than any cartridge in the supported set is
// either corrupt or hostile, and is refused rather than expanded.
const DefaultMaxArchiveMember = 2 << 30

// Analyzer computes the four identities for stored files.
//
// It never opens the source for writing. Scope of Work Phase 0: "Define the
// rule that canonicalization never rewrites the user's stored source file."
type Analyzer struct {
	// Registry supplies the platform adapters. Nil means DefaultRegistry.
	Registry *Registry

	// TempDir is where oversized archive members are spilled. Empty means the
	// operating system default. A Bridge should point this at its own staging
	// area so that a library scan cannot fill an unrelated filesystem.
	TempDir string

	// MaxInMemory overrides DefaultMaxInMemory.
	MaxInMemory int64

	// MaxArchiveMember overrides DefaultMaxArchiveMember.
	MaxArchiveMember int64
}

func (a *Analyzer) registry() *Registry {
	if a.Registry != nil {
		return a.Registry
	}
	return DefaultRegistry()
}

func (a *Analyzer) maxInMemory() int64 {
	if a.MaxInMemory > 0 {
		return a.MaxInMemory
	}
	return DefaultMaxInMemory
}

func (a *Analyzer) maxArchiveMember() int64 {
	if a.MaxArchiveMember > 0 {
		return a.MaxArchiveMember
	}
	return DefaultMaxArchiveMember
}

// AnalyzeFile analyses a file on disk. The file is opened read-only.
func (a *Analyzer) AnalyzeFile(filePath string, hint protocol.PlatformID) (*Result, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("verify: opening %s: %w", filePath, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("verify: stat %s: %w", filePath, err)
	}
	return a.Analyze(f, info.Size(), path.Base(filePath), hint)
}

// Analyze computes all four identities for a stored payload.
func (a *Analyzer) Analyze(src io.ReaderAt, size int64, name string, hint protocol.PlatformID) (*Result, error) {
	res := &Result{Name: name}

	// Identity one: the stored file, exactly as it sits on disk.
	stored, err := protocol.DigestReader(io.NewSectionReader(src, 0, size))
	if err != nil {
		return nil, fmt.Errorf("verify: hashing stored file: %w", err)
	}
	res.Stored = stored

	// Identity two: the container envelope, and the payload inside it.
	inner, innerSize, innerName, container, cleanup, notes, err := a.unwrap(src, size, name)
	if cleanup != nil {
		defer cleanup()
	}
	res.Notes = append(res.Notes, notes...)
	res.Container = container
	if err != nil {
		// An unreadable or unsafe container is not a fatal error for the scan:
		// the item is reported with a stored identity and no canonical identity,
		// which classifies as unmatched and stays local.
		res.Notes = append(res.Notes, err.Error())
		return res, nil
	}
	if inner == nil {
		return res, nil
	}

	// Identities three and four: the canonical payload, via the adapter that
	// claims it.
	peek, err := peekFrom(inner, innerSize, innerName)
	if err != nil {
		return nil, fmt.Errorf("verify: reading payload header: %w", err)
	}

	sel, selNotes := a.registry().selectAdapter(peek, hint)
	res.Notes = append(res.Notes, selNotes...)
	if sel.adapter == nil {
		res.Notes = append(res.Notes,
			"no platform adapter recognised this payload, so it has no canonical identity and stays local")
		return res, nil
	}

	res.Platform = sel.adapter.Platform()
	res.Adapter = sel.adapter.Ref()
	res.Confidence = sel.confidence

	canon, err := sel.adapter.Canonicalize(inner, innerSize)
	if err != nil {
		res.Notes = append(res.Notes, fmt.Sprintf("canonicalization failed: %v", err))
		return res, nil
	}
	res.Notes = append(res.Notes, canon.Notes...)

	canonDigest, err := protocol.DigestReader(canon.Reader)
	if err != nil {
		return nil, fmt.Errorf("verify: hashing canonical payload: %w", err)
	}
	res.Canonical = canonDigest
	res.Canonicalized = true

	if canon.Size >= 0 && canon.Size != canonDigest.Size {
		return nil, fmt.Errorf(
			"verify: adapter %s predicted a %d-byte canonical payload but produced %d bytes",
			res.Adapter, canon.Size, canonDigest.Size)
	}
	return res, nil
}

// unwrap opens a container, if there is one, and returns the payload inside.
//
// The returned cleanup function removes any temporary spill file and must be
// called by the caller.
func (a *Analyzer) unwrap(src io.ReaderAt, size int64, name string) (
	inner io.ReaderAt, innerSize int64, innerName string,
	container *protocol.ContainerInfo, cleanup func(), notes []string, err error,
) {
	head := make([]byte, 4)
	if size >= 4 {
		if _, rerr := src.ReadAt(head, 0); rerr != nil && rerr != io.EOF {
			return nil, 0, "", nil, nil, nil, fmt.Errorf("verify: reading container header: %w", rerr)
		}
	}

	if size < 4 || string(head) != "PK\x03\x04" {
		// A bare file. The stored bytes are the payload.
		return src, size, name, &protocol.ContainerInfo{Kind: protocol.ContainerNone}, nil, nil, nil
	}

	zr, zerr := zip.NewReader(src, size)
	if zerr != nil {
		return nil, 0, "", &protocol.ContainerInfo{Kind: protocol.ContainerZip}, nil, nil,
			fmt.Errorf("the zip archive could not be read: %v", zerr)
	}

	info := &protocol.ContainerInfo{Kind: protocol.ContainerZip}
	var candidates []*zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if unsafeArchiveName(f.Name) {
			// Phase 9 requires path-traversal and filename protections. Applied
			// here in Phase 0 because the analyser is the first code that ever
			// looks at an attacker-influenced archive.
			notes = append(notes, fmt.Sprintf(
				"the archive contains an entry with an unsafe name (%q); it was ignored", f.Name))
			continue
		}
		if int64(f.UncompressedSize64) > a.maxArchiveMember() {
			notes = append(notes, fmt.Sprintf(
				"the archive entry %q declares %d bytes, beyond the %d-byte limit; it was ignored",
				f.Name, f.UncompressedSize64, a.maxArchiveMember()))
			continue
		}
		info.Members = append(info.Members, protocol.ContainerMember{
			Name:  f.Name,
			Size:  int64(f.UncompressedSize64),
			CRC32: fmt.Sprintf("%08x", f.CRC32),
		})
		candidates = append(candidates, f)
	}

	switch len(candidates) {
	case 0:
		return nil, 0, "", info, nil, notes, errors.New("the archive contains no usable entries")
	case 1:
		// The MVP case.
	default:
		// Scope of Work §8: multi-file transfer is outside the MVP. The
		// container is still recorded, so the item is visible locally and the
		// data model is exercised, but it produces no canonical identity and is
		// therefore never advertised.
		notes = append(notes, fmt.Sprintf(
			"the archive holds %d entries; multi-file content is outside the MVP, so this item has no canonical identity and stays local",
			len(candidates)))
		return nil, 0, "", info, nil, notes, nil
	}

	member := candidates[0]
	ra, memberSize, cleanup, err := a.materialize(member)
	if err != nil {
		return nil, 0, "", info, cleanup, notes, err
	}
	notes = append(notes, fmt.Sprintf(
		"the payload was read from inside a zip archive (%q); the archive's own hash is recorded for provenance but is never used for matching",
		member.Name))
	return ra, memberSize, member.Name, info, cleanup, notes, nil
}

// materialize decompresses an archive member into something an adapter can read
// randomly: memory when it is small, a temporary file when it is not.
func (a *Analyzer) materialize(f *zip.File) (io.ReaderAt, int64, func(), error) {
	rc, err := f.Open()
	if err != nil {
		return nil, 0, nil, fmt.Errorf("the archive entry %q could not be opened: %v", f.Name, err)
	}
	defer rc.Close()

	declared := int64(f.UncompressedSize64)
	limit := a.maxArchiveMember()

	if declared <= a.maxInMemory() {
		// Read one byte beyond the declared size so that an entry lying about
		// its length is caught rather than silently truncated.
		buf, err := io.ReadAll(io.LimitReader(rc, declared+1))
		if err != nil {
			return nil, 0, nil, fmt.Errorf("the archive entry %q could not be decompressed: %v", f.Name, err)
		}
		if int64(len(buf)) != declared {
			return nil, 0, nil, fmt.Errorf(
				"the archive entry %q declares %d bytes but decompresses to a different length", f.Name, declared)
		}
		return newBytesReaderAt(buf), declared, nil, nil
	}

	tmp, err := os.CreateTemp(a.TempDir, "romm-swarm-analyze-*")
	if err != nil {
		return nil, 0, nil, fmt.Errorf("could not create a temporary file for %q: %v", f.Name, err)
	}
	cleanup := func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}

	written, err := io.Copy(tmp, io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, 0, cleanup, fmt.Errorf("the archive entry %q could not be decompressed: %v", f.Name, err)
	}
	if written > limit {
		return nil, 0, cleanup, fmt.Errorf(
			"the archive entry %q expanded beyond the %d-byte limit and was refused", f.Name, limit)
	}
	if written != declared {
		return nil, 0, cleanup, fmt.Errorf(
			"the archive entry %q declares %d bytes but decompressed to %d", f.Name, declared, written)
	}
	return tmp, written, cleanup, nil
}

// unsafeArchiveName reports whether an archive entry name must not be trusted.
func unsafeArchiveName(name string) bool {
	if name == "" {
		return true
	}
	if strings.ContainsRune(name, 0) {
		return true
	}
	// Absolute paths, drive letters, and parent traversal.
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return true
	}
	if len(name) >= 2 && name[1] == ':' {
		return true
	}
	normalized := strings.ReplaceAll(name, `\`, "/")
	for _, part := range strings.Split(normalized, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// peekFrom reads the leading bytes an adapter inspects.
func peekFrom(src io.ReaderAt, size int64, name string) (Peek, error) {
	n := int64(PeekSize)
	if size < n {
		n = size
	}
	head := make([]byte, n)
	if n > 0 {
		if _, err := src.ReadAt(head, 0); err != nil && err != io.EOF {
			return Peek{}, err
		}
	}
	return Peek{Head: head, Size: size, Name: name}, nil
}

// bytesReaderAt adapts a byte slice to io.ReaderAt.
type bytesReaderAt struct{ b []byte }

func newBytesReaderAt(b []byte) io.ReaderAt { return &bytesReaderAt{b: b} }

func (r *bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off > int64(len(r.b)) {
		return 0, io.EOF
	}
	n := copy(p, r.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
