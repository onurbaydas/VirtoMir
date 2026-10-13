package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/digitalocean/go-libvirt"

	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Migrator is the live-migration part of the backend (implemented by *virt.Client).
type Migrator interface {
	PlanMigration(uuid string) (virt.MigrationSource, error)
	CheckPaths(paths []string) ([]virt.PathCheck, error)
	MigrateBegin(uuid string, flags libvirt.DomainMigrateFlags, disks []string) ([]byte, string, error)
	MigratePrepare(name, xml, uri string, cookie []byte, flags libvirt.DomainMigrateFlags, disks []string) ([]byte, string, error)
	MigratePerform(ctx context.Context, uuid, uri string, cookie []byte, flags libvirt.DomainMigrateFlags, disks []string, progress func(string)) ([]byte, error)
	MigrateFinish(name, uri string, cookie []byte, flags libvirt.DomainMigrateFlags, disks []string, cancelled bool) ([]byte, error)
	MigrateConfirm(uuid string, cookie []byte, flags libvirt.DomainMigrateFlags, disks []string, cancelled bool) error
	RemoveImage(path string) error
	HostCPU() (virt.HostCPU, error)
}

// Target side of a live migration: only another member may call these, for an admin.
func (s *Server) migrateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/migrate/check", s.migrateCheck)
	mux.HandleFunc("POST /api/v1/migrate/prepare", s.migratePrepare)
	mux.HandleFunc("POST /api/v1/migrate/finish", s.migrateFinish)
	mux.HandleFunc("GET /api/v1/host/cpu", s.hostCPU)
	mux.HandleFunc("GET /api/v1/cluster/cpu-baseline", s.cpuBaseline)
}

func (s *Server) hostCPU(w http.ResponseWriter, r *http.Request) {
	m, ok := s.migrator(w, r, false)
	if !ok {
		return
	}
	h, err := m.HostCPU()
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, h)
}

// cpuBaseline names a CPU model every cluster member supports, so machines using it can
// be live-migrated anywhere in the cluster.
func (s *Server) cpuBaseline(w http.ResponseWriter, r *http.Request) {
	m, ok := s.migrator(w, r, false)
	if !ok {
		return
	}
	b, ok := s.backend.(interface {
		BaselineCPU([]virt.HostCPU) (string, error)
	})
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "not available"})
		return
	}
	here, err := m.HostCPU()
	if err != nil {
		s.writeError(w, err)
		return
	}
	cpus := []virt.HostCPU{here}
	if s.cluster != nil {
		members, _ := s.cluster.Members()
		for _, n := range members {
			if n.ID == s.cluster.SelfID() {
				continue
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			var c virt.HostCPU
			id := identity(r)
			err := s.peer(n).As(id.Name, id.Role.String(), id.Token).Call(ctx, http.MethodGet, "/api/v1/host/cpu", nil, &c)
			cancel()
			if err != nil {
				s.writeJSON(w, http.StatusConflict, map[string]string{"error": n.Name + " did not say which CPU it has: " + err.Error()})
				return
			}
			cpus = append(cpus, c)
		}
	}
	model, err := b.BaselineCPU(cpus)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"model": model, "hosts": len(cpus)})
}

// migrateStep is the body of prepare and finish.
type migrateStep struct {
	Name      string                     `json:"name"`
	XML       string                     `json:"xml,omitempty"`
	URI       string                     `json:"uri"`
	Cookie    []byte                     `json:"cookie"`
	Flags     libvirt.DomainMigrateFlags `json:"flags"`
	Disks     []string                   `json:"disks"`
	Cancelled bool                       `json:"cancelled,omitempty"`
}

type migrateAnswer struct {
	Cookie []byte `json:"cookie"`
	URI    string `json:"uri,omitempty"`
}

func (s *Server) migrator(w http.ResponseWriter, r *http.Request, peerOnly bool) (Migrator, bool) {
	m, ok := s.backend.(Migrator)
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "live migration is not available"})
		return nil, false
	}
	if peerOnly && s.notPeer(w, r) {
		return nil, false
	}
	return m, true
}

func (s *Server) migrateCheck(w http.ResponseWriter, r *http.Request) {
	m, ok := s.migrator(w, r, true)
	if !ok {
		return
	}
	var b struct {
		Paths []string `json:"paths"`
	}
	if !s.decode(w, r, &b) {
		return
	}
	res, err := m.CheckPaths(b.Paths)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"paths": res})
}

