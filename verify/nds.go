package verify

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// Nintendo DS.
//
// This is the first adapter where canonicalization does real work, and it is
// the reason Scope of Work Phase 0 asks for "trimmed-dump behavior" to be
// tested rather than assumed.
//
// A DS cartridge dump is the full chip: the game data followed by padding out
// to the chip's capacity. Trimming tools strip that padding to save space. A
// trimmed dump is the same game and will not match a reference entry, because
// the reference describes the full chip.
//
// So the adapter reconstructs: it streams the stored bytes and then streams the
// padding needed to reach the declared capacity. Two things about that are
// deliberate and worth stating plainly:
//
//   - The padding byte is a guess. Most cartridges pad with 0xFF, some with
//     0x00. The adapter tries 0xFF, records that it did so, and lets the
//     reference comparison decide. A reconstruction that does not match is
//     classified matched-unverified, which is the honest outcome; it is never
//     quietly rounded up to verified.
//
//   - The stored file is not modified. Reconstruction happens in the stream, on
//     the way to the hasher. The owner's trimmed file stays trimmed. When such
//     an item is later served to a peer, the sender streams the same
//     reconstruction, so the receiver verifies against the same canonical
//     payload the catalogue describes.

const (
	ndsGameCodeOffset    = 0x0C
	ndsCapacityOffset    = 0x14
	ndsUsedSizeOffset    = 0x80
	ndsLogoCRCOffset     = 0x15C
	ndsHeaderCRCOffset   = 0x15E
	ndsHeaderCRCCoverage = 0x15E // header CRC covers 0x000..0x15D
	ndsHeaderSize        = 0x200

	// ndsLogoCRCExpected is the CRC-16 of the compressed Nintendo logo every
	// genuine cartridge carries. The BIOS checks it at boot.
	ndsLogoCRCExpected = 0xCF56

	// ndsMaxCapacityShift bounds the declared capacity. The field is one byte,
	// so a corrupted header could otherwise ask for an absurd allocation; the
	// largest retail DS cartridge is 512 MiB, which is shift 12.
	ndsMaxCapacityShift = 12
)

