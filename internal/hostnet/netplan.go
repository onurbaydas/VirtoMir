package hostnet

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/onurbaydas/virtomir/internal/hostinfo"
)

// netplan: new definitions go into /etc/netplan/90-virtomir.yaml. Taking over a port's
// address edits the file that defines the port (netplan merges files by appending, so a
// later file cannot remove the port's addresses); the keys moved are kept in the record and
// given back on removal. When that file is cloud-init's, cloud-init is told to stop
// rewriting the network configuration.
type netplanBackend struct{ m *Manager }

func (b *netplanBackend) name() string { return "netplan" }

const netplanOwn = "etc/netplan/90-virtomir.yaml"

var netplanSections = []string{"ethernets", "bonds", "bridges", "vlans", "wifis", "tunnels", "vrfs", "modems", "dummy-devices", "virtual-ethernets"}

// netplanAddrKeys is a device's IP configuration: what moves to a bridge or bond.
var netplanAddrKeys = []string{"dhcp4", "dhcp6", "addresses", "routes", "gateway4", "gateway6", "nameservers", "accept-ra",
	"ipv6-privacy", "dhcp4-overrides", "dhcp6-overrides", "routing-policy", "dhcp-identifier", "ipv6-address-generation",
	"ipv6-address-token", "link-local"}

type npFile struct {
	path    string
	doc     *yaml.Node
	changed bool
}

func (b *netplanBackend) load() ([]*npFile, *npFile, error) {
	paths, _ := filepath.Glob(b.m.path("etc/netplan/*.yaml"))
	slices.Sort(paths)
	var files []*npFile
	var own *npFile
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, err
		}
		f := &npFile{path: p, doc: &yaml.Node{}}
		if err := yaml.Unmarshal(raw, f.doc); err != nil {
			return nil, nil, invalidf("%s is not valid YAML: %v", p, err)
		}
		if f.doc.Kind == 0 {
			f.doc = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
		}
		files = append(files, f)
		if p == b.m.path(netplanOwn) {
			own = f
		}
	}
	if own == nil {
		own = &npFile{path: b.m.path(netplanOwn), doc: &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}}
		files = append(files, own)
	}
	return files, own, nil
}

func scalar(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: v} }
func boolean(v bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(v)}
}
func mapping() *yaml.Node { return &yaml.Node{Kind: yaml.MappingNode} }
func sequence(vs []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, v := range vs {
		n.Content = append(n.Content, scalar(v))
	}
	return n
}

func mapGet(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func mapSet(n *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content[i+1] = v
			return
		}
	}
	n.Content = append(n.Content, scalar(key), v)
}

func mapDel(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			v := n.Content[i+1]
			n.Content = slices.Delete(n.Content, i, i+2)
			return v
		}
	}
	return nil
}

func ensureMap(n *yaml.Node, key string) *yaml.Node {
	if v := mapGet(n, key); v != nil && v.Kind == yaml.MappingNode {
		return v
	}
	v := mapping()
	mapSet(n, key, v)
	return v
}

func (f *npFile) network() *yaml.Node {
	root := f.doc.Content[0]
	net := ensureMap(root, "network")
	if mapGet(net, "version") == nil {
		net.Content = append([]*yaml.Node{scalar("version"), scalar("2")}, net.Content...)
	}
	return net
}

// find returns the file and node defining a device.
func find(files []*npFile, name string) (*npFile, *yaml.Node) {
	for _, f := range files {
		net := mapGet(f.doc.Content[0], "network")
		for _, sec := range netplanSections {
			if d := mapGet(mapGet(net, sec), name); d != nil {
				return f, d
			}
		}
	}
	return nil, nil
}

func encode(n *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	e := yaml.NewEncoder(&buf)
	e.SetIndent(2)
	if err := e.Encode(n); err != nil {
		return nil, err
	}
	_ = e.Close()
	return buf.Bytes(), nil
}

func section(kind string) string {
	switch kind {
	case "bond":
		return "bonds"
	case "vlan":
		return "vlans"
	}
	return "bridges"
}

