package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/reports"
	"github.com/onurbaydas/virtomir/internal/schedule"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Reports (ADR-0032): capacity, inventory and backup compliance over every cluster member,
// on demand or on a schedule. Each member answers /api/v1/cluster/peer/reports/facts with
// what it measures itself.
func (s *Server) reportRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/reports", s.listReports)
	mux.HandleFunc("POST /api/v1/reports", s.generateReport)
	mux.HandleFunc("GET /api/v1/reports/{id}", s.downloadReport)
	mux.HandleFunc("DELETE /api/v1/reports/{id}", s.deleteReport)
	mux.HandleFunc("GET /api/v1/report-schedules", s.listReportSchedules)
	mux.HandleFunc("PUT /api/v1/report-schedules", s.putReportSchedule)
	mux.HandleFunc("DELETE /api/v1/report-schedules/{id}", s.deleteReportSchedule)
	mux.HandleFunc("GET /api/v1/cluster/peer/reports/facts", s.peerReportFacts)
}

func (s *Server) noReports(w http.ResponseWriter) bool {
	if s.reports == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "reports are not available on this host"})
		return true
	}
	return false
}

// localFacts is what this host measures for reports.
func (s *Server) localFacts() (reports.HostFacts, error) {
	h, err := s.backend.HostInfo()
	if err != nil {
		return reports.HostFacts{}, err
	}
	f := reports.HostFacts{Name: s.hostName, CPUs: h.CPUs, MemoryKiB: h.MemoryKiB,
		Machines: []reports.Machine{}, Pools: []reports.Pool{}, Backups: []reports.Backup{}, Schedules: []reports.BackupSchedule{}}
	if f.Name == "" {
		f.Name = h.Hostname
	}
	doms, err := s.backend.ListDomains()
	if err != nil {
		return f, err
	}
	for _, d := range doms {
		m := reports.Machine{UUID: d.UUID, Name: d.Name, State: string(d.State), Owner: d.Owner, VCPUs: d.VCPUs,
			MemoryMiB: d.MaxMemoryKiB >> 10, Autostart: d.Autostart, Template: d.Template}
		if det, err := s.backend.DomainDetail(d.UUID); err == nil {
			for _, dev := range det.Devices {
				if dev.Kind == "disk" {
					m.Disks++
					m.DiskBytes += dev.CapacityBytes
				}
				m.Addresses = append(m.Addresses, dev.Addresses...)
			}
		}
		f.Machines = append(f.Machines, m)
	}
	if pools, err := s.backend.ListPools(); err == nil {
		for _, p := range pools {
			f.Pools = append(f.Pools, reports.Pool{Name: p.Name, Type: p.Type, Path: p.Path, Active: p.Active,
				CapacityBytes: p.CapacityBytes, AllocationBytes: p.AllocationBytes, AvailableBytes: p.AvailableBytes})
		}
	}
	if s.schedules != nil {
		if b, ok := s.backend.(Backupper); ok {
			if list, err := b.ListBackups(virt.BackupDir(s.schedules.Settings().BackupDir), ""); err == nil {
				for _, bk := range list {
					f.Backups = append(f.Backups, reports.Backup{DomainUUID: strings.ToLower(bk.DomainUUID), Created: bk.Created, SizeBytes: bk.SizeBytes})
				}
			}
		}
		for _, sc := range s.schedules.List("") {
			if sc.Kind != schedule.Backups || !sc.Enabled {
				continue
			}
			iv := 24 * time.Hour
			switch sc.Every {
			case "hours":
				iv = time.Duration(max(sc.Hours, 1)) * time.Hour
			case "weekly":
				iv = 7 * 24 * time.Hour
			}
			f.Schedules = append(f.Schedules, reports.BackupSchedule{DomainUUID: strings.ToLower(sc.Domain), Interval: iv})
		}
	}
	return f, nil
}

// clusterFacts asks every member (in parallel) and this host for their facts.
func (s *Server) clusterFacts(ctx context.Context) (reports.Facts, error) {
	local, err := s.localFacts()
	if err != nil {
		return reports.Facts{}, err
	}
	out := reports.Facts{Hosts: []reports.HostFacts{local}}
	if s.cluster == nil || !s.cluster.Clustered() {
		return out, nil
	}
	members, _ := s.cluster.Members()
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, n := range members {
		if n.ID == s.cluster.SelfID() {
			continue
		}
		wg.Add(1)
		go func(n cluster.Node) {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			var hf reports.HostFacts
			err := s.peer(n).Call(c, http.MethodGet, "/api/v1/cluster/peer/reports/facts", nil, &hf)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out.Missing = append(out.Missing, n.Name)
				return
			}
			out.Hosts = append(out.Hosts, hf)
		}(n)
	}
	wg.Wait()
	return out, nil
}