// crc16Modbus computes the CRC-16 variant the DS header uses: reflected
// polynomial 0xA001, initial value 0xFFFF, no final inversion.
func crc16Modbus(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

// ndsCapacityBytes decodes the device-capacity field into a chip size. The
// encoding is 128 KiB shifted left by the field value.
func ndsCapacityBytes(field byte) (int64, bool) {
	if field > ndsMaxCapacityShift {
		return 0, false
	}
	return int64(128*1024) << field, true
}

// ndsHeaderCRCValid recomputes the header CRC over 0x000..0x15D.
func ndsHeaderCRCValid(p Peek) (bool, bool) {
	region, ok := p.Range(0, ndsHeaderCRCCoverage)
	if !ok {
		return false, false
	}
	stored, ok := p.Range(ndsHeaderCRCOffset, ndsHeaderCRCOffset+2)
	if !ok {
		return false, false
	}
	return crc16Modbus(region) == binary.LittleEndian.Uint16(stored), true
}

// ndsLogoCRCValid checks the fixed logo CRC.
func ndsLogoCRCValid(p Peek) (bool, bool) {
	stored, ok := p.Range(ndsLogoCRCOffset, ndsLogoCRCOffset+2)
	if !ok {
		return false, false
	}
	return binary.LittleEndian.Uint16(stored) == ndsLogoCRCExpected, true
}

// ndsGameCodePlausible reports whether the four-character game code looks like
// a real one: uppercase letters and digits.
func ndsGameCodePlausible(p Peek) bool {
	code, ok := p.Range(ndsGameCodeOffset, ndsGameCodeOffset+4)
	if !ok {
		return false
	}
	for _, c := range code {
		isUpper := c >= 'A' && c <= 'Z'
		isDigit := c >= '0' && c <= '9'
		if !isUpper && !isDigit {
			return false
		}
	}
	return true
}

// ndsHeaderShapePlausible checks the structural constraints a real DS header
// satisfies beyond its game code.
//
// The game code alone is four bytes drawn from a 36-character alphabet, which
// arbitrary data satisfies far too easily: a file of repeated 0x5A bytes has the
// "game code" ZZZZ. Requiring the reserved region to actually be reserved, and
// the two size fields to be consistent with each other, is what stops the weak
// detection path from claiming payloads that are not cartridges at all.
func ndsHeaderShapePlausible(p Peek) bool {
	if !ndsGameCodePlausible(p) {
		return false
	}

	// 0x15..0x1D is reserved and zero on every retail cartridge.
	reserved, ok := p.Range(0x15, 0x1E)
	if !ok {
		return false
	}
	for _, b := range reserved {
		if b != 0 {
			return false
		}
	}

	capField, ok := p.At(ndsCapacityOffset)
	if !ok {
		return false
	}
	capacity, ok := ndsCapacityBytes(capField)
	if !ok {
		return false
	}

	usedRaw, ok := p.Range(ndsUsedSizeOffset, ndsUsedSizeOffset+4)
	if !ok {
		return false
	}
	used := int64(binary.LittleEndian.Uint32(usedRaw))

	// The declared game data must fit on the declared chip, and must at least
	// cover the header it is described by.
	return used >= ndsHeaderSize && used <= capacity
}

// NintendoDSAdapter canonicalizes Nintendo DS cartridges, restoring trimmed
// dumps to full chip size.
type NintendoDSAdapter struct{}

func (NintendoDSAdapter) Ref() protocol.AdapterRef {
	return protocol.AdapterRef{ID: "nintendo.nds", Version: "1"}
}

func (NintendoDSAdapter) Platform() protocol.PlatformID { return protocol.PlatformNDS }

func (a NintendoDSAdapter) Detect(p Peek) Confidence {
	if p.Size < ndsHeaderSize {
		return ConfidenceNone
	}
	logoOK, logoChecked := ndsLogoCRCValid(p)
	headerOK, headerChecked := ndsHeaderCRCValid(p)

	switch {
	case logoChecked && logoOK && headerChecked && headerOK:
		return ConfidenceStrong
	case logoChecked && logoOK:
		// The logo CRC is a fixed constant every genuine cartridge shares. On
		// its own it is strong evidence of the platform even when the header
		// CRC fails, which is what a damaged dump looks like.
		return ConfidenceStrong
	case headerChecked && headerOK && ndsGameCodePlausible(p):
		return ConfidenceStrong
	case ndsHeaderShapePlausible(p):
		return ConfidenceWeak
	}
	return ConfidenceNone
}

func (a NintendoDSAdapter) Canonicalize(src io.ReaderAt, size int64) (*Canonical, error) {
	p, err := peekFrom(src, size, "")
	if err != nil {
		return nil, err
	}
	if size < ndsHeaderSize {
		return nil, fmt.Errorf("nds: payload is %d bytes, shorter than the %d-byte header", size, ndsHeaderSize)
	}

	var notes []string
	if ok, checked := ndsHeaderCRCValid(p); checked && !ok {
		notes = append(notes, "the cartridge header CRC does not match; this dump may be damaged")
	}

	capField, ok := p.At(ndsCapacityOffset)
	if !ok {
		return nil, fmt.Errorf("nds: could not read the device capacity field")
	}
	capacity, ok := ndsCapacityBytes(capField)
	if !ok {
		// A corrupted capacity field must not drive an allocation. Fall back to
		// passing the payload through unchanged and say so.
		notes = append(notes, fmt.Sprintf(
			"the declared device capacity (0x%02X) is not a valid cartridge size, so no padding was reconstructed", capField))
		return &Canonical{Reader: io.NewSectionReader(src, 0, size), Size: size, Notes: notes}, nil
	}

	usedRaw, ok := p.Range(ndsUsedSizeOffset, ndsUsedSizeOffset+4)
	if !ok {
		return nil, fmt.Errorf("nds: could not read the used-size field")
	}
	used := int64(binary.LittleEndian.Uint32(usedRaw))

	switch {
	case size == capacity:
		notes = append(notes, "the dump is already the full cartridge size, so no padding was reconstructed")
		return &Canonical{Reader: io.NewSectionReader(src, 0, size), Size: size, Notes: notes}, nil

	case size > capacity:
		// More data than the chip can hold. Overdumps exist and are not this
		// adapter's business to silently truncate.
		notes = append(notes, fmt.Sprintf(
			"the dump is %d bytes but the cartridge declares a capacity of %d; it was left unchanged and will not match a reference entry",
			size, capacity))
		return &Canonical{Reader: io.NewSectionReader(src, 0, size), Size: size, Notes: notes}, nil

	case used > 0 && size < used:
		// Truncated below the game's own declared extent: data is missing, and
		// padding would fabricate it.
		notes = append(notes, fmt.Sprintf(
			"the dump is %d bytes but the cartridge declares %d bytes of used data; it is incomplete and was left unchanged",
			size, used))
		return &Canonical{Reader: io.NewSectionReader(src, 0, size), Size: size, Notes: notes}, nil

	default:
		pad := capacity - size
		notes = append(notes, fmt.Sprintf(
			"the dump is trimmed; %d bytes of 0xFF padding were reconstructed in the stream to reach the %d-byte cartridge size. The stored file was not modified. If the cartridge padded with a different byte this reconstruction will not match, and the item stays unverified",
			pad, capacity))
		return &Canonical{
			Reader: io.MultiReader(io.NewSectionReader(src, 0, size), newPadReader(0xFF, pad)),
			Size:   capacity,
			Notes:  notes,
		}, nil
	}
}

// padReader yields a fixed number of identical bytes without allocating them
// all at once.
type padReader struct {
	b         byte
	remaining int64
}

func newPadReader(b byte, n int64) io.Reader { return &padReader{b: b, remaining: n} }

func (p *padReader) Read(dst []byte) (int, error) {
	if p.remaining <= 0 {
		return 0, io.EOF
	}
	n := int64(len(dst))
	if n > p.remaining {
		n = p.remaining
	}
	for i := int64(0); i < n; i++ {
		dst[i] = p.b
	}
	p.remaining -= n
	return int(n), nil
}
