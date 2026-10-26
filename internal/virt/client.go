// Package virt is VirtoMir's gateway to libvirt. It owns the libvirt RPC connection,
// reconnects transparently when libvirtd restarts, and converts libvirt's raw
// values into the types the rest of VirtoMir works with.
package virt

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/digitalocean/go-libvirt"
	"github.com/digitalocean/go-libvirt/socket/dialers"
	"libvirt.org/go/libvirtxml"
)

// DefaultSocket is the system libvirtd (or virtproxyd) UNIX socket.
const DefaultSocket = "/var/run/libvirt/libvirt-sock"

// Client is safe for concurrent use; go-libvirt multiplexes calls over one connection.
type Client struct {
	socket string
	uri    libvirt.ConnectURI
	log    *slog.Logger

	mu sync.Mutex
	l  *libvirt.Libvirt
	lx lxcConn // system containers (lxc.go)

	// ContainerDir holds the containers' root file systems and the downloaded images
	// ("" = /var/lib/virtomir/containers).
	ContainerDir string

	// ProtectedDirs, when set, returns the directories VirtoMir keeps its own files in (the
	// state directory, the backup location); no storage pool may be created in them.
	ProtectedDirs func() []string

	metas metaCache
}

func NewClient(socket string, log *slog.Logger) *Client {
	return &Client{socket: socket, uri: libvirt.QEMUSystem, log: log}
}

// conn returns a live connection, (re)connecting if needed.
func (c *Client) conn() (*libvirt.Libvirt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.l != nil {
		select {
		case <-c.l.Disconnected():
			c.log.Warn("libvirt connection lost, reconnecting")
			c.l = nil
		default:
			return c.l, nil
		}
	}

	l := libvirt.NewWithDialer(dialers.NewLocal(
		dialers.WithSocket(c.socket),
		dialers.WithLocalTimeout(5*time.Second),
	))
	if err := l.ConnectToURI(c.uri); err != nil {
		return nil, fmt.Errorf("connect to libvirt at %s: %w", c.socket, err)
	}
	c.log.Info("connected to libvirt", "socket", c.socket, "uri", c.uri)
	c.l = l
	return l, nil
}

// Conn returns a live connection, (re)connecting if needed.
func (c *Client) Conn() (*libvirt.Libvirt, error) {
	return c.conn()
}

// NameTaken reports whether a machine or container named name exists.
func (c *Client) NameTaken(name string) (bool, error) {
	l, err := c.conn()
	if err != nil {
		return false, err
	}
	return c.nameTaken(l, name), nil
}

func (c *Client) Close() error {
	c.closeLXC()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.l == nil {
		return nil
	}
	err := c.l.Disconnect()
	c.l = nil
	return err
}

// HostInfo describes the hypervisor host.
type HostInfo struct {
	Hostname          string `json:"hostname"`
	Hypervisor        string `json:"hypervisor"`
	HypervisorVersion string `json:"hypervisorVersion"`
	LibvirtVersion    string `json:"libvirtVersion"`
	CPUModel          string `json:"cpuModel"`
	CPUs              int    `json:"cpus"`
	CPUMHz            int    `json:"cpuMHz"`
	NUMANodes         int    `json:"numaNodes"`
	Sockets           int    `json:"sockets"`
	CoresPerSocket    int    `json:"coresPerSocket"`
	ThreadsPerCore    int    `json:"threadsPerCore"`
	MemoryKiB         uint64 `json:"memoryKiB"`
}

func (c *Client) HostInfo() (HostInfo, error) {
	l, err := c.conn()
	if err != nil {
		return HostInfo{}, err
	}
	var h HostInfo
	if h.Hostname, err = l.ConnectGetHostname(); err != nil {
		return HostInfo{}, fmt.Errorf("get hostname: %w", err)
	}
	if h.Hypervisor, err = l.ConnectGetType(); err != nil {
		return HostInfo{}, fmt.Errorf("get hypervisor type: %w", err)
	}
	hvVer, err := l.ConnectGetVersion()
	if err != nil {
		return HostInfo{}, fmt.Errorf("get hypervisor version: %w", err)
	}
	libVer, err := l.ConnectGetLibVersion()
	if err != nil {
		return HostInfo{}, fmt.Errorf("get libvirt version: %w", err)
	}
	h.HypervisorVersion, h.LibvirtVersion = formatVersion(hvVer), formatVersion(libVer)

	model, mem, cpus, mhz, nodes, sockets, cores, threads, err := l.NodeGetInfo()
	if err != nil {
		return HostInfo{}, fmt.Errorf("get node info: %w", err)
	}
	h.CPUModel = int8sToString(model[:])
	h.MemoryKiB = mem
	h.CPUs, h.CPUMHz, h.NUMANodes = int(cpus), int(mhz), int(nodes)
	h.Sockets, h.CoresPerSocket, h.ThreadsPerCore = int(sockets), int(cores), int(threads)
	return h, nil
}

// Domain is a summary of one VM, suitable for list views.
type Domain struct {
	UUID         string      `json:"uuid"`
	Name         string      `json:"name"`
	ID           int         `json:"id,omitempty"` // only set while running
	State        DomainState `json:"state"`
	VCPUs        int         `json:"vcpus"`
	MemoryKiB    uint64      `json:"memoryKiB"`
	MaxMemoryKiB uint64      `json:"maxMemoryKiB"`
	CPUTimeNs    uint64      `json:"cpuTimeNs"`
	Persistent   bool        `json:"persistent"`
	Autostart    bool        `json:"autostart"`
	// Template machines never start; they are the source of new machines.
	Template bool `json:"template,omitempty"`
	// LinkedFrom is the template whose disks this machine's disks are overlays on.
	LinkedFrom string `json:"linkedFrom,omitempty"`
	// Owner created the machine through VirtoMir; it counts against their quota.
	Owner string `json:"owner,omitempty"`
	// Project is the multi-tenancy project the machine belongs to (ADR-0060); empty = global.
	Project string `json:"project,omitempty"`
	// Kind: "vm" (default) or "container" (LXC).
	Kind string `json:"kind,omitempty"`
	// GPUPool and GPUCount: the GPUs it got from a GPU pool (ADR-0071).
	GPUPool  string `json:"gpuPool,omitempty"`
	GPUCount int    `json:"gpuCount,omitempty"`
}

