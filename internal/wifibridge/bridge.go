package wifibridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Runner runs a command on the host (ceph.Exec does).
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Config is one Wi-Fi bridge: the machines' bridge (no address, no DHCP: the LAN's router
// answers), the veth between it and the relay, and the Wi-Fi card.
type Config struct {
	Network string `json:"network"` // the libvirt network on Bridge
	Bridge  string `json:"bridge"`  // wbr0, …
	Uplink  string `json:"uplink"`  // the Wi-Fi card
}

func (c Config) port() string  { return c.Bridge + "-p" } // the bridge's end of the veth
func (c Config) relay() string { return c.Bridge + "-r" } // the relay's end

const nftTable = "virtomir_wifi"

// Setup makes the bridge, the veth and the firewall set (idempotent: also after a reboot).
func Setup(ctx context.Context, r Runner, c Config) error {
	try := func(args ...string) { _, _ = r.Run(ctx, args[0], args[1:]...) }
	must := func(args ...string) error {
		if _, err := r.Run(ctx, args[0], args[1:]...); err != nil {
			return fmt.Errorf("%v: %w", args, err)
		}
		return nil
	}
	try("ip", "link", "add", c.Bridge, "type", "bridge")
	try("ip", "link", "add", c.port(), "type", "veth", "peer", "name", c.relay())
	for _, args := range [][]string{
		{"ip", "link", "set", c.port(), "master", c.Bridge},
		{"ip", "link", "set", c.Bridge, "up"},
		{"ip", "link", "set", c.port(), "up"},
		{"ip", "link", "set", c.relay(), "up"},
	} {
		if err := must(args...); err != nil {
			return err
		}
	}
	// Frames are relayed one by one: no merged super-frames larger than the Wi-Fi MTU, from
	// the machines (the bridge segments for the port) or from the card.
	try("ethtool", "-K", c.port(), "tso", "off", "gso", "off", "gro", "off")
	try("ethtool", "-K", c.relay(), "tso", "off", "gso", "off", "gro", "off")
	try("ethtool", "-K", c.Uplink, "gro", "off", "lro", "off")
	// Frames for the machines also reach the host's IP stack on the card: dropped there, so
	// the host does not route them a second time.
	set, chain := "vms_"+c.Bridge, "pre_"+c.Bridge
	for _, args := range [][]string{
		{"nft", "add", "table", "inet", nftTable},
		{"nft", "add", "set", "inet", nftTable, set, "{", "type", "ipv4_addr", ";", "}"},
		{"nft", "add", "chain", "inet", nftTable, chain, "{", "type", "filter", "hook", "prerouting", "priority", "raw", ";", "}"},
		{"nft", "flush", "chain", "inet", nftTable, chain},
		// Never the host's own addresses, whatever lands in the set.
		{"nft", "add", "rule", "inet", nftTable, chain, "iifname", c.Uplink, "ip", "daddr", "@" + set, "fib", "daddr", "type", "!=", "local", "drop"},
	} {
		if err := must(args...); err != nil {
			return err
		}
	}
	return nil
}

// Teardown undoes Setup.
func Teardown(ctx context.Context, r Runner, c Config) {
	for _, args := range [][]string{
		{"nft", "delete", "chain", "inet", nftTable, "pre_" + c.Bridge},
		{"nft", "delete", "set", "inet", nftTable, "vms_" + c.Bridge},
		{"ip", "link", "del", c.port()},
		{"ip", "link", "del", c.Bridge},
	} {
		_, _ = r.Run(ctx, args[0], args[1:]...)
	}
}

// table remembers the machines' addresses; a new one is added to the firewall set and gets a
// host route to the bridge, so the host itself reaches the machine too.
type table struct {
	mu    sync.Mutex
	m     map[netip.Addr]MAC
	learn func(netip.Addr)
	// local reports the host's own addresses: never a machine's. Learning one would drop the
	// host's own traffic on the card and route its address into the bridge.
	local func(netip.Addr) bool
}

func (t *table) Lookup(ip netip.Addr) (MAC, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.m[ip]
	return m, ok
}

func (t *table) Learn(ip netip.Addr, mac MAC) {
	if !ip.Is4() || ip.IsMulticast() || ip.IsLoopback() || ip == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return
	}
	t.mu.Lock()
	old, ok := t.m[ip]
	t.mu.Unlock()
	if ok && old == mac {
		return
	}
	if t.local != nil && t.local(ip) {
		return
	}
	t.mu.Lock()
	t.m[ip] = mac
	t.mu.Unlock()
	t.learn(ip)
}

