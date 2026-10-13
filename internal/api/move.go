package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Mover is the part of the backend that moves machines between hosts (ADR-0020).
type Mover interface {
	PlanMove(uuid string) (virt.MovePlan, error)
	AdoptDomain(xml string) (string, error)
}

func (s *Server) moveRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/domains/{uuid}/move", s.moveDomain)
	mux.HandleFunc("POST /api/v1/domains/import", s.importDomain)
}

func (s *Server) mover(w http.ResponseWriter) (Mover, bool) {
	m, ok := s.backend.(Mover)
	if !ok {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "moving machines is not available"})
	}
	return m, ok
}

// importDomain defines a machine that was moved here (its disks were uploaded first).
func (s *Server) importDomain(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mover(w)
	if !ok {
		return
	}
	var b struct {
		XML string `json:"xml"`
	}
	if !s.decode(w, r, &b) {
		return
	}
	uuid, err := m.AdoptDomain(b.XML)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.done(w, http.StatusCreated, map[string]string{"uuid": uuid})
}

// moveDomain moves a stopped machine to another cluster member in the background: checks
// the target first, copies the disks into the chosen pool there, defines the machine there
// and only then removes it here. A failure before the last step leaves this host untouched
// and removes what was copied.
func (s *Server) moveDomain(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mover(w)
	if !ok || s.noCluster(w) {
		return
	}
	var b struct {
		Host string `json:"host"`
		Pool string `json:"pool"`
	}
	if !s.decode(w, r, &b) {
		return
	}
	node, ok := s.cluster.Member(b.Host)
	if !ok || node.ID == s.cluster.SelfID() {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "choose another host of this cluster"})
		return
	}
	plan, err := m.PlanMove(r.PathValue("uuid"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	id := identity(r)
	peer := s.peer(node).As(id.Name, id.Role.String(), id.Token)
	// Check the target now, so the user gets the answer right away rather than in a job.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	poolPath, err := checkMoveTarget(ctx, peer, plan, b.Pool)
	cancel()
	if err != nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	job := s.jobs.Start("move", "Move "+plan.Name+" to "+node.Name, func(ctx context.Context, report func(string)) (string, error) {
		res, err := s.runMove(ctx, peer, plan, b.Pool, poolPath, report)
		s.live.Refresh()
		return res, err
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

// checkMoveTarget makes sure the target has the pool, the networks and room for the names.
func checkMoveTarget(ctx context.Context, p *cluster.Peer, plan virt.MovePlan, pool string) (string, error) {
	host := p.Node.Name
	var pools struct{ Pools []virt.Pool }
	if err := p.Call(ctx, http.MethodGet, "/api/v1/storage/pools", nil, &pools); err != nil {
		return "", fmt.Errorf("%s: %w", host, err)
	}
	i := slices.IndexFunc(pools.Pools, func(x virt.Pool) bool { return x.Name == pool })
	switch {
	case i < 0:
		return "", fmt.Errorf("%s has no storage pool named %s", host, pool)
	case !pools.Pools[i].Active:
		return "", fmt.Errorf("storage pool %s on %s is not started", pool, host)
	case pools.Pools[i].Type != "dir" && pools.Pools[i].Type != "netfs" && pools.Pools[i].Type != "fs":
		return "", fmt.Errorf("storage pool %s on %s keeps no image files (type %s)", pool, host, pools.Pools[i].Type)
	}
	var need uint64
	for _, d := range plan.Disks {
		need += uint64(d.Size)
	}
	if free := pools.Pools[i].AvailableBytes; free > 0 && free < need {
		return "", fmt.Errorf("storage pool %s on %s has %d MiB free, the disks need %d MiB", pool, host, free>>20, need>>20)
	}
	var doms struct{ Domains []virt.Domain }
	if err := p.Call(ctx, http.MethodGet, "/api/v1/domains", nil, &doms); err != nil {
		return "", err
	}
	for _, d := range doms.Domains {
		if strings.EqualFold(d.UUID, plan.UUID) || d.Name == plan.Name {
			return "", fmt.Errorf("%s already has a machine named %s", host, d.Name)
		}
	}
	var nets struct{ Networks []virt.VNetwork }
	if err := p.Call(ctx, http.MethodGet, "/api/v1/networks", nil, &nets); err != nil {
		return "", err
	}
	var missing []string
	for _, n := range plan.Networks {
		if !slices.ContainsFunc(nets.Networks, func(x virt.VNetwork) bool { return x.Name == n }) {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("%s has no network named %s; create it there first", host, strings.Join(missing, ", "))
	}
	var vols struct{ Volumes []virt.Volume }
	if err := p.Call(ctx, http.MethodGet, "/api/v1/storage/pools/"+url.PathEscape(pool)+"/volumes", nil, &vols); err != nil {
		return "", err
	}
	for _, d := range plan.Disks {
		name := filepath.Base(d.Path)
		if slices.ContainsFunc(vols.Volumes, func(v virt.Volume) bool { return v.Name == name }) {
			return "", fmt.Errorf("pool %s on %s already has a volume named %s", pool, host, name)
		}
	}
	return pools.Pools[i].Path, nil
}

func (s *Server) runMove(ctx context.Context, p *cluster.Peer, plan virt.MovePlan, pool, _ string, report func(string)) (string, error) {
	volPath := "/api/v1/storage/pools/" + url.PathEscape(pool)
	var copied []string // volume names on the target, removed again on failure
	cleanup := func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, v := range copied {
			_ = p.Call(c, http.MethodDelete, volPath+"/volumes/"+url.PathEscape(v), nil, nil)
		}
	}
	paths := map[string]string{}
	for i, d := range plan.Disks {
		f, err := os.Open(d.Path)
		if err != nil {
			cleanup()
			return "", fmt.Errorf("read disk %s: %w", d.Target, err)
		}
		name := filepath.Base(d.Path)
		pr := &progressReader{r: f, total: d.Size}
		stop := make(chan struct{})
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					report(fmt.Sprintf("Copying disk %s (%d of %d): %d %%", d.Target, i+1, len(plan.Disks), pr.percent()))
				}
			}
		}()
		var vol virt.Volume
		err = p.Send(ctx, http.MethodPost, volPath+"/upload?name="+url.QueryEscape(name), pr, d.Size, &vol)
		close(stop)
		f.Close()
		if err != nil {
			cleanup()
			return "", fmt.Errorf("copy disk %s to %s: %w", d.Target, p.Node.Name, err)
		}
		copied = append(copied, vol.Name)
		paths[d.Path] = vol.Path
	}

	report("Defining " + plan.Name + " on " + p.Node.Name + "…")
	doc, notes, err := virt.RewriteForMove(plan.XML, paths)
	if err != nil {
		cleanup()
		return "", err
	}
	if err := p.Call(ctx, http.MethodPost, "/api/v1/domains/import", map[string]string{"xml": doc}, nil); err != nil {
		cleanup()
		return "", fmt.Errorf("define on %s: %w", p.Node.Name, err)
	}

	report("Removing " + plan.Name + " from this host…")
	notes = append(notes, plan.Notes...)
	if _, err := s.backend.Delete(plan.UUID, true); err != nil {
		notes = append(notes, "It is still defined here too and must be deleted by hand: "+err.Error())
	}
	s.log.Info("domain moved", "domain", plan.Name, "to", p.Node.Name)
	msg := "Moved to " + p.Node.Name + "."
	if len(notes) > 0 {
		msg += " " + strings.Join(notes, " ")
	}
	return msg, nil
}

// progressReader counts what was read so a long copy can report progress.
type progressReader struct {
	r     io.Reader
	n     atomic.Int64
	total int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n.Add(int64(n))
	return n, err
}

func (p *progressReader) percent() int64 {
	if p.total <= 0 {
		return 100
	}
	return p.n.Load() * 100 / p.total
}
