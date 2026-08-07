// Package scan turns a RomM server's inventory into a normalized local
// manifest.
//
// Scope of Work Phase 0 deliverable: "Prototype a normalized local inventory
// manifest from one RomM server." This package is that prototype: it lists
// what one RomM instance holds, downloads and analyzes each item with the
// four-identity model in package verify, classifies it against a reference
// catalogue with package reference, and hands the result to
// bridge/publish.Snapshot to produce a protocol.Manifest.
//
// It is explicitly a prototype in the sense Phase 0 uses the word. The RomM
// side of it — Source — talks to RomM only through paths a capability probe
// has already resolved (see bridge/romm), so it carries no second, independent
// guess about RomM's URL shape. Where an assumption remains, it is named as
// one. See docs/phase0/multi-file-archive-and-ingestion-behavior.md for the
// broader picture this package is one piece of.
package scan

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
	"github.com/Crimson3076/RomM-Swarm/reference"
	"github.com/Crimson3076/RomM-Swarm/verify"
)

// Page requests one page of a RomM listing.
type Page struct {
	Limit  int
	Offset int
}

// ROMRecord is one item as RomM's inventory listing describes it, normalized
// to the fields this pipeline needs.
//
// Hashes carries whatever hash fields RomM reported, keyed by field name
// exactly as observed (see bridge/romm.Report.HashFields). It is never
// authoritative — Scope of Work requires reference-hash verification of the
// canonical payload, computed independently by this Bridge, not a value taken
// on trust from the server describing the file. It is retained only as a cheap
// early cross-check and for diagnostics when a download doesn't verify.
type ROMRecord struct {
	ID           string
	PlatformSlug string
	// Name is RomM's own metadata title. Used only as the cross-check
	// reference.Classify performs against the hash-derived identity; it never
	// grants verification on its own.
	Name        string
	FSName      string
	FSSizeBytes int64
	Hashes      map[string]string
}

// Source is the narrow adapter boundary onto a RomM server's inventory.
//
// Deliberately small, on the same pattern as ingest.Library and
// ingest.Uploader: a real implementation talks to RomM, a fake one talks to
// nothing, and the scanning logic below is identical either way.
type Source interface {
	// ListROMs returns one page of every record RomM reports, across every
	// platform, and whether more pages remain.
	//
	// Deliberately unfiltered by platform. RomM's platform_id query parameter
	// needs a numeric id this pipeline would have to resolve from a slug through
	// yet another unconfirmed field mapping, and filtering server-side would
	// desynchronize an offset-based page cursor from a client-side filtered
	// count. Scanner filters by platform itself, over pages whose size is
	// exactly what was requested.
	ListROMs(ctx context.Context, page Page) ([]ROMRecord, bool, error)

	// Download streams one record's content. The caller closes the reader.
	Download(ctx context.Context, rec ROMRecord) (io.ReadCloser, int64, error)
}

// Skipped explains one record that did not become a local inventory item, so a
// scan's output is never a silent subset of what RomM reported.
type Skipped struct {
	Record ROMRecord
	Reason string
}

// Result is one platform's scan output.
type Result struct {
	Items   []protocol.Item
	Skipped []Skipped

	// Scanned is the number of records RomM reported, including the skipped
	// ones. Items plus Skipped must always sum to Scanned.
	Scanned int
}

// PlatformSource maps a RomM platform slug to this Swarm's stable platform key,
// and to the reference selection used to classify items on it.
//
// RomM's platform slugs are, like its endpoint paths, an assumption pending
// confirmation against a real server — see
// docs/phase0/multi-file-archive-and-ingestion-behavior.md. Kept as an explicit
// mapping rather than guessed at, for the same reason the capability table is a
// data structure rather than inline string literals: a wrong guess becomes a
// one-line fix instead of a silent misclassification.
type PlatformSource struct {
	RomMSlug string
	Platform protocol.PlatformID
	// Selection classifies items on this platform. Nil means the platform has
	// no reference catalogue loaded yet, and every item on it is skipped with a
	// clear reason rather than silently omitted.
	Selection *reference.Selection
}

// Scanner assembles a local inventory from a RomM Source.
type Scanner struct {
	Source Source

	// Analyzer computes the four identities. Nil uses verify.DefaultRegistry.
	Analyzer *verify.Analyzer

	// PageSize bounds how many records are requested per page. Scope of Work §7
	// asks Bridges to avoid unnecessary load on participating RomM servers;
	// paging in modest batches is the concrete expression of that here, per the
	// Phase 3 deliverable "Large scans are uploaded in batches without
	// overwhelming the Host" applied on the read side as well.
	PageSize int

	// Now is injectable for tests.
	Now func() time.Time
}

func (s *Scanner) analyzer() *verify.Analyzer {
	if s.Analyzer != nil {
		return s.Analyzer
	}
	return &verify.Analyzer{}
}

func (s *Scanner) pageSize() int {
	if s.PageSize > 0 {
		return s.PageSize
	}
	return 50
}

