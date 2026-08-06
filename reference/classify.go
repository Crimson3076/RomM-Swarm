package reference

import (
	"fmt"
	"strings"

	"github.com/Crimson3076/RomM-Swarm/protocol"
	"github.com/Crimson3076/RomM-Swarm/verify"
)

// Classification of a held file against a reference set and a collection
// profile.
//
// This is where Scope of Work §3's central rule is actually enforced: "Only
// matched and reference-hash-verified content is advertised, transferred, or
// counted in the MVP." Everything upstream computes evidence; this function is
// the single place that turns evidence into permission.

// Outcome is the result of classifying one held file.
type Outcome struct {
	Classification protocol.Classification
	Match          *protocol.ReferenceMatch
	Notes          []string
}

// Classify decides what a held file is.
//
// metadataTitle is what the local RomM believes the file to be, and may be
// empty. It is used only to detect disagreement: a metadata title never makes an
// item verified, and its absence never prevents one from being verified. Scope
// of Work §10's response to "False metadata matches" is to "Require both
// accepted metadata identity and reference hash", and the asymmetry is
// deliberate — the hash is the authority, the metadata is a cross-check that can
// raise a conflict.
func Classify(res *verify.Result, sel *Selection, metadataTitle string) Outcome {
	if res == nil {
		return Outcome{Classification: protocol.ClassUnmatched}
	}

	if !res.Canonicalized {
		return Outcome{
			Classification: protocol.ClassUnmatched,
			Notes: []string{
				"no canonical payload could be produced, so this file cannot be verified against the reference set and stays local",
			},
		}
	}

	matches := sel.Set.Lookup(res.Canonical)
	if len(matches) == 0 {
		if strings.TrimSpace(metadataTitle) != "" {
			return Outcome{
				Classification: protocol.ClassMatchedUnverified,
				Notes: []string{fmt.Sprintf(
					"the library identifies this as %q, but its canonical payload matches no entry in %s. It is not advertised, transferred, or counted",
					metadataTitle, sel.Set.Ref()),
				},
			}
		}
		return Outcome{
			Classification: protocol.ClassUnmatched,
			Notes: []string{fmt.Sprintf(
				"the canonical payload matches no entry in %s", sel.Set.Ref())},
		}
	}

	// Matches spanning more than one canonical game cannot all be true.
	keys := map[string]bool{}
	for _, m := range matches {
		keys[m.Entry.CanonicalKey] = true
	}
	if len(keys) > 1 {
		names := make([]string, 0, len(matches))
		for _, m := range matches {
			names = append(names, m.Entry.GameName)
		}
		return Outcome{
			Classification: protocol.ClassConflict,
			Notes: []string{fmt.Sprintf(
				"the canonical payload matches entries for more than one game (%s); this needs review and is not advertised",
				strings.Join(names, ", "))},
		}
	}

	// Prefer a match the profile includes, so a file listed in the catalogue
	// under several names is not excluded on a technicality.
	best := matches[0]
	for _, m := range matches {
		if sel.Includes(m.Index) {
			best = m
			break
		}
	}

	refMatch := &protocol.ReferenceMatch{
		Family:       sel.Set.Family,
		SetName:      sel.Set.Name,
		SetVersion:   sel.Set.Version,
		EntryName:    best.Entry.ROMName,
		CanonicalKey: best.Entry.CanonicalKey,
		Strength:     best.Strength,
	}

	if title := strings.TrimSpace(metadataTitle); title != "" && !titlesAgree(title, best.Entry.CanonicalKey) {
		return Outcome{
			Classification: protocol.ClassConflict,
			Match:          refMatch,
			Notes: []string{fmt.Sprintf(
				"the library identifies this as %q but its canonical payload matches %q; this needs review and is not advertised",
				title, best.Entry.GameName)},
		}
	}

	if !sel.Includes(best.Index) {
		return Outcome{
			Classification: protocol.ClassVerifiedExcluded,
			Match:          refMatch,
			Notes: []string{fmt.Sprintf(
				"verified as %q, which the %s profile does not include. It is a genuine dump and is not missing; it simply does not count toward this profile's completion",
				best.Entry.GameName, sel.Profile.Ref())},
		}
	}

	return Outcome{
		Classification: protocol.ClassVerifiedEligible,
		Match:          refMatch,
		Notes: []string{fmt.Sprintf(
			"verified as %q against %s using %s agreement",
			best.Entry.GameName, sel.Set.Ref(), best.Strength)},
	}
}

// titlesAgree compares a library's title with a reference canonical key.
//
// Deliberately permissive. Library metadata is written by humans and by
// scrapers, and differs in punctuation, article placement, and subtitle
// handling in ways that carry no meaning. A false conflict is expensive: it
// pushes a genuine verified holding into a review queue and out of coverage. A
// missed conflict is cheap by comparison, because the hash has already proven
// what the file is — the metadata cross-check only ever adds suspicion, it never
// grants verification.
//
// So agreement is: equal after normalisation, or one contains the other.
func titlesAgree(a, b string) bool {
	na, nb := normalizeTitle(a), normalizeTitle(b)
	if na == "" || nb == "" {
		return true
	}
	return na == nb || strings.Contains(na, nb) || strings.Contains(nb, na)
}

// leadingArticles are moved or dropped during title normalisation. Reference
// catalogues write "Legend of Zelda, The" while library metadata writes "The
// Legend of Zelda"; both name the same game, and treating them as a conflict
// would push genuine verified holdings into a review queue for a formatting
// convention.
var leadingArticles = map[string]bool{
	"the": true, "a": true, "an": true,
	"le": true, "la": true, "les": true, "l": true,
	"der": true, "die": true, "das": true,
	"el": true, "los": true, "las": true,
	"il": true, "lo": true, "gli": true,
	"de": true, "het": true,
}

// normalizeTitle reduces a title to comparable letters and digits, with a
// leading or trailing article removed so that the two conventions agree.
func normalizeTitle(s string) string {
	// Split on anything that is not a letter or digit.
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		isLetter := r >= 'a' && r <= 'z'
		isDigit := r >= '0' && r <= '9'
		return !isLetter && !isDigit
	})

	// Drop an article at either end, but never reduce a title to nothing: a
	// game genuinely called "The" must still compare against itself.
	if len(fields) > 1 && leadingArticles[fields[0]] {
		fields = fields[1:]
	}
	if len(fields) > 1 && leadingArticles[fields[len(fields)-1]] {
		fields = fields[:len(fields)-1]
	}

	return strings.Join(fields, "")
}
