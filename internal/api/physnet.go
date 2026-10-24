package api

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/physnet"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Physical network (D16, ADR-0075): SR-IOV virtual functions and BGP are per host; IP pools
// (with their allocations) and the dynamic DNS settings are the same on every member: the
// member that changes them pushes the whole state to the others, and members that missed
// it catch up through SyncMembers. A machine created with an IP pool gets a fixed address
// through cloud-init and a DNS name; deleting it gives both back.

func (s *Server) physnetRoutes(mux *routeMux) {
	mux.HandleFunc("GET /api/v1/physnet/sriov", s.listSRIOV)
	mux.HandleFunc("PUT /api/v1/physnet/sriov/{iface}", s.setSRIOV)
	mux.HandleFunc("GET /api/v1/physnet/bgp", s.getBGP)
	mux.HandleFunc("PUT /api/v1/physnet/bgp", s.setBGP)
	mux.HandleFunc("GET /api/v1/physnet/ip-pools", s.listIPPools)
	mux.HandleFunc("POST /api/v1/physnet/ip-pools", s.createIPPool)
	mux.HandleFunc("GET /api/v1/physnet/ip-pools/{id}", s.getIPPool)
	mux.HandleFunc("PUT /api/v1/physnet/ip-pools/{id}", s.updateIPPool)
	mux.HandleFunc("DELETE /api/v1/physnet/ip-pools/{id}", s.deleteIPPool)
	mux.HandleFunc("POST /api/v1/physnet/ip-pools/{id}/check", s.checkIP)
	mux.HandleFunc("POST /api/v1/physnet/ip-pools/{id}/allocations", s.reserveIP)
	mux.HandleFunc("DELETE /api/v1/physnet/ip-pools/{id}/allocations/{ip}", s.releaseIP)
	mux.HandleFunc("GET /api/v1/physnet/dns", s.getDNS)
	mux.HandleFunc("PUT /api/v1/physnet/dns", s.setDNS)
	mux.HandleFunc("POST /api/v1/physnet/dns/test", s.testDNS)
	mux.HandleFunc("PUT /api/v1/cluster/peer/physnet", s.peerPhysnet)
}

func (s *Server) noPhysnet(w http.ResponseWriter) bool {
	if s.physnet == nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "physical network integration is not available on this host"})
		return true
	}
	return false
}

func physCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 30*time.Second)
}

// ---- SR-IOV -------------------------------------------------------------------------------

func (s *Server) listSRIOV(w http.ResponseWriter, _ *http.Request) {
	if s.noPhysnet(w) {
		return
	}
	devs, err := s.sriov.Devices()
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"devices": devs})
}

