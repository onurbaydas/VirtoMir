package confighist

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"libvirt.org/go/libvirtxml"
)

// What libvirt writes into a running machine's XML by itself, without anyone changing the
// machine: the runtime ID, device aliases, graphics ports picked at start, security
// labels, tap device names and pseudo-terminal paths. Leaving these out keeps a live
// version from being recorded on every start or scan.
var liveNoise = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`(<domain\b[^>]*?)\s+id='[0-9]+'`), "$1"},
	{regexp.MustCompile(`\n?[ \t]*<alias\b[^>]*/>`), ""},
	{regexp.MustCompile(`(?s)\n?[ \t]*<seclabel\b[^>]*/>`), ""},
	{regexp.MustCompile(`(?s)\n?[ \t]*<seclabel\b[^>]*[^/]>.*?</seclabel>`), ""},
	{regexp.MustCompile(`\n?[ \t]*<target dev='(?:vnet|tap|macvtap|macvlan)[0-9]+'/>`), ""},
	{regexp.MustCompile(`\n?[ \t]*<source path='/dev/pts/[0-9]+'/>`), ""},
	{regexp.MustCompile(`\s+tty='/dev/pts/[0-9]+'`), ""},
}

var graphicsTag = regexp.MustCompile(`<graphics\b[^>]*>`)
var graphicsPort = regexp.MustCompile(`\s+(?:port|tlsPort|websocket)='-?[0-9]+'`)

// NormalizeLive removes the parts of a running machine's XML that libvirt sets by itself
// (see liveNoise), so two live XMLs differ only when the machine was changed.
func NormalizeLive(doc string) string {
	for _, n := range liveNoise {
		doc = n.re.ReplaceAllString(doc, n.with)
	}
	return graphicsTag.ReplaceAllStringFunc(doc, func(tag string) string { return graphicsPort.ReplaceAllString(tag, "") })
}

// LiveChange is one difference between a running machine and its saved definition: a
// device added or removed only live (gone at the next shutdown), or a setting changed live.
type LiveChange struct {
	Kind  string `json:"kind"` // "added" (only live), "removed" (only in the definition) or "changed"
	What  string `json:"what"` // e.g. "disk vdb", "network adapter 52:54:00:…", "vCPUs"
	Live  string `json:"live,omitempty"`
	Saved string `json:"saved,omitempty"`
}

