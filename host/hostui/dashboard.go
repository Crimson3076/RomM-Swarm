package hostui

import "net/http"

type dashboardData struct {
	baseData
	Swarms []swarmSummary
}

type swarmSummary struct {
	ID   string
	Name string
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	data := dashboardData{baseData: s.base(r, "Dashboard")}

	swarms, err := s.Directory.ListSwarms(r.Context(), userFromContext(r.Context()))
	if err != nil {
		data.Error = "listing Swarms: " + err.Error()
		renderPage(w, "dashboard", data)
		return
	}
	for _, sw := range swarms {
		data.Swarms = append(data.Swarms, swarmSummary{ID: string(sw.ID), Name: sw.Name})
	}
	renderPage(w, "dashboard", data)
}
