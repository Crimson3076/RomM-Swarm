package verify

import (
	"bytes"
	"fmt"
	"io"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// Mega Drive and Genesis.
//
// This platform is in the initial set specifically because it forces the
// project to confront the case that a single raw-file hash rule cannot handle,
// and that Scope of Work §10 lists as a major risk: "Real files differ from
// reference encoding".
//
// The same game circulates in two encodings:
//
//   - .bin, a plain image. The No-Intro set describes this one.
//   - .smd, the Super Magic Drive copier format: a 512-byte copier header
//     followed by 16 KiB blocks in which the odd and even bytes have been
//     separated into the two halves of the block.
//
// The two files share no bytes in common order and hash to entirely different
// values, yet they are the same game. Hashing the stored file would report them
// as two unrelated holdings; de-interleaving the .smd produces exactly the .bin
// the catalogue describes. TestPhase0_GenesisSMDAndBinCanonicalizeIdentically
// asserts precisely that.
//
// The 512-byte copier header is removed because the applicable reference rules
// define the canonical image as headerless. That is the whole justification, and
// it is why Scope of Work Phase 4 phrases the deliverable as "Header handling
// only when defined by the applicable reference rules" rather than as a general
// permission to strip leading bytes.

const (
	smdHeaderSize = 512
	smdBlockSize  = 16384
	smdHalfBlock  = smdBlockSize / 2

	// The Super Magic Drive header's identifying bytes.
	smdMagicOffsetA = 8
	smdMagicOffsetB = 9
	smdMagicValueA  = 0xAA
	smdMagicValueB  = 0xBB

	// The console-name field in a plain image.
	genesisConsoleNameOffset = 0x100
)

// genesisConsoleName reports whether the payload carries a Sega console-name
// field. Both "SEGA MEGA DRIVE" and "SEGA GENESIS" appear, and some images
// begin the field with a space, so only the "SEGA" token is matched and only at
// the two offsets it legitimately occupies.
func genesisConsoleName(p Peek) bool {
	for _, off := range []int{genesisConsoleNameOffset, genesisConsoleNameOffset + 1} {
		if got, ok := p.Range(off, off+4); ok && bytes.Equal(got, []byte("SEGA")) {
			return true
		}
	}
	return false
}

// GenesisBinAdapter canonicalizes plain Mega Drive images. Canonicalization is
// the identity transform; the adapter exists to identify the platform and to
// pin a rule version.
type GenesisBinAdapter struct{}

func (GenesisBinAdapter) Ref() protocol.AdapterRef {
	return protocol.AdapterRef{ID: "sega.genesis.bin", Version: "1"}
}

func (GenesisBinAdapter) Platform() protocol.PlatformID { return protocol.PlatformGenesis }

func (a GenesisBinAdapter) Detect(p Peek) Confidence {
	if p.Size < genesisConsoleNameOffset+16 {
		return ConfidenceNone
	}
	if genesisConsoleName(p) {
		return ConfidenceStrong
	}
	return ConfidenceNone
}

func (a GenesisBinAdapter) Canonicalize(src io.ReaderAt, size int64) (*Canonical, error) {
	return &Canonical{
		Reader: io.NewSectionReader(src, 0, size),
		Size:   size,
		Notes:  []string{"the reference set describes a plain image, so no transformation was applied"},
	}, nil
}

// GenesisSMDAdapter canonicalizes Super Magic Drive images by removing the
// copier header and de-interleaving each block.
type GenesisSMDAdapter struct{}

func (GenesisSMDAdapter) Ref() protocol.AdapterRef {
	return protocol.AdapterRef{ID: "sega.genesis.smd", Version: "1"}
}

func (GenesisSMDAdapter) Platform() protocol.PlatformID { return protocol.PlatformGenesis }

func (a GenesisSMDAdapter) Detect(p Peek) Confidence {
	if !smdSizeShaped(p.Size) {
		return ConfidenceNone
	}
	magicA, okA := p.At(smdMagicOffsetA)
	magicB, okB := p.At(smdMagicOffsetB)
	if !okA || !okB || magicA != smdMagicValueA || magicB != smdMagicValueB {
		return ConfidenceNone
	}
	return ConfidenceStrong
}

// smdSizeShaped reports whether the size is a copier header plus a whole number
// of blocks.
func smdSizeShaped(size int64) bool {
	return size > smdHeaderSize && (size-smdHeaderSize)%smdBlockSize == 0
}

func (a GenesisSMDAdapter) Canonicalize(src io.ReaderAt, size int64) (*Canonical, error) {
	if !smdSizeShaped(size) {
		return nil, fmt.Errorf(
			"genesis.smd: %d bytes is not a 512-byte copier header plus whole %d-byte blocks", size, smdBlockSize)
	}
	body := size - smdHeaderSize
	return &Canonical{
		Reader: newSMDDeinterleaver(io.NewSectionReader(src, smdHeaderSize, body)),
		Size:   body,
		Notes: []string{
			"a 512-byte Super Magic Drive copier header was removed, because the reference set defines the canonical image as headerless",
			fmt.Sprintf("%d interleaved %d-byte blocks were de-interleaved into the plain image the reference set describes", body/smdBlockSize, smdBlockSize),
		},
	}, nil
}

// smdDeinterleaver streams the de-interleaved image.
//
// Within each 16 KiB block the first half holds the bytes at odd positions and
// the second half holds the bytes at even positions, so reassembly is:
//
//	out[2i]   = block[halfBlock+i]
//	out[2i+1] = block[i]
//
// Streaming a block at a time keeps peak memory at 32 KiB regardless of the
// cartridge size.
type smdDeinterleaver struct {
	src io.Reader
	in  []byte
	out []byte
	// pos is how much of out has already been handed to the caller.
	pos int
	err error
}

func newSMDDeinterleaver(src io.Reader) io.Reader {
	return &smdDeinterleaver{
		src: src,
		in:  make([]byte, smdBlockSize),
		out: make([]byte, 0, smdBlockSize),
	}
}

func (d *smdDeinterleaver) Read(p []byte) (int, error) {
	for d.pos >= len(d.out) {
		if d.err != nil {
			return 0, d.err
		}
		if err := d.fill(); err != nil {
			d.err = err
			if len(d.out) == 0 {
				return 0, err
			}
		}
	}
	n := copy(p, d.out[d.pos:])
	d.pos += n
	return n, nil
}

// fill reads and de-interleaves one block.
func (d *smdDeinterleaver) fill() error {
	n, err := io.ReadFull(d.src, d.in)
	if err == io.EOF {
		return io.EOF
	}
	if err == io.ErrUnexpectedEOF || (err == nil && n != smdBlockSize) {
		// The size check in Canonicalize should make this unreachable; a
		// truncated read here means the file changed underneath us mid-scan.
		return fmt.Errorf("genesis.smd: block truncated at %d of %d bytes", n, smdBlockSize)
	}
	if err != nil {
		return err
	}

	d.out = d.out[:smdBlockSize]
	for i := 0; i < smdHalfBlock; i++ {
		d.out[2*i] = d.in[smdHalfBlock+i]
		d.out[2*i+1] = d.in[i]
	}
	d.pos = 0
	return nil
}
