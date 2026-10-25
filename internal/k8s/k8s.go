// Package k8s runs Kubernetes clusters on VirtoMir machines (D7, ADR-0076). VirtoMir makes
// the cluster's certificate authorities and bootstrap token itself, so every node's
// cloud-init (kubeadm with containerd) is known before the machine exists and the admin
// kubeconfig is signed here, without reading anything back from a node. Later steps
// (readiness, joining tokens, draining, upgrades) run inside the nodes through their guest
// agent, on whichever member runs them.
package k8s

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Versions are the Kubernetes minor versions a new cluster can have (packages from
// pkgs.k8s.io, so the newest patch of the minor is installed).
var Versions = []string{"1.34", "1.35", "1.36", "1.37"}

// DefaultVersion is what a new cluster gets when none is named.
const DefaultVersion = "1.36"

// CNIs a cluster can be created with; the manifests are pinned.
const (
	CNIFlannel = "flannel"
	CNICalico  = "calico"
)

var cniManifests = map[string]string{
	CNIFlannel: "https://github.com/flannel-io/flannel/releases/download/v0.28.9/kube-flannel.yml",
	CNICalico:  "https://raw.githubusercontent.com/projectcalico/calico/v3.32.2/manifests/calico.yaml",
}

// KubeVIPImage gives three control planes one address (ARP, leader election).
const KubeVIPImage = "ghcr.io/kube-vip/kube-vip:v1.2.4"

// Default networks inside the cluster.
const (
	DefaultPodCIDR     = "10.244.0.0/16"
	DefaultServiceCIDR = "10.96.0.0/12"
)

// Roles and states.
const (
	RoleControlPlane = "control-plane"
	RoleWorker       = "worker"

	NodeCreating = "creating" // the machine is being made
	NodeJoining  = "joining"  // it runs its cloud-init: packages, kubeadm init or join
	NodeReady    = "ready"
	NodeFailed   = "failed"
	NodeDraining = "draining"
	NodeUpgrade  = "upgrading"

	StateProvisioning = "provisioning"
	StateReady        = "ready"
	StateScaling      = "scaling"
	StateUpgrading    = "upgrading"
	StateDeleting     = "deleting"
	StateFailed       = "failed"
)

// Errors for the API.
var (
	ErrInvalid  = errors.New("invalid")
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("exists")
	ErrBusy     = errors.New("busy")
)

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...)
}

// Size is one kind of node's machine.
type Size struct {
	VCPUs     int    `json:"vcpus"`
	MemoryMiB uint64 `json:"memoryMiB"`
	DiskGiB   uint64 `json:"diskGiB"`
}

// Spec is what a cluster is asked to be.
type Spec struct {
	Name          string `json:"name"`
	Project       string `json:"project,omitempty"`
	Version       string `json:"version"` // minor, e.g. 1.34
	ControlPlanes int    `json:"controlPlanes"`
	Workers       int    `json:"workers"`
	ControlPlane  Size   `json:"controlPlane"`
	Worker        Size   `json:"worker"`
	// Image is the cloud image every node starts from (Debian 12/13 or Ubuntu 22.04+).
	Image string `json:"image"`
	// StoragePool holds the nodes' disks ("" = the host's default).
	StoragePool string `json:"storagePool,omitempty"`
	// Network is the libvirt network of every node. With machines on more than one member it
	// must reach across them (a bridge, VXLAN overlay or SDN).
	Network string `json:"network"`
	// IPPool gives the nodes fixed addresses (ADR-0075); without one they use DHCP.
	IPPool string `json:"ipPool,omitempty"`
	// VIP is the API address shared by three control planes; with an IP pool and no VIP one
	// is reserved from the pool.
	VIP         string   `json:"vip,omitempty"`
	CNI         string   `json:"cni"`
	PodCIDR     string   `json:"podCidr"`
	ServiceCIDR string   `json:"serviceCidr"`
	SSHKeys     []string `json:"sshKeys,omitempty"`
	// Hosts are the members the nodes are spread over, in turn ("" or none = this host).
	Hosts []string `json:"hosts,omitempty"`
}