func (s *Scanner) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ScanPlatform lists and analyzes every record RomM reports for one platform.
//
// A record that fails to download, fails to canonicalize, or classifies as
// anything other than verified_eligible does not become an Item — it becomes a
// Skipped entry with the reason, so the caller can tell "RomM has nothing here"
// from "RomM has twelve things here and none of them verified".
func (s *Scanner) ScanPlatform(ctx context.Context, ps PlatformSource) (Result, error) {
	var res Result

	offset := 0
	for {
		page, hasMore, err := s.Source.ListROMs(ctx, Page{Limit: s.pageSize(), Offset: offset})
		if err != nil {
			return res, fmt.Errorf("scan: listing RomM's inventory: %w", err)
		}

		for _, rec := range page {
			if rec.PlatformSlug != ps.RomMSlug {
				continue
			}
			res.Scanned++
			item, reason, err := s.scanOne(ctx, ps, rec)
			if err != nil {
				return res, fmt.Errorf("scan: %s (%s): %w", rec.FSName, rec.ID, err)
			}
			if reason != "" {
				res.Skipped = append(res.Skipped, Skipped{Record: rec, Reason: reason})
				continue
			}
			res.Items = append(res.Items, *item)
		}

		// The page cursor always advances by what the server actually returned,
		// never by what survived the client-side platform filter, or a filtered
		// page would desynchronize the offset from the server's own pagination.
		if !hasMore || len(page) == 0 {
			break
		}
		offset += len(page)
	}

	sort.Slice(res.Items, func(i, j int) bool { return res.Items[i].FileID < res.Items[j].FileID })
	return res, nil
}

// scanOne analyzes and classifies a single record. A non-empty reason means the
// record was not turned into an item; err is reserved for failures the caller
// cannot classify as a normal skip (a download that hangs, a context
// cancellation) and stops the scan rather than silently continuing past them.
func (s *Scanner) scanOne(ctx context.Context, ps PlatformSource, rec ROMRecord) (*protocol.Item, string, error) {
	if ps.Selection == nil {
		return nil, "no reference catalogue is loaded for this platform, so nothing on it can be verified", nil
	}

	rc, size, err := s.Source.Download(ctx, rec)
	if err != nil {
		return nil, fmt.Sprintf("could not be downloaded from RomM: %v", err), nil
	}
	defer rc.Close()

	buf, err := readAllLimited(rc, size)
	if err != nil {
		return nil, fmt.Sprintf("download did not match its declared size: %v", err), nil
	}

	res, err := s.analyzer().Analyze(newBytesReaderAt(buf), int64(len(buf)), rec.FSName, ps.Platform)
	if err != nil {
		return nil, "", fmt.Errorf("analyzing %s: %w", rec.FSName, err)
	}

	outcome := reference.Classify(res, ps.Selection, rec.Name)
	if !outcome.Classification.Publishable() {
		return nil, fmt.Sprintf("classified %s: %s", outcome.Classification, joinNotes(outcome.Notes)), nil
	}

	item := protocol.Item{
		FileID:         protocol.FileIDFromCanonicalDigest(res.Canonical.SHA256),
		Platform:       res.Platform,
		Stored:         res.Stored,
		Container:      res.Container,
		Canonical:      res.Canonical,
		Adapter:        res.Adapter,
		Classification: outcome.Classification,
		Reference:      outcome.Match,
		Notes:          append(append([]string{}, res.Notes...), outcome.Notes...),
		Availability:   protocol.AvailAvailable,
		LastVerifiedAt: s.now(),
	}
	if outcome.Match != nil {
		item.GameID = protocol.GameIDFromReference(outcome.Match.Family, outcome.Match.CanonicalKey)
	}

	if err := item.Validate(); err != nil {
		// The assembly above should make this unreachable; surfaced rather than
		// silently trusted, because a scan is exactly the place a subtle
		// assembly bug would otherwise go unnoticed until publication failed.
		return nil, "", fmt.Errorf("assembling the local item for %s: %w", rec.FSName, err)
	}
	return &item, "", nil
}

func joinNotes(notes []string) string {
	if len(notes) == 0 {
		return "no further detail was recorded"
	}
	return notes[len(notes)-1]
}

// readAllLimited reads exactly declaredSize bytes when declaredSize is known,
// and reports a mismatch rather than silently accepting a short or long read.
// RomM's declared size is, like everything else about the response shape, not
// something this Bridge trusts blindly — the four-identity analysis that
// follows is what actually decides whether the content is right.
func readAllLimited(r io.Reader, declaredSize int64) ([]byte, error) {
	if declaredSize <= 0 {
		return io.ReadAll(r)
	}
	buf := make([]byte, declaredSize)
	n, err := io.ReadFull(r, buf)
	if err != nil {
		return nil, fmt.Errorf("read %d of %d declared bytes: %w", n, declaredSize, err)
	}
	// One extra byte to catch a stream that is longer than declared.
	var extra [1]byte
	if n2, _ := r.Read(extra[:]); n2 > 0 {
		return nil, fmt.Errorf("stream continued past its declared %d-byte size", declaredSize)
	}
	return buf, nil
}

type bytesReaderAt struct{ b []byte }

func newBytesReaderAt(b []byte) *bytesReaderAt { return &bytesReaderAt{b: b} }

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
