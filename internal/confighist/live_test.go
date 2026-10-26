package confighist

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const savedXML = `<domain type='kvm'>
  <name>web</name>
  <uuid>2d860e5c-844a-4c8a-bd06-282d61d6ea52</uuid>
  <memory unit='KiB'>2097152</memory>
  <currentMemory unit='KiB'>2097152</currentMemory>
  <vcpu placement='static'>2</vcpu>
  <devices>
    <disk type='file' device='disk'>
      <source file='/var/lib/libvirt/images/web.qcow2'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <disk type='file' device='cdrom'>
      <target dev='sda' bus='sata'/>
    </disk>
    <interface type='network'>
      <mac address='52:54:00:aa:bb:cc'/>
      <source network='default'/>
      <model type='virtio'/>
    </interface>
    <graphics type='vnc' port='-1' autoport='yes'/>
  </devices>
</domain>`

// What libvirt shows for the same machine while it runs, with nothing changed.
const liveXML = `<domain type='kvm' id='7'>
  <name>web</name>
  <uuid>2d860e5c-844a-4c8a-bd06-282d61d6ea52</uuid>
  <memory unit='KiB'>2097152</memory>
  <currentMemory unit='KiB'>2093000</currentMemory>
  <vcpu placement='static'>2</vcpu>
  <devices>
    <disk type='file' device='disk'>
      <source file='/var/lib/libvirt/images/web.qcow2' index='1'/>
      <target dev='vda' bus='virtio'/>
      <alias name='virtio-disk0'/>
    </disk>
    <disk type='file' device='cdrom'>
      <target dev='sda' bus='sata'/>
      <alias name='sata0-0-0'/>
    </disk>
    <interface type='network'>
      <mac address='52:54:00:aa:bb:cc'/>
      <source network='default'/>
      <target dev='vnet3'/>
      <model type='virtio'/>
      <alias name='net0'/>
    </interface>
    <serial type='pty'>
      <source path='/dev/pts/4'/>
      <alias name='serial0'/>
    </serial>
    <console type='pty' tty='/dev/pts/4'>
      <source path='/dev/pts/4'/>
    </console>
    <graphics type='vnc' port='5901' autoport='yes' websocket='5701'/>
  </devices>
  <seclabel type='dynamic' model='apparmor' relabel='yes'>
    <label>libvirt-2d860e5c</label>
    <imagelabel>libvirt-2d860e5c</imagelabel>
  </seclabel>
  <seclabel type='dynamic' model='dac' relabel='yes'/>
</domain>`

func TestNormalizeLiveDropsRuntimeNoise(t *testing.T) {
	a := NormalizeLive(liveXML)
	// The same machine after a restart: other ID, ports, tap device, terminal and labels.
	r := strings.NewReplacer("id='7'", "id='12'", "5901", "5903", "5701", "5703", "vnet3", "vnet9", "/dev/pts/4", "/dev/pts/2", "libvirt-2d860e5c", "libvirt-other")
	if b := NormalizeLive(r.Replace(liveXML)); a != b {
		t.Errorf("restart changed the normalized XML:\n%s\n---\n%s", a, b)
	}
	for _, gone := range []string{"id='7'", "<alias", "seclabel", "vnet3", "/dev/pts", "5901", "websocket"} {
		if strings.Contains(a, gone) {
			t.Errorf("%q still in %s", gone, a)
		}
	}
	for _, kept := range []string{"<target dev='vda'", "autoport='yes'", "52:54:00:aa:bb:cc", "<serial type='pty'>"} {
		if !strings.Contains(a, kept) {
			t.Errorf("%q lost from %s", kept, a)
		}
	}
	// A hot-plugged disk is a real change.
	plugged := strings.Replace(liveXML, "    <interface", "    <disk type='file' device='disk'>\n      <source file='/srv/data.qcow2'/>\n      <target dev='vdb' bus='virtio'/>\n      <alias name='virtio-disk1'/>\n    </disk>\n    <interface", 1)
	if NormalizeLive(plugged) == a {
		t.Error("hot-plugged disk not visible after normalizing")
	}
}

