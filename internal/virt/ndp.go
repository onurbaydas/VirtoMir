package virt

import (
	"bufio"
	"context"
	"net/netip"
	"os/exec"
	"strings"
	"time"
)

// IPv6 guests that configure themselves (SLAAC) never show up in DHCP leases, and libvirt's
// "arp" source reads only the IPv4 ARP table. The host's IPv6 neighbour table (NDP) knows
// them by MAC, the same way the ARP table knows bridged IPv4 guests.

// neighbors6 is the host's IPv6 neighbour table as MAC → addresses (nil when ip is missing).
var neighbors6 = func(ctx context.Context) map[string][]string {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ip", "-6", "neigh", "show").Output()
	if err != nil {
		return nil
	}
	return parseNeighbors6(string(out))
}

// parseNeighbors6 reads `ip -6 neigh show` lines such as
//
//	2001:db8::5 dev vnet0 lladdr 52:54:00:aa:bb:cc REACHABLE
//
// keeping global and unique-local addresses of entries that have a MAC and are not FAILED.
// Link-local fe80:: addresses say nothing useful about where to reach a machine.
func parseNeighbors6(out string) map[string][]string {
	res := map[string][]string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		addr, err := netip.ParseAddr(f[0])
		if err != nil || !addr.Is6() || addr.Is4In6() || !addr.IsGlobalUnicast() || addr.IsLinkLocalUnicast() {
			continue
		}
		mac := ""
		failed := false
		for i, w := range f {
			switch w {
			case "lladdr":
				if i+1 < len(f) {
					mac = strings.ToLower(f[i+1])
				}
			case "FAILED", "INCOMPLETE":
				failed = true
			}
		}
		if mac == "" || failed {
			continue
		}
		res[mac] = appendNew(res[mac], addr.String())
	}
	return res
}

// appendNew appends a to list unless an entry for the same address ("a" or "a/prefix") is there.
func appendNew(list []string, a string) []string {
	for _, x := range list {
		if x == a || strings.HasPrefix(x, a+"/") {
			return list
		}
	}
	return append(list, a)
}
