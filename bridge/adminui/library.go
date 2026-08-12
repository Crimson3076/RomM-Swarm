package adminui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/Crimson3076/RomM-Swarm/bridge/scan"
)

// libraryPageSize bounds both the server-rendered first page and each
// infinite-scroll chunk the browser fetches afterward — the page never
// renders (or sends) RomM's entire inventory in one response, which is
// what made a large library's Library page slow enough to feel broken.
const libraryPageSize = 100

type libraryItem struct {
	ID           string `json:"id"`
	FSName       string `json:"fs_name"`
	PlatformSlug string `json:"platform_slug"`
	SizeHuman    string `json:"size_human"`
}

type libraryData struct {
	baseData
	Platform string
	Items    []libraryItem
	Count    int // filtered items shown so far (this first page)
	Total    int // filtered items matching Platform, across the whole library
	Scanned  int // total RomM records, unfiltered
	HasMore  bool
}

// handleLibraryPage renders the first page of RomM's inventory, filtered
// server-side by platform slug — the same approach cmd/swarm-bridge's list
// subcommand uses, for the same reason (bridge/scan.RommSource.ListROMs's
// own doc comment): RomM's platform_id query parameter is undocumented and
// its filtering behaviour is unconfirmed, so filtering is done here
// instead of trusted to the server.
//
// The underlying listing comes from Backend.Library, which caches —
// without that, every page load (and, before infinite scroll, every one
// of potentially dozens of paginated RomM requests within a single load)
// re-fetched RomM's entire inventory from scratch. Pass refresh=1 to force
// a fresh fetch, for the page's own "Refresh" control.
func (s *Server) handleLibraryPage(w http.ResponseWriter, r *http.Request) {
	data := libraryData{baseData: s.base(r, "Library"), Platform: r.URL.Query().Get("platform")}

	if s.Backend.Connection() == nil {
		data.Error = "not connected to RomM — check Settings"
		renderPage(w, "library", data)
		return
	}

	forceRefresh := r.URL.Query().Get("refresh") == "1"
	records, err := s.Backend.Library(r.Context(), forceRefresh)
	if err != nil {
		data.Error = "listing RomM's inventory: " + err.Error()
		renderPage(w, "library", data)
		return
	}
	data.Scanned = len(records)

	filtered := filterByPlatform(records, data.Platform)
	data.Total = len(filtered)

	page := filtered
	if len(page) > libraryPageSize {
		page = page[:libraryPageSize]
	}
	data.Items = toLibraryItems(page)
	data.Count = len(data.Items)
	data.HasMore = data.Total > len(data.Items)

	renderPage(w, "library", data)
}

// handleLibraryItems serves one infinite-scroll chunk as JSON. Always
// reads from cache (never forces a refresh mid-scroll — a scroll session
// should see one consistent snapshot of the library, not one that shifts
// underneath it page to page); the page's own "Refresh" control is what
// bypasses the cache, via handleLibraryPage's refresh=1.
func (s *Server) handleLibraryItems(w http.ResponseWriter, r *http.Request) {
	if s.Backend.Connection() == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "not connected to RomM")
		return
	}

	platform := r.URL.Query().Get("platform")
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > libraryPageSize {
		limit = libraryPageSize
	}

	records, err := s.Backend.Library(r.Context(), false)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "listing RomM's inventory: "+err.Error())
		return
	}
	filtered := filterByPlatform(records, platform)

	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	var page []scan.ROMRecord
	if offset < len(filtered) {
		page = filtered[offset:end]
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"items":    toLibraryItems(page),
		"has_more": end < len(filtered),
	})
}

func filterByPlatform(records []scan.ROMRecord, platform string) []scan.ROMRecord {
	if platform == "" {
		return records
	}
	out := make([]scan.ROMRecord, 0, len(records))
	for _, rec := range records {
		if rec.PlatformSlug == platform {
			out = append(out, rec)
		}
	}
	return out
}

func toLibraryItems(records []scan.ROMRecord) []libraryItem {
	out := make([]libraryItem, 0, len(records))
	for _, rec := range records {
		out = append(out, libraryItem{
			ID: rec.ID, FSName: rec.FSName, PlatformSlug: rec.PlatformSlug,
			SizeHuman: humanBytes(rec.FSSizeBytes),
		})
	}
	return out
}

// handleLibraryDownload streams one item straight from RomM to the
// browser — nothing is staged on the Bridge for this, since it is a read
// straight back out, not an import.
func (s *Server) handleLibraryDownload(w http.ResponseWriter, r *http.Request) {
	conn := s.Backend.Connection()
	if conn == nil {
		http.Error(w, "not connected to RomM", http.StatusServiceUnavailable)
		return
	}
	id := r.URL.Query().Get("id")
	name := r.URL.Query().Get("name")
	if id == "" || name == "" {
		http.Error(w, "id and name query parameters are required", http.StatusBadRequest)
		return
	}

	source := &scan.RommSource{Client: conn.Client, Report: conn.Report}
	rc, size, err := source.Download(r.Context(), scan.ROMRecord{ID: id, FSName: name})
	if err != nil {
		http.Error(w, "downloading from RomM: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	io.Copy(w, rc)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n2 := n / unit; n2 >= unit; n2 /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