func (s *Server) setSRIOV(w http.ResponseWriter, r *http.Request) {
	if s.noPhysnet(w) {
		return
	}
	var body struct {
		VFs        int  `json:"vfs"`
		Persistent bool `json:"persistent"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	iface := r.PathValue("iface")
	// Changing the count removes every VF first: machines must not be using them.
	if nets, err := s.backend.ListNetworks(); err == nil {
		for _, n := range nets {
			if n.Mode == "hostdev" && n.PF == iface && len(n.UsedBy) > 0 {
				s.writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("machines use virtual functions of %s through network %s (%s): stop them first", iface, n.Name, strings.Join(n.UsedBy, ", "))})
				return
			}
		}
	}
	if err := s.sriov.SetVFs(iface, body.VFs, body.Persistent); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- BGP ----------------------------------------------------------------------------------

// BGPView is this host's BGP settings (passwords left out) with FRR's state.
type BGPView struct {
	Config   physnet.BGPConfig    `json:"config"`
	FRR      physnet.FRRStatus    `json:"frr"`
	Sessions []physnet.BGPSession `json:"sessions"`
	Error    string               `json:"error,omitempty"`
}

func (s *Server) getBGP(w http.ResponseWriter, r *http.Request) {
	if s.noPhysnet(w) {
		return
	}
	ctx, cancel := physCtx(r)
	defer cancel()
	v := BGPView{Config: s.physnet.BGP(), FRR: physnet.DetectFRR(ctx, s.physRunner), Sessions: []physnet.BGPSession{}}
	for i := range v.Config.Neighbors {
		if v.Config.Neighbors[i].Password != "" {
			v.Config.Neighbors[i].Password = "********"
		}
	}
	if v.FRR.BGPD && v.Config.AppliedAS != 0 {
		if list, err := physnet.Sessions(ctx, s.physRunner); err != nil {
			v.Error = err.Error()
		} else {
			v.Sessions = list
		}
	}
	s.writeJSON(w, http.StatusOK, v)
}

func (s *Server) setBGP(w http.ResponseWriter, r *http.Request) {
	if s.noPhysnet(w) {
		return
	}
	var c physnet.BGPConfig
	if !s.decode(w, r, &c) {
		return
	}
	old := s.physnet.BGP()
	c.AppliedAS = old.AppliedAS
	// A password left as shown (********) keeps the neighbor's stored one; empty removes it.
	for i, n := range c.Neighbors {
		if n.Password == "********" || n.Password == "" {
			c.Neighbors[i].Password = ""
			if j := slices.IndexFunc(old.Neighbors, func(o physnet.BGPNeighbor) bool { return o.Address == n.Address }); j >= 0 && n.Password != "" {
				c.Neighbors[i].Password = old.Neighbors[j].Password
			}
		}
	}
	if c.Neighbors == nil {
		c.Neighbors = []physnet.BGPNeighbor{}
	}
	if c.Announce == nil {
		c.Announce = []string{}
	}
	if err := c.Validate(); err != nil {
		s.writeError(w, err)
		return
	}
	ctx, cancel := physCtx(r)
	defer cancel()
	applied, err := physnet.ApplyBGP(ctx, s.physRunner, c)
	if serr := s.physnet.SetBGP(applied); serr != nil {
		s.writeError(w, serr)
		return
	}
	if err != nil {
		s.writeJSON(w, http.StatusBadGateway, map[string]string{"error": "saved, but FRR did not take it: " + err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- IP pools -----------------------------------------------------------------------------

func (s *Server) listIPPools(w http.ResponseWriter, _ *http.Request) {
	if s.noPhysnet(w) {
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"pools": s.physnet.Pools()})
}

func (s *Server) getIPPool(w http.ResponseWriter, r *http.Request) {
	if s.noPhysnet(w) {
		return
	}
	p, ok := s.physnet.Pool(r.PathValue("id"))
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "no IP pool " + r.PathValue("id")})
		return
	}
	s.writeJSON(w, http.StatusOK, p)
}

// dhcpClash answers 409 and returns true when the pool's range overlaps the DHCP range of
// its libvirt network here: libvirt would hand the same addresses to other machines.
func (s *Server) dhcpClash(w http.ResponseWriter, p physnet.IPPool) bool {
	if p.Network == "" {
		return false
	}
	nets, err := s.backend.ListNetworks()
	if err != nil {
		return false
	}
	for _, n := range nets {
		if n.Name != p.Network || n.DHCPStart == "" || n.DHCPEnd == "" {
			continue
		}
		ds, err1 := netip.ParseAddr(n.DHCPStart)
		de, err2 := netip.ParseAddr(n.DHCPEnd)
		ps, err3 := netip.ParseAddr(p.RangeStart)
		pe, err4 := netip.ParseAddr(p.RangeEnd)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil || ds.BitLen() != ps.BitLen() {
			continue
		}
		if !pe.Less(ds) && !de.Less(ps) {
			s.writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("the range overlaps the DHCP range %s – %s of network %s, which would hand the same addresses to other machines: shrink its DHCP range or pick addresses outside it", n.DHCPStart, n.DHCPEnd, n.Name)})
			return true
		}
	}
	return false
}

func (s *Server) createIPPool(w http.ResponseWriter, r *http.Request) {
	if s.noPhysnet(w) {
		return
	}
	var p physnet.IPPool
	if !s.decode(w, r, &p) || s.dhcpClash(w, p) {
		return
	}
	saved, err := s.physnet.CreatePool(p)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.pushPhysnet(r.Context())
	s.writeJSON(w, http.StatusCreated, saved)
}

func (s *Server) updateIPPool(w http.ResponseWriter, r *http.Request) {
	if s.noPhysnet(w) {
		return
	}
	var p physnet.IPPool
	if !s.decode(w, r, &p) || s.dhcpClash(w, p) {
		return
	}
	p.ID = r.PathValue("id")
	saved, err := s.physnet.UpdatePool(p)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.pushPhysnet(r.Context())
	s.writeJSON(w, http.StatusOK, saved)
}

func (s *Server) deleteIPPool(w http.ResponseWriter, r *http.Request) {
	if s.noPhysnet(w) {
		return
	}
	if err := s.physnet.DeletePool(r.PathValue("id")); err != nil {
		s.writeError(w, err)
		return
	}
	s.pushPhysnet(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) checkIP(w http.ResponseWriter, r *http.Request) {
	if s.noPhysnet(w) {
		return
	}
	var body struct {
		IP string `json:"ip"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	why, err := s.physnet.Check(r.PathValue("id"), body.IP)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"free": why == "", "reason": why})
}

