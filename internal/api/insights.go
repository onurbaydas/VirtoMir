package api

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/onurbaydas/virtomir/internal/insights"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Operations intelligence (ADR-0066): capacity forecasts, right-sizing and anomalies, all
// computed on request from this host's metric history. Other cluster members answer for
// themselves through /api/v1/hosts/<id>/insights.

func (s *Server) insightsRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/insights", s.getInsights)
	mux.HandleFunc("GET /api/v1/insights/vms/{uuid}", s.getMachineInsights)
}

type poolInsight struct {
	Name            string            `json:"name"`
	CapacityBytes   uint64            `json:"capacityBytes"`
	AllocationBytes uint64            `json:"allocationBytes"`
	Forecast        insights.Forecast `json:"forecast"`
}

type machineInsight struct {
	insights.Recommendation
	Name  string `json:"name"`
	State string `json:"state"`
}

func (s *Server) noInsights(w http.ResponseWriter) bool {
	if s.insights == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "insights are not available"})
		return true
	}
	return false
}

// getInsights: host CPU/memory forecasts, pool forecasts and a recommendation per machine.
func (s *Server) getInsights(w http.ResponseWriter, _ *http.Request) {
	if s.noInsights(w) {
		return
	}
	now := time.Now()
	cpu, mem, err := s.insights.HostForecast(now)
	if err != nil {
		s.writeError(w, err)
		return
	}
	out := struct {
		Host     map[string]insights.Forecast `json:"host"`
		Pools    []poolInsight                `json:"pools"`
		Machines []machineInsight             `json:"machines"`
	}{Host: map[string]insights.Forecast{"cpu": cpu, "mem": mem}, Pools: []poolInsight{}, Machines: []machineInsight{}}

	if pools, err := s.backend.ListPools(); err == nil {
		for _, p := range pools {
			if !p.Active || p.CapacityBytes == 0 {
				continue
			}
			f, _ := s.insights.PoolForecast(insights.Pool{Name: p.Name, CapacityBytes: p.CapacityBytes}, now)
			out.Pools = append(out.Pools, poolInsight{Name: p.Name, CapacityBytes: p.CapacityBytes, AllocationBytes: p.AllocationBytes, Forecast: f})
		}
	}
	doms, err := s.backend.ListDomains()
	if err != nil {
		s.writeError(w, err)
		return
	}
	for _, d := range doms {
		m := insights.Machine{UUID: d.UUID, Name: d.Name, VCPUs: d.VCPUs, MemMiB: d.MaxMemoryKiB >> 10}
		out.Machines = append(out.Machines, machineInsight{Recommendation: s.insights.VMRightSize(m, now), Name: d.Name, State: string(d.State)})
	}
	slices.SortFunc(out.Machines, func(a, b machineInsight) int { return strings.Compare(a.Name, b.Name) })
	s.writeJSON(w, http.StatusOK, out)
}

// getMachineInsights: one machine's right-sizing and the anomalies of the last day.
func (s *Server) getMachineInsights(w http.ResponseWriter, r *http.Request) {
	if s.noInsights(w) {
		return
	}
	uuid := r.PathValue("uuid")
	doms, err := s.backend.ListDomains()
	if err != nil {
		s.writeError(w, err)
		return
	}
	i := slices.IndexFunc(doms, func(d virt.Domain) bool { return strings.EqualFold(d.UUID, uuid) })
	if i < 0 {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such machine"})
		return
	}
	d := doms[i]
	now := time.Now()
	m := insights.Machine{UUID: d.UUID, Name: d.Name, VCPUs: d.VCPUs, MemMiB: d.MaxMemoryKiB >> 10}
	anomalies := []insights.Anomaly{}
	for _, list := range s.insights.VMDetect(m, now) {
		anomalies = append(anomalies, list...)
	}
	slices.SortFunc(anomalies, func(a, b insights.Anomaly) int { return b.Time.Compare(a.Time) })
	s.writeJSON(w, http.StatusOK, map[string]any{"rightSize": s.insights.VMRightSize(m, now), "anomalies": anomalies})
}
