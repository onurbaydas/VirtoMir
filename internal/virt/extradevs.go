package virt

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// The devices beyond disks, adapters and passthrough that people add from the Hardware tab:
// watchdog, TPM, random number generator, shared folders (virtiofs), mediated devices
// (vGPU slices), clipboard sharing (qemu-vdagent) and the video adapter (model, monitors).
// Each is written to the saved definition; where QEMU can hot-plug it, a running machine
// gets it right away too.

// DeviceSpec describes one device to add (Kind picks the fields that matter).
type DeviceSpec struct {
	// Kind: watchdog, tpm, rng, filesystem, mdev, clipboard, video.
	Kind string `json:"kind"`

	// watchdog: model i6300esb (default) or itco; action reset (default), poweroff, shutdown,
	// pause, dump, inject-nmi, none.
	Model  string `json:"model,omitempty"`
	Action string `json:"action,omitempty"`

	// filesystem: a host directory shared with the guest as a virtiofs mount tag.
	Source   string `json:"source,omitempty"`
	Tag      string `json:"tag,omitempty"`
	ReadOnly bool   `json:"readOnly,omitempty"`

	// mdev: the UUID of a mediated device created on the host (see Host hardware).
	UUID string `json:"uuid,omitempty"`

	// video: model virtio (default), qxl, vga, bochs, ramfb; heads = monitors (1–4).
	Heads uint `json:"heads,omitempty"`
}

// AddDeviceResult tells the UI whether the running machine already has it.
type AddDeviceResult struct {
	// AppliesOnNextBoot: only the saved definition changed (the machine runs and QEMU cannot
	// add this device live, or it failed: LiveError).
	AppliesOnNextBoot bool   `json:"appliesOnNextBoot"`
	LiveError         string `json:"liveError,omitempty"`
}

var tagRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,36}$`)

var watchdogActions = []string{"reset", "poweroff", "shutdown", "pause", "dump", "inject-nmi", "none"}

// AddDevice adds one of the extra devices.
func (c *Client) AddDevice(uuid string, spec DeviceSpec) (AddDeviceResult, error) {
	var res AddDeviceResult
	l, d, err := c.lookup(uuid)
	if err != nil {
		return res, err
	}
	x, err := c.parseDomain(l, d, true)
	if err != nil {
		return res, err
	}
	sum, err := summary(l, d)
	if err != nil {
		return res, classify(err)
	}
	running := isOn(sum.State) || sum.State == StatePaused
	if x.Devices == nil {
		x.Devices = &libvirtxml.DomainDeviceList{}
	}
	dev := x.Devices

	// live is the device XML to hot-plug on a running machine ("" = cannot).
	var live string
	marshal := func(m interface{ Marshal() (string, error) }) string {
		s, _ := m.Marshal()
		return s
	}
	switch spec.Kind {
	case "watchdog":
		w := libvirtxml.DomainWatchdog{Model: orDefault(spec.Model, "i6300esb"), Action: orDefault(spec.Action, "reset")}
		if w.Model != "i6300esb" && w.Model != "itco" {
			return res, invalidf("Watchdog model is i6300esb or itco.")
		}
		if !slices.Contains(watchdogActions, w.Action) {
			return res, invalidf("Watchdog action is one of %s.", strings.Join(watchdogActions, ", "))
		}
		if len(dev.Watchdogs) > 0 {
			return res, refusedf("This machine already has a watchdog; remove it first.")
		}
		dev.Watchdogs = append(dev.Watchdogs, w)
		if w.Model == "i6300esb" {
			live = marshal(&w)
		}
	case "tpm":
		if len(dev.TPMs) > 0 {
			return res, refusedf("This machine already has a TPM.")
		}
		model := orDefault(spec.Model, "tpm-crb")
		if model != "tpm-crb" && model != "tpm-tis" {
			return res, invalidf("TPM model is tpm-crb or tpm-tis.")
		}
		dev.TPMs = append(dev.TPMs, libvirtxml.DomainTPM{Model: model,
			Backend: &libvirtxml.DomainTPMBackend{Emulator: &libvirtxml.DomainTPMBackendEmulator{Version: "2.0"}}})
	case "rng":
		if len(dev.RNGs) > 0 {
			return res, refusedf("This machine already has a random number generator.")
		}
		r := libvirtxml.DomainRNG{Model: "virtio", Backend: &libvirtxml.DomainRNGBackend{
			Random: &libvirtxml.DomainRNGBackendRandom{Device: "/dev/urandom"}}}
		dev.RNGs = append(dev.RNGs, r)
		live = marshal(&r)
	case "filesystem":
		if !filepath.IsAbs(spec.Source) || filepath.Clean(spec.Source) != spec.Source || spec.Source == "/" {
			return res, invalidf("The shared folder is an absolute path on the host, like /srv/share.")
		}
		if !tagRe.MatchString(spec.Tag) {
			return res, invalidf("The mount tag is 1–36 letters, digits, '.', '_' or '-'; the guest mounts it with: mount -t virtiofs <tag> /mnt")
		}
		for _, f := range dev.Filesystems {
			if f.Target != nil && f.Target.Dir == spec.Tag {
				return res, refusedf("The tag %s is already used.", spec.Tag)
			}
		}
		fs := libvirtxml.DomainFilesystem{
			AccessMode: "passthrough",
			Driver:     &libvirtxml.DomainFilesystemDriver{Type: "virtiofs"},
			Source:     &libvirtxml.DomainFilesystemSource{Mount: &libvirtxml.DomainFilesystemSourceMount{Dir: spec.Source}},
			Target:     &libvirtxml.DomainFilesystemTarget{Dir: spec.Tag},
		}
		if spec.ReadOnly {
			fs.ReadOnly = &libvirtxml.DomainFilesystemReadOnly{}
		}
		dev.Filesystems = append(dev.Filesystems, fs)
		// virtiofsd maps the guest's memory: it must be shared.
		mb := orNew(x.MemoryBacking)
		if mb.MemorySource == nil || mb.MemoryAccess == nil || mb.MemoryAccess.Mode != "shared" {
			if mb.MemorySource == nil && mb.MemoryHugePages == nil {
				mb.MemorySource = &libvirtxml.DomainMemorySource{Type: "memfd"}
			}
			mb.MemoryAccess = &libvirtxml.DomainMemoryAccess{Mode: "shared"}
			x.MemoryBacking = mb
		} else if running {
			live = marshal(&fs)
		}
	case "mdev":
		if !uuidRe.MatchString(strings.ToLower(spec.UUID)) {
			return res, invalidf("Choose a mediated device by its UUID.")
		}
		for _, h := range dev.Hostdevs {
			if h.SubsysMDev != nil && h.SubsysMDev.Source != nil && h.SubsysMDev.Source.Address != nil &&
				strings.EqualFold(h.SubsysMDev.Source.Address.UUID, spec.UUID) {
				return res, refusedf("This machine already has that device.")
			}
		}
		if users := c.mdevUsers(l)[strings.ToLower(spec.UUID)]; len(users) > 0 {
			return res, refusedf("The mediated device is already given to %s.", strings.Join(users, ", "))
		}
		h := libvirtxml.DomainHostdev{Managed: "no", SubsysMDev: &libvirtxml.DomainHostdevSubsysMDev{
			Model: orDefault(spec.Model, "vfio-pci"), Source: &libvirtxml.DomainHostdevSubsysMDevSource{
				Address: &libvirtxml.DomainAddressMDev{UUID: strings.ToLower(spec.UUID)}}}}
		dev.Hostdevs = append(dev.Hostdevs, h)
		live = marshal(&h)
	case "clipboard":
		if hasVDAgent(x) {
			return res, refusedf("Clipboard sharing is already on.")
		}
		dev.Channels = append(dev.Channels, vdagentChannel())
	case "video":
		model := orDefault(spec.Model, "virtio")
		if !slices.Contains([]string{"virtio", "qxl", "vga", "bochs", "ramfb", "cirrus"}, model) {
			return res, invalidf("Video model is virtio, qxl, vga, bochs, ramfb or cirrus.")
		}
		heads := max(spec.Heads, 1)
		if heads > 4 {
			return res, invalidf("Up to 4 monitors.")
		}
		if heads > 1 && model != "virtio" && model != "qxl" {
			return res, invalidf("Several monitors need the virtio or qxl video model.")
		}
		v := libvirtxml.DomainVideo{Model: libvirtxml.DomainVideoModel{Type: model, Heads: heads, Primary: "yes"}}
		if model == "qxl" {
			v.Model.Ram, v.Model.VRam, v.Model.VGAMem = 65536, 65536, 16384
		}
		if len(dev.Videos) > 0 {
			v.Address = dev.Videos[0].Address
			dev.Videos[0] = v
		} else {
			dev.Videos = append(dev.Videos, v)
		}
	default:
		return res, invalidf("Unknown device kind %q.", spec.Kind)
	}

	doc, err := x.Marshal()
	if err != nil {
		return res, err
	}
	if _, err := l.DomainDefineXMLFlags(doc, libvirt.DomainDefineValidate); err != nil {
		return res, classify(err)
	}
	c.log.Info("device added", "domain", d.Name, "kind", spec.Kind)
	if running {
		res.AppliesOnNextBoot = true
		if live != "" {
			if err := l.DomainAttachDeviceFlags(d, live, uint32(libvirt.DomainAffectLive)); err != nil {
				res.LiveError = classify(err).Error()
			} else {
				res.AppliesOnNextBoot = false
			}
		}
	}
	return res, nil
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// vdagentChannel lets the VNC console and the guest share the clipboard (QEMU 6.1+; the guest
// needs spice-vdagent) and gives the guest an absolute mouse.
func vdagentChannel() libvirtxml.DomainChannel {
	return libvirtxml.DomainChannel{
		Source: &libvirtxml.DomainChardevSource{QEMUVDAgent: &libvirtxml.DomainChardevSourceQEMUVDAgent{
			ClipBoard: &libvirtxml.DomainChardevSourceQEMUVDAgentClipBoard{CopyPaste: "yes"},
			Mouse:     &libvirtxml.DomainChardevSourceQEMUVDAgentMouse{Mode: "client"},
		}},
		Target: &libvirtxml.DomainChannelTarget{VirtIO: &libvirtxml.DomainChannelTargetVirtIO{Name: "com.redhat.spice.0"}},
	}
}

func hasVDAgent(x *libvirtxml.Domain) bool {
	if x.Devices == nil {
		return false
	}
	for _, ch := range x.Devices.Channels {
		if ch.Source != nil && ch.Source.QEMUVDAgent != nil {
			return true
		}
	}
	return false
}

// removeExtraDevice drops one of the devices above from the definition (DetachDevice kinds
// watchdog, tpm, rng, filesystem, hostdev-mdev, clipboard). It reports the device XML so a
// running machine can lose it live where possible.
func removeExtraDevice(x *libvirtxml.Domain, ref DeviceRef) (liveXML string, err error) {
	dev := x.Devices
	if dev == nil {
		return "", fmt.Errorf("%w: no such device", ErrNotFound)
	}
	notFound := fmt.Errorf("%w: no %s %s", ErrNotFound, ref.Kind, ref.Target)
	switch ref.Kind {
	case "watchdog":
		if len(dev.Watchdogs) == 0 {
			return "", notFound
		}
		w := dev.Watchdogs[0]
		dev.Watchdogs = dev.Watchdogs[1:]
		if w.Model == "i6300esb" {
			liveXML, _ = w.Marshal()
		}
	case "tpm":
		if len(dev.TPMs) == 0 {
			return "", notFound
		}
		dev.TPMs = dev.TPMs[1:]
	case "rng":
		if len(dev.RNGs) == 0 {
			return "", notFound
		}
		r := dev.RNGs[0]
		dev.RNGs = dev.RNGs[1:]
		liveXML, _ = r.Marshal()
	case "filesystem":
		i := slices.IndexFunc(dev.Filesystems, func(f libvirtxml.DomainFilesystem) bool { return f.Target != nil && f.Target.Dir == ref.Target })
		if i < 0 {
			return "", notFound
		}
		f := dev.Filesystems[i]
		dev.Filesystems = slices.Delete(dev.Filesystems, i, i+1)
		liveXML, _ = f.Marshal()
	case "hostdev-mdev":
		i := slices.IndexFunc(dev.Hostdevs, func(h libvirtxml.DomainHostdev) bool {
			return h.SubsysMDev != nil && h.SubsysMDev.Source != nil && h.SubsysMDev.Source.Address != nil &&
				strings.EqualFold(h.SubsysMDev.Source.Address.UUID, ref.Target)
		})
		if i < 0 {
			return "", notFound
		}
		h := dev.Hostdevs[i]
		dev.Hostdevs = slices.Delete(dev.Hostdevs, i, i+1)
		liveXML, _ = h.Marshal()
	case "clipboard":
		i := slices.IndexFunc(dev.Channels, func(ch libvirtxml.DomainChannel) bool { return ch.Source != nil && ch.Source.QEMUVDAgent != nil })
		if i < 0 {
			return "", notFound
		}
		dev.Channels = slices.Delete(dev.Channels, i, i+1)
	default:
		return "", invalidf("Unknown device kind %q.", ref.Kind)
	}
	return liveXML, nil
}

// mdevUsers maps mediated device UUIDs to the machines that have them.
func (c *Client) mdevUsers(l *libvirt.Libvirt) map[string][]string {
	out := map[string][]string{}
	doms, _, err := l.ConnectListAllDomains(1, libvirt.ConnectListDomainsActive|libvirt.ConnectListDomainsInactive)
	if err != nil {
		return out
	}
	for _, d := range doms {
		x, err := c.parseDomain(l, d, true)
		if err != nil || x.Devices == nil {
			continue
		}
		for _, h := range x.Devices.Hostdevs {
			if h.SubsysMDev != nil && h.SubsysMDev.Source != nil && h.SubsysMDev.Source.Address != nil {
				u := strings.ToLower(h.SubsysMDev.Source.Address.UUID)
				out[u] = appendUnique(out[u], d.Name)
			}
		}
	}
	return out
}

// ---- QEMU command line (expert setting) ------------------------------------------------

// QEMUArgs are extra arguments and environment variables for QEMU itself
// (<qemu:commandline>). libvirt marks such machines "tainted": what they do is outside its
// control, and they can break in ways nothing else can.
type QEMUArgs struct {
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (c *Client) GetQEMUArgs(uuid string) (QEMUArgs, error) {
	out := QEMUArgs{Args: []string{}, Env: map[string]string{}}
	l, d, err := c.lookup(uuid)
	if err != nil {
		return out, err
	}
	x, err := c.parseDomain(l, d, true)
	if err != nil {
		return out, err
	}
	if q := x.QEMUCommandline; q != nil {
		for _, a := range q.Args {
			out.Args = append(out.Args, a.Value)
		}
		for _, e := range q.Envs {
			out.Env[e.Name] = e.Value
		}
	}
	return out, nil
}

// SetQEMUArgs replaces the extra QEMU arguments (empty removes them). Used from the next boot.
func (c *Client) SetQEMUArgs(uuid string, q QEMUArgs) (DefineResult, error) {
	for name := range q.Env {
		if !envNameRe.MatchString(name) {
			return DefineResult{}, invalidf("%q is not a valid environment variable name.", name)
		}
	}
	l, d, err := c.lookup(uuid)
	if err != nil {
		return DefineResult{}, err
	}
	x, err := c.parseDomain(l, d, true)
	if err != nil {
		return DefineResult{}, err
	}
	applyQEMUArgs(x, q)
	doc, err := x.Marshal()
	if err != nil {
		return DefineResult{}, err
	}
	sum, err := summary(l, d)
	if err != nil {
		return DefineResult{}, classify(err)
	}
	if _, err := l.DomainDefineXMLFlags(doc, libvirt.DomainDefineValidate); err != nil {
		return DefineResult{}, classify(err)
	}
	c.log.Warn("QEMU command line changed", "domain", d.Name, "args", len(q.Args), "env", len(q.Env))
	return DefineResult{AppliesOnNextBoot: isOn(sum.State)}, nil
}

func applyQEMUArgs(x *libvirtxml.Domain, q QEMUArgs) {
	var args []libvirtxml.DomainQEMUCommandlineArg
	for _, a := range q.Args {
		if a = strings.TrimSpace(a); a != "" {
			args = append(args, libvirtxml.DomainQEMUCommandlineArg{Value: a})
		}
	}
	names := make([]string, 0, len(q.Env))
	for n := range q.Env {
		names = append(names, n)
	}
	slices.Sort(names)
	var envs []libvirtxml.DomainQEMUCommandlineEnv
	for _, n := range names {
		envs = append(envs, libvirtxml.DomainQEMUCommandlineEnv{Name: n, Value: q.Env[n]})
	}
	if len(args) == 0 && len(envs) == 0 {
		x.QEMUCommandline = nil
		return
	}
	x.QEMUCommandline = &libvirtxml.DomainQEMUCommandline{Args: args, Envs: envs}
}
