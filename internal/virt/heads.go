package virt

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"libvirt.org/go/libvirtxml"
)

// Extra monitors in the browser console (P6). libvirt's VNC server shows only the first
// head of a video card; QEMU can serve every other head on a VNC server of its own. VirtoMir
// adds those servers as marked QEMU arguments (visible on the QEMU command line card), on
// 127.0.0.1 only, on the first free port from 6000 on (QEMU picks it, VirtoMir asks QEMU).

const headIDPrefix = "id=virtomir-head"

// headArg is the -vnc value for head n (n >= 1).
func headArg(n uint) string {
	return fmt.Sprintf("127.0.0.1:100,to=199,%s%d,display=video0,head=%d", headIDPrefix, n, n)
}

// isHeadArg reports whether a QEMU argument is one of ours (the value after "-vnc").
func isHeadArg(v string) bool { return strings.Contains(v, headIDPrefix) }

// syncHeadArgs makes the QEMU command line serve heads 1…heads-1 of the primary video card
// (and nothing more), leaving every other argument alone. It returns whether it changed.
func syncHeadArgs(x *libvirtxml.Domain, heads uint) bool {
	var kept []libvirtxml.DomainQEMUCommandlineArg
	var old int
	if q := x.QEMUCommandline; q != nil {
		for i := 0; i < len(q.Args); i++ {
			a := q.Args[i]
			if a.Value == "-vnc" && i+1 < len(q.Args) && isHeadArg(q.Args[i+1].Value) {
				i++
				old++
				continue
			}
			kept = append(kept, a)
		}
	}
	want := 0
	if heads > 1 && hasVNC(x) {
		want = int(heads) - 1
		for n := uint(1); n < heads; n++ {
			kept = append(kept, libvirtxml.DomainQEMUCommandlineArg{Value: "-vnc"}, libvirtxml.DomainQEMUCommandlineArg{Value: headArg(n)})
		}
	}
	if old == want {
		return false
	}
	if len(kept) == 0 && (x.QEMUCommandline == nil || len(x.QEMUCommandline.Envs) == 0) {
		x.QEMUCommandline = nil
		return true
	}
	if x.QEMUCommandline == nil {
		x.QEMUCommandline = &libvirtxml.DomainQEMUCommandline{}
	}
	x.QEMUCommandline.Args = kept
	return true
}

func hasVNC(x *libvirtxml.Domain) bool {
	if x.Devices == nil {
		return false
	}
	for _, g := range x.Devices.Graphics {
		if g.VNC != nil {
			return true
		}
	}
	return false
}

// liveHeads is how many monitors the running machine serves (1 + our extra VNC servers).
func liveHeads(x *libvirtxml.Domain) int {
	n := 1
	if q := x.QEMUCommandline; q != nil {
		for _, a := range q.Args {
			if isHeadArg(a.Value) {
				n++
			}
		}
	}
	return n
}

// headAddr asks QEMU where the VNC server of head n listens.
func (c *Client) headAddr(uuid string, n int) (string, error) {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return "", err
	}
	out, err := l.QEMUDomainMonitorCommand(d, `{"execute":"query-vnc-servers"}`, 0)
	if err != nil {
		return "", classify(err)
	}
	var r struct {
		Return []struct {
			ID     string `json:"id"`
			Server []struct {
				Host    string `json:"host"`
				Service string `json:"service"`
			} `json:"server"`
		} `json:"return"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		return "", fmt.Errorf("query-vnc-servers: %w", err)
	}
	id := fmt.Sprintf("virtomir-head%d", n)
	for _, s := range r.Return {
		if s.ID == id && len(s.Server) > 0 {
			return net.JoinHostPort(s.Server[0].Host, s.Server[0].Service), nil
		}
	}
	return "", refusedf("Monitor %d is not served yet: restart the machine after changing the number of monitors.", n+1)
}

// DialVNCHead connects to monitor head (0 = the first, served by libvirt).
func (c *Client) DialVNCHead(ctx context.Context, uuid string, head int) (net.Conn, error) {
	if head <= 0 {
		return c.DialVNC(ctx, uuid)
	}
	addr, err := c.headAddr(uuid, head)
	if err != nil {
		return nil, err
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connect to monitor %d: %w", head+1, err)
	}
	return conn, nil
}