// hostAddr reports whether an interface of this host has the address.
func hostAddr(ip netip.Addr) bool {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if h, ok := netip.AddrFromSlice(n.IP); ok && h.Unmap() == ip {
				return true
			}
		}
	}
	return false
}

// fromHost reports whether a frame on the bridge comes from the host itself (its bridge or
// veth MAC): the host talks on the card directly, and its frames are not a machine's.
func fromHost(f []byte, own []MAC) bool {
	if len(f) < ethLen {
		return true
	}
	src := MAC(f[6:12])
	for _, m := range own {
		if src == m {
			return true
		}
	}
	return false
}

// Relay moves frames between the card and the bridge until ctx ends.
func Relay(ctx context.Context, r Runner, c Config, log *slog.Logger) error {
	upIf, err := net.InterfaceByName(c.Uplink)
	if err != nil {
		return err
	}
	brIf, err := net.InterfaceByName(c.relay())
	if err != nil {
		return err
	}
	var host MAC
	copy(host[:], upIf.HardwareAddr)
	up, err := packetSocket(upIf.Index)
	if err != nil {
		return fmt.Errorf("open %s: %w", c.Uplink, err)
	}
	br, err := packetSocket(brIf.Index)
	if err != nil {
		unix.Close(up)
		return fmt.Errorf("open %s: %w", c.relay(), err)
	}
	var own []MAC
	for _, name := range []string{c.Bridge, c.port(), c.relay()} {
		if ifc, err := net.InterfaceByName(name); err == nil && len(ifc.HardwareAddr) == 6 {
			own = append(own, MAC(ifc.HardwareAddr))
		}
	}
	tab := &table{m: map[netip.Addr]MAC{}, local: hostAddr, learn: func(ip netip.Addr) {
		_, _ = r.Run(context.Background(), "nft", "add", "element", "inet", nftTable, "vms_"+c.Bridge, "{", ip.String(), "}")
		_, _ = r.Run(context.Background(), "ip", "route", "replace", ip.String()+"/32", "dev", c.Bridge)
		log.Info("wifi bridge: machine on the LAN", "bridge", c.Bridge, "ip", ip)
	}}
	go func() { <-ctx.Done(); unix.Close(up); unix.Close(br) }()
	errc := make(chan error, 2)
	go func() { errc <- pump(up, br, func(f []byte) ([]byte, bool) { return Inbound(f, host, tab) }) }()
	go func() {
		errc <- pump(br, up, func(f []byte) ([]byte, bool) {
			if fromHost(f, own) {
				return nil, false
			}
			return Outbound(f, host, tab)
		})
	}()
	gw := gateway(ctx, r, c.Uplink)
	guard := make(chan struct{})
	if gw.IsValid() && reaches(ctx, r, c.Uplink, gw) {
		go func() { watch(ctx, r, c.Uplink, gw, 5*time.Second, 3); close(guard) }()
	}
	select {
	case err = <-errc:
	case <-guard:
		err = ErrHostCutOff
	}
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// ErrHostCutOff: the host lost its own gateway while the bridge ran; the bridge is taken down.
var ErrHostCutOff = errors.New("the host stopped reaching its gateway while the Wi-Fi bridge ran; the bridge was taken down to give the host its connection back")

// gateway is the default route's next hop through the uplink.
func gateway(ctx context.Context, r Runner, uplink string) netip.Addr {
	out, err := r.Run(ctx, "ip", "-4", "route", "show", "default", "dev", uplink)
	if err != nil {
		return netip.Addr{}
	}
	f := strings.Fields(string(out))
	for i := 0; i+1 < len(f); i++ {
		if f[i] == "via" {
			a, _ := netip.ParseAddr(f[i+1])
			return a
		}
	}
	return netip.Addr{}
}

func reaches(ctx context.Context, r Runner, uplink string, gw netip.Addr) bool {
	_, err := r.Run(ctx, "ping", "-c", "1", "-W", "2", "-I", uplink, gw.String())
	return err == nil
}

// watch returns when the host misses its gateway misses times in a row (or ctx ends).
func watch(ctx context.Context, r Runner, uplink string, gw netip.Addr, every time.Duration, misses int) {
	t := time.NewTicker(every)
	defer t.Stop()
	missed := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if reaches(ctx, r, uplink, gw) {
			missed = 0
		} else if missed++; missed >= misses {
			return
		}
	}
}

