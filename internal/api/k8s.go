package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/k8s"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Kubernetes clusters on VirtoMir machines (D7, ADR-0076). A cluster lives on the member it
// was created on, which holds its CAs and runs its operations; its machines can be on any
// member and are made over the same API the UI uses, as the admin who created it.

func (s *Server) k8sRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/k8s/clusters", s.listK8s)
	mux.HandleFunc("POST /api/v1/k8s/clusters", s.createK8s)
	mux.HandleFunc("GET /api/v1/k8s/clusters/{id}", s.getK8s)
	mux.HandleFunc("DELETE /api/v1/k8s/clusters/{id}", s.deleteK8s)
	mux.HandleFunc("POST /api/v1/k8s/clusters/{id}/scale", s.scaleK8s)
	mux.HandleFunc("POST /api/v1/k8s/clusters/{id}/upgrade", s.upgradeK8s)
	mux.HandleFunc("GET /api/v1/k8s/clusters/{id}/kubeconfig", s.k8sKubeconfig)
}

func (s *Server) noK8s(w http.ResponseWriter) bool {
	if s.k8s == nil {
		s.writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "Kubernetes clusters are not available on this host"})
		return true
	}
	return false
}

// RunK8s is called once before the API serves: operations cut off by a restart are marked
// failed, deletions continue.
func (s *Server) RunK8s(ctx context.Context) {
	if s.runCtx == nil {
		s.runCtx = ctx
	}
	if s.k8sOrch != nil {
		s.k8sOrch.Recover(s.lcmCtx())
	}
}

// k8sDriverFor acts as the creator of c.
func (s *Server) k8sDriverFor(c k8s.Cluster) k8s.Driver {
	role, err := auth.ParseRole(c.CreatedByRole)
	if err != nil {
		role = auth.Admin
	}
	return &k8sDriver{s: s, id: auth.Identity{Name: c.CreatedBy, Role: role}}
}

func (s *Server) listK8s(w http.ResponseWriter, _ *http.Request) {
	out := []k8s.Cluster{}
	if s.k8s != nil {
		for _, c := range s.k8s.List() {
			out = append(out, c.Public())
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"enabled": s.k8s != nil, "clusters": out,
		"versions": k8s.Versions, "defaultVersion": k8s.DefaultVersion, "cnis": []string{k8s.CNIFlannel, k8s.CNICalico}})
}

func (s *Server) k8sCluster(w http.ResponseWriter, r *http.Request) (k8s.Cluster, bool) {
	if s.noK8s(w) {
		return k8s.Cluster{}, false
	}
	c, ok := s.k8s.Get(r.PathValue("id"))
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such Kubernetes cluster on this host"})
	}
	return c, ok
}

func (s *Server) getK8s(w http.ResponseWriter, r *http.Request) {
	if c, ok := s.k8sCluster(w, r); ok {
		s.writeJSON(w, http.StatusOK, c.Public())
	}
}

func k8sID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// k8sHosts resolves the members a cluster's nodes go to (IDs or names; none = this host).
func (s *Server) k8sHosts(names []string) ([]string, error) {
	self := ""
	if s.cluster != nil {
		self = s.cluster.SelfID()
	}
	if len(names) == 0 {
		return []string{self}, nil
	}
	var members []cluster.Node
	if s.cluster != nil {
		members, _ = s.cluster.Members()
	}
	var out []string
	for _, n := range names {
		i := slices.IndexFunc(members, func(m cluster.Node) bool { return m.ID == n || m.Name == n })
		switch {
		case n == self || n == s.hostName:
			n = self
		case i >= 0:
			n = members[i].ID
		default:
			return nil, fmt.Errorf("%w: %s is not a member of this cluster", k8s.ErrInvalid, n)
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out, nil
}

func (s *Server) createK8s(w http.ResponseWriter, r *http.Request) {
	if s.noK8s(w) {
		return
	}
	var spec k8s.Spec
	if !s.decode(w, r, &spec) {
		return
	}
	if err := spec.Normalize(); err != nil {
		s.writeError(w, err)
		return
	}
	hosts, err := s.k8sHosts(spec.Hosts)
	if err != nil {
		s.writeError(w, err)
		return
	}
	spec.Hosts = hosts
	if spec.IPPool != "" {
		if s.physnet == nil {
			s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "IP pools are not available on this host"})
			return
		}
		if _, ok := s.physnet.Pool(spec.IPPool); !ok {
			s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "no IP pool " + spec.IPPool})
			return
		}
	}
	pki, err := k8s.NewPKI(time.Now())
	if err != nil {
		s.writeError(w, err)
		return
	}
	id := identity(r)
	c := k8s.Cluster{ID: k8sID(), Spec: spec, State: k8s.StateProvisioning, Version: spec.Version,
		Nodes: k8s.Plan(spec, hosts), Events: []k8s.Event{}, CreatedBy: id.Name, CreatedByRole: id.Role.String(), PKI: pki}
	if err := s.k8s.Create(c); err != nil {
		s.writeError(w, err)
		return
	}
	if err := s.k8sOrch.Provision(s.lcmCtx(), c.ID); err != nil {
		s.writeError(w, err)
		return
	}
	c, _ = s.k8s.Get(c.ID)
	s.writeJSON(w, http.StatusAccepted, c.Public())
}

func (s *Server) scaleK8s(w http.ResponseWriter, r *http.Request) {
	c, ok := s.k8sCluster(w, r)
	if !ok {
		return
	}
	var b struct {
		Workers int `json:"workers"`
	}
	if !s.decode(w, r, &b) {
		return
	}
	if err := s.k8sOrch.Scale(s.lcmCtx(), c.ID, b.Workers); err != nil {
		s.writeError(w, err)
		return
	}
	c, _ = s.k8s.Get(c.ID)
	s.writeJSON(w, http.StatusAccepted, c.Public())
}

