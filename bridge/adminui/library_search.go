package adminui

import (
	"sort"
	"strings"

	"github.com/Crimson3076/RomM-Swarm/bridge/scan"
)

// Work on a copy: sorting a cached slice would race with other readers and
// change the page order underneath a second browser.
func searchLibrary(records []scan.ROMRecord, platform, query, order string) []scan.ROMRecord {
	query = strings.ToLower(strings.TrimSpace(query))
	out := make([]scan.ROMRecord, 0)
	for _, rec := range records {
		if platform != "" && rec.PlatformSlug != platform {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(rec.Name), query) &&
			!strings.Contains(strings.ToLower(rec.FSName), query) {
			continue
		}
		out = append(out, rec)
	}
	if order == "" {
		return out
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch order {
		case "size_desc":
			if a.FSSizeBytes != b.FSSizeBytes {
				return a.FSSizeBytes > b.FSSizeBytes
			}
		case "size_asc":
			if a.FSSizeBytes != b.FSSizeBytes {
				return a.FSSizeBytes < b.FSSizeBytes
			}
		case "platform":
			if a.PlatformSlug != b.PlatformSlug {
				return a.PlatformSlug < b.PlatformSlug
			}
		}
		an, bn := strings.ToLower(libraryTitle(a)), strings.ToLower(libraryTitle(b))
		if an != bn {
			if order == "name_desc" {
				return an > bn
			}
			return an < bn
		}
		return a.ID < b.ID
	})
	return out
}

func libraryTitle(rec scan.ROMRecord) string {
	if rec.Name != "" {
		return rec.Name
	}
	return rec.FSName
}

func libraryPlatforms(records []scan.ROMRecord) []string {
	seen := make(map[string]bool)
	for _, rec := range records {
		if rec.PlatformSlug != "" {
			seen[rec.PlatformSlug] = true
		}
	}
	out := make([]string, 0, len(seen))
	for slug := range seen {
		out = append(out, slug)
	}
	sort.Strings(out)
	return out
}