func packetSocket(ifindex int) (int, error) {
	proto := int(htons(unix.ETH_P_ALL))
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, proto)
	if err != nil {
		return -1, err
	}
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: htons(unix.ETH_P_ALL), Ifindex: ifindex}); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

// pump reads frames from one socket, translates them and writes them to the other. Frames
// the socket itself sent (the other pump's) are skipped.
func pump(from, to int, translate func([]byte) ([]byte, bool)) error {
	buf := make([]byte, 65536)
	for {
		n, sa, err := unix.Recvfrom(from, buf, 0)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return err
		}
		if ll, ok := sa.(*unix.SockaddrLinklayer); ok && ll.Pkttype == unix.PACKET_OUTGOING {
			continue
		}
		if out, ok := translate(buf[:n]); ok {
			_, _ = unix.Write(to, out) // a frame too large or a full queue is dropped, like on a wire
		}
	}
}

// Manager runs the Wi-Fi bridges of this host and keeps them across restarts.
type Manager struct {
	Runner Runner
	Log    *slog.Logger
	File   string // <state dir>/wifibridges.json

	mu      sync.Mutex
	bridges map[string]Config // by network
	stop    map[string]context.CancelFunc
}

// Load starts the bridges saved in File (after a restart or a reboot).
func (m *Manager) Load(ctx context.Context) {
	b, err := os.ReadFile(m.File)
	if err != nil {
		return
	}
	var cs []Config
	if json.Unmarshal(b, &cs) != nil {
		return
	}
	for _, c := range cs {
		if err := m.start(ctx, c); err != nil {
			m.Log.Warn("wifi bridge", "network", c.Network, "err", err)
		}
	}
}

// Add sets a bridge up, starts its relay and saves it.
func (m *Manager) Add(ctx context.Context, c Config) error {
	if err := m.start(ctx, c); err != nil {
		return err
	}
	return m.save()
}

func (m *Manager) start(ctx context.Context, c Config) error {
	if err := Setup(ctx, m.Runner, c); err != nil {
		return err
	}
	m.mu.Lock()
	if m.bridges == nil {
		m.bridges, m.stop = map[string]Config{}, map[string]context.CancelFunc{}
	}
	if stop := m.stop[c.Network]; stop != nil {
		stop()
	}
	rctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	m.bridges[c.Network], m.stop[c.Network] = c, cancel
	m.mu.Unlock()
	go func() {
		err := Relay(rctx, m.Runner, c, m.Log)
		if err == nil {
			return
		}
		m.Log.Error("wifi bridge relay stopped", "network", c.Network, "err", err)
		if errors.Is(err, ErrHostCutOff) {
			m.Remove(context.Background(), c.Network)
		}
	}()
	return nil
}

// Remove stops the bridge of a network (if it has one) and takes it down.
func (m *Manager) Remove(ctx context.Context, network string) {
	m.mu.Lock()
	c, ok := m.bridges[network]
	if ok {
		m.stop[network]()
		delete(m.bridges, network)
		delete(m.stop, network)
	}
	m.mu.Unlock()
	if ok {
		Teardown(ctx, m.Runner, c)
		_ = m.save()
	}
}

// Networks are the networks with a Wi-Fi bridge.
func (m *Manager) Networks() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for n := range m.bridges {
		out = append(out, n)
	}
	return out
}

// FreeBridge is the first unused wbrN name.
func (m *Manager) FreeBridge() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := 0; ; i++ {
		name := fmt.Sprintf("wbr%d", i)
		taken := false
		for _, c := range m.bridges {
			taken = taken || c.Bridge == name
		}
		if _, err := net.InterfaceByName(name); err == nil {
			taken = true
		}
		if !taken {
			return name
		}
	}
}

func (m *Manager) save() error {
	m.mu.Lock()
	cs := make([]Config, 0, len(m.bridges))
	for _, c := range m.bridges {
		cs = append(cs, c)
	}
	m.mu.Unlock()
	b, err := json.MarshalIndent(cs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.File), 0o755); err != nil {
		return err
	}
	return os.WriteFile(m.File, b, 0o600)
}
