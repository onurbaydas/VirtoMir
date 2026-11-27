package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/virt"
	"github.com/onurbaydas/virtomir/internal/vmware"
)

// ConnectionStorer holds the connection settings to vCenter or an ESXi host (ADR-0061).
type ConnectionStorer interface {
	Config() vmware.Connection
	SetConfig(c vmware.Connection) error
	Connection() vmware.Connection
}

// InventoryLister reads the VMware inventory over a connection.
type InventoryLister interface {
	List(ctx context.Context, c vmware.Connection) (vmware.Inventory, error)
}

// Preflighter checks conditions before migration starts.
type Preflighter interface {
	Preflight(ctx context.Context, c vmware.Connection, vm vmware.VM, poolFreeBytes uint64) error
}

// VMConverter migrates one VMware VM and defines it in VirtoMir.
type VMConverter interface {
	Convert(ctx context.Context, c vmware.Connection, vm vmware.VM, opts vmware.ConvertOpts, report func(string)) (string, error)
}

// Converter combines preflight checks and VM conversion.
type Converter interface {
	Preflighter
	VMConverter
}

// WaveRunner manages batches of VM conversions executed concurrently.
type WaveRunner interface {
	Start(ctx context.Context, conn vmware.Connection, vmIDs []string, parallel int, conv vmware.OneConverter, report func(string)) (vmware.Wave, error)
	Retry(ctx context.Context, waveID string, conv vmware.OneConverter, report func(string)) (vmware.Wave, error)
	Get(id string) (vmware.Wave, bool)
	List() []vmware.Wave
}

func (s *Server) vmwareRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/vmware/connection", s.getVMwareConnection)
	mux.HandleFunc("PUT /api/v1/vmware/connection", s.putVMwareConnection)
	mux.HandleFunc("DELETE /api/v1/vmware/connection", s.deleteVMwareConnection)
	mux.HandleFunc("POST /api/v1/vmware/connection/test", s.testVMwareConnection)
	mux.HandleFunc("GET /api/v1/vmware/inventory", s.getVMwareInventory)
	mux.HandleFunc("POST /api/v1/vmware/preflight", s.preflightVMware)
	mux.HandleFunc("POST /api/v1/vmware/import", s.importVMware)
	mux.HandleFunc("POST /api/v1/vmware/waves", s.createVMwareWave)
	mux.HandleFunc("GET /api/v1/vmware/waves", s.listVMwareWaves)
	mux.HandleFunc("GET /api/v1/vmware/waves/{id}", s.getVMwareWave)
	mux.HandleFunc("POST /api/v1/vmware/waves/{id}/retry", s.retryVMwareWave)
}

func (s *Server) vmwareStoreAvailable(w http.ResponseWriter) bool {
	if s.vmwareStore == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "VMware migration is not available on this host"})
		return false
	}
	return true
}

func (s *Server) vmwareWavesAvailable(w http.ResponseWriter) bool {
	if s.vmwareWaves == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "VMware wave registry is not available on this host"})
		return false
	}
	return true
}

func (s *Server) vmwareError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, vmware.ErrInvalid):
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
	case errors.Is(err, vmware.ErrRefused):
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		s.writeError(w, err)
	}
}

func (s *Server) findPool(nameOrPath string) (virt.Pool, bool) {
	if s.backend == nil || strings.TrimSpace(nameOrPath) == "" {
		return virt.Pool{}, false
	}
	pools, err := s.backend.ListPools()
	if err != nil {
		return virt.Pool{}, false
	}
	for _, p := range pools {
		if p.Name == nameOrPath || p.Path == nameOrPath {
			return p, true
		}
	}
	return virt.Pool{}, false
}

func (s *Server) getVMwareConnection(w http.ResponseWriter, _ *http.Request) {
	if !s.vmwareStoreAvailable(w) {
		return
	}
	s.writeJSON(w, http.StatusOK, s.vmwareStore.Config())
}

func (s *Server) putVMwareConnection(w http.ResponseWriter, r *http.Request) {
	if !s.vmwareStoreAvailable(w) {
		return
	}
	var c vmware.Connection
	if !s.decode(w, r, &c) {
		return
	}
	if err := s.vmwareStore.SetConfig(c); err != nil {
		s.vmwareError(w, err)
		return
	}
	id := identity(r)
	s.record(activity.Entry{
		User:   id.Name,
		Token:  id.Token,
		Remote: r.RemoteAddr,
		Method: http.MethodPut,
		Path:   "/api/v1/vmware/connection",
		Status: http.StatusOK,
		Error:  fmt.Sprintf("vmware connection saved: %s", c.URL),
	})
	s.writeJSON(w, http.StatusOK, s.vmwareStore.Config())
}

