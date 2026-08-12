package adminui

import (
	"context"
	"encoding/json"
	"fmt"
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
	// recent successful inventory publish (ADR 0019), from the durable
	// record in swarmconn.Config — survives a restart, unlike the fields
	// below.
	LastPublishedRevision uint64
	LastPublishedAt       string

	// Publish* fields are the live PublishStatus (ADR 0020's follow-on):
	// what's happening right now, or what just finished, in this running
	// process. Rendered server-side on every load so a reload shows
	// current state immediately; static/swarm.js polls
	// /api/swarm/publish-status to keep it live without a reload while a
	// publish is in progress.
	PublishPhase         string
	PublishRunning       bool
	PublishPlatform      string
	PublishPlatformIndex int
	PublishPlatformTotal int
	PublishItemsScanned  int
	PublishStartedAt     string
	PublishFinishedAt    string
	PublishSummary       string
	PublishError         string
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

	applyPublishStatus(&data, s.Backend.PublishStatus())
	return data
}

// applyPublishStatus fills in swarmPageData's live-status fields from a
// PublishStatus snapshot — shared by the page render and, via
// publishStatusJSON's parallel shape, the polling endpoint, so the two
// never drift apart.
func applyPublishStatus(data *swarmPageData, status PublishStatus) {
	data.PublishPhase = string(status.Phase)
	data.PublishRunning = status.Running()
	data.PublishPlatform = string(status.Platform)
	data.PublishPlatformIndex = status.PlatformIndex
	data.PublishPlatformTotal = status.PlatformTotal
	data.PublishItemsScanned = status.ItemsScanned
	if !status.StartedAt.IsZero() {
		data.PublishStartedAt = status.StartedAt.Format(time.RFC3339)
	}
	if !status.FinishedAt.IsZero() {
		data.PublishFinishedAt = status.FinishedAt.Format(time.RFC3339)
	}
	if status.Phase == PublishPhaseError {
		data.PublishError = status.Err
	} else if status.Phase == PublishPhaseDone {
		data.PublishSummary = publishResultSummary(status.Result)
	}
}

func publishResultSummary(result InventoryPublishResult) string {
	if !result.Published {
		if len(result.SkipReasons) == 0 {
			return "Nothing published — no eligible holdings found"
		}
		reasons := make([]string, 0, len(result.SkipReasons))
		for reason, count := range result.SkipReasons {
			reasons = append(reasons, fmt.Sprintf("%s (%d)", reason, count))
		}
		return "Nothing published — " + strings.Join(reasons, ", ")
	}
	return fmt.Sprintf("Published revision %d: %d item(s), %d distinct file(s) swarm-wide",
		result.Revision, result.ItemCount, result.DistinctFiles)
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

// handlePublishInventory starts a publish in the background (via
// TriggerPublishInventory, which handles "one already running" itself)
// and returns the resulting status immediately — never blocks on a
// publish actually finishing, since that can legitimately take minutes
// and this page's "Publish Inventory" button needs to answer right away
// so its status block (and static/swarm.js's polling) can start
// reflecting live progress instead of leaving the operator staring at a
// spinner with no way to tell a slow scan from a hang.
func (s *Server) handlePublishInventory(w http.ResponseWriter, r *http.Request) {
	writePublishStatusJSON(w, s.Backend.TriggerPublishInventory())
}

// handlePublishStatus serves the live PublishStatus as JSON, for
// static/swarm.js to poll while a publish is in progress and for a page
// reload to pick up current state without resubmitting anything.
func (s *Server) handlePublishStatus(w http.ResponseWriter, r *http.Request) {
	writePublishStatusJSON(w, s.Backend.PublishStatus())
}

func writePublishStatusJSON(w http.ResponseWriter, status PublishStatus) {
	body := map[string]any{
		"phase":          string(status.Phase),
		"running":        status.Running(),
		"platform":       string(status.Platform),
		"platform_index": status.PlatformIndex,
		"platform_total": status.PlatformTotal,
		"items_scanned":  status.ItemsScanned,
	}
	if !status.StartedAt.IsZero() {
		body["started_at"] = status.StartedAt
	}
	if !status.FinishedAt.IsZero() {
		body["finished_at"] = status.FinishedAt
	}
	if status.Phase == PublishPhaseError {
		body["error"] = status.Err
	}
	if status.Phase == PublishPhaseDone {
		body["result"] = map[string]any{
			"published":      status.Result.Published,
			"item_count":     status.Result.ItemCount,
			"skipped_count":  status.Result.SkippedCount,
			"distinct_files": status.Result.DistinctFiles,
			"revision":       uint64(status.Result.Revision),
			"skip_reasons":   status.Result.SkipReasons,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(body)
}
