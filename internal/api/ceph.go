package api

import (
	"context"
	"net/http"
	"net/url"
	"os/exec"
	"slices"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/ceph"
	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Hyper-converged Ceph (D4, ADR-0073). One member manages the cluster: the one that
// bootstrapped it (or adopted one made elsewhere) and has the admin keyring; the Ceph
// calls go to that member (the UI addresses it through /api/v1/hosts/<id>/…). Disks are
// inspected on the host they are in, by that host, and only empty ones become OSDs.
// Storage policies are cluster-wide like GPU pools.

func (s *Server) cephRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/ceph/status", s.cephStatus)
	mux.HandleFunc("GET /api/v1/ceph/disks", s.cephDisks)
	mux.HandleFunc("GET /api/v1/ceph/hosts", s.cephHosts)
	mux.HandleFunc("POST /api/v1/ceph/bootstrap", s.cephBootstrap)
	mux.HandleFunc("POST /api/v1/ceph/adopt", s.cephAdopt)
	mux.HandleFunc("POST /api/v1/ceph/hosts", s.cephAddHost)
	mux.HandleFunc("POST /api/v1/ceph/osds", s.cephAddOSD)
	mux.HandleFunc("PUT /api/v1/ceph/noout", s.cephNoout)
	mux.HandleFunc("POST /api/v1/ceph/maintenance/{action}", s.cephMaintenance)
	mux.HandleFunc("GET /api/v1/ceph/pools", s.cephPools)
	mux.HandleFunc("POST /api/v1/ceph/pools", s.cephCreatePool)
	mux.HandleFunc("DELETE /api/v1/ceph/pools/{name}", s.cephDeletePool)
	mux.HandleFunc("GET /api/v1/storage/policies", s.listPolicies)
	mux.HandleFunc("POST /api/v1/storage/policies", s.createPolicy)
	mux.HandleFunc("PUT /api/v1/storage/policies/{id}", s.updatePolicy)
	mux.HandleFunc("DELETE /api/v1/storage/policies/{id}", s.deletePolicy)
	mux.HandleFunc("GET /api/v1/cluster/peer/ceph/status", s.peerCephStatus)
	mux.HandleFunc("GET /api/v1/cluster/peer/ceph/disk", s.peerCephDisk)
	mux.HandleFunc("POST /api/v1/cluster/peer/ceph/authorized-key", s.peerCephKey)
}

func (s *Server) noCeph(w http.ResponseWriter) bool {
	if s.ceph == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "Ceph is not available on this host"})
		return true
	}
	return false
}

// cephManaged answers 409 and returns false unless this host manages a Ceph cluster.
func (s *Server) cephManaged(w http.ResponseWriter) (ceph.ClusterConfig, bool) {
	if s.noCeph(w) {
		return ceph.ClusterConfig{}, false
	}
	cfg, ok := s.ceph.Config()
	if !ok {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "this host manages no Ceph cluster: bootstrap one, or open the member that manages it"})
		return cfg, false
	}
	return cfg, true
}

func cephCtx(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

// CephStatus is what GET /api/v1/ceph/status answers.
type CephStatus struct {
	// Installed: the ceph command is here; Cephadm: so is cephadm (needed to bootstrap).
	Installed bool `json:"installed"`
	Cephadm   bool `json:"cephadm"`
	// Managed: this host manages a cluster; ManagedBy names the member that does when
	// another one does.
	Managed   bool                `json:"managed"`
	ManagedBy *CephManager        `json:"managedBy,omitempty"`
	Config    *ceph.ClusterConfig `json:"config,omitempty"`
	Telemetry *ceph.Telemetry     `json:"telemetry,omitempty"`
	Error     string              `json:"error,omitempty"`
}

// CephManager is the member that manages the cluster.
type CephManager struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	FSID string `json:"fsid"`
}

func lookPath(name string) bool { _, err := exec.LookPath(name); return err == nil }

// cephLookPath reports whether a command exists; tests replace it.
var cephLookPath = lookPath

func (s *Server) cephStatus(w http.ResponseWriter, r *http.Request) {
	if s.noCeph(w) {
		return
	}
	st := CephStatus{Installed: cephLookPath("ceph"), Cephadm: cephLookPath("cephadm")}
	if cfg, ok := s.ceph.Config(); ok {
		st.Managed = true
		cfg.ClientKey = ""
		st.Config = &cfg
		ctx, cancel := cephCtx(r, 20*time.Second)
		defer cancel()
		if t, err := ceph.Collect(ctx, s.cephRunner, time.Now()); err != nil {
			st.Error = err.Error()
		} else {
			st.Telemetry = &t
		}
	} else {
		st.ManagedBy = s.cephManager(r.Context())
	}
	s.writeJSON(w, http.StatusOK, st)
}