func TestCompareLive(t *testing.T) {
	if ch, err := CompareLive(savedXML, liveXML); err != nil || len(ch) != 0 {
		t.Fatalf("unchanged machine: %+v, %v", ch, err)
	}
	live := strings.Replace(liveXML, "    <interface", "    <disk type='file' device='disk'>\n      <source file='/srv/data.qcow2'/>\n      <target dev='vdb' bus='virtio'/>\n    </disk>\n    <interface", 1)
	live = strings.Replace(live, "<target dev='sda' bus='sata'/>", "<source file='/iso/debian.iso'/>\n      <target dev='sda' bus='sata'/>", 1)
	live = strings.Replace(live, "<vcpu placement='static'>2</vcpu>", "<vcpu placement='static' current='3'>4</vcpu>", 1)
	live = strings.Replace(live, "<currentMemory unit='KiB'>2093000</currentMemory>", "<currentMemory unit='KiB'>1048576</currentMemory>", 1)
	saved := strings.Replace(savedXML, "<vcpu placement='static'>2</vcpu>", "<vcpu placement='static' current='2'>4</vcpu>", 1)
	ch, err := CompareLive(saved, live)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]LiveChange{
		"disk vdb":  {Kind: "added", What: "disk vdb", Live: "/srv/data.qcow2"},
		"cdrom sda": {Kind: "changed", What: "cdrom sda", Live: "/iso/debian.iso", Saved: "empty"},
		"vCPUs":     {Kind: "changed", What: "vCPUs", Live: "3", Saved: "2"},
		"memory":    {Kind: "changed", What: "memory", Live: "1024 MiB", Saved: "2048 MiB"},
	}
	if len(ch) != len(want) {
		t.Fatalf("changes = %+v", ch)
	}
	for _, c := range ch {
		if want[c.What] != c {
			t.Errorf("got %+v, want %+v", c, want[c.What])
		}
	}
	// Unplugged live: the adapter is only in the definition.
	noNIC := strings.Replace(liveXML, "<mac address='52:54:00:aa:bb:cc'/>", "<mac address='52:54:00:00:00:01'/>", 1)
	ch, _ = CompareLive(savedXML, noNIC)
	if len(ch) != 2 || ch[0].Kind != "removed" && ch[1].Kind != "removed" {
		t.Errorf("swapped adapter: %+v", ch)
	}
}

type liveSrc struct {
	mu     sync.Mutex
	saved  map[string]string
	live   map[string]string
	active map[string]bool
}

func (f *liveSrc) DomainUUIDs() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for u := range f.saved {
		out = append(out, u)
	}
	return out, nil
}
func (f *liveSrc) DomainXML(u string, inactive bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if inactive || !f.active[u] {
		return f.saved[u], nil
	}
	return f.live[u], nil
}
func (f *liveSrc) DomainActive(u string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active[u], nil
}