// Node is one machine of a cluster.
type Node struct {
	Name    string `json:"name"`
	Role    string `json:"role"`
	HostID  string `json:"hostId"`
	UUID    string `json:"uuid,omitempty"`
	IP      string `json:"ip,omitempty"`
	State   string `json:"state"`
	Version string `json:"version,omitempty"` // kubelet version, e.g. v1.34.1
	Message string `json:"message,omitempty"`
}

// Event is one line of a cluster's log.
type Event struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`
	Error   bool      `json:"error,omitempty"`
}

// Cluster is one Kubernetes cluster and its machines.
type Cluster struct {
	ID      string `json:"id"`
	Spec    Spec   `json:"spec"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
	Version string `json:"version"` // minor running now
	// UpgradeTo is the minor an upgrade under way (or failed) goes to.
	UpgradeTo string `json:"upgradeTo,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"` // https://<vip or first control plane>:6443
	// ReservedVIP is the address taken from the IP pool for the API (released on delete).
	ReservedVIP string `json:"reservedVip,omitempty"`
	// RuleID is the placement rule that keeps the control planes apart.
	RuleID        string    `json:"ruleId,omitempty"`
	Nodes         []Node    `json:"nodes"`
	Events        []Event   `json:"events"`
	CreatedBy     string    `json:"createdBy"`
	CreatedByRole string    `json:"createdByRole"`
	Created       time.Time `json:"created"`
	Updated       time.Time `json:"updated"`
	// PKI never leaves this host's state file (the API strips it).
	PKI PKI `json:"pki"`
}

// Public is the cluster as the API shows it: without its keys.
func (c Cluster) Public() Cluster {
	c.PKI = PKI{}
	c.Nodes = slices.Clone(c.Nodes)
	c.Events = slices.Clone(c.Events)
	return c
}

// ControlPlanes are the control-plane nodes, the first one first.
func (c Cluster) ControlPlanes() []Node {
	var out []Node
	for _, n := range c.Nodes {
		if n.Role == RoleControlPlane {
			out = append(out, n)
		}
	}
	return out
}

// Busy reports whether something runs on the cluster now.
func (c Cluster) Busy() bool {
	switch c.State {
	case StateProvisioning, StateScaling, StateUpgrading, StateDeleting:
		return true
	}
	return false
}