// cephManager asks the other members which of them manages a cluster.
func (s *Server) cephManager(ctx context.Context) *CephManager {
	if s.cluster == nil || !s.cluster.Clustered() {
		return nil
	}
	members, _ := s.cluster.Members()
	var mu sync.Mutex
	var found *CephManager
	var wg sync.WaitGroup
	for _, n := range members {
		if n.ID == s.cluster.SelfID() {
			continue
		}
		wg.Add(1)
		go func(n cluster.Node) {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			var ans struct {
				Managed bool   `json:"managed"`
				FSID    string `json:"fsid"`
			}
			if s.peer(n).Call(c, http.MethodGet, "/api/v1/cluster/peer/ceph/status", nil, &ans) == nil && ans.Managed {
				mu.Lock()
				found = &CephManager{ID: n.ID, Name: n.Name, FSID: ans.FSID}
				mu.Unlock()
			}
		}(n)
	}
	wg.Wait()
	return found
}

func (s *Server) peerCephStatus(w http.ResponseWriter, r *http.Request) {
	if s.notPeer(w, r) || s.noCeph(w) {
		return
	}
	cfg, ok := s.ceph.Config()
	s.writeJSON(w, http.StatusOK, map[string]any{"managed": ok, "fsid": cfg.FSID})
}

func (s *Server) cephDisks(w http.ResponseWriter, r *http.Request) {
	if s.noCeph(w) {
		return
	}
	ctx, cancel := cephCtx(r, 15*time.Second)
	defer cancel()
	disks, err := ceph.ListDisks(ctx, s.cephRunner)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"disks": disks})
}

// peerCephDisk is a member asking this host about one of its disks before it becomes an OSD.
func (s *Server) peerCephDisk(w http.ResponseWriter, r *http.Request) {
	if s.notPeer(w, r) || s.noCeph(w) {
		return
	}
	ctx, cancel := cephCtx(r, 15*time.Second)
	defer cancel()
	info, err := ceph.InspectDisk(ctx, s.cephRunner, r.URL.Query().Get("path"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, info)
}

// CephHostView is a Ceph host with the cluster member it is, if any.
type CephHostView struct {
	ceph.CephHost
	MemberID string `json:"memberId,omitempty"`
}

func (s *Server) cephHosts(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.cephManaged(w); !ok {
		return
	}
	ctx, cancel := cephCtx(r, 15*time.Second)
	defer cancel()
	hosts, err := ceph.Hosts(ctx, s.cephRunner)
	if err != nil {
		s.writeError(w, err)
		return
	}
	out := make([]CephHostView, 0, len(hosts))
	for _, h := range hosts {
		v := CephHostView{CephHost: h}
		if n, ok := s.memberNamed(h.Hostname); ok {
			v.MemberID = n.ID
		}
		out = append(out, v)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"hosts": out})
}

// memberNamed finds a cluster member by its host name (this host too).
func (s *Server) memberNamed(name string) (cluster.Node, bool) {
	if s.cluster == nil {
		return cluster.Node{}, false
	}
	if name == s.hostName {
		return s.cluster.Self(""), true
	}
	members, _ := s.cluster.Members()
	i := slices.IndexFunc(members, func(n cluster.Node) bool { return n.Name == name })
	if i < 0 {
		return cluster.Node{}, false
	}
	return members[i], true
}

func (s *Server) cephBootstrap(w http.ResponseWriter, r *http.Request) {
	if s.noCeph(w) {
		return
	}
	var spec ceph.BootstrapSpec
	if !s.decode(w, r, &spec) {
		return
	}
	if err := spec.Validate(); err != nil {
		s.writeError(w, err)
		return
	}
	if _, ok := s.ceph.Config(); ok {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "this host manages a Ceph cluster already"})
		return
	}
	job := s.jobs.Start("ceph-bootstrap", "Bootstrap Ceph on "+s.hostName, func(ctx context.Context, report func(string)) (string, error) {
		report("cephadm bootstrap (downloads the Ceph container image; this takes a few minutes)")
		cfg, err := ceph.Bootstrap(ctx, s.cephRunner, s.ceph, s.hostName, spec)
		return cfg.FSID, err
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) cephAdopt(w http.ResponseWriter, r *http.Request) {
	if s.noCeph(w) {
		return
	}
	if _, ok := s.ceph.Config(); ok {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "this host manages a Ceph cluster already"})
		return
	}
	ctx, cancel := cephCtx(r, 30*time.Second)
	defer cancel()
	cfg, err := ceph.AdoptCluster(ctx, s.cephRunner, s.ceph, s.hostName)
	if err != nil {
		s.writeError(w, err)
		return
	}
	cfg.ClientKey = ""
	s.writeJSON(w, http.StatusOK, cfg)
}