func TestLiveVersions(t *testing.T) {
	src := &liveSrc{saved: map[string]string{u1: savedXML}, live: map[string]string{u1: liveXML}, active: map[string]bool{u1: true}}
	s, err := Open(t.TempDir(), src, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { clock = clock.Add(time.Second); return clock }

	s.Sweep()
	s.Sweep() // nothing new
	// Hot-plugged with virsh: an event, recorded as a live change from outside.
	src.live[u1] = strings.Replace(liveXML, "</devices>", "<disk type='file' device='disk'><source file='/srv/x.qcow2'/><target dev='vdb' bus='virtio'/><alias name='virtio-disk1'/></disk></devices>", 1)
	s.Changed(u1, false, true)
	// Then through VirtoMir by onur: the hot-plug event that follows is theirs.
	if err := s.Capture(u1, "onur"); err != nil {
		t.Fatal(err)
	}
	src.live[u1] = liveXML
	s.Changed(u1, false, true)
	// Stopped and started again with another ID and port: a new baseline only if it differs.
	s.Forget(u1)
	src.live[u1] = strings.NewReplacer("id='7'", "id='9'", "5901", "5905").Replace(liveXML)
	s.Changed(u1, false, true)

	vs, err := s.ListLive(u1)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 3 {
		t.Fatalf("live versions = %+v", vs)
	}
	if vs[2].Source != "first seen" || vs[2].Via != ViaScan || !vs[2].Live {
		t.Errorf("first: %+v", vs[2])
	}
	if vs[1].Source != "outside" || vs[1].Via != ViaEvent {
		t.Errorf("virsh hot-plug: %+v", vs[1])
	}
	if vs[0].Source != "virtomir" || vs[0].User != "onur" {
		t.Errorf("VirtoMir unplug: %+v", vs[0])
	}
	if !IsLiveID(vs[0].ID) || IsLiveID(strings.TrimPrefix(vs[0].ID, "live-")) {
		t.Errorf("live id %q", vs[0].ID)
	}
	if x, err := s.Get(u1, vs[1].ID); err != nil || !strings.Contains(x, "vdb") || strings.Contains(x, "<alias") {
		t.Errorf("Get live = %q, %v", x, err)
	}
	// Live versions stay out of the definitions.
	if ds, _ := s.List(u1); len(ds) != 1 || ds[0].Live {
		t.Errorf("definitions = %+v", ds)
	}
	// A stopped machine has no live version and no live diff.
	src.active[u1] = false
	if err := s.CaptureLive(u1, ViaScan); err != errNotRunning {
		t.Errorf("stopped machine: %v", err)
	}
	if d, err := s.LiveDiff(u1); d != nil || err != nil {
		t.Errorf("LiveDiff of a stopped machine = %+v, %v", d, err)
	}
	// A restart that changed the running configuration is recorded as "started".
	src.active[u1] = true
	s.Forget(u1)
	src.live[u1] = strings.Replace(liveXML, "<vcpu placement='static'>2</vcpu>", "<vcpu placement='static'>4</vcpu>", 1)
	s.Changed(u1, false, true)
	if vs, _ := s.ListLive(u1); len(vs) != 4 || vs[0].Source != "started" {
		t.Errorf("after restart: %+v", vs[0])
	}
}

func TestDebouncer(t *testing.T) {
	type call struct {
		uuid             string
		persistent, live bool
	}
	got := make(chan call, 8)
	d := NewDebouncer(40*time.Millisecond, func(u string, p, l bool) { got <- call{u, p, l} })
	d.Add("a", true, false)
	d.Add("b", false, true)
	time.Sleep(15 * time.Millisecond)
	d.Add("a", false, true) // pushes "a" back and merges
	calls := map[string]call{}
	for range 2 {
		select {
		case c := <-got:
			calls[c.uuid] = c
		case <-time.After(time.Second):
			t.Fatal("debouncer did not fire")
		}
	}
	if calls["a"] != (call{"a", true, true}) || calls["b"] != (call{"b", false, true}) {
		t.Errorf("calls = %+v", calls)
	}
	select {
	case c := <-got:
		t.Errorf("extra call %+v", c)
	case <-time.After(80 * time.Millisecond):
	}
	d.Stop()
	d.Add("c", true, true)
	select {
	case c := <-got:
		t.Errorf("call after Stop: %+v", c)
	case <-time.After(80 * time.Millisecond):
	}
}

func TestDrift(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "web.xml")
	onDisk := "<!--\nWARNING: THIS IS AN AUTO-GENERATED FILE. CHANGES TO IT ARE LIKELY TO BE\nOVERWRITTEN AND LOST.\n-->\n" +
		strings.Replace(savedXML, "autoport='yes'", "autoport='yes' passwd='secret'", 1)
	if err := os.WriteFile(file, []byte(onDisk), 0o600); err != nil {
		t.Fatal(err)
	}
	src := &liveSrc{saved: map[string]string{u1: savedXML}}
	w := NewDriftWatcher(dir, src, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.Check()
	if d, ok := w.Drift(u1); ok {
		t.Fatalf("comment and password counted as drift: %+v", d)
	}
	// Edited by hand: more memory. libvirt still has the old definition.
	edited := strings.Replace(onDisk, "<memory unit='KiB'>2097152</memory>", "<memory unit='KiB'>4194304</memory>", 1)
	if err := os.WriteFile(file, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	_ = os.Chtimes(file, later, later)
	w.Check()
	d, ok := w.Drift(u1)
	if !ok || d.File != file {
		t.Fatalf("hand edit not noticed: %+v %v", d, ok)
	}
	path, doc, err := w.EditedFile(u1)
	if err != nil || path != file || !strings.Contains(doc, "4194304") {
		t.Errorf("EditedFile = %q, %v", path, err)
	}
	// Loaded (libvirt now has it): no drift after the recheck.
	src.saved[u1] = strings.Replace(savedXML, "<memory unit='KiB'>2097152</memory>", "<memory unit='KiB'>4194304</memory>", 1)
	w.Recheck(u1)
	w.Check()
	if _, ok := w.Drift(u1); ok {
		t.Error("drift kept after the file was loaded")
	}
	if _, _, err := w.EditedFile(u1); err != ErrNoDrift {
		t.Errorf("EditedFile without drift: %v", err)
	}
	// The file removed: forgotten.
	_ = os.WriteFile(file, []byte(edited+"<!-- x -->"), 0o600)
	_ = os.Remove(file)
	w.Check()
	if _, ok := w.Drift(u1); ok {
		t.Error("drift of a removed file kept")
	}
}

func TestSameDefinitionNoticesChanges(t *testing.T) {
	if ok, err := SameDefinition(savedXML, savedXML); !ok || err != nil {
		t.Fatalf("same = %v, %v", ok, err)
	}
	if ok, _ := SameDefinition(savedXML, strings.Replace(savedXML, "virtio'/>\n    </disk>", "sata'/>\n    </disk>", 1)); ok {
		t.Error("bus change not noticed")
	}
	if _, err := SameDefinition("<not xml", savedXML); err == nil {
		t.Error("broken file accepted")
	}
}