func (s *Server) peerReportFacts(w http.ResponseWriter, r *http.Request) {
	if s.notPeer(w, r) {
		return
	}
	f, err := s.localFacts()
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, f)
}

func (s *Server) listReports(w http.ResponseWriter, _ *http.Request) {
	if s.reports == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"reports": []reports.Meta{}})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"reports": s.reports.List()})
}

// generateReport makes a report now: {"kind": "capacity", "format": "html"}.
func (s *Server) generateReport(w http.ResponseWriter, r *http.Request) {
	if s.noReports(w) {
		return
	}
	var b struct {
		Kind   reports.Kind   `json:"kind"`
		Format reports.Format `json:"format"`
	}
	if !s.decode(w, r, &b) {
		return
	}
	if b.Format == "" {
		b.Format = reports.HTML
	}
	f, err := s.clusterFacts(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	m, err := s.reports.Generate(b.Kind, b.Format, f, identity(r).Name, "")
	if err != nil {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusCreated, m)
}

// downloadReport serves a report; ?download=1 as an attachment, otherwise to view.
func (s *Server) downloadReport(w http.ResponseWriter, r *http.Request) {
	if s.noReports(w) {
		return
	}
	m, body, err := s.reports.Get(r.PathValue("id"))
	if err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such report"})
		return
	}
	w.Header().Set("Content-Type", map[reports.Format]string{
		reports.HTML: "text/html; charset=utf-8", reports.CSV: "text/csv; charset=utf-8", reports.JSON: "application/json",
	}[m.Format])
	// Reports are our own HTML, but keep them from running anything if a name slipped through.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	disp := "inline"
	if r.URL.Query().Get("download") != "" {
		disp = "attachment"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disp, m.ID+"."+string(m.Format)))
	_, _ = w.Write(body)
}

func (s *Server) deleteReport(w http.ResponseWriter, r *http.Request) {
	if s.noReports(w) {
		return
	}
	if err := s.reports.Delete(r.PathValue("id")); err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such report"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listReportSchedules(w http.ResponseWriter, _ *http.Request) {
	if s.reports == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"schedules": []reports.Schedule{}})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"schedules": s.reports.Schedules()})
}

func (s *Server) putReportSchedule(w http.ResponseWriter, r *http.Request) {
	if s.noReports(w) {
		return
	}
	var sc reports.Schedule
	if !s.decode(w, r, &sc) {
		return
	}
	out, err := s.reports.PutSchedule(sc)
	switch {
	case errors.Is(err, os.ErrNotExist):
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such schedule"})
	case err != nil:
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
	default:
		s.writeJSON(w, http.StatusOK, out)
	}
}

func (s *Server) deleteReportSchedule(w http.ResponseWriter, r *http.Request) {
	if s.noReports(w) {
		return
	}
	if err := s.reports.DeleteSchedule(r.PathValue("id")); err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such schedule"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RunReportSchedules makes this host's scheduled reports until ctx ends. Schedules are kept
// per host, but every report covers the whole cluster, so one host's schedules are enough.
func (s *Server) RunReportSchedules(ctx context.Context) {
	if s.reports == nil {
		return
	}
	since := time.Now()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, sc := range s.reports.Due(since) {
			f, err := s.clusterFacts(ctx)
			var m reports.Meta
			if err == nil {
				m, err = s.reports.Generate(sc.Kind, sc.Format, f, "schedule: "+sc.Name, sc.ID)
			}
			s.reports.Ran(sc.ID, err)
			if err != nil {
				s.log.Warn("scheduled report", "schedule", sc.Name, "err", err)
				if sc.Notify && s.alerts != nil {
					s.alerts.Notice("Report "+sc.Name+" failed", err.Error())
				}
				continue
			}
			s.log.Info("scheduled report", "schedule", sc.Name, "report", m.ID)
			if sc.Notify && s.alerts != nil {
				s.alerts.Notice(m.Title, m.Summary+" — open Reports in VirtoMir to read it.")
			}
		}
	}
}
