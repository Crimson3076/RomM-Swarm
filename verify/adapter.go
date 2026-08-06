// Package verify implements the format-aware canonicalization and verification
// model that everything else in RomM Swarm rests on.
//
// # The four identities
//
// Scope of Work Phase 0 requires a verification spike "that distinguishes
// stored-file, container, canonical-payload, and reference identities". Those
// four are genuinely different things, and collapsing any two of them produces a
// specific, known failure:
//
//   - Stored-file identity is the hash of the bytes on the owner's disk. Two
//     people holding the identical game as a bare ROM and as a zip have
//     different stored-file identities. Matching on this alone reports honest
//     duplicates as distinct holdings and inflates coverage.
//
//   - Container identity describes the envelope: archive format, member names,
//     member sizes. Recorded for provenance, never used for matching, because
//     two zips of the same ROM made by different tools differ.
//
//   - Canonical-payload identity is the hash of the payload after format-aware
//     normalisation: the archive opened, a copier header removed, an interleaved
//     dump de-interleaved, a trimmed dump restored. This is the only identity
//     that means "the same game data".
//
//   - Reference identity is what the approved catalogue says a correct dump of
//     that game hashes to. A canonical payload equal to a reference entry is a
//     verified holding; anything else is not, however plausible its filename.
//
// # The rule that constrains every adapter
//
// Scope of Work Phase 0: "Define the rule that canonicalization never rewrites
// the user's stored source file." Adapters here read; they never write. They
// take an io.ReaderAt and return a stream. Nothing in this package opens a file
// for writing, and TestPhase0_CanonicalizationNeverMutatesTheSource asserts the
// source bytes are untouched after a full analysis.
//
// # Versioning
//
// Every adapter carries an id and a version, and every result names the adapter
// that produced it. Phase 4 acceptance requires that "Verification results name
// the canonicalization-adapter version used" — without it, changing a rule
// silently reclassifies history and no one can tell which figures were computed
// under which rules. Changing an adapter's behaviour means incrementing its
// version, never editing the old rule in place.
package verify

