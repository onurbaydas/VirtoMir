package virt

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
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
	// pause, dump, inject-nmi, none. tpm: tpm-crb (default) or tpm-tis. video: see Heads.
	Model  string `json:"model,omitempty"`
	Action string `json:"action,omitempty"`

	// filesystem: a host directory shared with the guest as a virtiofs mount tag.
	Source   string `json:"source,omitempty"`
	Tag      string `json:"tag,omitempty"`
	ReadOnly bool   `json:"readOnly,omitempty"`

	// mdev: the UUID of a mediated device created on the host (see Host hardware).
	UUID string `json:"uuid,omitempty"`

	// video: model virtio (default), qxl, vga, bochs, ramfb, cirrus; heads = monitors (1–4).
	Heads uint `json:"heads,omitempty"`
}

// DeviceResult tells the UI whether a running machine already has the change.
type DeviceResult struct {
	// AppliesOnNextBoot: only the saved definition changed (the machine runs and QEMU cannot
	// change this device live, or it failed: LiveError).
	AppliesOnNextBoot bool   `json:"appliesOnNextBoot"`
	LiveError         string `json:"liveError,omitempty"`
}

var tagRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,36}$`)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var watchdogActions = []string{"reset", "poweroff", "shutdown", "pause", "dump", "inject-nmi", "none"}

// AddDevice adds one of the extra devices.
func (c *Client) AddDevice(uuid string, spec DeviceSpec) (DeviceResult, error) {
	var res DeviceResult
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
	if spec.Kind == "filesystem" && !hasVirtiofsd(vhostUserDirs) {
		// Without it the machine would not start at all.
		return res, refusedf("Shared folders need virtiofsd on this host, and it is not installed. Install the virtiofsd package (Debian/Ubuntu: apt install virtiofsd; Fedora/RHEL: dnf install virtiofsd) and try again.")
	}
	if spec.Kind == "mdev" {
		mdevs, err := c.hostMDevs(l, nil)
		if err != nil {
			return res, err
		}
		if !slices.ContainsFunc(mdevs, func(m HostMDev) bool { return strings.EqualFold(m.UUID, spec.UUID) }) {
			return res, invalidf("There is no mediated device %s on this host; create it on the Host hardware page first.", spec.UUID)
		}
	}
	live, err := addExtraDevice(x, spec, running, func() map[string][]string { return c.hostdevUsers(l).mdev })
	if err != nil {
		return res, err
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
		res = applyLive(live, func(doc string) error {
			return l.DomainAttachDeviceFlags(d, doc, uint32(libvirt.DomainAffectLive))
		})
	}
	return res, nil
}

// vhostUserDirs are where libvirt looks for vhost-user back ends such as virtiofsd
// (descriptors per QEMU's docs/interop/vhost-user.json).
var vhostUserDirs = []string{"/usr/share/qemu/vhost-user", "/etc/qemu/vhost-user"}

// hasVirtiofsd reports whether a vhost-user "fs" back end is installed (VirtoMir runs on
// the host it manages, so it can look).
func hasVirtiofsd(dirs []string) bool {
	for _, d := range dirs {
		files, _ := filepath.Glob(filepath.Join(d, "*.json"))
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			var desc struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(b, &desc) == nil && desc.Type == "fs" {
				return true
			}
		}
	}
	return false
}

// applyLive hot-plugs or unplugs doc ("" = QEMU cannot do it live) on a running machine and
// reports what is left for the next boot.
func applyLive(doc string, do func(string) error) DeviceResult {
	if doc == "" {
		return DeviceResult{AppliesOnNextBoot: true}
	}
	if err := do(doc); err != nil {
		return DeviceResult{AppliesOnNextBoot: true, LiveError: classify(err).Error()}
	}
	return DeviceResult{}
}

// addExtraDevice adds the device to the definition x. It returns the device XML to hot-plug
// when the machine runs ("" = QEMU cannot add it live, or it does not run). mdevUsers is
// only called for mediated devices.
func addExtraDevice(x *libvirtxml.Domain, spec DeviceSpec, running bool, mdevUsers func() map[string][]string) (live string, err error) {
	if x.Devices == nil {
		x.Devices = &libvirtxml.DomainDeviceList{}
	}
	dev := x.Devices
	marshal := func(m interface{ Marshal() (string, error) }) string {
		s, _ := m.Marshal()
		return s
	}
	switch spec.Kind {
	case "watchdog":
		w := libvirtxml.DomainWatchdog{Model: orDefault(spec.Model, "i6300esb"), Action: orDefault(spec.Action, "reset")}
		if w.Model != "i6300esb" && w.Model != "itco" {
			return "", invalidf("Watchdog model is i6300esb or itco.")
		}
		if !slices.Contains(watchdogActions, w.Action) {
			return "", invalidf("Watchdog action is one of %s.", strings.Join(watchdogActions, ", "))
		}
		// QEMU has one watchdog action per machine, so all of them get the chosen one.
		for i := range dev.Watchdogs {
			dev.Watchdogs[i].Action = w.Action
		}
		// One of each model (libvirt allows several; q35 always has its built-in iTCO). Asking
		// for a model the machine has only changes the action, from the next boot on.
		if slices.ContainsFunc(dev.Watchdogs, func(o libvirtxml.DomainWatchdog) bool { return o.Model == w.Model }) {
			break
		}
		dev.Watchdogs = append(dev.Watchdogs, w)
		if w.Model == "i6300esb" {
			live = marshal(&w)
		}
	case "tpm":
		if len(dev.TPMs) > 0 {
			return "", refusedf("This machine already has a TPM.")
		}
		model := orDefault(spec.Model, "tpm-crb")
		if model != "tpm-crb" && model != "tpm-tis" {
			return "", invalidf("TPM model is tpm-crb or tpm-tis.")
		}
		dev.TPMs = append(dev.TPMs, libvirtxml.DomainTPM{Model: model,
			Backend: &libvirtxml.DomainTPMBackend{Emulator: &libvirtxml.DomainTPMBackendEmulator{Version: "2.0"}}})
	case "rng":
		if len(dev.RNGs) > 0 {
			return "", refusedf("This machine already has a random number generator.")
		}
		r := libvirtxml.DomainRNG{Model: "virtio", Backend: &libvirtxml.DomainRNGBackend{
			Random: &libvirtxml.DomainRNGBackendRandom{Device: "/dev/urandom"}}}
		dev.RNGs = append(dev.RNGs, r)
		live = marshal(&r)
	case "filesystem":
		// path, not filepath: the folder is on the Linux host, whatever builds the daemon.
		if !strings.HasPrefix(spec.Source, "/") || path.Clean(spec.Source) != spec.Source || spec.Source == "/" {
			return "", invalidf("The shared folder is an absolute path on the host, like /srv/share.")
		}
		if !tagRe.MatchString(spec.Tag) {
			return "", invalidf("The mount tag is 1–36 letters, digits, '.', '_' or '-'; the guest mounts it with: mount -t virtiofs <tag> /mnt")
		}
		for _, f := range dev.Filesystems {
			if f.Target != nil && f.Target.Dir == spec.Tag {
				return "", refusedf("The tag %s is already used.", spec.Tag)
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
		// virtiofsd maps the guest's memory, so it must be shared. A running machine whose
		// memory is not shared yet gets the folder from its next boot on.
		mb := orNew(x.MemoryBacking)
		if mb.MemoryAccess != nil && mb.MemoryAccess.Mode == "shared" {
			live = marshal(&fs)
		} else {
			if mb.MemorySource == nil && mb.MemoryHugePages == nil {
				mb.MemorySource = &libvirtxml.DomainMemorySource{Type: "memfd"}
			}
			mb.MemoryAccess = &libvirtxml.DomainMemoryAccess{Mode: "shared"}
			x.MemoryBacking = mb
		}
	case "mdev":
		id := strings.ToLower(spec.UUID)
		if !uuidRe.MatchString(id) {
			return "", invalidf("Choose a mediated device by its UUID.")
		}
		if findMDev(x, id) >= 0 {
			return "", refusedf("This machine already has that device.")
		}
		if users := mdevUsers()[id]; len(users) > 0 {
			return "", refusedf("The mediated device is already given to %s.", strings.Join(users, ", "))
		}
		h := libvirtxml.DomainHostdev{Managed: "no", SubsysMDev: &libvirtxml.DomainHostdevSubsysMDev{
			Model: "vfio-pci", Source: &libvirtxml.DomainHostdevSubsysMDevSource{
				Address: &libvirtxml.DomainAddressMDev{UUID: id}}}}
		dev.Hostdevs = append(dev.Hostdevs, h)
		live = marshal(&h)
	case "clipboard":
		if hasVDAgent(x) {
			return "", refusedf("Clipboard sharing is already on.")
		}
		dev.Channels = append(dev.Channels, vdagentChannel())
	case "video":
		model := orDefault(spec.Model, "virtio")
		if !slices.Contains([]string{"virtio", "qxl", "vga", "bochs", "ramfb", "cirrus"}, model) {
			return "", invalidf("Video model is virtio, qxl, vga, bochs, ramfb or cirrus.")
		}
		heads := max(spec.Heads, 1)
		if heads > 4 {
			return "", invalidf("Up to 4 monitors.")
		}
		if heads > 1 && model != "virtio" && model != "qxl" {
			return "", invalidf("Several monitors need the virtio or qxl video model.")
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
		// Every extra monitor gets a VNC server of its own, so the browser console shows it.
		syncHeadArgs(x, heads)
	default:
		return "", invalidf("Unknown device kind %q.", spec.Kind)
	}
	if !running {
		return "", nil
	}
	return live, nil
}

// chipsetWatchdog: the iTCO watchdog of a q35 machine is part of its chipset; libvirt lists it
// and puts it back if it is removed.
func chipsetWatchdog(x *libvirtxml.Domain, w libvirtxml.DomainWatchdog) bool {
	return w.Model == "itco" && x.OS != nil && x.OS.Type != nil && strings.Contains(x.OS.Type.Machine, "q35")
}

// findMDev returns the index of the hostdev for mediated device id, or -1.
func findMDev(x *libvirtxml.Domain, id string) int {
	if x.Devices == nil {
		return -1
	}
	return slices.IndexFunc(x.Devices.Hostdevs, func(h libvirtxml.DomainHostdev) bool {
		return h.SubsysMDev != nil && h.SubsysMDev.Source != nil && h.SubsysMDev.Source.Address != nil &&
			strings.EqualFold(h.SubsysMDev.Source.Address.UUID, id)
	})
}

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
	return slices.ContainsFunc(x.Devices.Channels, isVDAgent)
}

func isVDAgent(ch libvirtxml.DomainChannel) bool {
	return ch.Source != nil && ch.Source.QEMUVDAgent != nil
}

// extraDeviceKinds are the DetachDevice kinds removeExtraDevice handles.
var extraDeviceKinds = []string{"watchdog", "tpm", "rng", "filesystem", "hostdev-mdev", "clipboard"}

// removeExtraDevice drops one of the devices above from the definition. It reports the device
// XML so a running machine can lose it live where QEMU allows ("" = only from the next boot).
func removeExtraDevice(x *libvirtxml.Domain, ref DeviceRef) (liveXML string, err error) {
	dev := x.Devices
	if dev == nil {
		return "", fmt.Errorf("%w: no such device", ErrNotFound)
	}
	notFound := fmt.Errorf("%w: no %s %s", ErrNotFound, ref.Kind, ref.Target)
	switch ref.Kind {
	case "watchdog": // Target: the model
		i := slices.IndexFunc(dev.Watchdogs, func(w libvirtxml.DomainWatchdog) bool { return w.Model == ref.Target })
		if i < 0 {
			return "", notFound
		}
		w := dev.Watchdogs[i]
		if chipsetWatchdog(x, w) {
			return "", refusedf("The iTCO watchdog is part of the q35 chipset and cannot be removed; set it to \"Do nothing\" instead.")
		}
		dev.Watchdogs = slices.Delete(dev.Watchdogs, i, i+1)
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
		i := findMDev(x, ref.Target)
		if i < 0 {
			return "", notFound
		}
		h := dev.Hostdevs[i]
		dev.Hostdevs = slices.Delete(dev.Hostdevs, i, i+1)
		liveXML, _ = h.Marshal()
	case "clipboard":
		i := slices.IndexFunc(dev.Channels, isVDAgent)
		if i < 0 {
			return "", notFound
		}
		dev.Channels = slices.Delete(dev.Channels, i, i+1)
	default:
		return "", invalidf("Unknown device kind %q.", ref.Kind)
	}
	return liveXML, nil
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
	l, d, err := c.lookup(uuid)
	if err != nil {
		return QEMUArgs{}, err
	}
	x, err := c.parseDomain(l, d, true)
	if err != nil {
		return QEMUArgs{}, err
	}
	return readQEMUArgs(x), nil
}

func readQEMUArgs(x *libvirtxml.Domain) QEMUArgs {
	out := QEMUArgs{Args: []string{}, Env: map[string]string{}}
	if q := x.QEMUCommandline; q != nil {
		for _, a := range q.Args {
			out.Args = append(out.Args, a.Value)
		}
		for _, e := range q.Envs {
			out.Env[e.Name] = e.Value
		}
	}
	return out
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
	return DefineResult{AppliesOnNextBoot: isOn(sum.State) || sum.State == StatePaused}, nil
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
