package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Cluster routes (ADR-0020). /api/v1/cluster/peer/* is the node-to-node protocol: hello and
// join are public (join is authorized by the code's one-time secret), the rest needs a peer.
func (s *Server) clusterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/cluster", s.getCluster)
	mux.HandleFunc("POST /api/v1/cluster/invite", s.invite)
	mux.HandleFunc("POST /api/v1/cluster/join", s.join)
	mux.HandleFunc("DELETE /api/v1/cluster/members/{id}", s.removeMember)
	mux.HandleFunc("POST /api/v1/cluster/pools", s.createClusterPool)

	mux.HandleFunc("GET /api/v1/cluster/peer/hello", s.peerHello)
	mux.HandleFunc("POST /api/v1/cluster/peer/join", s.peerJoin)
	mux.HandleFunc("GET /api/v1/cluster/peer/members", s.peerMembers)
	mux.HandleFunc("PUT /api/v1/cluster/peer/members", s.peerReplace)
}

func (s *Server) noCluster(w http.ResponseWriter) bool {
	if s.cluster == nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{
			"error": "joining hosts needs HTTPS (-tls auto or files); this host runs without TLS"})
		return true
	}
	return false
}

// MemberStatus is a member as the UI sees it: reachable now, which version it runs.
type MemberStatus struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"`
	Self        bool   `json:"self"`
	Reachable   bool   `json:"reachable"`
	Version     string `json:"version,omitempty"`
	Error       string `json:"error,omitempty"`
}

// getCluster lists the members and checks each one (in parallel, a few seconds at most).
func (s *Server) getCluster(w http.ResponseWriter, r *http.Request) {
	if s.cluster == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "members": []MemberStatus{}})
		return
	}
	members, version := s.cluster.Members()
	self := s.cluster.SelfID()
	out := make([]MemberStatus, len(members))
	var wg sync.WaitGroup
	for i, n := range members {
		out[i] = MemberStatus{ID: n.ID, Name: n.Name, Address: n.Address, Fingerprint: n.Fingerprint, Self: n.ID == self}
		if n.ID == self {
			out[i].Reachable, out[i].Version = true, s.version
			continue
		}
		wg.Add(1)
		go func(m *MemberStatus, n cluster.Node) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			h, err := s.peer(n).Hello(ctx)
			if err != nil {
				m.Error = err.Error()
				return
			}
			if h.ID != n.ID {
				m.Error = "another VirtoMir answers at this address (" + h.Name + ")"
				return
			}
			m.Reachable, m.Version = true, h.Version
		}(&out[i], n)
	}
	wg.Wait()
	s.writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "selfId": self, "version": version, "members": out})
}

// invite creates a join code pointing at this host as the admin reached it.
func (s *Server) invite(w http.ResponseWriter, r *http.Request) {
	if s.noCluster(w) {
		return
	}
	code := cluster.Code{Address: "https://" + r.Host, Fingerprint: s.fingerprint, Secret: s.cluster.Invite()}
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"code": code.String(), "address": code.Address, "expires": time.Now().Add(cluster.InviteTTL),
	})
}

// join adds this host to the cluster behind a code made on one of its members.
func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	if s.noCluster(w) {
		return
	}
	var b struct {
		Code string `json:"code"`
	}
	if !s.decode(w, r, &b) {
		return
	}
	code, err := cluster.ParseCode(b.Code)
	if err == nil {
		err = s.cluster.Join(r.Context(), code, s.listenPort)
	}
	if err != nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	s.log.Info("joined cluster", "via", code.Address)
	w.WriteHeader(http.StatusNoContent)
}

// removeMember takes a host out of the cluster (or this host, which then leaves). Everyone
// left gets the new list; the removed host gets it too and becomes standalone.
func (s *Server) removeMember(w http.ResponseWriter, r *http.Request) {
	if s.noCluster(w) {
		return
	}
	id := r.PathValue("id")
	before, _ := s.cluster.Members()
	_, version, err := s.cluster.Remove(id)
	if err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	// Everyone who was a member learns who is left; a list without them makes them standalone.
	s.push(before, cluster.MemberList{Version: version, Members: removeNode(before, id)})
	w.WriteHeader(http.StatusNoContent)
}

// HostResult is the outcome of one step of a cluster-wide action on one member.
type HostResult struct {
	Host  string `json:"host"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// createClusterPool creates the same storage pool on every member (typically one NFS share,
// so machines can be live-migrated without copying their disks). Each host reports its
// own result; hosts where it worked keep the pool.
func (s *Server) createClusterPool(w http.ResponseWriter, r *http.Request) {
	if s.noCluster(w) {
		return
	}
	var spec virt.PoolSpec
	if !s.decode(w, r, &spec) {
		return
	}
	id := identity(r)
	members, _ := s.cluster.Members()
	if len(members) == 0 {
		members = []cluster.Node{s.cluster.Self("")}
	}
	results := make([]HostResult, len(members))
	var wg sync.WaitGroup
	for i, n := range members {
		results[i].Host = n.Name
		wg.Add(1)
		go func(res *HostResult, n cluster.Node) {
			defer wg.Done()
			var err error
			if n.ID == s.cluster.SelfID() {
				err = s.backend.CreatePool(spec)
			} else {
				// Mounting an unreachable NFS server takes libvirt a few minutes to give up.
				ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
				defer cancel()
				err = s.peer(n).As(id.Name, id.Role.String(), id.Token).Call(ctx, http.MethodPost, "/api/v1/storage/pools", spec, nil)
			}
			res.OK = err == nil
			if err != nil {
				res.Error = err.Error()
			}
		}(&results[i], n)
	}
	wg.Wait()
	s.live.Refresh()
	s.writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func removeNode(nodes []cluster.Node, id string) []cluster.Node {
	out := make([]cluster.Node, 0, len(nodes))
	for _, n := range nodes {
		if n.ID != id {
			out = append(out, n)
		}
	}
	return out
}

func (s *Server) peerHello(w http.ResponseWriter, _ *http.Request) {
	if s.noCluster(w) {
		return
	}
	s.writeJSON(w, http.StatusOK, cluster.Hello{ID: s.cluster.SelfID(), Name: s.hostName, Version: s.version})
}

// peerJoin admits a host that presents a valid one-time secret. Before admitting, this host
// calls the newcomer back at the address and certificate it claims, so a wrong address or
// a blocked port fails now instead of later.
func (s *Server) peerJoin(w http.ResponseWriter, r *http.Request) {
	if s.noCluster(w) {
		return
	}
	var b cluster.JoinRequest
	if !s.decode(w, r, &b) {
		return
	}
	n := b.Node
	if n.ID == "" || n.Name == "" || n.Fingerprint == "" || n.KeyHash == "" || !strings.HasPrefix(n.Address, "https://") {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "incomplete node description"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	h, err := s.peer(n).Hello(ctx)
	if err != nil || h.ID != n.ID {
		msg := "the new host could reach this one, but not the other way round"
		if err != nil {
			msg += ": " + err.Error()
		}
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": msg})
		return
	}
	members, version, err := s.cluster.Admit(b.Secret, n, "https://"+r.Host)
	if err != nil {
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	s.log.Info("host joined the cluster", "host", n.Name, "address", n.Address)
	list := cluster.MemberList{Version: version, Members: members}
	s.push(removeNode(members, n.ID), list) // the newcomer gets it as the answer
	s.writeJSON(w, http.StatusOK, list)
}

func (s *Server) peerMembers(w http.ResponseWriter, r *http.Request) {
	if s.notPeer(w, r) {
		return
	}
	members, version := s.cluster.Members()
	s.writeJSON(w, http.StatusOK, cluster.MemberList{Version: version, Members: members})
}

func (s *Server) peerReplace(w http.ResponseWriter, r *http.Request) {
	if s.notPeer(w, r) {
		return
	}
	var b cluster.MemberList
	if !s.decode(w, r, &b) {
		return
	}
	if err := s.cluster.Replace(b.Members, b.Version); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) notPeer(w http.ResponseWriter, r *http.Request) bool {
	if s.noCluster(w) {
		return true
	}
	if identity(r).Via == "" {
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "only cluster members may call this"})
		return true
	}
	return false
}

// push sends a member list to every node in to (except this one), in the background.
// A member that misses it catches up through SyncMembers.
func (s *Server) push(to []cluster.Node, list cluster.MemberList) {
	self := s.cluster.SelfID()
	for _, n := range to {
		if n.ID == self {
			continue
		}
		go func(n cluster.Node) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := s.peer(n).Call(ctx, http.MethodPut, "/api/v1/cluster/peer/members", list, nil); err != nil {
				s.log.Warn("push cluster members", "host", n.Name, "err", err)
			}
		}(n)
	}
}

// SyncMembers pulls the member list from every peer and keeps the newest; run it now and
// then so a host that was down during a change catches up. A host that every other member
// refuses was removed while it was away: it becomes standalone.
func (s *Server) SyncMembers(ctx context.Context) {
	if s.cluster == nil || !s.cluster.Clustered() {
		return
	}
	members, version := s.cluster.Members()
	peers, refused := 0, 0
	for _, n := range members {
		if n.ID == s.cluster.SelfID() {
			continue
		}
		peers++
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		var list cluster.MemberList
		err := s.peer(n).Call(c, http.MethodGet, "/api/v1/cluster/peer/members", nil, &list)
		cancel()
		switch {
		case err == nil:
			_ = s.cluster.Replace(list.Members, list.Version)
		case cluster.IsStatus(err, http.StatusUnauthorized):
			refused++
		}
	}
	if peers > 0 && refused == peers {
		s.log.Warn("no member accepts this host any more; leaving the cluster")
		_ = s.cluster.Replace(nil, version+1)
	}
}

// peer returns a (cached) client for n. Transports are kept per fingerprint so connections
// are reused by the 1 s live streams.
func (s *Server) peer(n cluster.Node) *cluster.Peer {
	return s.cluster.Peer(n, s.peerTransport(n))
}

// ---- Forwarding to other members ------------------------------------------------------------

// hostPrefix is how the UI addresses a member: /api/v1/hosts/<id>/… is /api/v1/… on that host.
const hostPrefix = "/api/v1/hosts/"

// splitHost splits /api/v1/hosts/<id>/<rest> into id and /api/v1/<rest>.
func splitHost(path string) (id, inner string, ok bool) {
	rest, ok := strings.CutPrefix(path, hostPrefix)
	if !ok {
		return "", "", false
	}
	id, tail, _ := strings.Cut(rest, "/")
	if id == "" {
		return "", "", false
	}
	return id, "/api/v1/" + tail, true
}

// peerIdentity checks a peer's Authorization. ok=false means it claimed to be a peer and is
// not; isPeer=false means it did not claim to be one.
func (s *Server) peerIdentity(r *http.Request) (id auth.Identity, isPeer, ok bool) {
	cred, found := strings.CutPrefix(r.Header.Get("Authorization"), cluster.AuthScheme)
	if !found {
		return id, false, true
	}
	if s.cluster == nil {
		return id, true, false
	}
	n, valid := s.cluster.VerifyPeer(cred)
	if !valid {
		return id, true, false
	}
	id = auth.Identity{Name: r.Header.Get(cluster.HeaderUser), Token: r.Header.Get(cluster.HeaderToken), Via: n.Name}
	if id.Name == "" {
		id.Name = "host " + n.Name // the peer itself (membership protocol)
		return id, true, true
	}
	if err := json.Unmarshal([]byte(`"`+r.Header.Get(cluster.HeaderRole)+`"`), &id.Role); err != nil {
		id.Role = auth.NoAccess
	}
	return id, true, true
}

// forward proxies a request for another member: REST, the SSE stream and console
// WebSockets alike. The member trusts the user and role this host vouches for.
func (s *Server) forward(w http.ResponseWriter, r *http.Request, n cluster.Node, inner string, id auth.Identity) {
	if isUpgrade(r) && !sameOrigin(r) {
		s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-site request refused"})
		return
	}
	base, err := url.Parse(n.Address)
	if err != nil {
		s.writeJSON(w, http.StatusBadGateway, map[string]string{"error": "bad member address " + n.Address})
		return
	}
	// Long connections end when the browser session does, as they would locally.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go s.watchSession(ctx, cancel, r)

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(base)
			pr.Out.URL.Path, pr.Out.URL.RawPath = inner, ""
			h := pr.Out.Header
			h.Del("Cookie")
			h.Set("Authorization", s.cluster.Credential())
			h.Set(cluster.HeaderUser, id.Name)
			h.Set(cluster.HeaderRole, id.Role.String())
			h.Set(cluster.HeaderVia, s.hostName)
			if id.Token != "" {
				h.Set(cluster.HeaderToken, id.Token)
			} else {
				h.Del(cluster.HeaderToken)
			}
			if h.Get("Origin") != "" { // checked above; the member checks it against its own name
				h.Set("Origin", n.Address)
			}
		},
		Transport:     s.peerTransport(n),
		FlushInterval: -1, // SSE
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return
			}
			s.writeJSON(w, http.StatusBadGateway, map[string]string{"error": n.Name + " is not reachable: " + err.Error()})
		},
	}
	rp.ServeHTTP(w, r.WithContext(ctx))
}

func isUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func (s *Server) peerTransport(n cluster.Node) http.RoundTripper {
	s.transportsMu.Lock()
	defer s.transportsMu.Unlock()
	if t, ok := s.transports[n.Fingerprint]; ok {
		return t
	}
	t := cluster.PinnedClient(n.Fingerprint, 0).Transport
	if s.transports == nil {
		s.transports = map[string]http.RoundTripper{}
	}
	s.transports[n.Fingerprint] = t
	return t
}
