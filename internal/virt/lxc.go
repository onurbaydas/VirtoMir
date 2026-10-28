package virt

import (
	"errors"
	"sync"
	"time"

	"github.com/digitalocean/go-libvirt"
	"github.com/digitalocean/go-libvirt/socket/dialers"
)

// System containers (ADR-0055) live in libvirt's LXC driver, reached on the same socket as
// QEMU under lxc:///system. Everything that finds a machine by UUID (lookup) falls back to
// it, so lifecycle actions, the XML, the machine page and deleting work for containers as
// for machines. Hosts without the driver simply have no containers.

const lxcURI libvirt.ConnectURI = "lxc:///system"

// lxcRetry is how long a host without the LXC driver waits before asking again.
const lxcRetry = time.Minute

type lxcConn struct {
	mu      sync.Mutex
	l       *libvirt.Libvirt
	retryAt time.Time
	err     error
}

func errNoLXC(err error) error {
	return refusedf("System containers need libvirt's LXC driver on this host (Debian, Ubuntu: apt install libvirt-daemon-driver-lxc, then restart libvirtd; Fedora, RHEL: dnf install libvirt-daemon-driver-lxc, then systemctl enable --now virtlxcd.socket): %v", err)
}

// lxc returns a live connection to the LXC driver, or an error that says how to get one.
func (c *Client) lxc() (*libvirt.Libvirt, error) {
	x := &c.lx
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.l != nil {
		select {
		case <-x.l.Disconnected():
			x.l = nil
		default:
			return x.l, nil
		}
	}
	if time.Now().Before(x.retryAt) {
		return nil, x.err
	}
	socket := c.socketFor("lxc")
	l := libvirt.NewWithDialer(dialers.NewLocal(dialers.WithSocket(socket), dialers.WithLocalTimeout(5*time.Second)))
	if err := l.ConnectToURI(lxcURI); err != nil {
		x.retryAt, x.err = time.Now().Add(lxcRetry), errNoLXC(err)
		return nil, x.err
	}
	c.log.Info("connected to libvirt", "socket", socket, "uri", lxcURI)
	x.l, x.err = l, nil
	return l, nil
}

// ContainerSupport is nil when this host can run system containers, else why not.
func (c *Client) ContainerSupport() error {
	_, err := c.lxc()
	return err
}

// errUnknownInit: the container's init is neither systemd nor busybox; libvirt's own
// shutdown is used instead.
var errUnknownInit = errors.New("unknown init")

// powerContainer shuts a running container down or reboots it from inside (its init).
func powerContainer(l *libvirt.Libvirt, d libvirt.Domain, reboot bool) error {
	err := containerPower(d.ID, reboot)
	if err == nil {
		return nil
	}
	if !errors.Is(err, errUnknownInit) {
		return err
	}
	if reboot {
		return l.DomainReboot(d, libvirt.DomainRebootDefault)
	}
	return l.DomainShutdown(d)
}

// isLXC reports whether l is the container connection.
func (c *Client) isLXC(l *libvirt.Libvirt) bool {
	c.lx.mu.Lock()
	defer c.lx.mu.Unlock()
	return l != nil && l == c.lx.l
}

func (c *Client) closeLXC() {
	c.lx.mu.Lock()
	defer c.lx.mu.Unlock()
	if c.lx.l != nil {
		_ = c.lx.l.Disconnect()
		c.lx.l = nil
	}
}

// listContainers is the containers' summaries; none when the driver is missing.
func (c *Client) listContainers() []Domain {
	l, err := c.lxc()
	if err != nil {
		return nil
	}
	doms, _, err := l.ConnectListAllDomains(1, libvirt.ConnectListDomainsActive|libvirt.ConnectListDomainsInactive)
	if err != nil {
		return nil
	}
	out := make([]Domain, 0, len(doms))
	for _, d := range doms {
		s, err := summary(l, d)
		if err != nil {
			continue
		}
		c.addMeta(l, d, &s)
		s.Kind = "container"
		out = append(out, s)
	}
	return out
}

// containerStats: the LXC driver has no bulk statistics; CPU time and memory come from
// each running container's info.
func (c *Client) containerStats() []DomainStats {
	l, err := c.lxc()
	if err != nil {
		return nil
	}
	doms, _, err := l.ConnectListAllDomains(1, libvirt.ConnectListDomainsActive)
	if err != nil {
		return nil
	}
	out := make([]DomainStats, 0, len(doms))
	for _, d := range doms {
		_, maxMem, mem, _, cpuTime, err := l.DomainGetInfo(d)
		if err != nil {
			continue
		}
		out = append(out, DomainStats{UUID: formatUUID(d.UUID), CPUTimeNs: cpuTime,
			Balloon: BalloonStats{CurrentKiB: mem, MaximumKiB: maxMem}})
	}
	return out
}

// nameTaken reports whether a machine or container is called name.
func (c *Client) nameTaken(l *libvirt.Libvirt, name string) bool {
	if _, err := l.DomainLookupByName(name); err == nil {
		return true
	}
	if x, err := c.lxc(); err == nil {
		if _, err := x.DomainLookupByName(name); err == nil {
			return true
		}
	}
	return false
}
