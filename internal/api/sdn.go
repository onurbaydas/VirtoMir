package api

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/sdn"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// SDN on OVN (D3, ADR-0072). The desired state lives in the sdn store, the same on every
// member: the member that takes a request changes the store, applies the change to the
// OVN northbound database (one per cluster, reachable from every member) and pushes the
// whole state to the others, which only store it. When OVN refuses a change the state is
// kept and the answer says so; POST /api/v1/sdn/reconcile applies everything again.
// Projects own VPCs: their operators manage them, other members may look. External
// addresses (gateways, floating IPs) and IPsec are the admins' business.

func (s *Server) sdnRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/sdn/status", s.sdnStatus)
	mux.HandleFunc("POST /api/v1/sdn/reconcile", s.sdnReconcile)
	mux.HandleFunc("GET /api/v1/sdn/vpcs", s.listVPCs)
	mux.HandleFunc("POST /api/v1/sdn/vpcs", s.createVPC)
	mux.HandleFunc("GET /api/v1/sdn/vpcs/{id}", s.getVPC)
	mux.HandleFunc("PUT /api/v1/sdn/vpcs/{id}", s.updateVPC)
	mux.HandleFunc("DELETE /api/v1/sdn/vpcs/{id}", s.deleteVPC)
	mux.HandleFunc("PUT /api/v1/sdn/vpcs/{id}/routes", s.setVPCRoutes)
	mux.HandleFunc("PUT /api/v1/sdn/vpcs/{id}/gateway", s.setVPCGateway)
	mux.HandleFunc("GET /api/v1/sdn/subnets", s.listSDNSubnets)
	mux.HandleFunc("POST /api/v1/sdn/subnets", s.createSDNSubnet)
	mux.HandleFunc("PUT /api/v1/sdn/subnets/{id}", s.updateSDNSubnet)
	mux.HandleFunc("DELETE /api/v1/sdn/subnets/{id}", s.deleteSDNSubnet)
	mux.HandleFunc("GET /api/v1/sdn/ports", s.listSDNPorts)
	mux.HandleFunc("GET /api/v1/sdn/floating-ips", s.listFloatingIPs)
	mux.HandleFunc("POST /api/v1/sdn/floating-ips", s.createFloatingIP)
	mux.HandleFunc("DELETE /api/v1/sdn/floating-ips/{id}", s.deleteFloatingIP)
	mux.HandleFunc("POST /api/v1/sdn/floating-ips/{id}/assign", s.assignFloatingIP)
	mux.HandleFunc("POST /api/v1/sdn/floating-ips/{id}/unassign", s.unassignFloatingIP)
	mux.HandleFunc("GET /api/v1/sdn/security-groups", s.listSecurityGroups)
	mux.HandleFunc("POST /api/v1/sdn/security-groups", s.createSecurityGroup)
	mux.HandleFunc("PUT /api/v1/sdn/security-groups/{id}", s.updateSecurityGroup)
	mux.HandleFunc("DELETE /api/v1/sdn/security-groups/{id}", s.deleteSecurityGroup)
	mux.HandleFunc("POST /api/v1/sdn/security-groups/{id}/rules", s.addSecurityRule)
	mux.HandleFunc("DELETE /api/v1/sdn/security-groups/{id}/rules/{rule}", s.deleteSecurityRule)
	mux.HandleFunc("POST /api/v1/sdn/security-groups/{id}/ports", s.bindSecurityGroupPort)
	mux.HandleFunc("DELETE /api/v1/sdn/security-groups/{id}/ports/{port}", s.unbindSecurityGroupPort)
	mux.HandleFunc("GET /api/v1/sdn/load-balancers", s.listLoadBalancers)
	mux.HandleFunc("POST /api/v1/sdn/load-balancers", s.createLoadBalancer)
	mux.HandleFunc("PUT /api/v1/sdn/load-balancers/{id}", s.updateLoadBalancer)
	mux.HandleFunc("DELETE /api/v1/sdn/load-balancers/{id}", s.deleteLoadBalancer)
	mux.HandleFunc("GET /api/v1/sdn/ipsec", s.getIPsec)
	mux.HandleFunc("PUT /api/v1/sdn/ipsec", s.setIPsec)
	mux.HandleFunc("PUT /api/v1/cluster/peer/sdn", s.peerSDN)
}

