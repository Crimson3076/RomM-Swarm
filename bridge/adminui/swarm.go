package adminui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type swarmPageData struct {
	baseData
	Joined      bool
	HostURL     string
	BridgeID    string
	Generation  uint64
	LastRotated string

	// LastPublishedRevision and LastPublishedAt report this Bridge's most
	// recent successful inventory publish (ADR 0019). LastPublishedRevision
	// is zero when nothing has ever been published.
	LastPublishedRevision uint64
	LastPublishedAt       string
}

func (s *Server) swarmPageDataFrom(r *http.Request) swarmPageData {
	data := swarmPageData{baseData: s.base(r, "Swarm")}
	status, err := s.Backend.SwarmStatus()
	if err != nil {
		data.Error = "could not load Swarm status: " + err.Error()
		return data
	}
	data.Joined = status.Joined
	data.HostURL = status.HostURL
	data.BridgeID = string(status.BridgeID)
	data.Generation = status.Generation
	if !status.LastRotated.IsZero() {
		data.LastRotated = status.LastRotated.Format(time.RFC3339)
	}
	data.LastPublishedRevision = uint64(status.LastPublishedRevision)
	if !status.LastPublishedAt.IsZero() {
		data.LastPublishedAt = status.LastPublishedAt.Format(time.RFC3339)
	}
	return data
}

func (s *Server) handleSwarmPage(w http.ResponseWriter, r *http.Request) {
	renderPage(w, "swarm", s.swarmPageDataFrom(r))
}

func (s *Server) handleSwarmJoin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.swarmError(w, r, "could not read the submitted form")
		return
	}
	hostURL := strings.TrimSpace(r.FormValue("host_url"))
	code := strings.TrimSpace(r.FormValue("invitation_code"))
	if hostURL == "" || code == "" {
		s.swarmError(w, r, "a Host URL and invitation code are both required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if _, err := s.Backend.JoinSwarm(ctx, hostURL, code); err != nil {
		s.swarmError(w, r, "could not join the Swarm: "+err.Error())
		return
	}

	data := s.swarmPageDataFrom(r)
	data.Notice = "Joined the Swarm."
	renderPage(w, "swarm", data)
}

func (s *Server) swarmError(w http.ResponseWriter, r *http.Request, msg string) {
	data := s.swarmPageDataFrom(r)
	data.Error = msg
	w.WriteHeader(http.StatusUnprocessableEntity)
	renderPage(w, "swarm", data)
}

// handleSwarmTest wraps TestSwarmConnection for the page's "Test Swarm
// Connection" button, mirroring handleConnectionTest's JSON-response shape
// for the equivalent RomM-side check.
func (s *Server) handleSwarmTest(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	result, err := s.Backend.TestSwarmConnection(ctx)
	if err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"connected":  true,
		"outcome":    string(result.Outcome),
		"generation": result.Generation,
	})
}

// publishInventoryTimeout is generous relative to handleSwarmTest's default
// request handling — a full-catalogue scan-and-publish can take far longer
// than a credential rotation, since it downloads and hashes every holding.
const publishInventoryTimeout = 10 * time.Minute

// handlePublishInventory wraps PublishInventory for the page's "Publish
// Inventory" button, mirroring handleSwarmTest's JSON-response shape.
func (s *Server) handlePublishInventory(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), publishInventoryTimeout)
	defer cancel()
	result, err := s.Backend.PublishInventory(ctx)
	if err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"published":      result.Published,
		"item_count":     result.ItemCount,
		"skipped_count":  result.SkippedCount,
		"distinct_files": result.DistinctFiles,
		"revision":       uint64(result.Revision),
		"skip_reasons":   result.SkipReasons,
	})
}
