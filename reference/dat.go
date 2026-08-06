// Package reference imports approved reference catalogues and decides what a
// verified holding is.
//
// Scope of Work Phase 4 requires the reference model to record "source, date,
// version, and import checksum", and requires a completion percentage to name
// the profile and reference-set version it was computed against. Both follow
// from the same principle: a coverage figure is meaningless without the rules it
// was computed under, and those rules change when a catalogue is updated.
//
// Only the Logiqx XML format used by No-Intro is parsed here. Redump is
// deferred; Scope of Work Phase 4 makes that explicit, because disc content
// needs a canonicalization contract this project has not yet proven.
package reference

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// FamilyNoIntro is the catalogue family identifier for No-Intro sets.
const FamilyNoIntro = "no-intro"

// datFile mirrors the Logiqx document structure.
type datFile struct {
	XMLName xml.Name  `xml:"datafile"`
	Header  datHeader `xml:"header"`
	Games   []datGame `xml:"game"`
}

type datHeader struct {
	Name        string `xml:"name"`
	Description string `xml:"description"`
	Version     string `xml:"version"`
	Date        string `xml:"date"`
	Author      string `xml:"author"`
	Homepage    string `xml:"homepage"`
}

type datGame struct {
	Name        string   `xml:"name,attr"`
	Description string   `xml:"description"`
	ROMs        []datROM `xml:"rom"`
}

type datROM struct {
	Name   string `xml:"name,attr"`
	Size   int64  `xml:"size,attr"`
	CRC    string `xml:"crc,attr"`
	MD5    string `xml:"md5,attr"`
	SHA1   string `xml:"sha1,attr"`
	SHA256 string `xml:"sha256,attr"`
	Status string `xml:"status,attr"`
}

// ImportOptions configure a DAT import.
type ImportOptions struct {
	// Family is the catalogue family, defaulting to no-intro.
	Family string

	// Platform is the Swarm platform key this set describes. The DAT header
	// names a platform in prose ("Nintendo - Game Boy"), which is not a stable
	// key, so the caller states it explicitly.
	Platform protocol.PlatformID
}

// ImportDAT parses a Logiqx DAT and returns a reference set.
//
// The reader is consumed twice over conceptually — once to hash, once to parse —
// so it is buffered into memory. DAT files for the initial platform set are a
// few megabytes at most.
func ImportDAT(r io.Reader, opts ImportOptions) (*Set, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reference: reading catalogue: %w", err)
	}

	var doc datFile
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("reference: parsing catalogue: %w", err)
	}

	family := opts.Family
	if family == "" {
		family = FamilyNoIntro
	}

	set := &Set{
		Family: family,
		// The import checksum covers the catalogue file itself, so that a
		// coverage figure can be traced back to the exact bytes it was computed
		// from even if the publisher reissues a version under the same name.
		ImportDigest: protocol.DigestBytes(raw),
		Name:         doc.Header.Name,
		Description:  doc.Header.Description,
		Version:      doc.Header.Version,
		Date:         doc.Header.Date,
		Author:       doc.Header.Author,
		Homepage:     doc.Header.Homepage,
		Platform:     opts.Platform,
	}

	if set.Name == "" {
		return nil, fmt.Errorf("reference: catalogue header has no name")
	}
	if set.Version == "" {
		return nil, fmt.Errorf("reference: catalogue %q has no version; a coverage figure could not name its reference-set version", set.Name)
	}

	for _, g := range doc.Games {
		for _, rom := range g.ROMs {
			// A DAT entry with no usable hash cannot verify anything. Skipping
			// it is correct: including it would let an item be "matched" on
			// size alone.
			if rom.CRC == "" && rom.MD5 == "" && rom.SHA1 == "" && rom.SHA256 == "" {
				continue
			}
			// No-Intro marks known-bad dumps. They must never be a verification
			// target.
			if strings.EqualFold(rom.Status, "baddump") {
				continue
			}

			meta := ParseName(g.Name)
			set.Entries = append(set.Entries, Entry{
				GameName:     g.Name,
				ROMName:      rom.Name,
				CanonicalKey: meta.CanonicalKey,
				Regions:      meta.Regions,
				Languages:    meta.Languages,
				Revision:     meta.Revision,
				RevisionRank: meta.RevisionRank,
				Flags:        meta.Flags,
				IsBIOS:       meta.IsBIOS,
				Digest: protocol.Digest{
					Size:   rom.Size,
					CRC32:  strings.ToLower(rom.CRC),
					MD5:    strings.ToLower(rom.MD5),
					SHA1:   strings.ToLower(rom.SHA1),
					SHA256: strings.ToLower(rom.SHA256),
				},
			})
		}
	}

	if len(set.Entries) == 0 {
		return nil, fmt.Errorf("reference: catalogue %q contains no usable entries", set.Name)
	}

	set.index()
	return set, nil
}