var (
	nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,28}[a-z0-9]$`)
	keyRE  = regexp.MustCompile(`^(ssh-|ecdsa-|sk-)[^\n\r]*$`)
)

// NextVersion is the minor after v, or "" for the newest.
func NextVersion(v string) string {
	i := slices.Index(Versions, v)
	if i < 0 || i == len(Versions)-1 {
		return ""
	}
	return Versions[i+1]
}

// Normalize fills defaults and checks a spec.
func (s *Spec) Normalize() error {
	s.Name = strings.TrimSpace(s.Name)
	if !nameRE.MatchString(s.Name) {
		return invalidf("a cluster name is 3 to 30 lower-case letters, digits and dashes, starting with a letter")
	}
	if s.Version == "" {
		s.Version = DefaultVersion
	}
	if !slices.Contains(Versions, s.Version) {
		return invalidf("Kubernetes %s is not offered; choose one of %s", s.Version, strings.Join(Versions, ", "))
	}
	if s.ControlPlanes == 0 {
		s.ControlPlanes = 1
	}
	if s.ControlPlanes != 1 && s.ControlPlanes != 3 {
		return invalidf("a cluster has 1 or 3 control planes")
	}
	if s.Workers < 0 || s.Workers > 50 {
		return invalidf("a cluster has 0 to 50 workers")
	}
	fill := func(z *Size, cpu int, mem, disk uint64) {
		if z.VCPUs == 0 {
			z.VCPUs = cpu
		}
		if z.MemoryMiB == 0 {
			z.MemoryMiB = mem
		}
		if z.DiskGiB == 0 {
			z.DiskGiB = disk
		}
	}
	fill(&s.ControlPlane, 2, 2048, 20)
	fill(&s.Worker, 2, 2048, 20)
	// kubeadm's pre-flight checks want 2 CPUs and 1700 MB on a control plane.
	if s.ControlPlane.VCPUs < 2 || s.ControlPlane.MemoryMiB < 1800 {
		return invalidf("a control plane needs at least 2 vCPUs and 1800 MiB of memory")
	}
	if s.Worker.VCPUs < 1 || s.Worker.MemoryMiB < 1024 {
		return invalidf("a worker needs at least 1 vCPU and 1024 MiB of memory")
	}
	if s.ControlPlane.DiskGiB < 10 || s.Worker.DiskGiB < 10 {
		return invalidf("a node needs a disk of at least 10 GiB")
	}
	if strings.TrimSpace(s.Image) == "" {
		return invalidf("choose the cloud image the nodes start from (Debian or Ubuntu)")
	}
	if strings.TrimSpace(s.Network) == "" {
		return invalidf("choose the network of the nodes")
	}
	if s.VIP != "" {
		a, err := netip.ParseAddr(s.VIP)
		if err != nil || !a.Is4() {
			return invalidf("the API address %q is not an IPv4 address", s.VIP)
		}
	}
	if s.ControlPlanes == 3 && s.VIP == "" && s.IPPool == "" {
		return invalidf("three control planes share one API address: give it, or an IP pool to take it from")
	}
	if s.CNI == "" {
		s.CNI = CNIFlannel
	}
	if _, ok := cniManifests[s.CNI]; !ok {
		return invalidf("the pod network is flannel or calico")
	}
	if s.PodCIDR == "" {
		s.PodCIDR = DefaultPodCIDR
	}
	if s.ServiceCIDR == "" {
		s.ServiceCIDR = DefaultServiceCIDR
	}
	pod, err1 := netip.ParsePrefix(s.PodCIDR)
	svc, err2 := netip.ParsePrefix(s.ServiceCIDR)
	switch {
	case err1 != nil || !pod.Addr().Is4() || pod.Masked() != pod:
		return invalidf("the pod network %q is not an IPv4 network like 10.244.0.0/16", s.PodCIDR)
	case err2 != nil || !svc.Addr().Is4() || svc.Masked() != svc:
		return invalidf("the service network %q is not an IPv4 network like 10.96.0.0/12", s.ServiceCIDR)
	case pod.Overlaps(svc):
		return invalidf("the pod and service networks overlap")
	case pod.Bits() > 22:
		return invalidf("the pod network needs room for a /24 per node: /22 or larger")
	}
	keys := s.SSHKeys[:0]
	for _, k := range s.SSHKeys {
		if k = strings.TrimSpace(k); k == "" {
			continue
		}
		if !keyRE.MatchString(k) {
			return invalidf("that does not look like an SSH public key: %.30s…", k)
		}
		keys = append(keys, k)
	}
	s.SSHKeys = keys
	return nil
}

// Plan names the nodes of a new cluster and spreads them over hosts, in turn.
func Plan(s Spec, hosts []string) []Node {
	var nodes []Node
	for i := range s.ControlPlanes {
		nodes = append(nodes, Node{Name: fmt.Sprintf("%s-cp-%d", s.Name, i+1), Role: RoleControlPlane,
			HostID: hosts[i%len(hosts)], State: NodeCreating})
	}
	for i := range s.Workers {
		nodes = append(nodes, Node{Name: fmt.Sprintf("%s-worker-%d", s.Name, i+1), Role: RoleWorker,
			HostID: hosts[(s.ControlPlanes+i)%len(hosts)], State: NodeCreating})
	}
	return nodes
}

// NextWorkerName is a worker name the cluster has not used.
func NextWorkerName(c Cluster) string {
	for i := 1; ; i++ {
		name := fmt.Sprintf("%s-worker-%d", c.Spec.Name, i)
		if !slices.ContainsFunc(c.Nodes, func(n Node) bool { return n.Name == name }) {
			return name
		}
	}
}
