package api

import (
	"net/http"

	"github.com/onurbaydas/virtomir/internal/virt"
)

// BandwidthSetter limits a network adapter's traffic.
type BandwidthSetter interface {
	SetInterfaceBandwidth(uuid, mac string, bw virt.NICBandwidth) error
}

func (s *Server) setInterfaceBandwidth(w http.ResponseWriter, r *http.Request) {
	b, ok := s.backend.(BandwidthSetter)
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "adapter limits are not available"})
		return
	}
	var bw virt.NICBandwidth
	if !s.decode(w, r, &bw) {
		return
	}
	if err := b.SetInterfaceBandwidth(r.PathValue("uuid"), r.PathValue("mac"), bw); err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusNoContent, nil)
}
