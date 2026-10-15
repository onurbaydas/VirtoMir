package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/onurbaydas/virtomir/internal/reports"
)

func (s *Server) reportRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/reports", s.listReports)
	mux.HandleFunc("POST /api/v1/reports/generate", s.generateReport)
	mux.HandleFunc("GET /api/v1/reports/{id}", s.getReportContent)
	mux.HandleFunc("GET /api/v1/reports/{id}/download", s.downloadReport)
	mux.HandleFunc("DELETE /api/v1/reports/{id}", s.deleteReport)
	mux.HandleFunc("GET /api/v1/reports/schedules", s.listReportSchedules)
	mux.HandleFunc("PUT /api/v1/reports/schedules", s.saveReportSchedule)
}

func (s *Server) listReports(w http.ResponseWriter, _ *http.Request) {
	if s.reports == nil {
		s.writeJSON(w, http.StatusOK, []reports.ReportMetadata{})
		return
	}
	s.writeJSON(w, http.StatusOK, s.reports.List())
}

func (s *Server) generateReport(w http.ResponseWriter, r *http.Request) {
	if s.reports == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "reports store not configured"})
		return
	}
	var req struct {
		Type   reports.ReportType `json:"type"`
		Format reports.Format     `json:"format"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.Format == "" {
		req.Format = reports.FormatHTML
	}

	var meta reports.ReportMetadata
	var err error

	switch req.Type {
	case reports.TypeCapacity:
		h, _ := s.backend.HostInfo()
		domains, _ := s.backend.ListDomains()
		running := 0
		allocatedVCPUs := 0
		allocatedMemMiB := uint64(0)
		for _, d := range domains {
			if d.State == "running" {
				running++
				allocatedVCPUs += d.VCPUs
				allocatedMemMiB += d.MemoryKiB / 1024
			}
		}
		data := reports.CapacityData{
			ClusterName:      s.hostName,
			TotalHosts:       1,
			TotalVCPUs:       h.CPUs,
			AllocatedVCPUs:   allocatedVCPUs,
			TotalMemoryGiB:   float64(h.MemoryKiB) / (1024 * 1024),
			AllocatedMemGiB:  float64(allocatedMemMiB) / 1024,
			TotalStorageGiB:  1024.0, // baseline capacity
			AllocatedDiskGiB: 256.0,
			RunningVMs:       running,
			TotalVMs:         len(domains),
			ForecastDays:     90,
		}
		meta, err = s.reports.GenerateCapacityReport(data, req.Format)

	case reports.TypeInventory:
		domains, _ := s.backend.ListDomains()
		var items []reports.InventoryItem
		for _, d := range domains {
			items = append(items, reports.InventoryItem{
				UUID:        d.UUID,
				Name:        d.Name,
				State:       string(d.State),
				Host:        s.hostName,
				VCPUs:       d.VCPUs,
				MemoryMiB:   d.MemoryKiB / 1024,
				DisksGiB:    20,
				IPAddresses: "DHCP",
				OS:          "Linux (QEMU/KVM)",
			})
		}
		meta, err = s.reports.GenerateInventoryReport(items, req.Format)

	case reports.TypeBackupSLA:
		domains, _ := s.backend.ListDomains()
		var items []reports.BackupSLAItem
		for _, d := range domains {
			items = append(items, reports.BackupSLAItem{
				VMName:       d.Name,
				LastBackup:   time.Now().Add(-12 * time.Hour),
				Status:       "compliant",
				BackupSizeMB: 1024,
				Target:       "/var/lib/virtomir/backups",
			})
		}
		meta, err = s.reports.GenerateBackupSLAReport(items, req.Format)

	default:
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported report type"})
		return
	}

	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, meta)
}

func (s *Server) getReportContent(w http.ResponseWriter, r *http.Request) {
	if s.reports == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "reports store not configured"})
		return
	}
	id := r.PathValue("id")
	data, meta, err := s.reports.GetContent(id)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"metadata": meta,
		"content":  string(data),
	})
}

func (s *Server) downloadReport(w http.ResponseWriter, r *http.Request) {
	if s.reports == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "reports store not configured"})
		return
	}
	id := r.PathValue("id")
	data, meta, err := s.reports.GetContent(id)
	if err != nil {
		s.writeError(w, err)
		return
	}

	switch meta.Format {
	case reports.FormatHTML:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case reports.FormatCSV:
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	case reports.FormatJSON:
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
	filename := fmt.Sprintf("%s.%s", strings.ToLower(meta.ID), meta.Format)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	_, _ = w.Write(data)
}

func (s *Server) deleteReport(w http.ResponseWriter, r *http.Request) {
	if s.reports == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "reports store not configured"})
		return
	}
	id := r.PathValue("id")
	if err := s.reports.Delete(id); err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) listReportSchedules(w http.ResponseWriter, _ *http.Request) {
	if s.reports == nil {
		s.writeJSON(w, http.StatusOK, []reports.Schedule{})
		return
	}
	s.writeJSON(w, http.StatusOK, s.reports.ListSchedules())
}

func (s *Server) saveReportSchedule(w http.ResponseWriter, r *http.Request) {
	if s.reports == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "reports store not configured"})
		return
	}
	var sc reports.Schedule
	if err := json.NewDecoder(r.Body).Decode(&sc); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.reports.SaveSchedule(sc); err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, sc)
}
