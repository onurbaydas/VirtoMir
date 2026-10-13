package virt

import (
	"slices"
	"testing"

	"libvirt.org/go/libvirtxml"
)

func TestFindVNC(t *testing.T) {
	x := fixture(t) // VNC port 5900 on 127.0.0.1
	got, ok := findVNC(x)
	if !ok || got.network != "tcp" || got.addr != "127.0.0.1:5900" {
		t.Errorf("vnc = %+v %v", got, ok)
	}

	parse := func(dev string) *libvirtxml.Domain {
		var d libvirtxml.Domain
		if err := d.Unmarshal(`<domain type='kvm'><name>v</name><devices>` + dev + `</devices></domain>`); err != nil {
			t.Fatal(err)
		}
		return &d
	}
	// Listening everywhere → dial loopback.
	if g, _ := findVNC(parse(`<graphics type='vnc' port='5905' listen='0.0.0.0' passwd='s3cret'/>`)); g.addr != "127.0.0.1:5905" || g.password != "s3cret" {
		t.Errorf("all addresses: %+v", g)
	}
	// UNIX socket.
	if g, _ := findVNC(parse(`<graphics type='vnc'><listen type='socket' socket='/run/vnc.sock'/></graphics>`)); g.network != "unix" || g.addr != "/run/vnc.sock" {
		t.Errorf("socket: %+v", g)
	}
	// Not running: no port yet.
	if _, ok := findVNC(parse(`<graphics type='vnc' port='-1' autoport='yes'/>`)); ok {
		t.Error("autoport without a port must not be dialable")
	}
	// SPICE only: no VNC console.
	if _, ok := findVNC(parse(`<graphics type='spice' port='5900'/>`)); ok {
		t.Error("spice is not vnc")
	}
}

func TestSerialAliases(t *testing.T) {
	x := fixture(t) // console + serial both alias serial0
	if got := serialAliases(x); !slices.Equal(got, []string{"serial0"}) {
		t.Errorf("aliases = %v", got)
	}
}
