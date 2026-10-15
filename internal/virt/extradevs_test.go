package virt

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"libvirt.org/go/libvirtxml"
)

func isRefused(err error) bool {
	var oe *OperationError
	return errors.As(err, &oe)
}

func noMDevUsers() map[string][]string { return nil }

// addOK adds spec to x, re-parses the saved XML (as libvirt would get it) and returns it
// with the live XML.
func addOK(t *testing.T, x *libvirtxml.Domain, spec DeviceSpec, running bool) (*libvirtxml.Domain, string) {
	t.Helper()
	live, err := addExtraDevice(x, spec, running, noMDevUsers)
	if err != nil {
		t.Fatalf("%+v: %v", spec, err)
	}
	doc, err := x.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var back libvirtxml.Domain
	if err := back.Unmarshal(doc); err != nil {
		t.Fatal(err)
	}
	return &back, live
}

func TestAddWatchdog(t *testing.T) {
	x := fixture(t)
	x, live := addOK(t, x, DeviceSpec{Kind: "watchdog", Action: "poweroff"}, true)
	if w := x.Devices.Watchdogs; len(w) != 1 || w[0].Model != "i6300esb" || w[0].Action != "poweroff" {
		t.Errorf("watchdog = %+v", w)
	}
	if !strings.Contains(live, "i6300esb") {
		t.Errorf("i6300esb is hot-pluggable: live = %q", live)
	}
	// The same model again only changes what it does.
	x, live = addOK(t, x, DeviceSpec{Kind: "watchdog", Action: "pause"}, true)
	if w := x.Devices.Watchdogs; len(w) != 1 || w[0].Action != "pause" || live != "" {
		t.Errorf("same model = %+v, live %q", w, live)
	}
	for _, bad := range []DeviceSpec{{Kind: "watchdog", Model: "ib700"}, {Kind: "watchdog", Action: "explode"}} {
		if _, err := addExtraDevice(fixture(t), bad, false, noMDevUsers); !isInvalid(err) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	// q35's built-in iTCO (libvirt lists it) sits next to an i6300esb; it cannot be hot-plugged
	// and cannot be removed.
	x, live = addOK(t, x, DeviceSpec{Kind: "watchdog", Model: "itco", Action: "none"}, true)
	if w := x.Devices.Watchdogs; len(w) != 2 || live != "" || w[0].Action != "none" {
		t.Errorf("itco = %+v (QEMU has one action for all), live %q", w, live)
	}
	if _, err := removeExtraDevice(x, DeviceRef{Kind: "watchdog", Target: "itco"}); !isRefused(err) {
		t.Errorf("removing q35's iTCO: %v", err)
	}
	d := buildDetail(Domain{}, x)
	if !hasDevice(d.Devices, "watchdog", "i6300esb") || !hasDevice(d.Devices, "watchdog", "") {
		t.Errorf("detail must name the i6300esb only: %+v", d.Devices)
	}
}

func TestAddTPMAndRNG(t *testing.T) {
	// The fixture has both already.
	for _, k := range []string{"tpm", "rng"} {
		if _, err := addExtraDevice(fixture(t), DeviceSpec{Kind: k}, false, noMDevUsers); !isRefused(err) {
			t.Errorf("duplicate %s: %v", k, err)
		}
	}
	x := fixture(t)
	x.Devices.TPMs, x.Devices.RNGs = nil, nil
	x, live := addOK(t, x, DeviceSpec{Kind: "tpm"}, true)
	if tp := x.Devices.TPMs; len(tp) != 1 || tp[0].Model != "tpm-crb" || tp[0].Backend.Emulator == nil || tp[0].Backend.Emulator.Version != "2.0" {
		t.Errorf("tpm = %+v", tp)
	}
	if live != "" {
		t.Errorf("a TPM cannot be hot-plugged: %q", live)
	}
	if _, err := addExtraDevice(fixture(t), DeviceSpec{Kind: "tpm", Model: "tpm-spapr"}, false, noMDevUsers); err == nil {
		t.Error("bad TPM model accepted")
	}
	x, live = addOK(t, x, DeviceSpec{Kind: "rng"}, true)
	if r := x.Devices.RNGs; len(r) != 1 || r[0].Backend.Random.Device != "/dev/urandom" || !strings.Contains(live, "urandom") {
		t.Errorf("rng = %+v, live %q", r, live)
	}
	// A stopped machine has nothing to hot-plug.
	x.Devices.RNGs = nil
	if _, live := addOK(t, x, DeviceSpec{Kind: "rng"}, false); live != "" {
		t.Errorf("stopped machine live = %q", live)
	}
}

func TestAddFilesystem(t *testing.T) {
	x, live := addOK(t, fixture(t), DeviceSpec{Kind: "filesystem", Source: "/srv/share", Tag: "share", ReadOnly: true}, true)
	f := x.Devices.Filesystems
	if len(f) != 1 || f[0].Driver.Type != "virtiofs" || f[0].Source.Mount.Dir != "/srv/share" || f[0].Target.Dir != "share" || f[0].ReadOnly == nil {
		t.Fatalf("filesystem = %+v", f)
	}
	mb := x.MemoryBacking
	if mb == nil || mb.MemoryAccess == nil || mb.MemoryAccess.Mode != "shared" || mb.MemorySource == nil || mb.MemorySource.Type != "memfd" {
		t.Errorf("memory must become shared memfd: %+v", mb)
	}
	// Memory was not shared while it ran: the folder arrives with the next boot.
	if live != "" {
		t.Errorf("live = %q", live)
	}
	// Now memory is shared: a second folder can be hot-plugged.
	x, live = addOK(t, x, DeviceSpec{Kind: "filesystem", Source: "/srv/other", Tag: "other"}, true)
	if !strings.Contains(live, "other") {
		t.Errorf("second folder live = %q", live)
	}
	if _, err := addExtraDevice(x, DeviceSpec{Kind: "filesystem", Source: "/srv/x", Tag: "share"}, false, noMDevUsers); !isRefused(err) {
		t.Errorf("tag reused: %v", err)
	}
	for _, bad := range []DeviceSpec{
		{Source: "srv/share", Tag: "a"}, {Source: "/", Tag: "a"}, {Source: "/srv/../etc", Tag: "a"},
		{Source: "/srv/share", Tag: ""}, {Source: "/srv/share", Tag: "a b"}, {Source: "/srv/share", Tag: strings.Repeat("t", 37)},
	} {
		bad.Kind = "filesystem"
		if _, err := addExtraDevice(fixture(t), bad, false, noMDevUsers); !isInvalid(err) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	// Huge pages back the memory already: no memfd source on top.
	y := fixture(t)
	y.MemoryBacking = &libvirtxml.DomainMemoryBacking{MemoryHugePages: &libvirtxml.DomainMemoryHugepages{}}
	y, _ = addOK(t, y, DeviceSpec{Kind: "filesystem", Source: "/srv/share", Tag: "share"}, false)
	if y.MemoryBacking.MemorySource != nil || y.MemoryBacking.MemoryAccess.Mode != "shared" {
		t.Errorf("huge pages: %+v", y.MemoryBacking)
	}
}

func TestAddMDev(t *testing.T) {
	const id = "4b20d080-1b54-4048-85b3-a6a62d165c01"
	x, live := addOK(t, fixture(t), DeviceSpec{Kind: "mdev", UUID: strings.ToUpper(id)}, true)
	i := findMDev(x, id)
	if i < 0 || x.Devices.Hostdevs[i].SubsysMDev.Source.Address.UUID != id || x.Devices.Hostdevs[i].SubsysMDev.Model != "vfio-pci" {
		t.Fatalf("mdev hostdev = %+v", x.Devices.Hostdevs)
	}
	if !strings.Contains(live, id) {
		t.Errorf("live = %q", live)
	}
	if _, err := addExtraDevice(x, DeviceSpec{Kind: "mdev", UUID: id}, false, noMDevUsers); !isRefused(err) {
		t.Errorf("same mdev twice: %v", err)
	}
	taken := func() map[string][]string { return map[string][]string{id: {"web1"}} }
	if _, err := addExtraDevice(fixture(t), DeviceSpec{Kind: "mdev", UUID: id}, false, taken); !isRefused(err) || !strings.Contains(err.Error(), "web1") {
		t.Errorf("mdev of another machine: %v", err)
	}
	if _, err := addExtraDevice(fixture(t), DeviceSpec{Kind: "mdev", UUID: "nope"}, false, noMDevUsers); !isInvalid(err) {
		t.Errorf("bad uuid: %v", err)
	}
}

func TestAddClipboardAndVideo(t *testing.T) {
	x, live := addOK(t, fixture(t), DeviceSpec{Kind: "clipboard"}, true)
	if !hasVDAgent(x) || live != "" {
		t.Errorf("clipboard: has=%v live=%q", hasVDAgent(x), live)
	}
	if _, err := addExtraDevice(x, DeviceSpec{Kind: "clipboard"}, false, noMDevUsers); !isRefused(err) {
		t.Errorf("clipboard twice: %v", err)
	}
	if d := buildDetail(Domain{}, x); !hasDevice(d.Devices, "clipboard", "") {
		t.Errorf("detail lacks the clipboard device")
	}

	addr := fixture(t).Devices.Videos[0].Address
	x, _ = addOK(t, fixture(t), DeviceSpec{Kind: "video", Model: "qxl", Heads: 2}, false)
	v := x.Devices.Videos
	if len(v) != 1 || v[0].Model.Type != "qxl" || v[0].Model.Heads != 2 || v[0].Model.VRam == 0 {
		t.Fatalf("video = %+v", v)
	}
	if v[0].Address == nil || addr == nil || *v[0].Address.PCI.Slot != *addr.PCI.Slot {
		t.Errorf("video must keep its PCI address: %+v", v[0].Address)
	}
	for _, bad := range []DeviceSpec{{Model: "vga", Heads: 2}, {Model: "virtio", Heads: 5}, {Model: "voodoo"}} {
		bad.Kind = "video"
		if _, err := addExtraDevice(fixture(t), bad, false, noMDevUsers); !isInvalid(err) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if _, err := addExtraDevice(fixture(t), DeviceSpec{Kind: "floppy"}, false, noMDevUsers); !isInvalid(err) {
		t.Errorf("unknown kind: %v", err)
	}
}

func hasDevice(devs []Device, kind, target string) bool {
	for _, d := range devs {
		if d.Kind == kind && d.Target == target {
			return true
		}
	}
	return false
}

func TestRemoveExtraDevice(t *testing.T) {
	const id = "4b20d080-1b54-4048-85b3-a6a62d165c01"
	x := fixture(t)
	for _, s := range []DeviceSpec{
		{Kind: "watchdog"}, {Kind: "filesystem", Source: "/srv/share", Tag: "share"},
		{Kind: "mdev", UUID: id}, {Kind: "clipboard"},
	} {
		if _, err := addExtraDevice(x, s, false, noMDevUsers); err != nil {
			t.Fatal(err)
		}
	}
	// The machine page names every one of them the way DetachDevice expects.
	d := buildDetail(Domain{}, x)
	for _, ref := range []DeviceRef{
		{Kind: "watchdog", Target: "i6300esb"}, {Kind: "tpm"}, {Kind: "rng"}, {Kind: "filesystem", Target: "share"},
		{Kind: "hostdev-mdev", Target: id}, {Kind: "clipboard"},
	} {
		if !hasDevice(d.Devices, ref.Kind, ref.Target) {
			t.Errorf("detail lacks %+v", ref)
		}
		live, err := removeExtraDevice(x, ref)
		if err != nil {
			t.Fatalf("%+v: %v", ref, err)
		}
		wantLive := ref.Kind != "tpm" && ref.Kind != "clipboard"
		if (live != "") != wantLive {
			t.Errorf("%+v live = %q", ref, live)
		}
		if _, err := removeExtraDevice(x, ref); !errors.Is(err, ErrNotFound) {
			t.Errorf("%+v twice: %v", ref, err)
		}
	}
	if _, err := x.Marshal(); err != nil {
		t.Fatal(err)
	}
	if _, err := removeExtraDevice(x, DeviceRef{Kind: "disk"}); !isInvalid(err) {
		t.Errorf("disk is not an extra device: %v", err)
	}
}

func TestHasVirtiofsd(t *testing.T) {
	low, high := t.TempDir(), t.TempDir()
	write := func(dir, name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(low, "50-qemu-gpu.json", `{"description":"QEMU vhost-user-gpu","type":"gpu","binary":"/usr/libexec/vhost-user-gpu"}`)
	write(low, "broken.json", `{`)
	dirs := []string{low, high, filepath.Join(high, "missing")}
	if hasVirtiofsd(dirs) {
		t.Error("a gpu back end is not virtiofsd")
	}
	write(high, "50-virtiofsd.json", `{"description":"QEMU virtiofsd vhost-user-fs","type":"fs","binary":"/usr/libexec/virtiofsd"}`)
	if !hasVirtiofsd(dirs) {
		t.Error("virtiofsd not found")
	}
}

func TestQEMUArgs(t *testing.T) {
	x := fixture(t)
	applyQEMUArgs(x, QEMUArgs{Args: []string{" -device ", "", "ich9-intel-hda"}, Env: map[string]string{"QEMU_B": "2", "QEMU_A": "1"}})
	doc, err := x.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, `<commandline xmlns="http://libvirt.org/schemas/domain/qemu/1.0">`) {
		t.Errorf("no qemu:commandline in %s", doc)
	}
	var back libvirtxml.Domain
	if err := back.Unmarshal(doc); err != nil {
		t.Fatal(err)
	}
	q := readQEMUArgs(&back)
	if strings.Join(q.Args, "|") != "-device|ich9-intel-hda" || q.Env["QEMU_A"] != "1" || len(q.Env) != 2 {
		t.Errorf("read back = %+v", q)
	}
	if e := back.QEMUCommandline.Envs; e[0].Name != "QEMU_A" {
		t.Errorf("env not sorted: %+v", e)
	}
	applyQEMUArgs(&back, QEMUArgs{Args: []string{"  "}})
	if back.QEMUCommandline != nil {
		t.Errorf("empty must remove: %+v", back.QEMUCommandline)
	}
	if q := readQEMUArgs(&back); q.Args == nil || q.Env == nil {
		t.Errorf("empty lists must not be null in JSON: %+v", q)
	}
}