func (s *Server) deleteVMwareConnection(w http.ResponseWriter, r *http.Request) {
	if !s.vmwareStoreAvailable(w) {
		return
	}
	if err := s.vmwareStore.SetConfig(vmware.Connection{}); err != nil {
		s.vmwareError(w, err)
		return
	}
	id := identity(r)
	s.record(activity.Entry{
		User:   id.Name,
		Token:  id.Token,
		Remote: r.RemoteAddr,
		Method: http.MethodDelete,
		Path:   "/api/v1/vmware/connection",
		Status: http.StatusNoContent,
		Error:  "vmware connection deleted",
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testVMwareConnection(w http.ResponseWriter, r *http.Request) {
	if !s.vmwareStoreAvailable(w) {
		return
	}
	id := identity(r)
	var conn vmware.Connection
	if r.Body != nil && r.ContentLength > 0 {
		var body vmware.Connection
		if !s.decode(w, r, &body) {
			return
		}
		conn = body
	}
	stored := s.vmwareStore.Connection()
	if conn.URL == "" {
		conn = stored
	} else if conn.Password == vmware.Secret || conn.Password == "" {
		conn.Password = stored.Password
	}
	if !conn.Configured() {
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "Set up the VMware connection first."})
		return
	}
	if s.vmwareLister == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "VMware lister is not available"})
		return
	}
	_, err := s.vmwareLister.List(r.Context(), conn)
	ok := err == nil
	msg := "Connected successfully"
	if err != nil {
		msg = err.Error()
	}
	if conn.Password != "" {
		msg = strings.ReplaceAll(msg, conn.Password, vmware.Secret)
	}
	s.record(activity.Entry{
		User:   id.Name,
		Token:  id.Token,
		Remote: r.RemoteAddr,
		Method: http.MethodPost,
		Path:   "/api/v1/vmware/connection/test",
		Status: http.StatusOK,
		Error:  fmt.Sprintf("vmware connection tested: ok=%t message=%s", ok, msg),
	})
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "message": msg})
}

func (s *Server) getVMwareInventory(w http.ResponseWriter, r *http.Request) {
	if !s.vmwareStoreAvailable(w) {
		return
	}
	conn := s.vmwareStore.Connection()
	if !conn.Configured() {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "Set up the VMware connection first."})
		return
	}
	if s.vmwareLister == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "VMware inventory lister is not available"})
		return
	}
	inv, err := s.vmwareLister.List(r.Context(), conn)
	if err != nil {
		s.writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, inv)
}

type preflightRequest struct {
	VMIDs []string `json:"vmIds"`
	Pool  string   `json:"pool"`
}

func (s *Server) preflightVMware(w http.ResponseWriter, r *http.Request) {
	if !s.vmwareStoreAvailable(w) {
		return
	}
	var body preflightRequest
	if !s.decode(w, r, &body) {
		return
	}
	if len(body.VMIDs) == 0 {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "no VMs specified for preflight"})
		return
	}
	if strings.TrimSpace(body.Pool) == "" {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "choose the storage pool"})
		return
	}
	pool, ok := s.findPool(body.Pool)
	if !ok {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "storage pool not found: " + body.Pool})
		return
	}
	conn := s.vmwareStore.Connection()
	if !conn.Configured() {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "Set up the VMware connection first."})
		return
	}
	if s.vmwareLister == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "VMware inventory lister is not available"})
		return
	}
	inv, err := s.vmwareLister.List(r.Context(), conn)
	if err != nil {
		s.writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	type vmPreflightResult struct {
		VMID   string   `json:"vmId"`
		Issues []string `json:"issues"`
	}
	issuesMap := make(map[string][]string, len(body.VMIDs))
	var items []vmPreflightResult

	for _, id := range body.VMIDs {
		var target *vmware.VM
		for i := range inv.VMs {
			if inv.VMs[i].ID == id || inv.VMs[i].Name == id {
				target = &inv.VMs[i]
				break
			}
		}
		var curIssues []string
		if target == nil {
			curIssues = []string{fmt.Sprintf("VM %q not found in VMware inventory", id)}
		} else if s.vmwarePreflight != nil {
			if err := s.vmwarePreflight.Preflight(r.Context(), conn, *target, pool.AvailableBytes); err != nil {
				var iss vmware.Issues
				if errors.As(err, &iss) {
					curIssues = []string(iss)
				} else {
					curIssues = []string{err.Error()}
				}
			}
		}
		if curIssues == nil {
			curIssues = []string{}
		}
		issuesMap[id] = curIssues
		items = append(items, vmPreflightResult{VMID: id, Issues: curIssues})
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"pool":          pool.Name,
		"poolPath":      pool.Path,
		"poolFreeBytes": pool.AvailableBytes,
		"issues":        issuesMap,
		"items":         items,
	})
}

