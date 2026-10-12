// Package virt is VirtoMir's gateway to libvirt. It owns the libvirt RPC connection,
// reconnects transparently when libvirtd restarts, and converts libvirt's raw
// values into the types the rest of VirtoMir works with.
package virt

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/digitalocean/go-libvirt"
	"github.com/digitalocean/go-libvirt/socket/dialers"
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

func (c *Client) Close() error {
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
		state, maxMem, mem, vcpus, cpuTime, err := l.DomainGetInfo(d)
		if err != nil {
			// The domain may have been undefined between list and query; skip it.
			c.log.Debug("skipping domain", "name", d.Name, "err", err)
			continue
		}
		persistent, err := l.DomainIsPersistent(d)
		if err != nil {
			continue
		}
		autostart, err := l.DomainGetAutostart(d)
		if err != nil {
			continue
		}
		out = append(out, Domain{
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
		})
	}
	return out, nil
}
