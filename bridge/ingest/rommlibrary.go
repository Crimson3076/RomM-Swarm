package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Crimson3076/RomM-Swarm/bridge/scan"
	"github.com/Crimson3076/RomM-Swarm/protocol"
	"github.com/Crimson3076/RomM-Swarm/verify"
)

// RommLibrary implements Library against a real RomM server.
//
// It reuses bridge/scan.Source rather than talking to RomM's listing and
// download endpoints a second time — those are already proven against RomM's
// confirmed API shape (docs/adr/0003-supported-romm-versions.md), and a
// second, independent implementation would just be a second place for the
// same assumption to go stale.
//
// The one thing this type adds is the rule the ingestion docs describe as
// still outstanding when this codebase last checked: an observation is never
// RomM's self-reported hash taken on trust. Every candidate is downloaded and
// run back through the same verify.Analyzer the sending side used, so the
// canonical identity Reconciler compares against Expected is one this Bridge
// computed itself. See
// docs/phase0/multi-file-archive-and-ingestion-behavior.md, "Still assumed,
// or newly identified as unbuilt" (as of the commit that added this file,
// item 2).
type RommLibrary struct {
	Source scan.Source

	// Analyzer independently recomputes the canonical identity of whatever
	// RomM reports having ingested. Nil uses a default Analyzer.
	Analyzer *verify.Analyzer

	// PageSize bounds how many records are requested per listing page. Scope
	// of Work §7: avoid unnecessary load on participating RomM servers.
	PageSize int
}

func (l *RommLibrary) analyzer() *verify.Analyzer {
	if l.Analyzer != nil {
		return l.Analyzer
	}
	return &verify.Analyzer{}
}

func (l *RommLibrary) pageSize() int {
	if l.PageSize > 0 {
		return l.PageSize
	}
	return 50
}

// Observe implements Library.
//
// filename locates the candidate: it is the same value Upload or Publish
// were given for this transfer. It never decides the outcome on its own —
// only a downloaded-and-recomputed canonical identity does, in classify.
func (l *RommLibrary) Observe(ctx context.Context, filename string, expected Expected) (Observation, error) {
	if l.Source == nil {
		return Observation{}, errors.New("ingest: RommLibrary requires a Source")
	}
	if filename == "" {
		return Observation{}, errors.New("ingest: RommLibrary needs the filename the item was published or uploaded under")
	}

	offset := 0
	for {
		page, hasMore, err := l.Source.ListROMs(ctx, scan.Page{Limit: l.pageSize(), Offset: offset})
		if err != nil {
			return Observation{}, fmt.Errorf("ingest: listing RomM's inventory: %w", err)
		}

		for _, rec := range page {
			if rec.PlatformSlug != string(expected.Platform) || rec.FSName != filename {
				continue
			}
			return l.observeRecord(ctx, rec)
		}

		if !hasMore || len(page) == 0 {
			break
		}
		offset += len(page)

		select {
		case <-ctx.Done():
			return Observation{}, ctx.Err()
		default:
		}
	}
	return Observation{Present: false}, nil
}

// observeRecord downloads one candidate and recomputes its canonical
// identity. It never trusts rec.Hashes: those are RomM's own hash fields,
// computed over the raw stored bytes, which for a platform with a copier
// header or a trimmed dump differ from this project's canonical identity —
// exactly the case this independent recomputation exists to catch.
func (l *RommLibrary) observeRecord(ctx context.Context, rec scan.ROMRecord) (Observation, error) {
	rc, size, err := l.Source.Download(ctx, rec)
	if err != nil {
		return Observation{}, fmt.Errorf("ingest: downloading %s from RomM to verify it: %w", rec.FSName, err)
	}
	defer rc.Close()

	buf, err := readAllDeclared(rc, size)
	if err != nil {
		return Observation{}, fmt.Errorf("ingest: %s did not download cleanly: %w", rec.FSName, err)
	}

	res, err := l.analyzer().Analyze(newBufferReaderAt(buf), int64(len(buf)), rec.FSName, protocol.PlatformID(rec.PlatformSlug))
	if err != nil {
		return Observation{}, fmt.Errorf("ingest: analyzing %s: %w", rec.FSName, err)
	}

	return Observation{
		Present:   true,
		Platform:  res.Platform,
		Canonical: res.Canonical,
		Name:      rec.Name,
	}, nil
}

// readAllDeclared reads exactly declaredSize bytes when it is known, and
// reports a mismatch rather than silently accepting a short or long read —
// the same rule bridge/scan applies to a RomM download, applied again here
// because RomM's declared content length is no more trusted on the
// observation side than on the scanning side.
func readAllDeclared(r io.Reader, declaredSize int64) ([]byte, error) {
	if declaredSize <= 0 {
		return io.ReadAll(r)
	}
	buf := make([]byte, declaredSize)
	n, err := io.ReadFull(r, buf)
	if err != nil {
		return nil, fmt.Errorf("read %d of %d declared bytes: %w", n, declaredSize, err)
	}
	var extra [1]byte
	if n2, _ := r.Read(extra[:]); n2 > 0 {
		return nil, fmt.Errorf("stream continued past its declared %d-byte size", declaredSize)
	}
	return buf, nil
}

type bufferReaderAt struct{ b []byte }

func newBufferReaderAt(b []byte) *bufferReaderAt { return &bufferReaderAt{b: b} }

func (r *bufferReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off > int64(len(r.b)) {
		return 0, io.EOF
	}
	n := copy(p, r.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