type importVMwareRequest struct {
	VMID       string            `json:"vmId"`
	Pool       string            `json:"pool"`
	Name       string            `json:"name"`
	NetworkMap map[string]string `json:"networkMap,omitempty"`
	VirtioISO  string            `json:"virtioIso,omitempty"`
}

func (s *Server) importVMware(w http.ResponseWriter, r *http.Request) {
	if !s.vmwareStoreAvailable(w) {
		return
	}
	var body importVMwareRequest
	if !s.decode(w, r, &body) {
		return
	}
	if body.VMID == "" || strings.TrimSpace(body.Pool) == "" {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "choose the VMware machine and storage pool"})
		return
	}
	pool, ok := s.findPool(body.Pool)
	if !ok {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "storage pool not found: " + body.Pool})
		return
	}
	conn := s.vmwareStore.Connection()
	if !conn.Configured() {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "Set up the VMware connection first."})
		return
	}
	if s.vmwareConverter == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "VMware converter is not available"})
		return
	}
	if body.Name != "" {
		if err := virt.CheckName("Machine", body.Name); err != nil {
			s.writeError(w, err)
			return
		}
	} else {
		body.Name = body.VMID
	}

	id := identity(r)
	poolPath := pool.Path
	poolAvailable := pool.AvailableBytes
	job := s.jobs.Start("vmware-import", "Import "+body.Name, func(ctx context.Context, report func(string)) (string, error) {
		var target vmware.VM
		if s.vmwareLister != nil {
			inv, err := s.vmwareLister.List(ctx, conn)
			if err != nil {
				s.record(activity.Entry{
					User:   id.Name,
					Token:  id.Token,
					Method: "JOB",
					Path:   "/api/v1/vmware/import",
					Status: http.StatusInternalServerError,
					Error:  fmt.Sprintf("vmware import failed: list inventory: %v", err),
				})
				return "", fmt.Errorf("list vmware inventory: %w", err)
			}
			var found bool
			for _, v := range inv.VMs {
				if v.ID == body.VMID || (v.ID == "" && v.Name == body.VMID) || v.Name == body.VMID {
					target = v
					found = true
					break
				}
			}
			if !found {
				err := fmt.Errorf("VM %q not found in VMware inventory", body.VMID)
				s.record(activity.Entry{
					User:   id.Name,
					Token:  id.Token,
					Method: "JOB",
					Path:   "/api/v1/vmware/import",
					Status: http.StatusInternalServerError,
					Error:  fmt.Sprintf("vmware import failed: %v", err),
				})
				return "", err
			}
		} else {
			target = vmware.VM{ID: body.VMID, Name: body.Name}
		}

		if s.vmwarePreflight != nil {
			report("Checking preflight conditions…")
			if err := s.vmwarePreflight.Preflight(ctx, conn, target, poolAvailable); err != nil {
				s.record(activity.Entry{
					User:   id.Name,
					Token:  id.Token,
					Method: "JOB",
					Path:   "/api/v1/vmware/import",
					Status: http.StatusInternalServerError,
					Error:  fmt.Sprintf("vmware import preflight failed: vm=%s pool=%s err=%v", body.VMID, body.Pool, err),
				})
				return "", fmt.Errorf("preflight: %w", err)
			}
		}

		targetName := body.Name
		if targetName == "" {
			targetName = target.Name
		}
		if targetName == "" {
			targetName = body.VMID
		}

		opts := vmware.ConvertOpts{
			PoolPath:   poolPath,
			NetworkMap: body.NetworkMap,
			Name:       targetName,
			VirtioISO:  body.VirtioISO,
		}
		uuid, err := s.vmwareConverter.Convert(ctx, conn, target, opts, report)
		status := http.StatusOK
		errStr := fmt.Sprintf("vmware import finished: vm=%s pool=%s uuid=%s", body.VMID, body.Pool, uuid)
		if err != nil {
			status = http.StatusInternalServerError
			errStr = fmt.Sprintf("vmware import failed: vm=%s pool=%s err=%v", body.VMID, body.Pool, err)
		}
		s.record(activity.Entry{
			User:   id.Name,
			Token:  id.Token,
			Method: "JOB",
			Path:   "/api/v1/vmware/import",
			Status: status,
			Error:  errStr,
		})
		if err != nil {
			return "", err
		}
		if s.live != nil {
			s.live.Refresh()
		}
		return uuid, nil
	})

	s.record(activity.Entry{
		User:   id.Name,
		Token:  id.Token,
		Remote: r.RemoteAddr,
		Method: http.MethodPost,
		Path:   "/api/v1/vmware/import",
		Status: http.StatusAccepted,
		Error:  fmt.Sprintf("vmware import started: vm=%s pool=%s name=%s", body.VMID, body.Pool, body.Name),
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

type oneConverterAdapter struct {
	lister        InventoryLister
	preflight     Preflighter
	converter     VMConverter
	opts          vmware.ConvertOpts
	poolFreeBytes uint64
}

func (a *oneConverterAdapter) ConvertOne(ctx context.Context, conn vmware.Connection, vmID string, report func(string)) error {
	var target vmware.VM
	if a.lister != nil {
		inv, err := a.lister.List(ctx, conn)
		if err != nil {
			return fmt.Errorf("list vmware inventory: %w", err)
		}
		var found bool
		for _, v := range inv.VMs {
			if v.ID == vmID || (v.ID == "" && v.Name == vmID) || v.Name == vmID {
				target = v
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("VM %q not found in VMware inventory", vmID)
		}
	} else {
		target = vmware.VM{ID: vmID, Name: vmID}
	}
	if a.preflight != nil {
		if err := a.preflight.Preflight(ctx, conn, target, a.poolFreeBytes); err != nil {
			return err
		}
	}
	if a.converter != nil {
		opts := a.opts
		opts.Name = target.Name
		if opts.Name == "" {
			opts.Name = vmID
		}
		_, err := a.converter.Convert(ctx, conn, target, opts, report)
		return err
	}
	return nil
}

type createWaveRequest struct {
	VMIDs      []string          `json:"vmIds"`
	Pool       string            `json:"pool"`
	Parallel   int               `json:"parallel,omitempty"`
	NetworkMap map[string]string `json:"networkMap,omitempty"`
	VirtioISO  string            `json:"virtioIso,omitempty"`
}

func (s *Server) createVMwareWave(w http.ResponseWriter, r *http.Request) {
	if !s.vmwareStoreAvailable(w) || !s.vmwareWavesAvailable(w) {
		return
	}
	var body createWaveRequest
	if !s.decode(w, r, &body) {
		return
	}
	if len(body.VMIDs) == 0 {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "no VMs specified for wave migration"})
		return
	}
	if strings.TrimSpace(body.Pool) == "" {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "choose the storage pool"})
		return
	}
	pool, ok := s.findPool(body.Pool)
	if !ok {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "storage pool not found: " + body.Pool})
		return
	}
	conn := s.vmwareStore.Connection()
	if !conn.Configured() {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "Set up the VMware connection first."})
		return
	}

	opts := vmware.ConvertOpts{
		PoolPath:   pool.Path,
		NetworkMap: body.NetworkMap,
		VirtioISO:  body.VirtioISO,
	}
	var conv vmware.OneConverter
	if realConv, ok := s.vmwarePreflight.(*vmware.Converter); ok {
		if realLister, ok := s.vmwareLister.(vmware.Lister); ok {
			wc := vmware.NewWaveConverter(realConv, realLister, opts)
			wc.PoolFreeBytes = pool.AvailableBytes
			conv = wc
		}
	}
	if conv == nil {
		conv = &oneConverterAdapter{
			lister:        s.vmwareLister,
			preflight:     s.vmwarePreflight,
			converter:     s.vmwareConverter,
			opts:          opts,
			poolFreeBytes: pool.AvailableBytes,
		}
	}

	id := identity(r)
	job := s.jobs.Start("vmware-wave", fmt.Sprintf("Migration wave: %d VMs", len(body.VMIDs)), func(ctx context.Context, report func(string)) (string, error) {
		wave, err := s.vmwareWaves.Start(ctx, conn, body.VMIDs, body.Parallel, conv, report)
		var done, failed int
		for _, it := range wave.Items {
			if it.State == vmware.WaveItemDone {
				done++
			} else if it.State == vmware.WaveItemFailed {
				failed++
			}
		}
		status := http.StatusOK
		if err != nil {
			status = http.StatusInternalServerError
		}
		s.record(activity.Entry{
			User:   id.Name,
			Token:  id.Token,
			Method: "JOB",
			Path:   "/api/v1/vmware/waves",
			Status: status,
			Error:  fmt.Sprintf("vmware wave finished: id=%s total=%d done=%d failed=%d", wave.ID, len(wave.Items), done, failed),
		})
		if s.live != nil {
			s.live.Refresh()
		}
		return wave.ID, err
	})

	s.record(activity.Entry{
		User:   id.Name,
		Token:  id.Token,
		Remote: r.RemoteAddr,
		Method: http.MethodPost,
		Path:   "/api/v1/vmware/waves",
		Status: http.StatusAccepted,
		Error:  fmt.Sprintf("vmware wave started: %d VMs pool=%s", len(body.VMIDs), body.Pool),
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) listVMwareWaves(w http.ResponseWriter, _ *http.Request) {
	if !s.vmwareWavesAvailable(w) {
		return
	}
	waves := s.vmwareWaves.List()
	if waves == nil {
		waves = []vmware.Wave{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"waves": waves})
}

func (s *Server) getVMwareWave(w http.ResponseWriter, r *http.Request) {
	if !s.vmwareWavesAvailable(w) {
		return
	}
	waveID := r.PathValue("id")
	wave, ok := s.vmwareWaves.Get(waveID)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "wave not found"})
		return
	}
	s.writeJSON(w, http.StatusOK, wave)
}

