package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/onurbaydas/virtomir/internal/ceph"
	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/drs"
	"github.com/onurbaydas/virtomir/internal/gpool"
	"github.com/onurbaydas/virtomir/internal/placement"
	"github.com/onurbaydas/virtomir/internal/sdn"
	"github.com/onurbaydas/virtomir/internal/storageeff"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Cluster-wide state that changes reach only the members online at that moment (#37):
// DRS settings, placement rules and host labels, GPU pools, the SDN, storage policies and tiers, and the cluster-wide VXLAN overlays.
// Every minute (SyncMembers) each member asks the others for theirs: settings with a
// newer version are taken over, and a cluster overlay this host never had is created here,
// with every member's peer list following the member list.

// SharedState is what /api/v1/cluster/peer/shared answers.
type SharedState struct {
	DRS       *drs.Config             `json:"drs,omitempty"`
	Placement *placement.Shared       `json:"placement,omitempty"`
	GPUPools  *gpool.Shared           `json:"gpuPools,omitempty"`
	SDN       *sdn.Shared             `json:"sdn,omitempty"`
	Policies  *ceph.PoliciesShared    `json:"storagePolicies,omitempty"`
	Tiers     *storageeff.TiersShared `json:"storageTiers,omitempty"`
	Overlays  []Overlay               `json:"overlays"`
}

// Overlay is a cluster-wide VXLAN network.
type Overlay struct {
	Name string `json:"name"`
	VNI  uint   `json:"vni"`
}

// VXLANPeerSetter is the backend side of keeping a cluster overlay's peers current.
type VXLANPeerSetter interface {
	SetVXLANPeers(name string, peers []string) error
}

func (s *Server) peerShared(w http.ResponseWriter, r *http.Request) {
	if s.notPeer(w, r) {
		return
	}
	s.writeJSON(w, http.StatusOK, s.sharedState())
}

func (s *Server) sharedState() SharedState {
	st := SharedState{Overlays: []Overlay{}}
	if s.drs != nil {
		c := s.drs.GetConfig()
		st.DRS = &c
	}
	if s.placement != nil {
		p := s.placement.Shared()
		st.Placement = &p
	}
	if s.gpools != nil {
		g := s.gpools.Shared()
		st.GPUPools = &g
	}
	if s.sdn != nil {
		n := s.sdn.Shared()
		st.SDN = &n
	}
	if s.ceph != nil {
		p := s.ceph.Shared()
		st.Policies = &p
	}
	if s.storageEff != nil {
		t := s.storageEff.Shared()
		st.Tiers = &t
	}
	if nets, err := s.backend.ListNetworks(); err == nil {
		for _, n := range nets {
			if n.Mode == "vxlan" && n.ClusterWide {
				st.Overlays = append(st.Overlays, Overlay{Name: n.Name, VNI: n.VNI})
			}
		}
	}
	return st
}

// syncShared catches up with the members' shared state.
func (s *Server) syncShared(ctx context.Context, members []cluster.Node) {
	var remote []Overlay
	for _, n := range members {
		if n.ID == s.cluster.SelfID() {
			continue
		}
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		var st SharedState
		err := s.peer(n).Call(c, http.MethodGet, "/api/v1/cluster/peer/shared", nil, &st)
		cancel()
		if err != nil {
			continue // down, or a version without this endpoint
		}
		if st.DRS != nil && s.drs != nil {
			if ok, err := s.drs.Adopt(*st.DRS); ok {
				s.log.Info("DRS settings taken over from a member", "member", n.Name, "version", st.DRS.Version)
			} else if err != nil {
				s.log.Warn("DRS settings of a member", "member", n.Name, "err", err)
			}
		}
		if st.Placement != nil && s.placement != nil {
			if ok, err := s.placement.Adopt(*st.Placement); ok {
				s.log.Info("placement rules taken over from a member", "member", n.Name, "version", st.Placement.Version)
			} else if err != nil {
				s.log.Warn("placement rules of a member", "member", n.Name, "err", err)
			}
		}
		if st.GPUPools != nil && s.gpools != nil {
			if ok, err := s.gpools.Adopt(*st.GPUPools); ok {
				s.log.Info("GPU pools taken over from a member", "member", n.Name, "version", st.GPUPools.Version)
			} else if err != nil {
				s.log.Warn("GPU pools of a member", "member", n.Name, "err", err)
			}
		}
		if st.SDN != nil && s.sdn != nil {
			if ok, err := s.sdn.Adopt(*st.SDN); ok {
				s.log.Info("SDN state taken over from a member", "member", n.Name, "version", st.SDN.Version)
			} else if err != nil {
				s.log.Warn("SDN state of a member", "member", n.Name, "err", err)
			}
		}
		if st.Policies != nil && s.ceph != nil {
			if ok, err := s.ceph.Adopt(*st.Policies); ok {
				s.log.Info("storage policies taken over from a member", "member", n.Name, "version", st.Policies.Version)
			} else if err != nil {
				s.log.Warn("storage policies of a member", "member", n.Name, "err", err)
			}
		}
		if st.Tiers != nil && s.storageEff != nil {
			if ok, err := s.storageEff.Adopt(*st.Tiers); ok {
				s.log.Info("storage tiers taken over from a member", "member", n.Name, "version", st.Tiers.Version)
			} else if err != nil {
				s.log.Warn("storage tiers of a member", "member", n.Name, "err", err)
			}
		}
		remote = append(remote, st.Overlays...)
	}
	s.syncOverlays(ctx, members, remote)
}

// syncOverlays creates the cluster overlays the other members have and this host never
// had, and points every cluster overlay here at all the other members.
func (s *Server) syncOverlays(ctx context.Context, members []cluster.Node, remote []Overlay) {
	others := []string{}
	for _, n := range members {
		if n.ID == s.cluster.SelfID() {
			continue
		}
		a, err := memberIP(ctx, n.Address)
		if err != nil {
			return // never drop a peer because its name did not resolve this once
		}
		others = append(others, a)
	}
	slices.Sort(others)
	nets, err := s.backend.ListNetworks()
	if err != nil {
		return
	}
	seen := s.seenOverlays()
	local := map[string]virt.VNetwork{}
	for _, n := range nets {
		local[n.Name] = n
		if n.Mode == "vxlan" && n.ClusterWide {
			seen[n.Name] = n.VNI
		}
	}
	for _, o := range remote {
		if _, had := seen[o.Name]; had {
			continue // here, or deleted here on purpose
		}
		if _, clash := local[o.Name]; clash {
			s.log.Warn("cluster overlay not created: a network of that name exists", "network", o.Name)
			continue
		}
		spec := virt.NetworkSpec{Name: o.Name, Mode: "vxlan", VNI: o.VNI, Peers: others, Cluster: true}
		if err := s.backend.CreateNetwork(spec); err != nil {
			s.log.Warn("cluster overlay not created here", "network", o.Name, "err", err)
			continue
		}
		seen[o.Name] = o.VNI
		s.log.Info("cluster overlay created on this member", "network", o.Name, "vni", o.VNI)
	}
	if setter, ok := s.backend.(VXLANPeerSetter); ok {
		for _, n := range nets {
			if n.Mode != "vxlan" || !n.ClusterWide {
				continue
			}
			have := slices.Clone(n.Peers)
			slices.Sort(have)
			if !slices.Equal(have, others) {
				if err := setter.SetVXLANPeers(n.Name, others); err != nil {
					s.log.Warn("cluster overlay peers not updated", "network", n.Name, "err", err)
				}
			}
		}
	}
	s.saveSeenOverlays(seen)
}

// seenOverlays are the cluster overlays this host ever had (-state-dir/overlays.json), so
// one deleted here is not brought back by a member that still has it.
func (s *Server) seenOverlays() map[string]uint {
	seen := map[string]uint{}
	if s.stateDir == "" {
		return seen
	}
	if raw, err := os.ReadFile(filepath.Join(s.stateDir, "overlays.json")); err == nil {
		_ = json.Unmarshal(raw, &seen)
	}
	return seen
}

func (s *Server) saveSeenOverlays(seen map[string]uint) {
	if s.stateDir == "" {
		return
	}
	raw, _ := json.Marshal(seen)
	path := filepath.Join(s.stateDir, "overlays.json")
	if old, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(old)) == string(raw) {
		return
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		s.log.Warn("save overlays.json", "err", err)
	}
}