// sdnOperatorWrite: project operators change their VPCs; the handlers check the project.
// Gateways, floating IP allocation, reconcile and IPsec stay with the admins.
func sdnOperatorWrite(method, path string) bool {
	rest, ok := strings.CutPrefix(path, "/api/v1/sdn/")
	if !ok || method == http.MethodGet {
		return false
	}
	switch {
	case rest == "reconcile", rest == "ipsec", strings.HasSuffix(rest, "/gateway"):
		return false
	case rest == "floating-ips" && method == http.MethodPost:
		return false
	case strings.HasPrefix(rest, "floating-ips/") && method == http.MethodDelete:
		return false
	}
	return true
}

func (s *Server) noSDN(w http.ResponseWriter) bool {
	if s.sdn == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "SDN is not available on this host"})
		return true
	}
	return false
}

func (s *Server) nb() sdn.NB { return sdn.NB{R: s.sdnRunner, DB: s.sdnDB} }

// sdnMayChange answers 403 and returns false unless the caller may change the project's
// networks: a global admin, or an operator (or better) in the project.
func (s *Server) sdnMayChange(w http.ResponseWriter, r *http.Request, projectID string) bool {
	id := identity(r)
	if id.Role >= auth.Admin || (s.projects != nil && projectID != "" && s.projectOperator(projectID, id.Name, id.Role)) {
		return true
	}
	s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "only operators of project " + projectID + " may change its networks"})
	return false
}

// sdnSees reports whether the caller may see the project's networks: global admins see
// all, everyone else the projects they are a member of.
func (s *Server) sdnSees(r *http.Request, projectID string) bool {
	id := identity(r)
	if id.Role >= auth.Admin {
		return true
	}
	if s.projects == nil {
		return id.Role >= auth.Viewer
	}
	_, member := s.projects.RoleFor(projectID, id.Name, s.groupsOf(id.Name))
	return member
}

func visible[T any](s *Server, r *http.Request, list []T, project func(T) string) []T {
	out := make([]T, 0, len(list))
	for _, v := range list {
		if s.sdnSees(r, project(v)) {
			out = append(out, v)
		}
	}
	return out
}

// vpcFor finds the VPC of a request, answering 404 (also for VPCs the caller may not see).
func (s *Server) vpcFor(w http.ResponseWriter, r *http.Request, id string) (sdn.VPC, bool) {
	v, ok := s.sdn.GetVPC(id)
	if !ok || !s.sdnSees(r, v.ProjectID) {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no VPC " + id})
		return sdn.VPC{}, false
	}
	return v, true
}

// ovnError is the reason OVN did not take a change, in words for people.
func ovnError(err error) string {
	if errors.Is(err, exec.ErrNotFound) {
		return "OVN is not installed on this host (ovn-nbctl missing)"
	}
	return err.Error()
}

// applied answers a change that is saved: with OVN's error as 502 if applying it failed.
// Either way the other members get the new state. v may be a func() any, for an answer
// that reflects what applying stored (a subnet's DHCP options).
func (s *Server) applied(w http.ResponseWriter, r *http.Request, status int, v any, apply func(ctx context.Context, nb sdn.NB) error) {
	var err error
	if apply != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		err = apply(ctx, s.nb())
		cancel()
	}
	s.pushSDN(r.Context())
	if err != nil {
		s.log.Warn("SDN change saved but not applied to OVN", "err", err)
		s.writeJSON(w, http.StatusBadGateway, map[string]string{"error": "saved, but OVN did not take it: " + ovnError(err) + " (Reconcile applies it again)"})
		return
	}
	if f, ok := v.(func() any); ok {
		v = f()
	}
	if v == nil {
		w.WriteHeader(status)
		return
	}
	s.writeJSON(w, status, v)
}

// pushSDN sends the SDN state to the other members; one that misses it catches up through
// SyncMembers.
func (s *Server) pushSDN(ctx context.Context) {
	if s.cluster == nil || !s.cluster.Clustered() {
		return
	}
	st := s.sdn.Shared()
	members, _ := s.cluster.Members()
	var wg sync.WaitGroup
	for _, n := range members {
		if n.ID == s.cluster.SelfID() {
			continue
		}
		wg.Add(1)
		go func(n cluster.Node) {
			defer wg.Done()
			c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if err := s.peer(n).Call(c, http.MethodPut, "/api/v1/cluster/peer/sdn", st, nil); err != nil {
				s.log.Warn("SDN state not copied to a member", "member", n.Name, "err", err)
			}
		}(n)
	}
	wg.Wait()
}