func (b *netplanBackend) create(_ context.Context, s Spec, ifs map[string]hostinfo.Interface) (plan, error) {
	files, own, err := b.load()
	if err != nil {
		return plan{}, err
	}
	rec := &Record{}
	def := mapping()
	switch s.Kind {
	case "vlan":
		mapSet(def, "id", scalar(strconv.Itoa(s.VLAN)))
		mapSet(def, "link", scalar(s.Parent))
		if f, _ := find(files, s.Parent); f == nil {
			e := mapping()
			mapSet(e, "dhcp4", boolean(false))
			mapSet(e, "dhcp6", boolean(false))
			mapSet(ensureMap(own.network(), "ethernets"), s.Parent, e)
			rec.Added = append(rec.Added, s.Parent)
		}
	default:
		mapSet(def, "interfaces", sequence(s.Ports))
		params := mapping()
		if s.Kind == "bond" {
			mapSet(params, "mode", scalar(s.BondMode))
			mapSet(params, "mii-monitor-interval", scalar("100"))
		} else {
			mapSet(params, "stp", boolean(s.STP))
			mapSet(params, "forward-delay", scalar("0"))
		}
		mapSet(def, "parameters", params)
		for _, p := range s.Ports {
			f, node := find(files, p)
			if p == s.TakeAddressFrom {
				if node == nil {
					return plan{}, refusedf("netplan does not configure %s's address, so VirtoMir cannot move it.", p)
				}
				moved := mapping()
				for _, k := range netplanAddrKeys {
					if v := mapDel(node, k); v != nil {
						moved.Content = append(moved.Content, scalar(k), v)
						def.Content = append(def.Content, scalar(k), v)
					}
				}
				if len(moved.Content) == 0 {
					return plan{}, refusedf("netplan has no address settings for %s.", p)
				}
				mapSet(node, "dhcp4", boolean(false))
				mapSet(node, "dhcp6", boolean(false))
				out, err := encode(moved)
				if err != nil {
					return plan{}, err
				}
				rec.Moved = string(out)
				f.changed = true
				// The bridge keeps the port's MAC, so DHCP hands out the same address.
				if mac := ifs[p].MAC; s.Kind == "bridge" && mac != "" {
					mapSet(def, "macaddress", scalar(mac))
				}
				continue
			}
			if node == nil {
				e := mapping()
				mapSet(e, "dhcp4", boolean(false))
				mapSet(e, "dhcp6", boolean(false))
				mapSet(ensureMap(own.network(), "ethernets"), p, e)
				rec.Added = append(rec.Added, p)
			}
		}
		if s.TakeAddressFrom == "" {
			mapSet(def, "dhcp4", boolean(false))
			mapSet(def, "dhcp6", boolean(false))
			mapSet(def, "link-local", sequence(nil))
		}
	}
	mapSet(ensureMap(own.network(), section(s.Kind)), s.Name, def)
	own.changed = true

	p := newPlan()
	for _, f := range files {
		if !f.changed {
			continue
		}
		out, err := encode(f.doc)
		if err != nil {
			return plan{}, err
		}
		p.writes[f.path], p.modes[f.path] = out, 0o600
		if filepath.Base(f.path) == "50-cloud-init.yaml" {
			if _, err := os.Stat(b.m.path("etc/cloud/cloud.cfg.d")); err == nil {
				p.writes[b.m.path("etc/cloud/cloud.cfg.d/99-virtomir-network.cfg")] = []byte("# VirtoMir moved an address in /etc/netplan/50-cloud-init.yaml; keep cloud-init from rewriting it.\nnetwork: {config: disabled}\n")
			}
		}
	}
	p.apply = []cmd{must("netplan", "generate"), must("netplan", "apply")}
	p.undo = []cmd{try("ip", "link", "delete", s.Name)}
	p.reapply = []cmd{try("netplan", "apply")}
	p.record = rec
	return p, nil
}

func (b *netplanBackend) remove(_ context.Context, r Record) (plan, error) {
	files, own, err := b.load()
	if err != nil {
		return plan{}, err
	}
	net := own.network()
	mapDel(mapGet(net, section(r.Kind)), r.Name)
	for _, a := range r.Added {
		mapDel(mapGet(net, "ethernets"), a)
	}
	own.changed = true
	if r.TakeAddressFrom != "" && r.Moved != "" {
		if f, node := find(files, r.TakeAddressFrom); node != nil {
			var moved yaml.Node
			if err := yaml.Unmarshal([]byte(r.Moved), &moved); err == nil && len(moved.Content) == 1 {
				mapDel(node, "dhcp4")
				mapDel(node, "dhcp6")
				node.Content = append(node.Content, moved.Content[0].Content...)
				f.changed = true
			}
		}
	}
	p := newPlan()
	for _, f := range files {
		if f.changed {
			out, err := encode(f.doc)
			if err != nil {
				return plan{}, err
			}
			p.writes[f.path], p.modes[f.path] = out, 0o600
		}
	}
	if netplanEmpty(net) {
		p.writes[own.path] = nil // nothing of VirtoMir's left
	}
	// The device itself goes before netplan applies again, so the ports are free for
	// their own configuration.
	p.apply = []cmd{must("netplan", "generate"), try("ip", "link", "delete", r.Name), must("netplan", "apply")}
	p.reapply = []cmd{try("netplan", "apply")}
	return p, nil
}

// netplanEmpty: a network section with nothing but its version and empty sections.
func netplanEmpty(net *yaml.Node) bool {
	for i := 0; i+1 < len(net.Content); i += 2 {
		k, v := net.Content[i].Value, net.Content[i+1]
		if k == "version" || (v.Kind == yaml.MappingNode && len(v.Content) == 0) {
			continue
		}
		return false
	}
	return true
}
