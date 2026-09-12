package adminui

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
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
	Name         string `json:"name"`
	FSName       string `json:"fs_name"`
	PlatformSlug string `json:"platform_slug"`
	SizeHuman    string `json:"size_human"`
}

type libraryData struct {
	baseData
	Platform string
	Query    string
	Sort     string
}

// handleLibraryPage renders the page shell only — no items, and no call to
// Backend.Library, which is why this returns instantly even on a cold
// cache. It used to call Backend.Library synchronously before rendering
// anything at all, so a large library (or a cold cache right after a
// restart) left the browser showing nothing whatsoever until that fetch
// finished, indistinguishable from the page being frozen. static/library.js
// fetches the first chunk itself on load, from handleLibraryItems below,
// and shows a loading message until that arrives — the same information,
// just visible instead of silent.
func (s *Server) handleLibraryPage(w http.ResponseWriter, r *http.Request) {
	data := libraryData{baseData: s.base(r, "Library"), Platform: r.URL.Query().Get("platform"), Query: r.URL.Query().Get("q"), Sort: r.URL.Query().Get("sort")}

	if s.Backend.Connection() == nil {
		data.Error = "not connected to RomM — check Settings"
	}

	renderPage(w, "library", data)
}

// handleLibraryItems serves one infinite-scroll chunk as JSON — including
// the very first chunk the page loads with, now that handleLibraryPage no
// longer fetches anything itself. refresh=1 forces a fresh RomM listing
// rather than serving Backend.Library's cache, for the page's "Refresh"
// control; every other request (every scroll-triggered chunk after the
// first) always reads from cache, so a scroll session sees one consistent
// snapshot of the library rather than one that shifts underneath it page
// to page.
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
	forceRefresh := r.URL.Query().Get("refresh") == "1"

	records, err := s.Backend.Library(r.Context(), forceRefresh)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "listing RomM's inventory: "+err.Error())
		return
	}
	filtered := searchLibrary(records, platform, r.URL.Query().Get("q"), r.URL.Query().Get("sort"))

	if offset > len(filtered) {
		offset = len(filtered)
	}
	end := offset + min(limit, len(filtered)-offset)
	var page []scan.ROMRecord
	if offset < len(filtered) {
		page = filtered[offset:end]
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"items":     toLibraryItems(page),
		"has_more":  end < len(filtered),
		"total":     len(filtered),
		"scanned":   len(records),
		"platforms": libraryPlatforms(records),
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
			ID: rec.ID, Name: rec.Name, FSName: rec.FSName, PlatformSlug: rec.PlatformSlug,
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

	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
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