func (s *Server) peerSDN(w http.ResponseWriter, r *http.Request) {
	if s.notPeer(w, r) || s.noSDN(w) {
		return
	}
	var st sdn.Shared
	if !s.decode(w, r, &st) {
		return
	}
	if _, err := s.sdn.Adopt(st); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Status and reconcile ---------------------------------------------------------------------

// SDNStatus is this host's OVN and the overlay's chassis.
type SDNStatus struct {
	Host         sdn.HostStatus    `json:"host"`
	Chassis      []sdn.ChassisInfo `json:"chassis"`
	ChassisError string            `json:"chassisError,omitempty"`
}

func (s *Server) sdnStatus(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	host, err := sdn.DetectHost(ctx, s.sdnRunner)
	if err != nil {
		s.writeError(w, err)
		return
	}
	st := SDNStatus{Host: host, Chassis: []sdn.ChassisInfo{}}
	if host.Installed {
		if ch, err := sdn.ListChassis(ctx, s.sdnRunner); err != nil {
			st.ChassisError = ovnError(err)
		} else if ch != nil {
			st.Chassis = ch
		}
	}
	s.writeJSON(w, http.StatusOK, st)
}

// ReconcileResult names what OVN refused.
type ReconcileResult struct {
	Applied int      `json:"applied"`
	Errors  []string `json:"errors"`
}

func (s *Server) sdnReconcile(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	res := s.reconcileAll(ctx)
	s.pushSDN(r.Context())
	s.writeJSON(w, http.StatusOK, res)
}

// reconcileAll applies the whole state to OVN: routers, subnets, routes, gateways, floating
// IPs, security groups and load balancers. Machine ports are made when machines plug in.
func (s *Server) reconcileAll(ctx context.Context) ReconcileResult {
	nb := s.nb()
	res := ReconcileResult{Errors: []string{}}
	note := func(what string, err error) {
		if err != nil {
			res.Errors = append(res.Errors, what+": "+ovnError(err))
		} else {
			res.Applied++
		}
	}
	for _, v := range s.sdn.ListVPCs("") {
		note("VPC "+v.ID, sdn.ReconcileVPC(ctx, nb, v))
		for _, sub := range s.sdn.ListSubnets(v.ID) {
			note("subnet "+sub.ID, s.applySubnet(ctx, nb, v, sub))
		}
		if rt, ok := s.sdn.GetRouter(v.ID); ok {
			note("routes of "+v.ID, sdn.ReconcileRoutes(ctx, nb, rt))
		}
		if gw, ok := s.sdn.GetExternalGateway(v.ID); ok {
			note("gateway of "+v.ID, sdn.ReconcileExternalGateway(ctx, nb, gw, s.sdn.ListSubnets(v.ID)))
		}
	}
	for _, f := range s.sdn.ListFloatingIPs("", "") {
		note("floating IP "+f.ExternalIP, sdn.ReconcileFloatingIP(ctx, nb, f, s.portMAC(f.PortID)))
	}
	for _, g := range s.sdn.ListSecurityGroups("", "") {
		note("security group "+g.Name, sdn.ReconcileSecurityGroup(ctx, nb, g))
	}
	for _, lb := range s.sdn.ListLoadBalancers("", "") {
		note("load balancer "+lb.Name, sdn.ReconcileLoadBalancer(ctx, nb, lb, s.memberPort))
	}
	return res
}

// applySubnet makes the subnet in OVN and keeps the DHCP options' UUID OVN gave it.
func (s *Server) applySubnet(ctx context.Context, nb sdn.NB, v sdn.VPC, sub sdn.Subnet) error {
	dhcp, err := sdn.ReconcileSubnet(ctx, nb, v, sub)
	if err != nil {
		return err
	}
	if dhcp != sub.OVNDHCPID {
		sub.OVNDHCPID = dhcp
		_, err = s.sdn.UpdateSubnet(sub)
	}
	return err
}

func (s *Server) portMAC(portID string) string {
	if p, ok := s.sdn.GetPort(portID); ok {
		return p.MAC
	}
	return ""
}

func (s *Server) memberPort(ip string) (string, sdn.Subnet, bool) {
	p, sub, ok := s.sdn.PortWithIP(ip)
	return p.ID, sub, ok
}

// ---- VPCs ---------------------------------------------------------------------------------------

func (s *Server) listVPCs(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	list := s.sdn.ListVPCs(r.URL.Query().Get("project"))
	s.writeJSON(w, http.StatusOK, map[string]any{"vpcs": visible(s, r, list, func(v sdn.VPC) string { return v.ProjectID })})
}

// VPCDetail is a VPC with its router and gateway.
type VPCDetail struct {
	sdn.VPC
	Router  sdn.Router           `json:"router"`
	Gateway *sdn.ExternalGateway `json:"gateway,omitempty"`
}

func (s *Server) vpcDetail(v sdn.VPC) VPCDetail {
	d := VPCDetail{VPC: v}
	d.Router, _ = s.sdn.GetRouter(v.ID)
	if gw, ok := s.sdn.GetExternalGateway(v.ID); ok {
		d.Gateway = &gw
	}
	return d
}

func (s *Server) getVPC(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	if v, ok := s.vpcFor(w, r, r.PathValue("id")); ok {
		s.writeJSON(w, http.StatusOK, s.vpcDetail(v))
	}
}

func (s *Server) createVPC(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	var v sdn.VPC
	if !s.decode(w, r, &v) || !s.sdnMayChange(w, r, v.ProjectID) {
		return
	}
	if s.projects != nil && v.ProjectID != "" {
		if _, ok := s.projects.Get(v.ProjectID); !ok {
			s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "no project " + v.ProjectID})
			return
		}
	}
	created, err := s.sdn.CreateVPC(v)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.applied(w, r, http.StatusCreated, s.vpcDetail(created), func(ctx context.Context, nb sdn.NB) error {
		return sdn.ReconcileVPC(ctx, nb, created)
	})
}