func (c *Client) ListDomains() ([]Domain, error) {
	l, err := c.conn()
	if err != nil {
		return nil, err
	}
	doms, _, err := l.ConnectListAllDomains(1, libvirt.ConnectListDomainsActive|libvirt.ConnectListDomainsInactive)
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}

	out := make([]Domain, 0, len(doms))
	for _, d := range doms {
		s, err := summary(l, d)
		if err != nil {
			// The domain may have been undefined between list and query; skip it.
			c.log.Debug("skipping domain", "name", d.Name, "err", err)
			continue
		}
		c.addMeta(l, d, &s)
		out = append(out, s)
	}
	return append(out, c.listContainers()...), nil
}

// DomainXML returns the domain definition. inactive selects the persistent config the
// domain gets on its next boot instead of the live one. Secrets (e.g. VNC passwords)
// are never included.
func (c *Client) DomainXML(uuid string, inactive bool) (string, error) {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return "", err
	}
	var flags libvirt.DomainXMLFlags
	if inactive {
		flags |= libvirt.DomainXMLInactive
	}
	x, err := l.DomainGetXMLDesc(d, flags)
	if err != nil {
		return "", classify(err)
	}
	return x, nil
}

// DomainDetail returns the machine page model: summary, parsed XML, disk sizes and,
// for running machines, the guest IP addresses libvirt knows about.
func (c *Client) DomainDetail(uuid string) (DomainDetail, error) {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return DomainDetail{}, err
	}
	sum, err := summary(l, d)
	if err != nil {
		return DomainDetail{}, classify(err)
	}
	c.addMeta(l, d, &sum)
	raw, err := l.DomainGetXMLDesc(d, 0)
	if err != nil {
		return DomainDetail{}, classify(err)
	}
	var x libvirtxml.Domain
	if err := x.Unmarshal(raw); err != nil {
		return DomainDetail{}, fmt.Errorf("parse domain XML: %w", err)
	}
	detail := buildDetail(sum, &x)

	// Best effort: sizes and addresses are extras, never a reason to fail the page.
	addrs := map[string][]string{}
	if sum.State == StateRunning || sum.State == StateBlocked || sum.State == StatePaused {
		addrs = c.guestAddresses(l, d)
	}
	for i := range detail.Devices {
		dev := &detail.Devices[i]
		if dev.Target != "" && (dev.Kind == "disk" || dev.Kind == "cdrom") {
			if alloc, capacity, _, err := l.DomainGetBlockInfo(d, dev.Target, 0); err == nil {
				dev.CapacityBytes, dev.AllocationBytes = capacity, alloc
			}
		}
		if dev.MAC != "" {
			dev.Addresses = addrs[dev.MAC]
		}
	}
	return detail, nil
}

// guestAddresses maps MAC → "addr/prefix" from DHCP leases, falling back to the host ARP table
// (bridged guests). The guest agent is not asked: it can stall when the agent is missing.
func (c *Client) guestAddresses(l *libvirt.Libvirt, d libvirt.Domain) map[string][]string {
	out := map[string][]string{}
	for _, src := range []libvirt.DomainInterfaceAddressesSource{libvirt.DomainInterfaceAddressesSrcLease, libvirt.DomainInterfaceAddressesSrcArp} {
		ifaces, err := l.DomainInterfaceAddresses(d, uint32(src), 0)
		if err != nil {
			c.log.Debug("interface addresses", "domain", d.Name, "source", src, "err", err)
			continue
		}
		for _, ifc := range ifaces {
			if len(ifc.Hwaddr) == 0 {
				continue
			}
			mac := strings.ToLower(ifc.Hwaddr[0])
			for _, a := range ifc.Addrs {
				out[mac] = append(out[mac], fmt.Sprintf("%s/%d", a.Addr, a.Prefix))
			}
		}
		if len(out) > 0 {
			break
		}
	}
	return out
}

func summary(l *libvirt.Libvirt, d libvirt.Domain) (Domain, error) {
	state, maxMem, mem, vcpus, cpuTime, err := l.DomainGetInfo(d)
	if err != nil {
		return Domain{}, err
	}
	persistent, err := l.DomainIsPersistent(d)
	if err != nil {
		return Domain{}, err
	}
	autostart, err := l.DomainGetAutostart(d)
	if err != nil {
		return Domain{}, err
	}
	return Domain{
		UUID:         formatUUID(d.UUID),
		Name:         d.Name,
		ID:           max(int(d.ID), 0),
		State:        domainStateFrom(libvirt.DomainState(state)),
		VCPUs:        int(vcpus),
		MemoryKiB:    mem,
		MaxMemoryKiB: maxMem,
		CPUTimeNs:    cpuTime,
		Persistent:   persistent == 1,
		Autostart:    autostart == 1,
	}, nil
}

// DomainUUIDs lists every machine's UUID (config history sweeps).
func (c *Client) DomainUUIDs() ([]string, error) {
	doms, err := c.ListDomains()
	if err != nil {
		return nil, err
	}
	out := make([]string, len(doms))
	for i, d := range doms {
		out[i] = d.UUID
	}
	return out, nil
}
