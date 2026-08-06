package reference

import (
	"strconv"
	"strings"
)

// Parsing of the No-Intro naming convention.
//
// A No-Intro entry name encodes structured facts in a human-readable string:
//
//	Super Mario Land (World) (Rev 1)
//	Pokemon - Red Version (USA, Europe) (SGB Enhanced)
//	Legend of Zelda, The - Oracle of Ages (Europe) (En,Fr,De,Es,It)
//	[BIOS] Game Boy Advance (World)
//
// Reading those tags is unavoidable, because region and revision rules are the
// whole basis of a collection profile. What matters is where the parse is
// trusted: these tags decide which *reference entries* a profile includes. They
// never decide whether a *held file* is verified — that is always a hash
// comparison. A misparsed tag can therefore mis-scope a completion percentage,
// but it can never cause unverified content to be advertised.

// NameMeta is the structured content of an entry name.
type NameMeta struct {
	// CanonicalKey is the name with every parenthesised and bracketed tag
	// removed. Two regional releases of one game share a canonical key, which
	// is what makes them a single canonical game.
	CanonicalKey string

	Regions   []string
	Languages []string

	// Revision is the tag as written, for display.
	Revision string

	// RevisionRank orders revisions: 0 for an original release, higher for
	// later ones. Used to pick the newest revision under a 1G1R profile.
	RevisionRank int

	// Flags are every other tag: Beta, Proto, Demo, Sample, Unl, and the many
	// hardware-feature tags.
	Flags []string

	IsBIOS bool
}

// knownRegions is the set of region tokens the parser recognises. A tag whose
// comma-separated tokens are all in this set is a region tag; anything else
// falls through to Flags rather than being guessed at.
var knownRegions = map[string]string{
	"world": "World", "usa": "USA", "europe": "Europe", "japan": "Japan",
	"asia": "Asia", "australia": "Australia", "brazil": "Brazil", "canada": "Canada",
	"china": "China", "korea": "Korea", "taiwan": "Taiwan", "russia": "Russia",
	"france": "France", "germany": "Germany", "italy": "Italy", "spain": "Spain",
	"netherlands": "Netherlands", "sweden": "Sweden", "norway": "Norway",
	"denmark": "Denmark", "finland": "Finland", "portugal": "Portugal",
	"greece": "Greece", "hong kong": "Hong Kong", "india": "India",
	"israel": "Israel", "mexico": "Mexico", "new zealand": "New Zealand",
	"poland": "Poland", "scandinavia": "Scandinavia", "south africa": "South Africa",
	"switzerland": "Switzerland", "turkey": "Turkey", "united kingdom": "United Kingdom",
	"belgium": "Belgium", "austria": "Austria", "ireland": "Ireland",
	"latin america": "Latin America", "unknown": "Unknown",
}

// knownLanguages is the set of two-letter language codes used in language tags.
var knownLanguages = map[string]bool{
	"en": true, "ja": true, "fr": true, "de": true, "es": true, "it": true,
	"nl": true, "pt": true, "sv": true, "no": true, "da": true, "fi": true,
	"zh": true, "ko": true, "ru": true, "pl": true, "cs": true, "hu": true,
	"el": true, "tr": true, "ar": true, "he": true, "ca": true, "sl": true,
	"hr": true, "sk": true, "ro": true, "bg": true, "uk": true, "et": true,
	"lv": true, "lt": true, "id": true, "ms": true, "th": true, "vi": true,
	"hi": true, "gd": true, "af": true, "sq": true, "eu": true, "fa": true,
}

// ParseName decomposes a reference entry name.
func ParseName(name string) NameMeta {
	meta := NameMeta{}

	work := strings.TrimSpace(name)
	if strings.HasPrefix(work, "[BIOS]") {
		meta.IsBIOS = true
		work = strings.TrimSpace(strings.TrimPrefix(work, "[BIOS]"))
	}

	tags, base := splitTags(work)
	meta.CanonicalKey = normalizeKey(base)

	for _, tag := range tags {
		switch {
		case isRegionTag(tag):
			meta.Regions = append(meta.Regions, splitRegions(tag)...)
		case isLanguageTag(tag):
			meta.Languages = append(meta.Languages, splitLanguages(tag)...)
		case isRevisionTag(tag):
			meta.Revision = tag
			meta.RevisionRank = revisionRank(tag)
		default:
			meta.Flags = append(meta.Flags, tag)
			if strings.EqualFold(tag, "BIOS") {
				meta.IsBIOS = true
			}
		}
	}
	return meta
}

