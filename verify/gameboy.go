package verify

import (
	"bytes"
	"io"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// Game Boy and Game Boy Color.
//
// Canonicalization is the identity transform: the No-Intro sets for both
// platforms describe bare, headerless ROM images, so a correct dump is already
// canonical. The adapters still exist rather than being folded into a generic
// "raw" adapter, for three reasons that matter downstream:
//
//   - They separate GB from GBC by the cartridge's own CGB flag rather than by
//     which library folder the file happens to sit in.
//   - They validate structure, so a truncated or corrupted image is reported as
//     such instead of being hashed and silently classified as unmatched.
//   - They pin a rule version. If a future reference set ever describes these
//     platforms differently, that becomes gb/2 rather than a silent change to
//     what gb/1 meant.

// gbLogo is the Nintendo logo the boot ROM checks, stored at 0x104..0x133. A
// cartridge whose logo does not match will not boot on real hardware, which
// makes it the strongest cheap structural signal available for the platform.
var gbLogo = []byte{
	0xCE, 0xED, 0x66, 0x66, 0xCC, 0x0D, 0x00, 0x0B, 0x03, 0x73, 0x00, 0x83,
	0x00, 0x0C, 0x00, 0x0D, 0x00, 0x08, 0x11, 0x1F, 0x88, 0x89, 0x00, 0x0E,
	0xDC, 0xCC, 0x6E, 0xE6, 0xDD, 0xDD, 0xD9, 0x99, 0xBB, 0xBB, 0x67, 0x63,
	0x6E, 0x0E, 0xEC, 0xCC, 0xDD, 0xDC, 0x99, 0x9F, 0xBB, 0xB9, 0x33, 0x3E,
}

const (
	gbLogoOffset     = 0x104
	gbCGBFlagOffset  = 0x143
	gbHeaderChkStart = 0x134
	gbHeaderChkEnd   = 0x14C // inclusive
	gbHeaderChkAt    = 0x14D
	gbMinSize        = 0x150
)

// gbHeaderPresent reports whether the payload carries a valid Game Boy header.
func gbHeaderPresent(p Peek) bool {
	if p.Size < gbMinSize {
		return false
	}
	logo, ok := p.Range(gbLogoOffset, gbLogoOffset+len(gbLogo))
	if !ok {
		return false
	}
	return bytes.Equal(logo, gbLogo)
}

// gbIsColor reports whether the cartridge declares Game Boy Color support.
// 0x80 means "enhanced for, but compatible with, the original"; 0xC0 means
// "Color only". Both belong to the GBC reference set.
func gbIsColor(p Peek) bool {
	flag, ok := p.At(gbCGBFlagOffset)
	if !ok {
		return false
	}
	return flag == 0x80 || flag == 0xC0
}

// gbHeaderChecksumValid recomputes the cartridge header checksum. A failure
// does not stop canonicalization: the payload is still hashed and still
// compared against the reference set. It is recorded as a note so that a
// holding which fails to match has an explanation attached.
func gbHeaderChecksumValid(p Peek) (bool, bool) {
	region, ok := p.Range(gbHeaderChkStart, gbHeaderChkEnd+1)
	if !ok {
		return false, false
	}
	stored, ok := p.At(gbHeaderChkAt)
	if !ok {
		return false, false
	}
	var sum byte
	for _, b := range region {
		sum = sum - b - 1
	}
	return sum == stored, true
}

// gbCanonicalize is shared by both Game Boy adapters: pass the payload through
// unchanged, and report what was observed about it.
func gbCanonicalize(src io.ReaderAt, size int64, p Peek) *Canonical {
	notes := []string{"the reference set describes a bare ROM image, so no transformation was applied"}
	if ok, checked := gbHeaderChecksumValid(p); checked && !ok {
		notes = append(notes, "the cartridge header checksum does not match; this dump may be damaged")
	}
	return &Canonical{
		Reader: io.NewSectionReader(src, 0, size),
		Size:   size,
		Notes:  notes,
	}
}

// GameBoyAdapter canonicalizes original Game Boy cartridges.
type GameBoyAdapter struct{}

func (GameBoyAdapter) Ref() protocol.AdapterRef {
	return protocol.AdapterRef{ID: "nintendo.gb", Version: "1"}
}

func (GameBoyAdapter) Platform() protocol.PlatformID { return protocol.PlatformGB }

func (a GameBoyAdapter) Detect(p Peek) Confidence {
	if !gbHeaderPresent(p) {
		return ConfidenceNone
	}
	if gbIsColor(p) {
		// A Color cartridge belongs to the GBC adapter. Declining here rather
		// than competing on confidence keeps the two platforms from depending on
		// tie-break order.
		return ConfidenceNone
	}
	return ConfidenceStrong
}

func (a GameBoyAdapter) Canonicalize(src io.ReaderAt, size int64) (*Canonical, error) {
	p, err := peekFrom(src, size, "")
	if err != nil {
		return nil, err
	}
	return gbCanonicalize(src, size, p), nil
}

// GameBoyColorAdapter canonicalizes Game Boy Color cartridges.
type GameBoyColorAdapter struct{}

func (GameBoyColorAdapter) Ref() protocol.AdapterRef {
	return protocol.AdapterRef{ID: "nintendo.gbc", Version: "1"}
}

func (GameBoyColorAdapter) Platform() protocol.PlatformID { return protocol.PlatformGBC }

func (a GameBoyColorAdapter) Detect(p Peek) Confidence {
	if !gbHeaderPresent(p) || !gbIsColor(p) {
		return ConfidenceNone
	}
	return ConfidenceStrong
}

func (a GameBoyColorAdapter) Canonicalize(src io.ReaderAt, size int64) (*Canonical, error) {
	p, err := peekFrom(src, size, "")
	if err != nil {
		return nil, err
	}
	return gbCanonicalize(src, size, p), nil
}
