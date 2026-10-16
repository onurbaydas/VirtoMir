package hostnet

import (
	"context"
	"strconv"
	"strings"

	"github.com/onurbaydas/virtomir/internal/hostinfo"
)

// NetworkManager: everything goes through nmcli, as connections named virtomir-<name>
// (and virtomir-<name>-<port> for ports). A port's previous connection is not touched; it
// simply gives way when the port connection comes up, and comes back on removal or
// rollback.
type nmBackend struct{ m *Manager }

func (b *nmBackend) name() string { return "networkmanager" }

func (b *nmBackend) get(ctx context.Context, args ...string) []string {
	out, err := b.m.Run(ctx, append([]string{"nmcli", "-g"}, args...)...)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") // empty trailing fields count
	for i, l := range lines {
		lines[i] = strings.ReplaceAll(l, `\:`, ":") // -g escapes colons (IPv6 addresses)
	}
	return lines
}

// nmIPFields are copied from the port's connection when the address moves.
var nmIPFields = []string{"ipv4.method", "ipv4.addresses", "ipv4.gateway", "ipv4.dns", "ipv4.dns-search", "ipv4.routes",
	"ipv6.method", "ipv6.addresses", "ipv6.gateway", "ipv6.dns", "ipv6.dns-search", "ipv6.routes"}

func (b *nmBackend) create(ctx context.Context, s Spec, ifs map[string]hostinfo.Interface) (plan, error) {
	conn := "virtomir-" + s.Name
	add := []string{"nmcli", "connection", "add", "type", s.Kind, "con-name", conn, "ifname", s.Name}
	switch s.Kind {
	case "bridge":
		add = append(add, "bridge.stp", yesNo(s.STP), "bridge.forward-delay", "2", "connection.autoconnect-slaves", "yes")
	case "bond":
		add = append(add, "bond.options", "mode="+s.BondMode+",miimon=100", "connection.autoconnect-slaves", "yes")
	case "vlan":
		add = append(add, "dev", s.Parent, "id", strconv.Itoa(s.VLAN))
	}
	if s.TakeAddressFrom != "" {
		old := b.get(ctx, "GENERAL.CONNECTION", "device", "show", s.TakeAddressFrom)
		if len(old) == 0 || old[0] == "" {
			return plan{}, refusedf("NetworkManager has no active connection on %s, so there is no address to take over.", s.TakeAddressFrom)
		}
		vals := b.get(ctx, strings.Join(nmIPFields, ","), "connection", "show", old[0])
		if len(vals) != len(nmIPFields) {
			return plan{}, refusedf("Could not read the address settings of %s.", old[0])
		}
		for i, f := range nmIPFields {
			if vals[i] != "" {
				add = append(add, f, vals[i])
			}
		}
		if mac := ifs[s.TakeAddressFrom].MAC; s.Kind == "bridge" && mac != "" {
			add = append(add, "bridge.mac-address", mac) // DHCP hands out the same address
		}
	} else {
		add = append(add, "ipv4.method", "disabled", "ipv6.method", "disabled")
	}

	rec := &Record{OldConns: map[string]string{}}
	create := []cmd{must(add...)}
	up := []cmd{must("nmcli", "connection", "up", conn)}
	undo := []cmd{}
	reapply := []cmd{}
	for _, port := range s.Ports {
		typ := b.get(ctx, "GENERAL.TYPE", "device", "show", port)
		cur := b.get(ctx, "GENERAL.CONNECTION", "device", "show", port)
		old := ""
		if len(cur) > 0 {
			old = cur[0]
		}
		switch {
		case len(typ) > 0 && typ[0] == "ethernet":
			pc := conn + "-" + port
			create = append(create, must("nmcli", "connection", "add", "type", "ethernet", "con-name", pc, "ifname", port, "master", s.Name, "slave-type", s.Kind))
			up = append(up, must("nmcli", "connection", "up", pc))
			undo = append(undo, try("nmcli", "connection", "delete", pc))
			if old != "" {
				rec.OldConns[port] = old
				reapply = append(reapply, try("nmcli", "connection", "up", old))
			}
		case len(typ) > 0 && typ[0] == "bond" && s.Kind == "bridge" && old != "":
			create = append(create, must("nmcli", "connection", "modify", old, "connection.master", s.Name, "connection.slave-type", "bridge"))
			up = append(up, must("nmcli", "connection", "up", old))
			undo = append(undo, try("nmcli", "connection", "modify", old, "connection.master", "", "connection.slave-type", ""))
			reapply = append(reapply, try("nmcli", "connection", "up", old))
			rec.OldConns[port] = "bond:" + old
		default:
			return plan{}, refusedf("%s is not an Ethernet port or bond NetworkManager knows; VirtoMir cannot add it.", port)
		}
	}
	undo = append(undo, try("nmcli", "connection", "delete", conn))
	p := newPlan()
	p.apply = append(create, up...)
	p.undo, p.reapply = undo, reapply
	rec.Recreate = p.apply
	p.record = rec
	return p, nil
}

func (b *nmBackend) remove(_ context.Context, r Record) (plan, error) {
	conn := "virtomir-" + r.Name
	p := newPlan()
	for _, port := range r.Ports {
		if old, ok := strings.CutPrefix(r.OldConns[port], "bond:"); ok {
			p.apply = append(p.apply, try("nmcli", "connection", "modify", old, "connection.master", "", "connection.slave-type", ""))
			continue
		}
		p.apply = append(p.apply, try("nmcli", "connection", "delete", conn+"-"+port))
	}
	p.apply = append(p.apply, must("nmcli", "connection", "delete", conn))
	for _, port := range r.Ports {
		if old := strings.TrimPrefix(r.OldConns[port], "bond:"); old != "" {
			p.apply = append(p.apply, try("nmcli", "connection", "up", old))
		}
	}
	// Rolling back a removal makes the connections again.
	for _, c := range r.Recreate {
		p.reapply = append(p.reapply, try(c.Args...))
	}
	return p, nil
}
