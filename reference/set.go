package reference

import (
	"fmt"
	"sort"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// Entry is one exact file described by a reference catalogue.
type Entry struct {
	// GameName is the catalogue's game name, tags included.
	GameName string
	// ROMName is the catalogue's filename for this exact file.
	ROMName string
	// CanonicalKey groups regional and revision variants of one work.
	CanonicalKey string

	Regions      []string
	Languages    []string
	Revision     string
	RevisionRank int
	Flags        []string
	IsBIOS       bool

	Digest protocol.Digest
}

// Set is one imported version of one reference catalogue.
type Set struct {
	Family      string
	Name        string
	Description string
	Version     string
	Date        string
	Author      string
	Homepage    string

	// ImportDigest is the hash of the catalogue file as imported, so a coverage
	// figure can be traced to the exact bytes behind it.
	ImportDigest protocol.Digest

	Platform protocol.PlatformID
	Entries  []Entry

	bySHA256 map[string][]int
	bySHA1   map[string][]int
	byMD5    map[string][]int
	byCRC    map[string][]int
}

// index builds the hash lookup tables.
func (s *Set) index() {
	s.bySHA256 = map[string][]int{}
	s.bySHA1 = map[string][]int{}
	s.byMD5 = map[string][]int{}
	s.byCRC = map[string][]int{}

	for i := range s.Entries {
		d := s.Entries[i].Digest.Normalized()
		if d.SHA256 != "" {
			s.bySHA256[d.SHA256] = append(s.bySHA256[d.SHA256], i)
		}
		if d.SHA1 != "" {
			s.bySHA1[d.SHA1] = append(s.bySHA1[d.SHA1], i)
		}
		if d.MD5 != "" {
			s.byMD5[d.MD5] = append(s.byMD5[d.MD5], i)
		}
		if d.CRC32 != "" {
			s.byCRC[d.CRC32] = append(s.byCRC[d.CRC32], i)
		}
	}
}

// Ref names this reference set for the record.
func (s *Set) Ref() string {
	return fmt.Sprintf("%s %s %s", s.Family, s.Name, s.Version)
}

// Match is one reference entry a canonical payload matched.
type Match struct {
	Index    int
	Entry    *Entry
	Strength protocol.StrengthLevel
}

// Lookup finds every reference entry a canonical payload matches.
//
// The search runs strongest hash first and stops at the first algorithm that
// produces candidates, so a SHA-1 match is not diluted by a wider CRC32 sweep.
// Every candidate is then re-checked with the full digest comparison, which
// rejects any candidate that agrees on one hash while disagreeing on another.
func (s *Set) Lookup(canonical protocol.Digest) []Match {
	d := canonical.Normalized()

	var candidates []int
	switch {
	case d.SHA256 != "" && len(s.bySHA256[d.SHA256]) > 0:
		candidates = s.bySHA256[d.SHA256]
	case d.SHA1 != "" && len(s.bySHA1[d.SHA1]) > 0:
		candidates = s.bySHA1[d.SHA1]
	case d.MD5 != "" && len(s.byMD5[d.MD5]) > 0:
		candidates = s.byMD5[d.MD5]
	case d.CRC32 != "" && len(s.byCRC[d.CRC32]) > 0:
		candidates = s.byCRC[d.CRC32]
	default:
		return nil
	}

	var out []Match
	for _, idx := range candidates {
		entry := &s.Entries[idx]
		strength, ok := d.Compare(entry.Digest)
		if !ok || strength == protocol.StrengthNone {
			// Agreement on the indexed hash but disagreement elsewhere, or size
			// disagreement. Not a match.
			continue
		}
		out = append(out, Match{Index: idx, Entry: entry, Strength: strength})
	}

	// Deterministic order so two Bridges reach the same conclusion.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Strength != out[j].Strength {
			return out[i].Strength > out[j].Strength
		}
		return out[i].Entry.ROMName < out[j].Entry.ROMName
	})
	return out
}

// CanonicalKeys returns the distinct canonical game keys in the set.
func (s *Set) CanonicalKeys() []string {
	seen := map[string]bool{}
	var out []string
	for i := range s.Entries {
		k := s.Entries[i].CanonicalKey
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