// cephAddHost lets the cluster's cephadm into a member (its SSH key goes to the member's
// root) and adds the member as a Ceph host.
func (s *Server) cephAddHost(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.cephManaged(w)
	if !ok {
		return
	}
	var body struct {
		Member string `json:"member"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	n, found := s.memberByID(body.Member)
	if !found || n.ID == s.cluster.SelfID() {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "pick another member of the cluster"})
		return
	}
	ctx, cancel := cephCtx(r, 2*time.Minute)
	defer cancel()
	ip, err := memberIP(ctx, n.Address)
	if err != nil {
		s.writeError(w, err)
		return
	}
	if err := s.peer(n).Call(ctx, http.MethodPost, "/api/v1/cluster/peer/ceph/authorized-key", map[string]string{"publicKey": cfg.SSHPublicKey}, nil); err != nil {
		s.writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the cluster's SSH key did not reach " + n.Name + ": " + err.Error()})
		return
	}
	if err := ceph.AddHost(ctx, s.cephRunner, n.Name, ip); err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"hostname": n.Name, "ip": ip})
}

func (s *Server) memberByID(id string) (cluster.Node, bool) {
	if s.cluster == nil || id == "" {
		return cluster.Node{}, false
	}
	members, _ := s.cluster.Members()
	i := slices.IndexFunc(members, func(n cluster.Node) bool { return n.ID == id })
	if i < 0 {
		return cluster.Node{}, false
	}
	return members[i], true
}

func (s *Server) peerCephKey(w http.ResponseWriter, r *http.Request) {
	if s.notPeer(w, r) {
		return
	}
	var body struct {
		PublicKey string `json:"publicKey"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if err := ceph.AuthorizePeerKey(body.PublicKey); err != nil {
		s.writeError(w, err)
		return
	}
	s.log.Info("Ceph cluster SSH key authorized for root", "from", identity(r).Via)
	w.WriteHeader(http.StatusNoContent)
}

// cephAddOSD makes an OSD of an empty disk on a Ceph host; the host itself says whether
// the disk is empty.
func (s *Server) cephAddOSD(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.cephManaged(w); !ok {
		return
	}
	var body struct {
		Hostname string `json:"hostname"`
		Disk     string `json:"disk"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	ctx, cancel := cephCtx(r, 20*time.Second)
	defer cancel()
	var info ceph.DiskInfo
	var err error
	if body.Hostname == s.hostName {
		info, err = ceph.InspectDisk(ctx, s.cephRunner, body.Disk)
	} else if n, ok := s.memberNamed(body.Hostname); ok {
		err = s.peer(n).Call(ctx, http.MethodGet, "/api/v1/cluster/peer/ceph/disk?path="+url.QueryEscape(body.Disk), nil, &info)
	} else {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": body.Hostname + " is not a member of this VirtoMir cluster, so its disks cannot be checked"})
		return
	}
	if err != nil {
		s.writeError(w, err)
		return
	}
	if !info.Empty {
		s.writeError(w, ceph.AddOSD(ctx, s.cephRunner, body.Hostname, info))
		return
	}
	job := s.jobs.Start("ceph-add-osd", "Add OSD "+body.Hostname+":"+body.Disk, func(ctx context.Context, report func(string)) (string, error) {
		return body.Hostname + ":" + body.Disk, ceph.AddOSD(ctx, s.cephRunner, body.Hostname, info)
	})
	s.writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) cephNoout(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.cephManaged(w); !ok {
		return
	}
	var t toggle
	if !s.decode(w, r, &t) {
		return
	}
	ctx, cancel := cephCtx(r, 30*time.Second)
	defer cancel()
	if err := ceph.SetNoout(ctx, s.cephRunner, s.ceph, t.Enabled); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) cephMaintenance(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.cephManaged(w); !ok {
		return
	}
	var body struct {
		Hostname string `json:"hostname"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	ctx, cancel := cephCtx(r, 5*time.Minute)
	defer cancel()
	var err error
	switch r.PathValue("action") {
	case "enter":
		err = ceph.EnterHostMaintenance(ctx, s.cephRunner, s.ceph, body.Hostname)
	case "exit":
		err = ceph.ExitHostMaintenance(ctx, s.cephRunner, s.ceph, body.Hostname)
	default:
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "the maintenance actions are enter and exit"})
		return
	}
	if err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CephPoolView is an RBD pool and the libvirt pool that uses it here.
type CephPoolView struct {
	ceph.PoolDetail
	LibvirtPool string `json:"libvirtPool,omitempty"`
}

func (s *Server) cephPools(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.cephManaged(w); !ok {
		return
	}
	ctx, cancel := cephCtx(r, 20*time.Second)
	defer cancel()
	pools, err := ceph.ListRBDPools(ctx, s.cephRunner)
	if err != nil {
		s.writeError(w, err)
		return
	}
	used := s.libvirtRBDPools()
	out := make([]CephPoolView, 0, len(pools))
	for _, p := range pools {
		out = append(out, CephPoolView{PoolDetail: p, LibvirtPool: used[p.Name]})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"pools": out})
}

