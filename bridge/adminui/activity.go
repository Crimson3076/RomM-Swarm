package adminui

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Crimson3076/RomM-Swarm/bridge/ingest"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

type activityRow struct {
	ID        protocol.TransferID       `json:"id"`
	State     protocol.DestinationState `json:"state"`
	Label     string                    `json:"label"`
	Tone      string                    `json:"tone"`
	UpdatedAt string                    `json:"updated_at"`
	Detail    string                    `json:"detail"`
	Running   bool                      `json:"running"`
}

type activityDetail struct {
	ID      protocol.TransferID
	History []ingest.Transition
}

type activityData struct {
	baseData
	Recent []activityRow
	Detail *activityDetail
}

func activityState(state protocol.DestinationState) (string, string) {
	switch state {
	case protocol.StateReceiving:
		return "Receiving", "busy"
	case protocol.StateVerifiedInStaging:
		return "Verified in staging", "busy"
	case protocol.StateUploadingToRomm:
		return "Uploading to RomM", "busy"
	case protocol.StatePublishedToFilesystem:
		return "Published to filesystem", "busy"
	case protocol.StateAwaitingRommIngestion:
		return "Waiting for RomM", "busy"
	case protocol.StateRommMatched:
		return "Matched by RomM", "busy"
	case protocol.StateSourceActive:
		return "Complete", "ok"
	case protocol.StateCancelled:
		return "Cancelled", "muted"
	case protocol.StateVerificationConflict:
		return "Verification conflict", "bad"
	case protocol.StateRommUnmatched:
		return "RomM match needs review", "bad"
	case protocol.StateIngestionTimeout:
		return "RomM indexing timed out", "bad"
	case protocol.StateQuarantined:
		return "Quarantined", "bad"
	default:
		return "Waiting to start", "busy"
	}
}

func (s *Server) activityRows() ([]activityRow, error) {
	ids, err := s.Backend.Journal().Recent(50)
	if err != nil {
		return nil, err
	}
	rows := make([]activityRow, 0, len(ids))
	for _, id := range ids {
		history := s.Backend.Journal().History(id)
		if len(history) == 0 {
			continue
		}
		last := history[len(history)-1]
		label, tone := activityState(last.To)
		rows = append(rows, activityRow{
			ID: id, State: last.To, Label: label, Tone: tone,
			UpdatedAt: last.At.Format(time.RFC3339), Detail: last.Detail,
			Running: !last.To.Terminal(),
		})
	}
	return rows, nil
}

func (s *Server) handleActivityPage(w http.ResponseWriter, r *http.Request) {
	data := activityData{baseData: s.base(r, "Activity")}
	if id := protocol.TransferID(r.URL.Query().Get("id")); id != "" {
		if err := id.Validate(); err != nil {
			http.Error(w, "invalid transfer id", http.StatusBadRequest)
			return
		}
		data.Detail = &activityDetail{ID: id, History: s.Backend.Journal().History(id)}
	} else {
		rows, err := s.activityRows()
		if err != nil {
			data.Error = "listing recent activity: " + err.Error()
		}
		data.Recent = rows
	}
	renderPage(w, "activity", data)
}

// Only journal data is returned. The browser never sees configuration or tokens.
func (s *Server) handleActivityJSON(w http.ResponseWriter, r *http.Request) {
	if id := protocol.TransferID(r.URL.Query().Get("id")); id != "" {
		if err := id.Validate(); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid transfer id")
			return
		}
		history := s.Backend.Journal().History(id)
		if history == nil {
			history = []ingest.Transition{}
		}
		running := len(history) == 0 || !history[len(history)-1].To.Terminal()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": id, "history": history, "running": running})
		return
	}
	rows, err := s.activityRows()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not read activity")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"items": rows})
}
