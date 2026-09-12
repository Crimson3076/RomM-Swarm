package adminui

import "net/http"

type dashboardData struct {
	baseData
	Connected     bool
	RommURL       string
	ServerVersion string
	DisplayName   string
	Swarm         SwarmStatus
	Publish       PublishStatus
	Recent        []activityRow
	Active        int
	NeedsReview   int
	Completed     int
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.Backend.ConfigStore().Load()
	data := dashboardData{baseData: s.base(r, "Dashboard"), RommURL: cfg.RommURL, DisplayName: cfg.DisplayName}
	if err != nil {
		data.Error = "Could not read configuration: " + err.Error()
	}
	if conn := s.Backend.Connection(); conn != nil {
		data.Connected = true
		if conn.Report != nil {
			data.ServerVersion = conn.Report.ServerVersion
		}
	}
	data.Swarm, err = s.Backend.SwarmStatus()
	if err != nil {
		data.Error = "Could not read Swarm connection: " + err.Error()
	}
	data.Publish = s.Backend.PublishStatus()
	rows, err := s.activityRows()
	if err != nil {
		data.Error = "Could not read activity: " + err.Error()
	}
	for _, row := range rows {
		switch {
		case row.Running:
			data.Active++
		case row.State.NeedsReview():
			data.NeedsReview++
		case row.State.IsSource():
			data.Completed++
		}
	}
	if len(rows) > 5 {
		rows = rows[:5]
	}
	data.Recent = rows
	renderPage(w, "dashboard", data)
}