func (s *Server) upgradeK8s(w http.ResponseWriter, r *http.Request) {
	c, ok := s.k8sCluster(w, r)
	if !ok {
		return
	}
	if err := s.k8sOrch.Upgrade(s.lcmCtx(), c.ID); err != nil {
		s.writeError(w, err)
		return
	}
	c, _ = s.k8s.Get(c.ID)
	s.writeJSON(w, http.StatusAccepted, c.Public())
}

func (s *Server) deleteK8s(w http.ResponseWriter, r *http.Request) {
	c, ok := s.k8sCluster(w, r)
	if !ok {
		return
	}
	if err := s.k8sOrch.Delete(s.lcmCtx(), c.ID); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

var kubeUserRE = regexp.MustCompile(`[^a-z0-9.-]+`)

// k8sKubeconfig mints an admin kubeconfig (admins only, see requiredRole): the certificate
// names the VirtoMir user, so the API server's audit log shows who it was.
func (s *Server) k8sKubeconfig(w http.ResponseWriter, r *http.Request) {
	c, ok := s.k8sCluster(w, r)
	if !ok {
		return
	}
	if c.Endpoint == "" {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "the cluster has no API address yet; wait until its first control plane is up"})
		return
	}
	user := "virtomir-" + strings.Trim(kubeUserRE.ReplaceAllString(strings.ToLower(identity(r).Name), "-"), "-")
	kc, err := c.PKI.Kubeconfig(c.Spec.Name, c.Endpoint, user, time.Now())
	if err != nil {
		s.writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.kubeconfig"`, c.Spec.Name))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(kc)
}

// k8sDriver is k8s.Driver over the members' API.
type k8sDriver struct {
	s  *Server
	id auth.Identity
}

func (d *k8sDriver) call(ctx context.Context, hostID, method, path string, in, out any) error {
	err := d.s.callHost(ctx, d.id, hostID, method, path, in, out)
	var se *cluster.StatusError
	if errors.As(err, &se) && se.Code == http.StatusNotFound {
		return fmt.Errorf("%w: %s", k8s.ErrNotFound, se.Message)
	}
	return err
}

func (d *k8sDriver) self() string {
	if d.s.cluster == nil {
		return ""
	}
	return d.s.cluster.SelfID()
}

func (d *k8sDriver) CreateVM(ctx context.Context, hostID string, r k8s.VMRequest) (string, error) {
	body := map[string]any{
		"name": r.Name, "vcpus": r.VCPUs, "memoryMiB": r.MemoryMiB, "diskGiB": r.DiskGiB,
		"baseImage": r.Image, "pool": r.StoragePool, "start": true,
		"cloudInit": virt.CloudInit{Hostname: r.Name, UserData: r.UserData},
		"network":   virt.NICSpec{Network: r.Network},
	}
	if r.Project != "" {
		body["project"] = r.Project
	}
	if r.IPPool != "" {
		body["ip"] = IPRequest{Pool: r.IPPool}
	}
	var res virt.CreateResult
	if err := d.call(ctx, hostID, http.MethodPost, "/api/v1/domains", body, &res); err != nil {
		return "", err
	}
	if res.StartError != "" {
		_ = d.DeleteVM(ctx, hostID, res.UUID)
		return "", fmt.Errorf("created but not started, so removed again: %s", res.StartError)
	}
	return res.UUID, nil
}

func (d *k8sDriver) DeleteVM(ctx context.Context, hostID, uuid string) error {
	if err := d.call(ctx, hostID, http.MethodPost, "/api/v1/domains/"+uuid+"/actions/"+string(virt.ActionForceOff), nil, nil); err != nil && errors.Is(err, k8s.ErrNotFound) {
		return err
	}
	return d.call(ctx, hostID, http.MethodDelete, "/api/v1/domains/"+uuid+"?storage=remove", nil, nil)
}

func (d *k8sDriver) Exec(ctx context.Context, hostID, uuid string, argv ...string) (k8s.ExecResult, error) {
	var res virt.GuestExecResult
	err := d.call(ctx, hostID, http.MethodPost, "/api/v1/domains/"+uuid+"/guest/exec", map[string]any{"argv": argv}, &res)
	return k8s.ExecResult{ExitCode: res.ExitCode, Stdout: res.Stdout, Stderr: res.Stderr}, err
}

func (d *k8sDriver) ReserveIP(ctx context.Context, pool, name string) (string, error) {
	var a struct {
		IP string `json:"ip"`
	}
	err := d.call(ctx, d.self(), http.MethodPost, "/api/v1/physnet/ip-pools/"+url.PathEscape(pool)+"/allocations", map[string]string{"name": name}, &a)
	return a.IP, err
}

func (d *k8sDriver) ReleaseIP(ctx context.Context, pool, ip string) error {
	return d.call(ctx, d.self(), http.MethodDelete, "/api/v1/physnet/ip-pools/"+url.PathEscape(pool)+"/allocations/"+url.PathEscape(ip), nil, nil)
}

func (d *k8sDriver) AntiAffinity(ctx context.Context, name string, uuids []string) (string, error) {
	var rule struct {
		ID string `json:"id"`
	}
	err := d.call(ctx, d.self(), http.MethodPost, "/api/v1/placement/rules",
		map[string]any{"name": name, "kind": "vm_anti_affinity", "enabled": true, "strength": "should", "domains": uuids}, &rule)
	return rule.ID, err
}

func (d *k8sDriver) DeleteRule(ctx context.Context, id string) error {
	return d.call(ctx, d.self(), http.MethodDelete, "/api/v1/placement/rules/"+url.PathEscape(id), nil, nil)
}