import (
	"fmt"
	"io"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// PeekSize is how many leading bytes an adapter may inspect when deciding
// whether a payload is its business. It covers every header the initial
// platform set uses: the Nintendo DS header runs to 0x200, the Mega Drive
// console-name field sits at 0x100, and a Super Magic Drive copier header is
// 512 bytes.
const PeekSize = 4096

// Confidence is how strongly an adapter claims a payload.
type Confidence int

const (
	// ConfidenceNone means the adapter does not recognise the payload.
	ConfidenceNone Confidence = iota

	// ConfidenceWeak means only soft evidence matched, typically a file
	// extension or a plausible size. Never sufficient on its own: a weak claim
	// is used only when no adapter makes a strong one, and the result is
	// annotated to say so.
	ConfidenceWeak

	// ConfidenceStrong means a structural invariant matched: an embedded logo,
	// a magic number, a header checksum. This is what an adapter needs to claim
	// a payload when several adapters are plausible.
	ConfidenceStrong
)

func (c Confidence) String() string {
	switch c {
	case ConfidenceWeak:
		return "weak"
	case ConfidenceStrong:
		return "strong"
	default:
		return "none"
	}
}

// Peek is what an adapter sees when deciding whether to claim a payload.
type Peek struct {
	// Head is the first PeekSize bytes, or the whole payload when it is
	// shorter. Adapters must range-check before indexing: a truncated or
	// deliberately malformed file is a normal input, not an exceptional one.
	Head []byte

	// Size is the full payload size in bytes.
	Size int64

	// Name is the payload's filename, when one is known. Adapters may use the
	// extension as a hint but must never treat it as evidence: a filename is
	// attacker-controlled and is exactly the signal this project exists to stop
	// trusting.
	Name string
}

// At returns the byte at offset, and whether the peek buffer reaches that far.
func (p Peek) At(offset int) (byte, bool) {
	if offset < 0 || offset >= len(p.Head) {
		return 0, false
	}
	return p.Head[offset], true
}

// Range returns Head[from:to], and whether the peek buffer covers it.
func (p Peek) Range(from, to int) ([]byte, bool) {
	if from < 0 || to < from || to > len(p.Head) {
		return nil, false
	}
	return p.Head[from:to], true
}

// Canonical is an adapter's output: a stream of canonical payload bytes plus
// the observations that explain how they were produced.
type Canonical struct {
	// Reader yields the canonical payload.
	Reader io.Reader

	// Size is the canonical payload's length, or -1 when it cannot be known
	// before reading.
	Size int64

	// Notes record every transformation applied, in the order applied. These
	// reach the operator, so they are written as plain statements about what
	// happened rather than as internal jargon.
	Notes []string
}

// Adapter turns a container-unwrapped payload into its canonical form.
type Adapter interface {
	// Ref identifies the adapter and its rule version.
	Ref() protocol.AdapterRef

	// Platform is the platform this adapter canonicalizes for.
	Platform() protocol.PlatformID

	// Detect reports how strongly the adapter claims the payload.
	Detect(p Peek) Confidence

	// Canonicalize returns the canonical payload stream. It must not modify the
	// source.
	Canonicalize(src io.ReaderAt, size int64) (*Canonical, error)
}

// Registry holds the adapters available to a build.
//
// Order of registration does not affect selection: selection is by confidence,
// then by a deterministic tie-break on adapter id, so two Bridges with the same
// adapter set always reach the same conclusion about the same file.
type Registry struct {
	adapters []Adapter
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{} }

// Register adds an adapter. It panics on a duplicate id and version, because a
// duplicate means two different rule sets are claiming the same identity and
// results would stop being reproducible.
func (r *Registry) Register(a Adapter) {
	ref := a.Ref()
	for _, existing := range r.adapters {
		if existing.Ref() == ref {
			panic(fmt.Sprintf("verify: adapter %s registered twice", ref))
		}
	}
	r.adapters = append(r.adapters, a)
}

// Adapters returns the registered adapters.
func (r *Registry) Adapters() []Adapter { return r.adapters }

// Lookup finds an adapter by its reference.
func (r *Registry) Lookup(ref protocol.AdapterRef) (Adapter, bool) {
	for _, a := range r.adapters {
		if a.Ref() == ref {
			return a, true
		}
	}
	return nil, false
}

// selection is an adapter and the confidence with which it claimed a payload.
type selection struct {
	adapter    Adapter
	confidence Confidence
}

// selectAdapter picks the adapter for a payload.
//
// A platform hint from RomM narrows the field but does not decide the outcome:
// RomM's platform assignment comes from the library layout, which is a human
// convention and is wrong often enough that trusting it would defeat the point.
// When the hint and the structural evidence disagree, the evidence wins and the
// disagreement is recorded as a note.
func (r *Registry) selectAdapter(p Peek, hint protocol.PlatformID) (selection, []string) {
	var notes []string
	var best selection

	for _, a := range r.adapters {
		c := a.Detect(p)
		if c == ConfidenceNone {
			continue
		}
		if c > best.confidence {
			best = selection{adapter: a, confidence: c}
			continue
		}
		if c == best.confidence && best.adapter != nil {
			// Deterministic tie-break, so the choice does not depend on
			// registration order or map iteration.
			if a.Ref().String() < best.adapter.Ref().String() {
				best = selection{adapter: a, confidence: c}
			}
		}
	}

	if best.adapter == nil {
		return selection{}, notes
	}
	if hint != "" && best.adapter.Platform() != hint {
		notes = append(notes, fmt.Sprintf(
			"the library says this is %s, but its structure identifies it as %s; the structure was used",
			hint, best.adapter.Platform()))
	}
	if best.confidence == ConfidenceWeak {
		notes = append(notes, fmt.Sprintf(
			"%s claimed this payload on weak evidence only; treat the canonical identity as provisional",
			best.adapter.Ref()))
	}
	return best, notes
}

// DefaultRegistry returns the adapters for the initial MVP platform set.
//
// Scope of Work Phase 0 fixes that set at three to five primarily single-file
// platforms; see docs/adr/0004-initial-platforms.md. Disc platforms are absent
// deliberately: Phase 4 gates them behind an adapter contract that can verify a
// track layout and a CHD's internal state, which nothing here attempts.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(&GameBoyAdapter{})
	r.Register(&GameBoyColorAdapter{})
	r.Register(&GameBoyAdvanceAdapter{})
	r.Register(&NintendoDSAdapter{})
	r.Register(&GenesisBinAdapter{})
	r.Register(&GenesisSMDAdapter{})
	return r
}