func (s *Server) migratePrepare(w http.ResponseWriter, r *http.Request) {
	m, ok := s.migrator(w, r, true)
	if !ok {
		return
	}
	var b migrateStep
	if !s.decode(w, r, &b) {
		return
	}
	cookie, uri, err := m.MigratePrepare(b.Name, b.XML, b.URI, b.Cookie, b.Flags, b.Disks)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, migrateAnswer{Cookie: cookie, URI: uri})
}

func (s *Server) migrateFinish(w http.ResponseWriter, r *http.Request) {
	m, ok := s.migrator(w, r, true)
	if !ok {
		return
	}
	var b migrateStep
	if !s.decode(w, r, &b) {
		return
	}
	cookie, err := m.MigrateFinish(b.Name, b.URI, b.Cookie, b.Flags, b.Disks, b.Cancelled)
	if !b.Cancelled {
		s.live.Refresh()
	}
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, migrateAnswer{Cookie: cookie})
}

// migrationPlan is what the source decided after asking the target.
type migrationPlan struct {
	src   virt.MigrationSource
	copy  []string // disk targets copied live; the rest are on shared storage
	flags libvirt.DomainMigrateFlags
}

// planMigration checks the target: networks, name, and for every disk whether it is shared
// (same path on NFS on both hosts), can be copied (a pool there keeps that directory), or
// blocks the migration.
func planMigration(ctx context.Context, p *cluster.Peer, m Migrator, src virt.MigrationSource) (migrationPlan, error) {
	host := p.Node.Name
	plan := migrationPlan{src: src, flags: virt.MigrateFlags}
	var doms struct{ Domains []virt.Domain }
	if err := p.Call(ctx, http.MethodGet, "/api/v1/domains", nil, &doms); err != nil {
		return plan, fmt.Errorf("%s: %w", host, err)
	}
	for _, d := range doms.Domains {
		if strings.EqualFold(d.UUID, src.UUID) || d.Name == src.Name {
			return plan, fmt.Errorf("%s already has a machine named %s", host, d.Name)
		}
	}
	var nets struct{ Networks []virt.VNetwork }
	if err := p.Call(ctx, http.MethodGet, "/api/v1/networks", nil, &nets); err != nil {
		return plan, err
	}
	for _, n := range src.Networks {
		i := slices.IndexFunc(nets.Networks, func(x virt.VNetwork) bool { return x.Name == n })
		if i < 0 || !nets.Networks[i].Active {
			return plan, fmt.Errorf("%s has no running network named %s; create or start it there first", host, n)
		}
	}
	if src.CPUMode == "host-passthrough" {
		if err := checkSameCPU(ctx, p, m); err != nil {
			return plan, err
		}
	}
	paths := make([]string, len(src.Disks))
	for i, d := range src.Disks {
		paths[i] = d.Path
	}
	var check struct{ Paths []virt.PathCheck }
	if err := p.Call(ctx, http.MethodPost, "/api/v1/migrate/check", map[string]any{"paths": paths}, &check); err != nil {
		return plan, err
	}
	copyDisks, err := disksToCopy(host, src.Disks, check.Paths)
	if err != nil {
		return plan, err
	}
	plan.copy = copyDisks
	if len(plan.copy) > 0 {
		plan.flags |= libvirt.MigrateNonSharedDisk
	}
	return plan, nil
}

// checkSameCPU refuses to send a machine that sees the host's exact CPU to a host with a
// different one: the guest could crash on the first instruction the new CPU lacks.
func checkSameCPU(ctx context.Context, p *cluster.Peer, m Migrator) error {
	here, err := m.HostCPU()
	if err != nil {
		return err
	}
	var there virt.HostCPU
	if err := p.Call(ctx, http.MethodGet, "/api/v1/host/cpu", nil, &there); err != nil {
		return err
	}
	if !virt.SameCPU(here, there) {
		return fmt.Errorf("this machine uses the host's exact CPU (%s), and %s has a different one (%s). In Settings, choose \"Named model\" with a model every host supports (VirtoMir can pick it), restart the machine, then migrate", here.Model, p.Node.Name, there.Model)
	}
	return nil
}

