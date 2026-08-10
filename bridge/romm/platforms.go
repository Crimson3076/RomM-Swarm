package romm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
)

// Platform resolution.
//
// RomM's upload API identifies a platform by its own instance-local numeric
// id, not by a portable slug — confirmed against a live RomM 5.0.0 instance,
// where a platform list entry looked like
// {"id": 11, "slug": "gb", "fs_slug": "gb", "name": "Game Boy", ...}. That
// numeric id is assigned by each RomM installation independently and is not
// something a Bridge can know in advance; it has to be looked up.
//
// The lookup is by slug because slug is the stable, human-legible key RomM
// itself uses for a platform across imports and rescans, and it is what this
// project's own PlatformID values are chosen to match for the initial
// platform set (see docs/adr/0004-initial-platforms.md). That match is a
// convention, not a guarantee: if a RomM instance's slug for a platform ever
// diverges from this project's own key, Lookup fails with a clear message
// naming both, rather than silently uploading to the wrong platform.

// Platform is one entry from a RomM server's platform list, reduced to what
// the Bridge needs.
type Platform struct {
	ID   int
	Slug string
	Name string
}

// Platforms indexes a RomM server's platform list by slug.
type Platforms struct {
	bySlug map[string]Platform
	all    []Platform
}

// FetchPlatforms lists every platform a RomM server reports, using the
// already-resolved platforms.list capability path.
//
// It never guesses a path on its own, for the same reason RommSource in
// bridge/scan doesn't: if the probe could not resolve platforms.list, this
// refuses to run rather than falling back to a hard-coded guess.
func FetchPlatforms(ctx context.Context, client *Client, report *Report) (*Platforms, error) {
	cap, err := report.Capability("platforms.list")
	if err != nil {
		return nil, err
	}

	status, body, err := client.Get(ctx, cap.Path, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("romm: %s returned status %d", cap.Path, status)
	}

	objects, err := decodePlatformList(body)
	if err != nil {
		return nil, fmt.Errorf("romm: decoding %s: %w", cap.Path, err)
	}

	p := &Platforms{bySlug: make(map[string]Platform, len(objects))}
	for _, obj := range objects {
		plat := Platform{
			ID:   platformFieldInt(obj, "id"),
			Slug: platformFieldString(obj, "slug", "fs_slug"),
			Name: platformFieldString(obj, "name"),
		}
		if plat.Slug == "" || plat.ID == 0 {
			// A platform entry missing its id or slug cannot be looked up or
			// uploaded to. Skipped rather than failing the whole fetch: one
			// malformed entry (RomM does carry some legacy/placeholder platform
			// rows) must not block resolving every other platform.
			continue
		}
		// A duplicate slug is possible in practice (a probe against a real
		// instance found two "n64" and three "snes" entries, evidently from
		// re-imports under different metadata provider ids). The first one
		// found wins; recording which was discarded keeps that decision visible
		// rather than silent.
		if _, exists := p.bySlug[plat.Slug]; !exists {
			p.bySlug[plat.Slug] = plat
		}
		p.all = append(p.all, plat)
	}
	return p, nil
}

// Lookup finds the RomM-local platform for a Swarm platform key.
func (p *Platforms) Lookup(id string) (Platform, error) {
	plat, ok := p.bySlug[id]
	if !ok {
		known := make([]string, 0, len(p.bySlug))
		for slug := range p.bySlug {
			known = append(known, slug)
		}
		sort.Strings(known)
		return Platform{}, fmt.Errorf(
			"romm: no platform with slug %q was found on this server; known slugs: %s", id, joinLimited(known, 20))
	}
	return plat, nil
}

// All returns every resolved platform, including any duplicate slugs that
// Lookup could not disambiguate.
func (p *Platforms) All() []Platform { return p.all }

func joinLimited(items []string, max int) string {
	if len(items) <= max {
		return fmt.Sprintf("%v", items)
	}
	return fmt.Sprintf("%v… (%d total)", items[:max], len(items))
}

// decodePlatformList parses a platform listing response, tolerant of the same
// bare-array-or-envelope shapes the ROM listing endpoint uses.
func decodePlatformList(body []byte) ([]map[string]any, error) {
	var asArray []map[string]any
	if json.Unmarshal(body, &asArray) == nil {
		return asArray, nil
	}

	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	for _, key := range []string{"items", "results", "data", "platforms"} {
		raw, ok := envelope[key]
		if !ok {
			continue
		}
		list, ok := raw.([]any)
		if !ok {
			continue
		}
		var out []map[string]any
		for _, v := range list {
			if obj, ok := v.(map[string]any); ok {
				out = append(out, obj)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("response is neither an array nor a recognised envelope")
}

func platformFieldString(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := obj[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func platformFieldInt(obj map[string]any, keys ...string) int {
	for _, k := range keys {
		v, ok := obj[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case float64:
			return int(t)
		case string:
			if n, err := strconv.Atoi(t); err == nil {
				return n
			}
		}
	}
	return 0
}
