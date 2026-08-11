package adminui

import "net/http"

// handleHealthz is deliberately unauthenticated and tells nothing about
// configuration state — just that the process is up and able to serve HTTP.
// This is what cmd/bridge's -healthcheck mode and Docker's HEALTHCHECK call.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}