// reserveIP holds an address for something outside VirtoMir (a printer, a VIP).
func (s *Server) reserveIP(w http.ResponseWriter, r *http.Request) {
	if s.noPhysnet(w) {
		return
	}
	var body struct {
		IP   string `json:"ip,omitempty"`
		Name string `json:"name"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "say what the address is for"})
		return
	}
	a, err := s.physnet.Allocate(r.PathValue("id"), physnet.Allocation{VMName: body.Name, Host: s.hostName}, body.IP)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.pushPhysnet(r.Context())
	s.writeJSON(w, http.StatusCreated, a)
}

func (s *Server) releaseIP(w http.ResponseWriter, r *http.Request) {
	if s.noPhysnet(w) {
		return
	}
	if err := s.physnet.Release(r.PathValue("id"), r.PathValue("ip")); err != nil {
		s.writeError(w, err)
		return
	}
	s.pushPhysnet(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// ---- Dynamic DNS --------------------------------------------------------------------------

func (s *Server) getDNS(w http.ResponseWriter, _ *http.Request) {
	if s.noPhysnet(w) {
		return
	}
	c := s.physnet.DNS()
	has := c.Secret != ""
	c.Secret = ""
	s.writeJSON(w, http.StatusOK, map[string]any{"config": c, "hasSecret": has})
}

func (s *Server) setDNS(w http.ResponseWriter, r *http.Request) {
	if s.noPhysnet(w) {
		return
	}
	var c physnet.DNSConfig
	if !s.decode(w, r, &c) {
		return
	}
	if _, err := s.physnet.SetDNS(c); err != nil {
		s.writeError(w, err)
		return
	}
	s.pushPhysnet(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// testDNS registers a throw-away name (192.0.2.1, documentation range) and removes it.
func (s *Server) testDNS(w http.ResponseWriter, r *http.Request) {
	if s.noPhysnet(w) {
		return
	}
	c := s.physnet.DNS()
	if !c.Enabled {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "dynamic DNS is off"})
		return
	}
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	host := fmt.Sprintf("virtomir-test-%x", b)
	ctx, cancel := physCtx(r)
	defer cancel()
	ip := []netip.Addr{netip.MustParseAddr("192.0.2.1")}
	if err := physnet.Register(ctx, c, host, ip); err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	if err := physnet.Unregister(ctx, c, host, ip); err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "added " + host + " but could not remove it: " + err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "added and removed " + host + "." + c.Zone})
}

// ---- Cluster ------------------------------------------------------------------------------

// pushPhysnet sends the shared state to the other members.
func (s *Server) pushPhysnet(ctx context.Context) {
	if s.cluster == nil || !s.cluster.Clustered() {
		return
	}
	st := s.physnet.Shared()
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
			if err := s.peer(n).Call(c, http.MethodPut, "/api/v1/cluster/peer/physnet", st, nil); err != nil {
				s.log.Warn("IP pools not copied to a member", "member", n.Name, "err", err)
			}
		}(n)
	}
	wg.Wait()
}

func (s *Server) peerPhysnet(w http.ResponseWriter, r *http.Request) {
	if s.notPeer(w, r) || s.noPhysnet(w) {
		return
	}
	var st physnet.Shared
	if !s.decode(w, r, &st) {
		return
	}
	if _, err := s.physnet.Adopt(st); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Machine lifecycle --------------------------------------------------------------------

// IPRequest asks for a fixed address of an IP pool for a new machine (ADR-0075).
type IPRequest struct {
	Pool string `json:"pool"`
	IP   string `json:"ip,omitempty"` // "" = the first free one
}

// fixedAddress takes an address of the pool for a new machine and writes it into its
// cloud-init; it returns the allocation (to claim or release) or answers the error.
func (s *Server) fixedAddress(w http.ResponseWriter, spec *virt.CreateSpec, req IPRequest) (physnet.Allocation, bool) {
	if s.noPhysnet(w) {
		return physnet.Allocation{}, false
	}
	if spec.CloudInit == nil || spec.Network == nil {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "a fixed address is set through cloud-init: create the machine from a cloud image, with a network adapter"})
		return physnet.Allocation{}, false
	}
	pool, ok := s.physnet.Pool(req.Pool)
	if !ok {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "no IP pool " + req.Pool})
		return physnet.Allocation{}, false
	}
	if pool.Network != "" && spec.Network.Network != pool.Network {
		s.writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": fmt.Sprintf("addresses of %s are for network %s", pool.Name, pool.Network)})
		return physnet.Allocation{}, false
	}
	if spec.Network.MAC == "" {
		spec.Network.MAC = newMAC()
	}
	host := spec.CloudInit.Hostname
	if host == "" {
		host = strings.ToLower(strings.NewReplacer(".", "-", "_", "-", "+", "-").Replace(spec.Name))
	}
	a, err := s.physnet.Allocate(pool.ID, physnet.Allocation{MAC: spec.Network.MAC, VMName: spec.Name, Hostname: host, Host: s.hostName}, req.IP)
	if err != nil {
		s.writeError(w, err)
		return physnet.Allocation{}, false
	}
	n := &virt.StaticNetwork{MAC: spec.Network.MAC, Addresses: []string{fmt.Sprintf("%s/%d", a.IP, pool.Prefix().Bits())},
		Nameservers: pool.DNSServers}
	if pool.Gateway != "" {
		if netip.MustParseAddr(pool.Gateway).Is4() {
			n.Gateway4 = pool.Gateway
		} else {
			n.Gateway6 = pool.Gateway
		}
	}
	dns := s.physnet.DNS()
	if pool.Domain != "" {
		n.Search = []string{pool.Domain}
	} else if dns.Enabled {
		n.Search = []string{strings.TrimSuffix(dns.Zone, ".")}
	}
	spec.CloudInit.Network = n
	return a, true
}

// dropAddress gives back an address taken for a machine that was not created.
func (s *Server) dropAddress(req *IPRequest, a physnet.Allocation) {
	if req != nil && a.IP != "" {
		_ = s.physnet.Release(req.Pool, a.IP)
	}
}

// claimAddress binds the address to the machine that now exists, registers its name and
// copies the pools to the members.
func (s *Server) claimAddress(ctx context.Context, pool string, a physnet.Allocation, uuid string) string {
	if err := s.physnet.Claim(pool, a.IP, uuid); err != nil {
		return err.Error()
	}
	s.pushPhysnet(ctx)
	if err := physnet.Register(context.WithoutCancel(ctx), s.physnet.DNS(), a.Hostname, []netip.Addr{netip.MustParseAddr(a.IP)}); err != nil {
		s.log.Warn("DNS name of a new machine", "machine", a.VMName, "err", err)
		return "the machine has " + a.IP + ", but its DNS name was not registered: " + err.Error()
	}
	return ""
}

// releaseAddresses gives a deleted machine's addresses and DNS names back.
func (s *Server) releaseAddresses(ctx context.Context, uuid string) {
	if s.physnet == nil {
		return
	}
	released, err := s.physnet.ReleaseVM(uuid)
	if err != nil || len(released) == 0 {
		return
	}
	s.pushPhysnet(ctx)
	dns := s.physnet.DNS()
	for _, a := range released {
		if a.Hostname == "" {
			continue
		}
		if err := physnet.Unregister(context.WithoutCancel(ctx), dns, a.Hostname, []netip.Addr{netip.MustParseAddr(a.IP)}); err != nil {
			s.log.Warn("DNS name of a deleted machine", "machine", a.VMName, "err", err)
		}
	}
}
