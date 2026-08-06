package verify

import (
	"io"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// Game Boy Advance.
//
// As with Game Boy, the No-Intro set describes a bare ROM image and
// canonicalization is the identity transform. Detection uses the header's fixed
// byte and header checksum rather than the full Nintendo logo: the logo is 156
// bytes at 0x04, and checking the two computed invariants is both cheaper and
// harder to satisfy by accident than a byte-for-byte constant comparison.

const (
	gbaFixedByteOffset = 0xB2 // must be 0x96
	gbaChkStart        = 0xA0
	gbaChkEnd          = 0xBC // inclusive
	gbaChkAt           = 0xBD
	gbaMinSize         = 0xC0
)

// gbaHeaderChecksum computes the cartridge header checksum over 0xA0..0xBC.
func gbaHeaderChecksum(region []byte) byte {
	var chk byte
	for _, b := range region {
		chk -= b
	}
	return chk - 0x19
}

// GameBoyAdvanceAdapter canonicalizes Game Boy Advance cartridges.
type GameBoyAdvanceAdapter struct{}

func (GameBoyAdvanceAdapter) Ref() protocol.AdapterRef {
	return protocol.AdapterRef{ID: "nintendo.gba", Version: "1"}
}

func (GameBoyAdvanceAdapter) Platform() protocol.PlatformID { return protocol.PlatformGBA }

func (a GameBoyAdvanceAdapter) Detect(p Peek) Confidence {
	if p.Size < gbaMinSize {
		return ConfidenceNone
	}
	fixed, ok := p.At(gbaFixedByteOffset)
	if !ok || fixed != 0x96 {
		return ConfidenceNone
	}
	region, ok := p.Range(gbaChkStart, gbaChkEnd+1)
	if !ok {
		return ConfidenceNone
	}
	stored, ok := p.At(gbaChkAt)
	if !ok {
		return ConfidenceNone
	}
	if gbaHeaderChecksum(region) != stored {
		// The fixed byte alone is one byte and will collide. Without a valid
		// header checksum this is a weak claim, and the analysis records it.
		return ConfidenceWeak
	}
	return ConfidenceStrong
}

func (a GameBoyAdvanceAdapter) Canonicalize(src io.ReaderAt, size int64) (*Canonical, error) {
	p, err := peekFrom(src, size, "")
	if err != nil {
		return nil, err
	}

	notes := []string{"the reference set describes a bare ROM image, so no transformation was applied"}
	if region, ok := p.Range(gbaChkStart, gbaChkEnd+1); ok {
		if stored, ok := p.At(gbaChkAt); ok && gbaHeaderChecksum(region) != stored {
			notes = append(notes, "the cartridge header checksum does not match; this dump may be damaged")
		}
	}

	return &Canonical{
		Reader: io.NewSectionReader(src, 0, size),
		Size:   size,
		Notes:  notes,
	}, nil
}
