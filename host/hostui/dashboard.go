package hostui

import "net/http"

type dashboardData struct {
	baseData
	Swarms []swarmSummary
}

type swarmSummary struct {
	ID         string
	Name       string
	Bridges    int
	Files      int
	Catalogues int
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
		summary := swarmSummary{ID: string(sw.ID), Name: sw.Name}
		bridges, err := s.Directory.ListBridgesForSwarm(r.Context(), sw.ID)
		if err != nil {
			data.Error = "Could not load Bridge counts."
		}
		summary.Bridges = len(bridges)
		totals, _, err := s.Directory.SwarmInventorySummary(r.Context(), sw.ID)
		if err != nil {
			data.Error = "Could not load published inventory counts."
		}
		summary.Files = totals.DistinctFiles
		catalogues, err := s.Directory.ListReferenceCatalogues(r.Context(), userFromContext(r.Context()), sw.ID)
		if err != nil {
			data.Error = "Could not load reference catalogue counts."
		}
		summary.Catalogues = len(catalogues)
		data.Swarms = append(data.Swarms, summary)
	}
	renderPage(w, "dashboard", data)
}