// libvirtRBDPools maps Ceph pool names to the libvirt pools on this host that use them.
func (s *Server) libvirtRBDPools() map[string]string {
	out := map[string]string{}
	pools, err := s.backend.ListPools()
	if err != nil {
		return out
	}
	for _, p := range pools {
		if p.Type == "rbd" && p.Source != "" {
			out[p.Source] = p.Name
		}
	}
	return out
}

func (s *Server) cephCreatePool(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.cephManaged(w)
	if !ok {
		return
	}
	var body struct {
		ceph.PoolSpec
		// LibvirtPool: the libvirt pool to create for it on every member ("" = none).
		LibvirtPool string `json:"libvirtPool,omitempty"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	spec := body.PoolSpec
	if err := spec.Normalize(); err != nil {
		s.writeError(w, err)
		return
	}
	ctx, cancel := cephCtx(r, 2*time.Minute)
	defer cancel()
	if err := ceph.CreateRBDPool(ctx, s.cephRunner, spec); err != nil {
		s.writeError(w, err)
		return
	}
	ans := map[string]any{"name": spec.Name}
	if body.LibvirtPool != "" {
		lv := ceph.LibvirtPool(cfg, spec.Name, body.LibvirtPool)
		if s.cluster != nil && s.cluster.Clustered() {
			ans["results"] = s.everyHost(r, time.Minute, func() error { return s.backend.CreatePool(lv) }, "/api/v1/storage/pools", lv)
		} else {
			err := s.backend.CreatePool(lv)
			res := HostResult{Host: s.hostName, OK: err == nil}
			if err != nil {
				res.Error = err.Error()
			}
			ans["results"] = []HostResult{res}
		}
	}
	s.writeJSON(w, http.StatusCreated, ans)
}

func (s *Server) cephDeletePool(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.cephManaged(w); !ok {
		return
	}
	name := r.PathValue("name")
	ctx, cancel := cephCtx(r, 2*time.Minute)
	defer cancel()
	pools, err := ceph.ListRBDPools(ctx, s.cephRunner)
	if err != nil {
		s.writeError(w, err)
		return
	}
	i := slices.IndexFunc(pools, func(p ceph.PoolDetail) bool { return p.Name == name })
	if i < 0 {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no RBD pool " + name})
		return
	}
	if lv := s.libvirtRBDPools()[name]; lv != "" {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "libvirt pool " + lv + " still uses it: delete that pool on every host first (its disks go with the Ceph pool)"})
		return
	}
	if err := ceph.DeleteRBDPool(ctx, s.cephRunner, name, pools[i].DataPool); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listPolicies(w http.ResponseWriter, _ *http.Request) {
	if s.noCeph(w) {
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"policies": s.ceph.Policies()})
}

func (s *Server) createPolicy(w http.ResponseWriter, r *http.Request) {
	if s.noCeph(w) {
		return
	}
	var p ceph.StoragePolicy
	if !s.decode(w, r, &p) {
		return
	}
	saved, err := s.ceph.CreatePolicy(p)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.replicateToMembers(r, http.MethodPost, "/api/v1/storage/policies", saved)
	s.writeJSON(w, http.StatusCreated, saved)
}

func (s *Server) updatePolicy(w http.ResponseWriter, r *http.Request) {
	if s.noCeph(w) {
		return
	}
	var p ceph.StoragePolicy
	if !s.decode(w, r, &p) {
		return
	}
	p.ID = r.PathValue("id")
	saved, err := s.ceph.UpdatePolicy(p)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.replicateToMembers(r, http.MethodPut, "/api/v1/storage/policies/"+p.ID, saved)
	s.writeJSON(w, http.StatusOK, saved)
}

func (s *Server) deletePolicy(w http.ResponseWriter, r *http.Request) {
	if s.noCeph(w) {
		return
	}
	id := r.PathValue("id")
	if err := s.ceph.DeletePolicy(id); err != nil {
		s.writeError(w, err)
		return
	}
	s.replicateToMembers(r, http.MethodDelete, "/api/v1/storage/policies/"+id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// policyPool is the pool for a new disk of policy id; "" picks the default policy, and
// "" comes back when there is none.
func (s *Server) policyPool(id string) (string, error) {
	if s.ceph == nil {
		if id != "" {
			return "", &virt.InvalidError{Message: "storage policies are not available on this host"}
		}
		return "", nil
	}
	for _, p := range s.ceph.Policies() {
		if p.ID == id || (id == "" && p.Default) {
			return p.Pool, nil
		}
	}
	if id != "" {
		return "", &virt.InvalidError{Message: "no storage policy " + id}
	}
	return "", nil
}