// CompareLive lists how the running configuration (live) differs from the saved
// definition (saved) in what users change live: devices, media, vCPUs and memory.
func CompareLive(saved, live string) ([]LiveChange, error) {
	var s, l libvirtxml.Domain
	if err := s.Unmarshal(saved); err != nil {
		return nil, fmt.Errorf("saved definition: %w", err)
	}
	if err := l.Unmarshal(live); err != nil {
		return nil, fmt.Errorf("running configuration: %w", err)
	}
	sd, ld := devices(&s), devices(&l)
	var out []LiveChange
	keys := make([]string, 0, len(sd)+len(ld))
	for k := range sd {
		keys = append(keys, k)
	}
	for k := range ld {
		if _, ok := sd[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		a, inSaved := sd[k]
		b, inLive := ld[k]
		switch {
		case !inSaved:
			out = append(out, LiveChange{Kind: "added", What: b.what, Live: b.value})
		case !inLive:
			out = append(out, LiveChange{Kind: "removed", What: a.what, Saved: a.value})
		case a.value != b.value:
			out = append(out, LiveChange{Kind: "changed", What: a.what, Live: b.value, Saved: a.value})
		}
	}
	if sv, lv := vcpus(&s), vcpus(&l); sv != lv && sv > 0 && lv > 0 {
		out = append(out, LiveChange{Kind: "changed", What: "vCPUs", Live: fmt.Sprint(lv), Saved: fmt.Sprint(sv)})
	}
	// The balloon reports what the guest really has, which wobbles a little: only a real
	// change (more than 5 %) counts.
	if sm, lm := memKiB(&s), memKiB(&l); sm > 0 && lm > 0 && (lm*20 < sm*19 || lm*20 > sm*21) {
		out = append(out, LiveChange{Kind: "changed", What: "memory", Live: fmt.Sprintf("%d MiB", lm/1024), Saved: fmt.Sprintf("%d MiB", sm/1024)})
	}
	return out, nil
}

type device struct{ what, value string }

// devices maps a stable identity of each user-visible device (disk target, MAC address,
// host device address, shared folder target) to a description of what it is.
func devices(d *libvirtxml.Domain) map[string]device {
	out := map[string]device{}
	if d.Devices == nil {
		return out
	}
	for _, x := range d.Devices.Disks {
		if x.Target == nil || x.Target.Dev == "" {
			continue
		}
		kind := x.Device
		if kind == "" {
			kind = "disk"
		}
		out["disk/"+x.Target.Dev] = device{kind + " " + x.Target.Dev, diskSource(x.Source)}
	}
	for _, x := range d.Devices.Interfaces {
		if x.MAC == nil || x.MAC.Address == "" {
			continue
		}
		v := ifaceSource(x.Source)
		if x.Model != nil && x.Model.Type != "" {
			v += " (" + x.Model.Type + ")"
		}
		out["nic/"+strings.ToLower(x.MAC.Address)] = device{"network adapter " + strings.ToLower(x.MAC.Address), v}
	}
	for _, x := range d.Devices.Hostdevs {
		if id := hostdevID(x); id != "" {
			out["hostdev/"+id] = device{"host device " + id, id}
		}
	}
	for _, x := range d.Devices.Filesystems {
		if x.Target != nil && x.Target.Dir != "" {
			out["fs/"+x.Target.Dir] = device{"shared folder " + x.Target.Dir, fsSource(x.Source)}
		}
	}
	return out
}

func diskSource(s *libvirtxml.DomainDiskSource) string {
	v := ""
	switch {
	case s == nil:
	case s.File != nil:
		v = s.File.File
	case s.Block != nil:
		v = s.Block.Dev
	case s.Volume != nil && s.Volume.Volume != "":
		v = s.Volume.Pool + "/" + s.Volume.Volume
	case s.Network != nil && s.Network.Name != "":
		v = s.Network.Protocol + ":" + s.Network.Name
	}
	if v == "" {
		return "empty" // e.g. a CD drive without a disc
	}
	return v
}

func ifaceSource(s *libvirtxml.DomainInterfaceSource) string {
	switch {
	case s == nil:
		return ""
	case s.Network != nil:
		return "network " + s.Network.Network
	case s.Bridge != nil:
		return "bridge " + s.Bridge.Bridge
	case s.Direct != nil:
		return "direct " + s.Direct.Dev
	}
	return ""
}

func fsSource(s *libvirtxml.DomainFilesystemSource) string {
	if s != nil && s.Mount != nil {
		return s.Mount.Dir
	}
	return ""
}

// hostdevID names a passed-through device by its host address (PCI) or IDs (USB).
func hostdevID(h libvirtxml.DomainHostdev) string {
	switch {
	case h.SubsysPCI != nil && h.SubsysPCI.Source != nil && h.SubsysPCI.Source.Address != nil:
		a := h.SubsysPCI.Source.Address
		u := func(p *uint) uint {
			if p == nil {
				return 0
			}
			return *p
		}
		return fmt.Sprintf("%04x:%02x:%02x.%x", u(a.Domain), u(a.Bus), u(a.Slot), u(a.Function))
	case h.SubsysUSB != nil && h.SubsysUSB.Source != nil:
		b, _ := xml.Marshal(h.SubsysUSB.Source)
		return "usb " + string(b)
	case h.SubsysMDev != nil && h.SubsysMDev.Source != nil && h.SubsysMDev.Source.Address != nil:
		return "mdev " + h.SubsysMDev.Source.Address.UUID
	}
	return ""
}

func vcpus(d *libvirtxml.Domain) uint {
	if d.VCPU == nil {
		return 0
	}
	if d.VCPU.Current > 0 {
		return d.VCPU.Current
	}
	return d.VCPU.Value
}

func memKiB(d *libvirtxml.Domain) uint {
	if d.CurrentMemory == nil {
		return 0
	}
	return toKiB(d.CurrentMemory.Value, d.CurrentMemory.Unit)
}

func toKiB(v uint, unit string) uint {
	switch strings.ToLower(unit) {
	case "b", "bytes":
		return v / 1024
	case "m", "mib":
		return v * 1024
	case "g", "gib":
		return v * 1024 * 1024
	case "mb":
		return v * 1000 * 1000 / 1024
	case "gb":
		return v * 1000 * 1000 * 1000 / 1024
	}
	return v // "", "k", "KiB"
}

// LiveDiff compares a running machine's configuration with its saved definition; nil
// when the machine is not running (or the host cannot tell).
func (s *Store) LiveDiff(uuid string) ([]LiveChange, error) {
	as, ok := s.src.(ActiveSource)
	if !ok {
		return nil, nil
	}
	if on, err := as.DomainActive(uuid); err != nil || !on {
		return nil, err
	}
	saved, err := s.src.DomainXML(uuid, true)
	if err != nil {
		return nil, err
	}
	live, err := s.src.DomainXML(uuid, false)
	if err != nil {
		return nil, err
	}
	return CompareLive(saved, live)
}
