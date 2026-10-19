package api

import (
	"context"
	"net/http"
)

// SystemUpgrader upgrades this host's OS packages (*systemd.Upgrader).
type SystemUpgrader interface {
	Upgrade(ctx context.Context, reboot bool) (string, error)
}

func (s *Server) systemRoutes(mux *routeMux) {
	mux.HandleFunc("POST /api/v1/system/upgrade", s.systemUpgrade)
}

// systemUpgrade upgrades the host's packages (apt or dnf) and, if asked, reboots it after.
func (s *Server) systemUpgrade(w http.ResponseWriter, r *http.Request) {
	if s.upgrader == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "OS upgrades are not available on this host"})
		return
	}
	var b struct {
		Reboot bool `json:"reboot"`
	}
	if !s.decodeOptional(w, r, &b) {
		return
	}
	title := "Upgrade the host's packages"
	if b.Reboot {
		title += " and reboot"
	}
	job := s.jobs.Start("system-upgrade", title, func(ctx context.Context, report func(string)) (string, error) {
		report("Upgrading packages…")
		return s.upgrader.Upgrade(ctx, b.Reboot)
	})
	s.writeJSON(w, http.StatusAccepted, job)
}
