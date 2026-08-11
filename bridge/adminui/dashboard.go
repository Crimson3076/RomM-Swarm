package adminui

import "net/http"

type dashboardData struct {
	baseData
	Connected     bool
	RommURL       string
	ServerVersion string
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.Backend.ConfigStore().Load()
	data := dashboardData{baseData: s.base(r, "Dashboard"), RommURL: cfg.RommURL}

	if conn := s.Backend.Connection(); conn != nil {
		data.Connected = true
		data.ServerVersion = conn.Report.ServerVersion
	}

	renderPage(w, "dashboard", data)
}
