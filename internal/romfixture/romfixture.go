// Package romfixture synthesises structurally valid cartridge images for tests.
//
// Scope of Work Phase 0 acceptance: "Every initial platform has repeatable
// canonicalization fixtures that accept known-good variants and reject
// corrupted payloads." Repeatable is the operative word. These fixtures are
// generated from a seed rather than checked in as binaries, which means:
//
//   - The test suite carries no ROM data, which matters for a project whose
//     entire legal posture is that it never centralises content.
//   - A fixture is byte-identical on every machine and every run, so a hash
//     assertion is stable.
//   - The headers are genuinely valid — real checksums, real magic values — so
//     the adapters are exercised the same way a real cartridge would exercise
//     them, rather than against a shape built to satisfy the detector.
//
// Nothing here reproduces copyrighted content. The payload behind each header is
// deterministic filler.
package romfixture

import (
	"encoding/binary"
	"fmt"
)

// filler produces deterministic pseudo-random bytes from a seed, so a fixture is
// identical on every run without being a constant blob.
func filler(seed uint64, n int) []byte {
	out := make([]byte, n)
	state := seed*6364136223846793005 + 1442695040888963407
	for i := range out {
		state = state*6364136223846793005 + 1442695040888963407
		out[i] = byte(state >> 33)
	}
	return out
}