func (s *Server) updateVPC(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	old, ok := s.vpcFor(w, r, r.PathValue("id"))
	if !ok || !s.sdnMayChange(w, r, old.ProjectID) {
		return
	}
	var v sdn.VPC
	if !s.decode(w, r, &v) {
		return
	}
	v.ID = old.ID
	saved, err := s.sdn.UpdateVPC(v)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.applied(w, r, http.StatusOK, s.vpcDetail(saved), nil)
}

func (s *Server) deleteVPC(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	v, ok := s.vpcFor(w, r, r.PathValue("id"))
	if !ok || !s.sdnMayChange(w, r, v.ProjectID) {
		return
	}
	_, hadGateway := s.sdn.GetExternalGateway(v.ID)
	if err := s.sdn.DeleteVPC(v.ID); err != nil {
		s.writeError(w, err)
		return
	}
	s.applied(w, r, http.StatusNoContent, nil, func(ctx context.Context, nb sdn.NB) error {
		if hadGateway {
			if err := sdn.RemoveExternalGateway(ctx, nb, v.ID); err != nil {
				return err
			}
		}
		return sdn.DeleteVPCResources(ctx, nb, v.ID)
	})
}

func (s *Server) setVPCRoutes(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	v, ok := s.vpcFor(w, r, r.PathValue("id"))
	if !ok || !s.sdnMayChange(w, r, v.ProjectID) {
		return
	}
	var body struct {
		Routes []sdn.Route `json:"routes"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	old, _ := s.sdn.GetRouter(v.ID)
	saved, err := s.sdn.UpdateRouter(sdn.Router{VPCID: v.ID, Routes: body.Routes})
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.applied(w, r, http.StatusOK, saved, func(ctx context.Context, nb sdn.NB) error {
		return sdn.ReplaceRoutes(ctx, nb, old, saved)
	})
}

func (s *Server) setVPCGateway(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	v, ok := s.vpcFor(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	var gw sdn.ExternalGateway
	if !s.decode(w, r, &gw) {
		return
	}
	gw.VPCID = v.ID
	_, had := s.sdn.GetExternalGateway(v.ID)
	saved, err := s.sdn.SetExternalGateway(gw)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.applied(w, r, http.StatusOK, saved, func(ctx context.Context, nb sdn.NB) error {
		if had { // a changed address or chassis: start from a clean router
			if err := sdn.RemoveExternalGateway(ctx, nb, v.ID); err != nil {
				return err
			}
		}
		if !saved.Enabled {
			return nil
		}
		return sdn.ReconcileExternalGateway(ctx, nb, saved, s.sdn.ListSubnets(v.ID))
	})
}

// ---- Subnets and ports ----------------------------------------------------------------------------

func (s *Server) listSDNSubnets(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	list := s.sdn.ListSubnets(r.URL.Query().Get("vpc"))
	s.writeJSON(w, http.StatusOK, map[string]any{"subnets": visible(s, r, list, s.subnetProject)})
}

func (s *Server) subnetProject(sub sdn.Subnet) string {
	v, _ := s.sdn.GetVPC(sub.VPCID)
	return v.ProjectID
}

func (s *Server) createSDNSubnet(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	var sub sdn.Subnet
	if !s.decode(w, r, &sub) {
		return
	}
	v, ok := s.vpcFor(w, r, sub.VPCID)
	if !ok || !s.sdnMayChange(w, r, v.ProjectID) {
		return
	}
	created, err := s.sdn.CreateSubnet(sub)
	if err != nil {
		s.writeError(w, err)
		return
	}
	stored := func() any {
		if got, ok := s.sdn.GetSubnet(created.ID); ok {
			return got // with the DHCP options' UUID
		}
		return created
	}
	s.applied(w, r, http.StatusCreated, stored, func(ctx context.Context, nb sdn.NB) error {
		if err := s.applySubnet(ctx, nb, v, created); err != nil {
			return err
		}
		if gw, ok := s.sdn.GetExternalGateway(v.ID); ok { // the new subnet needs SNAT too
			return sdn.ReconcileExternalGateway(ctx, nb, gw, s.sdn.ListSubnets(v.ID))
		}
		return nil
	})
}

// subnetFor finds the subnet of a request and its VPC, answering 404 when not visible.
func (s *Server) subnetFor(w http.ResponseWriter, r *http.Request, id string) (sdn.Subnet, sdn.VPC, bool) {
	sub, ok := s.sdn.GetSubnet(id)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no subnet " + id})
		return sdn.Subnet{}, sdn.VPC{}, false
	}
	v, ok := s.sdn.GetVPC(sub.VPCID)
	if !ok || !s.sdnSees(r, v.ProjectID) {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no subnet " + id})
		return sdn.Subnet{}, sdn.VPC{}, false
	}
	return sub, v, true
}

func (s *Server) updateSDNSubnet(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	old, v, ok := s.subnetFor(w, r, r.PathValue("id"))
	if !ok || !s.sdnMayChange(w, r, v.ProjectID) {
		return
	}
	var sub sdn.Subnet
	if !s.decode(w, r, &sub) {
		return
	}
	sub.ID = old.ID
	if sub.CIDR != old.CIDR && len(s.sdn.ListPorts(old.ID)) > 0 {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "machines are plugged into the subnet; its network stays"})
		return
	}
	saved, err := s.sdn.UpdateSubnet(sub)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.applied(w, r, http.StatusOK, func() any { got, _ := s.sdn.GetSubnet(saved.ID); return got }, func(ctx context.Context, nb sdn.NB) error {
		return s.applySubnet(ctx, nb, v, saved)
	})
}

func (s *Server) deleteSDNSubnet(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	sub, v, ok := s.subnetFor(w, r, r.PathValue("id"))
	if !ok || !s.sdnMayChange(w, r, v.ProjectID) {
		return
	}
	if err := s.sdn.DeleteSubnet(sub.ID); err != nil {
		s.writeError(w, err)
		return
	}
	s.applied(w, r, http.StatusNoContent, nil, func(ctx context.Context, nb sdn.NB) error {
		if _, ok := s.sdn.GetExternalGateway(v.ID); ok {
			if err := sdn.RemoveSubnetSNAT(ctx, nb, v.ID, sub.CIDR); err != nil {
				return err
			}
		}
		return sdn.DeleteSubnetResources(ctx, nb, v.ID, sub)
	})
}

func (s *Server) listSDNPorts(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	var list []sdn.Port
	if vm := r.URL.Query().Get("vm"); vm != "" {
		list = s.sdn.PortsOf(vm)
	} else {
		list = s.sdn.ListPorts(r.URL.Query().Get("subnet"))
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ports": visible(s, r, list, func(p sdn.Port) string {
		sub, _ := s.sdn.GetSubnet(p.SubnetID)
		return s.subnetProject(sub)
	})})
}

// ---- Machine adapters -------------------------------------------------------------------------------

// newMAC is a random address in QEMU's 52:54:00 range.
func newMAC() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", b[0], b[1], b[2])
}

// ovnNICAllowed answers and returns false unless the caller may plug a machine of project
// projectID into the subnet: the subnet's VPC must belong to the machine's project.
func (s *Server) ovnNICAllowed(w http.ResponseWriter, r *http.Request, nic virt.NICSpec, projectID string) bool {
	if s.sdn == nil {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "SDN is not available on this host"})
		return false
	}
	sub, v, ok := s.subnetFor(w, r, nic.OVNSubnet)
	if !ok {
		return false
	}
	if v.ProjectID != projectID && identity(r).Role < auth.Admin {
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "subnet " + sub.Name + " belongs to another project's VPC"})
		return false
	}
	return true
}

// plugOVN makes the machine's adapter a port of its subnet and attaches it.
func (s *Server) plugOVN(ctx context.Context, uuid string, nic virt.NICSpec) error {
	if nic.MAC == "" {
		nic.MAC = newMAC()
	}
	port, err := sdn.AllocatePort(ctx, s.nb(), s.sdn, nic.OVNSubnet, uuid, nic.MAC, nic.IPAddress, nic.SecurityGroups)
	if err != nil {
		if p, ok := s.sdn.GetPort(sdn.PortID(uuid, nic.MAC)); ok {
			_ = sdn.DeallocatePort(ctx, s.nb(), s.sdn, p.ID)
		}
		return fmt.Errorf("OVN port: %s", ovnError(err))
	}
	nic.IfaceID, nic.MAC = port.ID, port.MAC
	if err := s.backend.AttachInterface(uuid, nic); err != nil {
		_ = sdn.DeallocatePort(ctx, s.nb(), s.sdn, port.ID)
		return err
	}
	return nil
}

// unplugOVN removes the OVN ports of a machine (mac "" = all of them).
func (s *Server) unplugOVN(ctx context.Context, uuid, mac string) {
	if s.sdn == nil {
		return
	}
	n := 0
	for _, p := range s.sdn.PortsOf(uuid) {
		if mac != "" && !strings.EqualFold(p.MAC, mac) {
			continue
		}
		if err := sdn.DeallocatePort(ctx, s.nb(), s.sdn, p.ID); err != nil {
			s.log.Warn("OVN port of a removed adapter", "machine", uuid, "port", p.ID, "err", ovnError(err))
		}
		n++
	}
	if n > 0 {
		s.pushSDN(ctx)
	}
}

// ---- Floating IPs -----------------------------------------------------------------------------------

func (s *Server) listFloatingIPs(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	list := s.sdn.ListFloatingIPs(r.URL.Query().Get("project"), r.URL.Query().Get("vpc"))
	s.writeJSON(w, http.StatusOK, map[string]any{"floatingIps": visible(s, r, list, func(f sdn.FloatingIP) string { return f.ProjectID })})
}

func (s *Server) createFloatingIP(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	var f sdn.FloatingIP
	if !s.decode(w, r, &f) {
		return
	}
	created, err := s.sdn.CreateFloatingIP(f)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.applied(w, r, http.StatusCreated, created, nil)
}

func (s *Server) fipFor(w http.ResponseWriter, r *http.Request) (sdn.FloatingIP, bool) {
	f, ok := s.sdn.GetFloatingIP(r.PathValue("id"))
	if !ok || !s.sdnSees(r, f.ProjectID) {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no floating IP " + r.PathValue("id")})
		return sdn.FloatingIP{}, false
	}
	return f, true
}

func (s *Server) deleteFloatingIP(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	f, ok := s.fipFor(w, r)
	if !ok {
		return
	}
	if err := s.sdn.DeleteFloatingIP(f.ID); err != nil {
		s.writeError(w, err)
		return
	}
	s.applied(w, r, http.StatusNoContent, nil, nil)
}

func (s *Server) assignFloatingIP(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	f, ok := s.fipFor(w, r)
	if !ok || !s.sdnMayChange(w, r, f.ProjectID) {
		return
	}
	var body struct {
		InternalIP string `json:"internalIp"`
		PortID     string `json:"portId,omitempty"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.PortID == "" { // a machine's address: NAT on its own host
		if p, _, ok := s.sdn.PortWithIP(body.InternalIP); ok {
			body.PortID = p.ID
		}
	}
	saved, err := s.sdn.AssignFloatingIP(f.ID, body.InternalIP, body.PortID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.applied(w, r, http.StatusOK, saved, func(ctx context.Context, nb sdn.NB) error {
		return sdn.ReconcileFloatingIP(ctx, nb, saved, s.portMAC(saved.PortID))
	})
}

func (s *Server) unassignFloatingIP(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	f, ok := s.fipFor(w, r)
	if !ok || !s.sdnMayChange(w, r, f.ProjectID) {
		return
	}
	saved, err := s.sdn.UnassignFloatingIP(f.ID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.applied(w, r, http.StatusOK, saved, func(ctx context.Context, nb sdn.NB) error {
		return sdn.ReconcileFloatingIP(ctx, nb, saved, "")
	})
}

// ---- Security groups ----------------------------------------------------------------------------------

func (s *Server) listSecurityGroups(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	list := s.sdn.ListSecurityGroups(r.URL.Query().Get("project"), r.URL.Query().Get("vpc"))
	s.writeJSON(w, http.StatusOK, map[string]any{"securityGroups": visible(s, r, list, func(g sdn.SecurityGroup) string { return g.ProjectID })})
}

func (s *Server) applyGroup(id string) func(ctx context.Context, nb sdn.NB) error {
	return func(ctx context.Context, nb sdn.NB) error {
		g, ok := s.sdn.GetSecurityGroup(id)
		if !ok {
			return nil
		}
		return sdn.ReconcileSecurityGroup(ctx, nb, g)
	}
}

func (s *Server) createSecurityGroup(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	var g sdn.SecurityGroup
	if !s.decode(w, r, &g) {
		return
	}
	v, ok := s.vpcFor(w, r, g.VPCID)
	if !ok || !s.sdnMayChange(w, r, v.ProjectID) {
		return
	}
	g.MemberPorts = nil // machines join through their adapters or the ports endpoint
	created, err := s.sdn.CreateSecurityGroup(g)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.applied(w, r, http.StatusCreated, created, s.applyGroup(created.ID))
}

func (s *Server) groupFor(w http.ResponseWriter, r *http.Request) (sdn.SecurityGroup, bool) {
	g, ok := s.sdn.GetSecurityGroup(r.PathValue("id"))
	if !ok || !s.sdnSees(r, g.ProjectID) {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no security group " + r.PathValue("id")})
		return sdn.SecurityGroup{}, false
	}
	return g, s.sdnMayChange(w, r, g.ProjectID)
}

func (s *Server) updateSecurityGroup(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	old, ok := s.groupFor(w, r)
	if !ok {
		return
	}
	var g sdn.SecurityGroup
	if !s.decode(w, r, &g) {
		return
	}
	g.ID = old.ID
	saved, err := s.sdn.UpdateSecurityGroup(g)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.applied(w, r, http.StatusOK, saved, s.applyGroup(saved.ID))
}

func (s *Server) deleteSecurityGroup(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	g, ok := s.groupFor(w, r)
	if !ok {
		return
	}
	if err := s.sdn.DeleteSecurityGroup(g.ID); err != nil {
		s.writeError(w, err)
		return
	}
	s.applied(w, r, http.StatusNoContent, nil, func(ctx context.Context, nb sdn.NB) error {
		return sdn.DeleteSecurityGroupResources(ctx, nb, g.ID)
	})
}

func (s *Server) addSecurityRule(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	g, ok := s.groupFor(w, r)
	if !ok {
		return
	}
	var rule sdn.SecurityRule
	if !s.decode(w, r, &rule) {
		return
	}
	saved, err := s.sdn.AddSecurityRule(g.ID, rule)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.applied(w, r, http.StatusCreated, saved, s.applyGroup(g.ID))
}

func (s *Server) deleteSecurityRule(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	g, ok := s.groupFor(w, r)
	if !ok {
		return
	}
	if err := s.sdn.DeleteSecurityRule(g.ID, r.PathValue("rule")); err != nil {
		s.writeError(w, err)
		return
	}
	s.applied(w, r, http.StatusNoContent, nil, s.applyGroup(g.ID))
}

func (s *Server) bindSecurityGroupPort(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	g, ok := s.groupFor(w, r)
	if !ok {
		return
	}
	var body struct {
		PortID string `json:"portId"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if err := s.sdn.BindPortToSecurityGroup(g.ID, body.PortID); err != nil {
		s.writeError(w, err)
		return
	}
	g, _ = s.sdn.GetSecurityGroup(g.ID)
	s.applied(w, r, http.StatusOK, g, s.applyGroup(g.ID))
}

func (s *Server) unbindSecurityGroupPort(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	g, ok := s.groupFor(w, r)
	if !ok {
		return
	}
	if err := s.sdn.UnbindPortFromSecurityGroup(g.ID, r.PathValue("port")); err != nil {
		s.writeError(w, err)
		return
	}
	g, _ = s.sdn.GetSecurityGroup(g.ID)
	s.applied(w, r, http.StatusOK, g, s.applyGroup(g.ID))
}

// ---- Load balancers -----------------------------------------------------------------------------------

func (s *Server) listLoadBalancers(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	list := s.sdn.ListLoadBalancers(r.URL.Query().Get("project"), r.URL.Query().Get("vpc"))
	s.writeJSON(w, http.StatusOK, map[string]any{"loadBalancers": visible(s, r, list, func(lb sdn.LoadBalancer) string { return lb.ProjectID })})
}

func (s *Server) applyLB(id string) func(ctx context.Context, nb sdn.NB) error {
	return func(ctx context.Context, nb sdn.NB) error {
		lb, ok := s.sdn.GetLoadBalancer(id)
		if !ok {
			return nil
		}
		return sdn.ReconcileLoadBalancer(ctx, nb, lb, s.memberPort)
	}
}

func (s *Server) createLoadBalancer(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	var lb sdn.LoadBalancer
	if !s.decode(w, r, &lb) {
		return
	}
	v, ok := s.vpcFor(w, r, lb.VPCID)
	if !ok || !s.sdnMayChange(w, r, v.ProjectID) {
		return
	}
	created, err := s.sdn.CreateLoadBalancer(lb)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.applied(w, r, http.StatusCreated, created, s.applyLB(created.ID))
}

func (s *Server) lbFor(w http.ResponseWriter, r *http.Request) (sdn.LoadBalancer, bool) {
	lb, ok := s.sdn.GetLoadBalancer(r.PathValue("id"))
	if !ok || !s.sdnSees(r, lb.ProjectID) {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no load balancer " + r.PathValue("id")})
		return sdn.LoadBalancer{}, false
	}
	return lb, s.sdnMayChange(w, r, lb.ProjectID)
}

func (s *Server) updateLoadBalancer(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	old, ok := s.lbFor(w, r)
	if !ok {
		return
	}
	var lb sdn.LoadBalancer
	if !s.decode(w, r, &lb) {
		return
	}
	lb.ID, lb.VPCID = old.ID, old.VPCID
	saved, err := s.sdn.UpdateLoadBalancer(lb)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.applied(w, r, http.StatusOK, saved, s.applyLB(saved.ID))
}

func (s *Server) deleteLoadBalancer(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	lb, ok := s.lbFor(w, r)
	if !ok {
		return
	}
	if err := s.sdn.DeleteLoadBalancer(lb.ID); err != nil {
		s.writeError(w, err)
		return
	}
	s.applied(w, r, http.StatusNoContent, nil, func(ctx context.Context, nb sdn.NB) error {
		return sdn.DeleteLoadBalancerResources(ctx, nb, lb)
	})
}

// ---- IPsec ----------------------------------------------------------------------------------------------

func (s *Server) getIPsec(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	s.writeJSON(w, http.StatusOK, s.sdn.GetIPsecConfig().Redacted())
}

// setIPsec stores the settings and applies them on every member: each host's Open vSwitch
// needs its certificate, and the member that was asked also switches OVN's NB_Global.
func (s *Server) setIPsec(w http.ResponseWriter, r *http.Request) {
	if s.noSDN(w) {
		return
	}
	var c sdn.IPsecConfig
	if !s.decode(w, r, &c) {
		return
	}
	saved, err := s.sdn.SetIPsecConfig(c)
	if err != nil {
		s.writeError(w, err)
		return
	}
	central := r.URL.Query().Get("local") == ""
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	err = sdn.ReconcileIPsec(ctx, s.sdnRunner, s.nb(), central, saved)
	cancel()
	status, msg := "disabled", ""
	switch {
	case err != nil:
		status, msg = "error", ovnError(err)
	case saved.Enabled:
		status = "active"
	}
	if serr := s.sdn.SetIPsecStatus(status, msg); serr != nil {
		s.writeError(w, serr)
		return
	}
	s.replicateToMembers(r, http.MethodPut, "/api/v1/sdn/ipsec", c)
	if err != nil {
		s.writeJSON(w, http.StatusBadGateway, map[string]string{"error": "saved, but not applied: " + msg})
		return
	}
	s.writeJSON(w, http.StatusOK, s.sdn.GetIPsecConfig().Redacted())
}
