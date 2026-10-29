package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/onurbaydas/virtomir/internal/ceph"
	"github.com/onurbaydas/virtomir/internal/virt"
	"github.com/onurbaydas/virtomir/internal/wifibridge"
)

// Networks bridged to a Wi-Fi card like VirtualBox and VMware do (wifibridge): the machines
// get their addresses from the LAN's router and show on the LAN.

var uplinkRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,14}$`)

func (s *Server) wifiRoutes(mux *routeMux) {
	mux.HandleFunc("POST /api/v1/networks/wifi", s.wifiNetwork)
}

func (s *Server) wifiBridges() *wifibridge.Manager {
	s.wifiOnce.Do(func() {
		s.wifi = &wifibridge.Manager{Runner: ceph.Exec{}, Log: s.log, File: filepath.Join(s.stateDir, "wifibridges.json")}
	})
	return s.wifi
}

// RunWifiBridges brings back the Wi-Fi bridges after a restart or a reboot, and starts their
// networks (libvirt could not at boot: the bridge was not there yet).
func (s *Server) RunWifiBridges(ctx context.Context) {
	m := s.wifiBridges()
	m.Load(ctx)
	for _, n := range m.Networks() {
		_ = s.backend.NetworkAction(n, "start")
	}
}

// isWifi reports whether a host interface is a Wi-Fi card.
func isWifi(name string) bool {
	for _, p := range []string{"wireless", "phy80211"} {
		if _, err := os.Stat(filepath.Join("/sys/class/net", name, p)); err == nil {
			return true
		}
	}
	return false
}

func (s *Server) wifiNetwork(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string `json:"name"`
		Uplink string `json:"uplink"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if !uplinkRE.MatchString(body.Uplink) || !isWifi(body.Uplink) {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": body.Uplink + " is not a Wi-Fi card of this host."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	m := s.wifiBridges()
	c := wifibridge.Config{Network: body.Name, Bridge: m.FreeBridge(), Uplink: body.Uplink}
	if err := m.Add(ctx, c); err != nil {
		m.Remove(ctx, c.Network)
		s.writeError(w, err)
		return
	}
	if err := s.backend.CreateNetwork(virt.NetworkSpec{Name: body.Name, Mode: "bridge", Bridge: c.Bridge}); err != nil {
		m.Remove(ctx, c.Network)
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]string{"name": body.Name, "bridge": c.Bridge})
}