func seedFor(s string) uint64 {
	var h uint64 = 1469598103934665603
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

// gbLogo is the Nintendo logo a Game Boy cartridge must carry.
var gbLogo = []byte{
	0xCE, 0xED, 0x66, 0x66, 0xCC, 0x0D, 0x00, 0x0B, 0x03, 0x73, 0x00, 0x83,
	0x00, 0x0C, 0x00, 0x0D, 0x00, 0x08, 0x11, 0x1F, 0x88, 0x89, 0x00, 0x0E,
	0xDC, 0xCC, 0x6E, 0xE6, 0xDD, 0xDD, 0xD9, 0x99, 0xBB, 0xBB, 0x67, 0x63,
	0x6E, 0x0E, 0xEC, 0xCC, 0xDD, 0xDC, 0x99, 0x9F, 0xBB, 0xB9, 0x33, 0x3E,
}

// GameBoy builds a Game Boy cartridge image with a valid header.
// Set color to produce a Game Boy Color cartridge instead.
func GameBoy(title string, size int, color bool) []byte {
	if size < 0x8000 {
		size = 0x8000
	}
	rom := filler(seedFor(title), size)

	copy(rom[0x104:], gbLogo)

	// Title field, 0x134..0x143, space padded and truncated.
	for i := 0x134; i <= 0x143; i++ {
		rom[i] = 0
	}
	copy(rom[0x134:0x143], title)

	if color {
		rom[0x143] = 0x80 // enhanced for Color, compatible with the original
	} else {
		rom[0x143] = 0x00
	}

	rom[0x146] = 0x00 // no Super Game Boy support
	rom[0x147] = 0x00 // ROM only
	rom[0x148] = 0x00 // 32 KiB
	rom[0x149] = 0x00 // no cartridge RAM
	rom[0x14A] = 0x01 // non-Japanese
	rom[0x14B] = 0x33
	rom[0x14C] = 0x00 // mask ROM version

	var sum byte
	for i := 0x134; i <= 0x14C; i++ {
		sum = sum - rom[i] - 1
	}
	rom[0x14D] = sum

	return rom
}

// GameBoyAdvance builds a Game Boy Advance cartridge image with a valid header.
func GameBoyAdvance(title string, size int) []byte {
	if size < 0x200 {
		size = 0x200
	}
	rom := filler(seedFor(title), size)

	rom[0x00] = 0x2E // branch to the entry point
	rom[0x01] = 0x00
	rom[0x02] = 0x00
	rom[0x03] = 0xEA

	for i := 0xA0; i <= 0xAB; i++ {
		rom[i] = 0
	}
	copy(rom[0xA0:0xAC], title)

	copy(rom[0xAC:0xB0], "AXXE") // game code
	copy(rom[0xB0:0xB2], "01")   // maker code
	rom[0xB2] = 0x96             // the fixed value every cartridge carries
	rom[0xB3] = 0x00             // main unit code
	rom[0xB4] = 0x00             // device type
	for i := 0xB5; i <= 0xBC; i++ {
		rom[i] = 0
	}

	var chk byte
	for i := 0xA0; i <= 0xBC; i++ {
		chk -= rom[i]
	}
	rom[0xBD] = chk - 0x19
	rom[0xBE] = 0
	rom[0xBF] = 0

	return rom
}

// crc16Modbus is the CRC the Nintendo DS header uses.
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

// NintendoDSOptions configure a DS fixture.
type NintendoDSOptions struct {
	Title    string
	GameCode string
	// CapacityShift encodes the chip size as 128 KiB << CapacityShift.
	CapacityShift byte
	// UsedBytes is how much of the chip the game occupies. The remainder is
	// 0xFF padding, exactly as a real cartridge dump would be.
	UsedBytes int
}

// NintendoDS builds a full-size Nintendo DS cartridge dump: the game data
// followed by 0xFF padding out to the declared chip capacity.
func NintendoDS(opts NintendoDSOptions) []byte {
	if opts.GameCode == "" {
		opts.GameCode = "ANDE"
	}
	capacity := (128 * 1024) << opts.CapacityShift
	if opts.UsedBytes <= 0 || opts.UsedBytes > capacity {
		opts.UsedBytes = capacity / 2
	}
	if opts.UsedBytes < 0x200 {
		opts.UsedBytes = 0x200
	}

	// Start from a fully padded chip so the trailing bytes are genuine padding.
	rom := make([]byte, capacity)
	for i := range rom {
		rom[i] = 0xFF
	}
	copy(rom, filler(seedFor(opts.Title), opts.UsedBytes))

	for i := 0x00; i < 0x0C; i++ {
		rom[i] = 0
	}
	copy(rom[0x00:0x0C], opts.Title)
	copy(rom[0x0C:0x10], opts.GameCode)
	copy(rom[0x10:0x12], "01")
	rom[0x12] = 0x00 // unit code: Nintendo DS
	rom[0x13] = 0x00
	rom[0x14] = opts.CapacityShift
	for i := 0x15; i <= 0x1D; i++ {
		rom[i] = 0
	}
	rom[0x1E] = 0x00 // ROM version
	rom[0x1F] = 0x00

	binary.LittleEndian.PutUint32(rom[0x80:], uint32(opts.UsedBytes))
	binary.LittleEndian.PutUint32(rom[0x84:], 0x4000)

	// The compressed Nintendo logo occupies 0xC0..0x15B. Its content is fixed
	// on real hardware; what the adapter checks is the stored CRC constant, so
	// deterministic filler plus the correct constant exercises the same path.
	copy(rom[0xC0:0x15C], filler(0xD5, 0x15C-0xC0))
	binary.LittleEndian.PutUint16(rom[0x15C:], 0xCF56)

	// The header CRC covers 0x000..0x15D and must be written last.
	binary.LittleEndian.PutUint16(rom[0x15E:], crc16Modbus(rom[0:0x15E]))

	return rom
}

// NintendoDSTrimmed returns the trimmed form of a full DS dump: the same bytes
// with the trailing 0xFF padding removed, which is what trimming tools produce
// and what the adapter must reconstruct.
func NintendoDSTrimmed(full []byte) []byte {
	used := int(binary.LittleEndian.Uint32(full[0x80:]))
	if used <= 0 || used > len(full) {
		return full
	}
	out := make([]byte, used)
	copy(out, full[:used])
	return out
}

// GenesisBin builds a plain Mega Drive image with a Sega console-name field.
// The size is rounded up to a whole number of 16 KiB blocks so it can also be
// expressed in the interleaved copier format.
func GenesisBin(title string, size int) []byte {
	const block = 16384
	if size < 0x200 {
		size = 0x200
	}
	if rem := size % block; rem != 0 {
		size += block - rem
	}
	rom := filler(seedFor(title), size)

	copy(rom[0x100:0x110], "SEGA MEGA DRIVE ")
	copy(rom[0x110:0x120], "(C)ROMM 2026.AUG")

	for i := 0x120; i < 0x150; i++ {
		rom[i] = ' '
	}
	copy(rom[0x120:0x150], title)

	return rom
}

// GenesisSMD converts a plain image into the Super Magic Drive copier format:
// a 512-byte header followed by 16 KiB blocks whose odd and even bytes have been
// separated into the two halves of the block.
//
// This is the exact inverse of what the adapter does, which is what makes the
// round-trip test meaningful.
func GenesisSMD(bin []byte) ([]byte, error) {
	const (
		block = 16384
		half  = block / 2
	)
	if len(bin)%block != 0 {
		return nil, fmt.Errorf("romfixture: a plain image of %d bytes is not a whole number of %d-byte blocks", len(bin), block)
	}
	blocks := len(bin) / block

	out := make([]byte, 512+len(bin))
	out[0] = byte(blocks) // block count, low byte
	out[1] = 0x03
	out[2] = 0x00 // not a split file
	out[8] = 0xAA // the copier format's identifying bytes
	out[9] = 0xBB
	out[10] = 0x06

	for b := 0; b < blocks; b++ {
		src := bin[b*block : (b+1)*block]
		dst := out[512+b*block : 512+(b+1)*block]
		for i := 0; i < half; i++ {
			dst[i] = src[2*i+1]    // first half holds the odd bytes
			dst[half+i] = src[2*i] // second half holds the even bytes
		}
	}
	return out, nil
}

// Corrupt returns a copy of data with one byte flipped at the given offset,
// standing in for the damaged dumps the harness must reject.
func Corrupt(data []byte, offset int) []byte {
	out := make([]byte, len(data))
	copy(out, data)
	if offset >= 0 && offset < len(out) {
		out[offset] ^= 0xFF
	}
	return out
}
