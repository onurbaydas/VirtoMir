package hostinfo

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Interface is one host network interface and how it is wired to others.
type Interface struct {
	Name string `json:"name"`
	// Kind: physical, bond, bridge, vlan, tap (a machine's NIC), veth, loopback, virtual.
	Kind      string   `json:"kind"`
	MAC       string   `json:"mac,omitempty"`
	MTU       int      `json:"mtu,omitempty"`
	State     string   `json:"state"`               // up, down, unknown, lowerlayerdown, …
	SpeedMbps int      `json:"speedMbps,omitempty"` // physical links only, when known
	Driver    string   `json:"driver,omitempty"`
	Master    string   `json:"master,omitempty"`  // the bond or bridge it belongs to
	Members   []string `json:"members,omitempty"` // bond slaves / bridge ports
	BondMode  string   `json:"bondMode,omitempty"`
	VLANID    int      `json:"vlanId,omitempty"`
	Parent    string   `json:"parent,omitempty"` // a VLAN's underlying interface
	Addresses []string `json:"addresses,omitempty"`
	RxBytes   uint64   `json:"rxBytes"`
	TxBytes   uint64   `json:"txBytes"`
}

// Interfaces lists the host's network interfaces with their bond/bridge/VLAN relations.
// addrs looks up the IP addresses (nil: net.InterfaceByName, which only works on the host).
func (r *Reader) Interfaces(addrs func(name string) []string) []Interface {
	if addrs == nil {
		addrs = hostAddrs
	}
	dirs, _ := filepath.Glob(r.path("sys/class/net/*"))
	vlans := r.vlanConfig()
	out := []Interface{}
	for _, d := range dirs {
		name := filepath.Base(d)
		rel := "sys/class/net/" + name
		it := Interface{Name: name, MAC: r.read(rel + "/address"), State: r.read(rel + "/operstate")}
		if v, ok := r.readInt(rel + "/mtu"); ok {
			it.MTU = int(v)
		}
		if v, ok := r.readInt(rel + "/statistics/rx_bytes"); ok {
			it.RxBytes = uint64(v)
		}
		if v, ok := r.readInt(rel + "/statistics/tx_bytes"); ok {
			it.TxBytes = uint64(v)
		}
		if link, err := os.Readlink(filepath.Join(d, "master")); err == nil {
			it.Master = filepath.Base(link)
		}
		if link, err := os.Readlink(filepath.Join(d, "device", "driver")); err == nil {
			it.Driver = filepath.Base(link)
		}
		_, hasDevice := os.Stat(filepath.Join(d, "device"))
		switch {
		case name == "lo":
			it.Kind = "loopback"
		case exists(filepath.Join(d, "bonding")):
			it.Kind = "bond"
			it.Members = strings.Fields(r.read(rel + "/bonding/slaves"))
			it.BondMode = strings.Fields(r.read(rel+"/bonding/mode") + " ")[0]
		case exists(filepath.Join(d, "bridge")):
			it.Kind = "bridge"
			ports, _ := filepath.Glob(filepath.Join(d, "brif", "*"))
			for _, p := range ports {
				it.Members = append(it.Members, filepath.Base(p))
			}
		case vlans[name].parent != "":
			it.Kind, it.VLANID, it.Parent = "vlan", vlans[name].id, vlans[name].parent
		case exists(filepath.Join(d, "tun_flags")):
			it.Kind = "tap"
		case hasDevice == nil:
			it.Kind = "physical"
		case r.read(rel+"/iflink") != r.read(rel+"/ifindex"):
			it.Kind = "veth"
		default:
			it.Kind = "virtual"
		}
		if it.Kind == "physical" || it.Kind == "bond" {
			if v, ok := r.readInt(rel + "/speed"); ok && v > 0 {
				it.SpeedMbps = int(v)
			}
		}
		sort.Strings(it.Members)
		it.Addresses = addrs(name)
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

type vlanInfo struct {
	id     int
	parent string
}

// vlanConfig reads /proc/net/vlan/config: "eth0.100 | 100 | eth0".
func (r *Reader) vlanConfig() map[string]vlanInfo {
	out := map[string]vlanInfo{}
	f, err := os.Open(r.path("proc/net/vlan/config"))
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Split(sc.Text(), "|")
		if len(parts) != 3 {
			continue
		}
		var id int
		for _, c := range strings.TrimSpace(parts[1]) {
			if c < '0' || c > '9' {
				id = -1
				break
			}
			id = id*10 + int(c-'0')
		}
		if id > 0 {
			out[strings.TrimSpace(parts[0])] = vlanInfo{id: id, parent: strings.TrimSpace(parts[2])}
		}
	}
	return out
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func hostAddrs(name string) []string {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return nil
	}
	as, err := ifc.Addrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range as {
		out = append(out, a.String())
	}
	return out
}

// ValidInterface reports whether name is an existing interface with a harmless name, so it
// can be handed to tools like tcpdump.
func (r *Reader) ValidInterface(name string) bool {
	if name == "" || len(name) > 15 || name[0] == '-' || name[0] == '.' {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_' || c == '@') {
			return false
		}
	}
	return exists(r.path("sys/class/net/" + name))
}
