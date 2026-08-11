package adminui

import (
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/Crimson3076/RomM-Swarm/bridge/scan"
)

type libraryItem struct {
	ID           string
	FSName       string
	PlatformSlug string
	SizeHuman    string
}

type libraryData struct {
	baseData
	Platform string
	Items    []libraryItem
	Count    int
	Scanned  int
}

// handleLibraryPage lists RomM's own inventory, filtered client-side by
// platform slug — the same approach cmd/swarm-bridge's list subcommand
// uses, for the same reason (bridge/scan.RommSource.ListROMs's own doc
// comment): RomM's platform_id query parameter is undocumented and its
// filtering behaviour is unconfirmed, so filtering is done here instead of
// trusted to the server.
func (s *Server) handleLibraryPage(w http.ResponseWriter, r *http.Request) {
	data := libraryData{baseData: s.base(r, "Library"), Platform: r.URL.Query().Get("platform")}

	conn := s.Backend.Connection()
	if conn == nil {
		data.Error = "not connected to RomM — check Settings"
		renderPage(w, "library", data)
		return
	}

	source := &scan.RommSource{Client: conn.Client, Report: conn.Report}
	offset := 0
	for {
		page, hasMore, err := source.ListROMs(r.Context(), scan.Page{Limit: 100, Offset: offset})
		if err != nil {
			data.Error = "listing RomM's inventory: " + err.Error()
			renderPage(w, "library", data)
			return
		}
		for _, rec := range page {
			data.Scanned++
			if data.Platform != "" && rec.PlatformSlug != data.Platform {
				continue
			}
			data.Items = append(data.Items, libraryItem{
				ID: rec.ID, FSName: rec.FSName, PlatformSlug: rec.PlatformSlug,
				SizeHuman: humanBytes(rec.FSSizeBytes),
			})
		}
		if !hasMore || len(page) == 0 {
			break
		}
		offset += len(page)
		if offset > 50_000 {
			// A hard safety cap: an unbounded pagination loop must never
			// hang a request forever, however large a real library gets.
			break
		}
	}
	data.Count = len(data.Items)
	renderPage(w, "library", data)
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