type retryWaveRequest struct {
	Pool       string            `json:"pool,omitempty"`
	NetworkMap map[string]string `json:"networkMap,omitempty"`
	VirtioISO  string            `json:"virtioIso,omitempty"`
}

func (s *Server) retryVMwareWave(w http.ResponseWriter, r *http.Request) {
	if !s.vmwareWavesAvailable(w) {
		return
	}
	waveID := r.PathValue("id")
	if _, ok := s.vmwareWaves.Get(waveID); !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "wave not found"})
		return
	}
	var body retryWaveRequest
	if r.Body != nil && r.ContentLength > 0 {
		if !s.decode(w, r, &body) {
			return
		}
	}
	if strings.TrimSpace(body.Pool) == "" {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "choose the storage pool"})
		return
	}
	pool, ok := s.findPool(body.Pool)
	if !ok {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "storage pool not found: " + body.Pool})
		return
	}
	opts := vmware.ConvertOpts{
		PoolPath:   pool.Path,
		NetworkMap: body.NetworkMap,
		VirtioISO:  body.VirtioISO,
	}
	var conv vmware.OneConverter
	if realConv, ok := s.vmwarePreflight.(*vmware.Converter); ok {
		if realLister, ok := s.vmwareLister.(vmware.Lister); ok {
			wc := vmware.NewWaveConverter(realConv, realLister, opts)
			wc.PoolFreeBytes = pool.AvailableBytes
			conv = wc
		}
	}
	if conv == nil {
		conv = &oneConverterAdapter{
			lister:        s.vmwareLister,
			preflight:     s.vmwarePreflight,
			converter:     s.vmwareConverter,
			opts:          opts,
			poolFreeBytes: pool.AvailableBytes,
		}
	}

	id := identity(r)
	job := s.jobs.Start("vmware-wave-retry", "Retry wave "+waveID, func(ctx context.Context, report func(string)) (string, error) {
		wave, err := s.vmwareWaves.Retry(ctx, waveID, conv, report)
		var done, failed int
		for _, it := range wave.Items {
			if it.State == vmware.WaveItemDone {
				done++
			} else if it.State == vmware.WaveItemFailed {
				failed++
			}
		}
		status := http.StatusOK
		if err != nil {
			status = http.StatusInternalServerError
		}
		s.record(activity.Entry{
			User:   id.Name,
			Token:  id.Token,
			Method: "JOB",
			Path:   "/api/v1/vmware/waves/" + waveID + "/retry",
			Status: status,
			Error:  fmt.Sprintf("vmware wave retry finished: id=%s total=%d done=%d failed=%d", wave.ID, len(wave.Items), done, failed),
		})
		if s.live != nil {
			s.live.Refresh()
		}
		return wave.ID, err
	})

	s.record(activity.Entry{
		User:   id.Name,
		Token:  id.Token,
		Remote: r.RemoteAddr,
		Method: http.MethodPost,
		Path:   "/api/v1/vmware/waves/" + waveID + "/retry",
		Status: http.StatusAccepted,
		Error:  fmt.Sprintf("vmware wave retry started: id=%s", waveID),
	})
	s.writeJSON(w, http.StatusAccepted, job)
}