// splitTags separates the trailing tag groups from the base title.
//
// Only groups at the end of the name are treated as tags. A parenthesis inside
// the title itself, which does happen, is left alone as long as the title does
// not end with it.
func splitTags(s string) (tags []string, base string) {
	base = s
	for {
		trimmed := strings.TrimSpace(base)
		if len(trimmed) == 0 {
			break
		}
		var open, close byte
		switch trimmed[len(trimmed)-1] {
		case ')':
			open, close = '(', ')'
		case ']':
			open, close = '[', ']'
		default:
			base = trimmed
			// Reverse, since tags were collected from the right.
			for i, j := 0, len(tags)-1; i < j; i, j = i+1, j-1 {
				tags[i], tags[j] = tags[j], tags[i]
			}
			return tags, base
		}

		idx := strings.LastIndexByte(trimmed, open)
		if idx < 0 {
			base = trimmed
			break
		}
		_ = close
		tags = append(tags, strings.TrimSpace(trimmed[idx+1:len(trimmed)-1]))
		base = trimmed[:idx]
	}

	for i, j := 0, len(tags)-1; i < j; i, j = i+1, j-1 {
		tags[i], tags[j] = tags[j], tags[i]
	}
	return tags, strings.TrimSpace(base)
}

// normalizeKey collapses a base title into a stable canonical key.
func normalizeKey(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func isRegionTag(tag string) bool {
	parts := strings.Split(tag, ",")
	for _, p := range parts {
		if _, ok := knownRegions[strings.ToLower(strings.TrimSpace(p))]; !ok {
			return false
		}
	}
	return len(parts) > 0
}

func splitRegions(tag string) []string {
	var out []string
	for _, p := range strings.Split(tag, ",") {
		if canonical, ok := knownRegions[strings.ToLower(strings.TrimSpace(p))]; ok {
			out = append(out, canonical)
		}
	}
	return out
}

func isLanguageTag(tag string) bool {
	parts := strings.Split(tag, ",")
	if len(parts) == 0 {
		return false
	}
	for _, p := range parts {
		if !knownLanguages[strings.ToLower(strings.TrimSpace(p))] {
			return false
		}
	}
	return true
}

func splitLanguages(tag string) []string {
	var out []string
	for _, p := range strings.Split(tag, ",") {
		out = append(out, strings.ToLower(strings.TrimSpace(p)))
	}
	return out
}

func isRevisionTag(tag string) bool {
	lower := strings.ToLower(tag)
	if strings.HasPrefix(lower, "rev ") || lower == "rev" {
		return true
	}
	// Version tags such as "v1.1". Require a digit after the v so that a title
	// tag like "vs" is not misread.
	if len(lower) > 1 && lower[0] == 'v' && lower[1] >= '0' && lower[1] <= '9' {
		return true
	}
	return false
}

// revisionRank turns a revision tag into an ordering. Later revisions rank
// higher, so a 1G1R profile keeps the newest.
func revisionRank(tag string) int {
	lower := strings.ToLower(strings.TrimSpace(tag))

	if strings.HasPrefix(lower, "rev") {
		rest := strings.TrimSpace(strings.TrimPrefix(lower, "rev"))
		if rest == "" {
			return 1
		}
		if n, err := strconv.Atoi(rest); err == nil {
			return n
		}
		// Letter revisions: A is the first revision after the original.
		if len(rest) == 1 && rest[0] >= 'a' && rest[0] <= 'z' {
			return int(rest[0]-'a') + 1
		}
		return 1
	}

	if len(lower) > 1 && lower[0] == 'v' {
		// "v1.1" ranks above "v1.0". Scale so the major version dominates.
		parts := strings.SplitN(lower[1:], ".", 3)
		rank := 0
		scale := 10000
		for _, p := range parts {
			n, err := strconv.Atoi(strings.TrimFunc(p, func(r rune) bool {
				return r < '0' || r > '9'
			}))
			if err != nil {
				break
			}
			rank += n * scale
			scale /= 100
			if scale == 0 {
				break
			}
		}
		return rank
	}
	return 0
}