// disksToCopy decides per disk: shared (same path on network storage on both hosts: not
// copied), copied live (a pool on the target keeps that directory), or a reason to stop.
// A file that merely exists at the same path on the target is never treated as shared.
func disksToCopy(host string, disks []virt.LiveDisk, checks []virt.PathCheck) ([]string, error) {
	if len(checks) != len(disks) {
		return nil, fmt.Errorf("%s gave an unexpected answer", host)
	}
	var out []string
	for i, d := range disks {
		c := checks[i]
		switch {
		case !d.Writable:
			if !c.Exists {
				return nil, fmt.Errorf("%s is not on %s. Eject it first, or put the same file at that path there", d.Path, host)
			}
		case d.Network && c.Network && c.Exists:
			// shared storage: nothing to copy
		case c.Exists:
			return nil, fmt.Errorf("%s already has another file at %s; VirtoMir will not overwrite it", host, d.Path)
		case c.Pool == "":
			return nil, fmt.Errorf("disk %s would have to be copied, but %s has no storage pool at %s. Create one with the same path there, or keep the disk on storage both hosts share (an NFS pool on every host)", d.Target, host, path.Dir(d.Path))
		default:
			out = append(out, d.Target)
		}
	}
	return out, nil
}

// runMigration drives the five phases. Any failure before the target has resumed the
// machine cancels both sides, so it keeps running here.
func (s *Server) runMigration(ctx context.Context, m Migrator, p *cluster.Peer, plan migrationPlan, report func(string)) (string, error) {
	name, uuid, flags, disks := plan.src.Name, plan.src.UUID, plan.flags, plan.copy
	u, _ := url.Parse(p.Node.Address)
	dataURI := "tcp://" + net.JoinHostPort(u.Hostname(), "") // QEMU picks a port from libvirt's range
	dataURI = strings.TrimSuffix(dataURI, ":")

	report("Preparing " + p.Node.Name + "…")
	cookie, xml, err := m.MigrateBegin(uuid, flags, disks)
	if err != nil {
		return "", err
	}
	cancelSource := func() { _ = m.MigrateConfirm(uuid, nil, flags, disks, true) }

	var prep migrateAnswer
	step := migrateStep{Name: name, XML: xml, URI: dataURI, Cookie: cookie, Flags: flags, Disks: disks}
	if err := p.Call(ctx, http.MethodPost, "/api/v1/migrate/prepare", step, &prep); err != nil {
		cancelSource()
		return "", fmt.Errorf("%s could not take the machine: %w", p.Node.Name, err)
	}

	report("Migrating live…")
	cookie, err = m.MigratePerform(ctx, uuid, prep.URI, prep.Cookie, flags, disks, report)
	if err != nil {
		finishCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		_ = p.Call(finishCtx, http.MethodPost, "/api/v1/migrate/finish",
			migrateStep{Name: name, URI: prep.URI, Flags: flags, Disks: disks, Cancelled: true}, nil)
		cancel()
		cancelSource()
		return "", fmt.Errorf("migration failed, %s keeps running here: %w", name, err)
	}

	report("Resuming on " + p.Node.Name + "…")
	finishCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var fin migrateAnswer
	if err := p.Call(finishCtx, http.MethodPost, "/api/v1/migrate/finish",
		migrateStep{Name: name, URI: prep.URI, Cookie: cookie, Flags: flags, Disks: disks}, &fin); err != nil {
		_ = m.MigrateConfirm(uuid, nil, flags, disks, true)
		return "", fmt.Errorf("%s could not resume the machine, it keeps running here: %w", p.Node.Name, err)
	}
	if err := m.MigrateConfirm(uuid, fin.Cookie, flags, disks, false); err != nil {
		s.log.Warn("confirm migration", "domain", name, "err", err)
		return fmt.Sprintf("Running on %s. Cleaning up here reported: %v", p.Node.Name, err), nil
	}
	s.log.Info("domain migrated", "domain", name, "to", p.Node.Name, "copied disks", len(disks))
	msg := "Now running on " + p.Node.Name + "."
	if len(disks) > 0 {
		msg += fmt.Sprintf(" %d disk(s) were copied.", len(disks))
	}
	// The copies here are stale now; shared disks are the machine's only copy and stay.
	for _, d := range plan.src.Disks {
		if slices.Contains(disks, d.Target) {
			if err := m.RemoveImage(d.Path); err != nil {
				msg += " The old copy " + d.Path + " could not be removed here: " + err.Error()
			}
		}
	}
	return msg, nil
}
