package adminui

import (
	"net/http"

	"github.com/Crimson3076/RomM-Swarm/bridge/ingest"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

type activityRow struct {
	ID    protocol.TransferID
	State protocol.DestinationState
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

func (s *Server) handleActivityPage(w http.ResponseWriter, r *http.Request) {
	data := activityData{baseData: s.base(r, "Activity")}
	data.AutoRefresh = true

	if id := protocol.TransferID(r.URL.Query().Get("id")); id != "" {
		history := s.Backend.Journal().History(id)
		data.Detail = &activityDetail{ID: id, History: history}
		renderPage(w, "activity", data)
		return
	}

	ids, err := s.Backend.Journal().Recent(50)
	if err != nil {
		data.Error = "listing recent activity: " + err.Error()
		renderPage(w, "activity", data)
		return
	}
	for _, id := range ids {
		state, _ := s.Backend.Journal().Current(id)
		data.Recent = append(data.Recent, activityRow{ID: id, State: state})
	}
	renderPage(w, "activity", data)
}
