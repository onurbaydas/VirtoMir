package api

import (
	"net/http"

	"github.com/onurbaydas/virtomir/internal/alerts"
)

func (s *Server) alertRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/alerts", s.listAlerts)
	mux.HandleFunc("POST /api/v1/alerts/{id}/ack", s.ackAlert)
	mux.HandleFunc("GET /api/v1/alerts/config", s.getAlertConfig)
	mux.HandleFunc("PUT /api/v1/alerts/config", s.putAlertConfig)
	mux.HandleFunc("POST /api/v1/alerts/test", s.testAlertChannel)
}

func (s *Server) noAlerts(w http.ResponseWriter) bool {
	if s.alerts == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "alerts are not enabled"})
		return true
	}
	return false
}

func (s *Server) listAlerts(w http.ResponseWriter, _ *http.Request) {
	if s.alerts == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"alerts": []alerts.Alert{}})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"alerts": s.alerts.List()})
}

func (s *Server) ackAlert(w http.ResponseWriter, r *http.Request) {
	if s.noAlerts(w) {
		return
	}
	if err := s.alerts.Ack(r.PathValue("id"), identity(r).Name); err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getAlertConfig(w http.ResponseWriter, _ *http.Request) {
	if s.noAlerts(w) {
		return
	}
	s.writeJSON(w, http.StatusOK, s.alerts.Config())
}

func (s *Server) putAlertConfig(w http.ResponseWriter, r *http.Request) {
	if s.noAlerts(w) {
		return
	}
	var c alerts.Config
	if !s.decode(w, r, &c) {
		return
	}
	if err := s.alerts.SetConfig(c); err != nil {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, s.alerts.Config())
}

// testAlertChannel sends a test notification through the given (possibly unsaved) channel.
func (s *Server) testAlertChannel(w http.ResponseWriter, r *http.Request) {
	if s.noAlerts(w) {
		return
	}
	var ch alerts.Channel
	if !s.decode(w, r, &ch) {
		return
	}
	if err := s.alerts.Test(ch); err != nil {
		s.writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
