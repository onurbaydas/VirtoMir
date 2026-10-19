package api

import (
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"

	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/virt"
)

type overlayBackend struct {
	Backend
	nets    []virt.VNetwork
	created []virt.NetworkSpec
	peers   map[string][]string
}

func (b *overlayBackend) ListNetworks() ([]virt.VNetwork, error) { return b.nets, nil }
func (b *overlayBackend) CreateNetwork(s virt.NetworkSpec) error {
	b.created = append(b.created, s)
	b.nets = append(b.nets, virt.VNetwork{Name: s.Name, Mode: s.Mode, VNI: s.VNI, Peers: s.Peers, ClusterWide: s.Cluster})
	return nil
}
func (b *overlayBackend) SetVXLANPeers(name string, peers []string) error {
	b.peers[name] = peers
	return nil
}

// #37: a member that joins later gets the cluster overlays, and every member's peer list
// follows the member list; an overlay deleted here does not come back.
func TestSyncOverlays(t *testing.T) {
	dir := t.TempDir()
	store, err := cluster.Open(filepath.Join(dir, "cluster.json"), "gamma", "fp")
	if err != nil {
		t.Fatal(err)
	}
	b := &overlayBackend{peers: map[string][]string{}, nets: []virt.VNetwork{
		{Name: "old", Mode: "vxlan", VNI: 9, Peers: []string{"10.0.0.1"}, ClusterWide: true},
		{Name: "lab", Mode: "vxlan", VNI: 5, Peers: []string{"10.0.0.9"}}, // made by hand: left alone
	}}
	s := &Server{backend: b, cluster: store, stateDir: dir, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	members := []cluster.Node{
		{ID: "a", Address: "https://10.0.0.1:8420"},
		{ID: "b", Address: "https://10.0.0.2:8420"},
		{ID: store.SelfID(), Address: "https://10.0.0.3:8420"},
	}
	s.syncOverlays(t.Context(), members, []Overlay{{Name: "prod", VNI: 100}, {Name: "old", VNI: 9}})
	want := []string{"10.0.0.1", "10.0.0.2"}
	if len(b.created) != 1 || b.created[0].Name != "prod" || b.created[0].VNI != 100 || !b.created[0].Cluster ||
		!slices.Equal(b.created[0].Peers, want) || b.created[0].Subnet != "" {
		t.Fatalf("created = %+v", b.created)
	}
	if !slices.Equal(b.peers["old"], want) {
		t.Errorf("old overlay peers = %v, want %v", b.peers["old"], want)
	}
	if _, touched := b.peers["lab"]; touched {
		t.Error("a hand-made overlay was changed")
	}

	// Deleted here on purpose: a member that still has it does not bring it back.
	b.nets = slices.DeleteFunc(b.nets, func(n virt.VNetwork) bool { return n.Name == "prod" })
	b.created = nil
	s.syncOverlays(t.Context(), members, []Overlay{{Name: "prod", VNI: 100}})
	if len(b.created) != 0 {
		t.Errorf("deleted overlay created again: %+v", b.created)
	}
}
