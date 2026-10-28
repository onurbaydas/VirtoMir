package storconn

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"
)

// ports tell which protocol a server offers.
var ports = []struct {
	kind string
	port int
}{{KindISCSI, 3260}, {KindNFS, 2049}, {KindSMB, 445}}

// maxBits: larger subnets (a /16 bridge) are scanned only in the /24 around this host.
const maxBits = 22

// Networks are the IPv4 subnets of this host's interfaces that are up, loopback left out.
func Networks() []netip.Prefix {
	var out []netip.Prefix
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil {
				continue
			}
			ip, _ := netip.AddrFromSlice(ipn.IP.To4())
			bits, _ := ipn.Mask.Size()
			out = append(out, scanPrefix(ip, bits))
		}
	}
	return out
}

func scanPrefix(ip netip.Addr, bits int) netip.Prefix {
	if bits < maxBits {
		bits = 24
	}
	p, _ := ip.Prefix(bits)
	return p
}

// addresses lists the hosts of the prefixes (without network and broadcast addresses).
func addresses(nets []netip.Prefix) []netip.Addr {
	var out []netip.Addr
	for _, p := range nets {
		p = p.Masked()
		first, n := p.Addr(), 1<<(32-p.Bits())
		a := first
		for i := 0; i < n; i, a = i+1, a.Next() {
			if n > 2 && (i == 0 || i == n-1) {
				continue
			}
			if !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	return out
}

// Dial checks whether a TCP port answers; tests replace it.
var Dial = func(ctx context.Context, addr string) bool {
	d := net.Dialer{Timeout: 400 * time.Millisecond}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// Probe finds the servers in nets (and the extra addresses) that answer on a storage port:
// server → kinds it offers.
func Probe(ctx context.Context, nets []netip.Prefix, extra ...string) map[string][]string {
	targets := []string{}
	for _, a := range addresses(nets) {
		targets = append(targets, a.String())
	}
	for _, e := range extra {
		if !slices.Contains(targets, e) {
			targets = append(targets, e)
		}
	}
	found := map[string][]string{}
	var mu sync.Mutex
	sem := make(chan struct{}, 256)
	var wg sync.WaitGroup
	for _, host := range targets {
		for _, p := range ports {
			wg.Add(1)
			sem <- struct{}{}
			go func(host, kind string, port int) {
				defer func() { <-sem; wg.Done() }()
				if Dial(ctx, net.JoinHostPort(host, strconv.Itoa(port))) {
					mu.Lock()
					found[host] = append(found[host], kind)
					mu.Unlock()
				}
			}(host, p.kind, p.port)
		}
	}
	wg.Wait()
	for h := range found {
		slices.Sort(found[h])
	}
	return found
}
