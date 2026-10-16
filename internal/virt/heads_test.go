package virt

import (
	"testing"

	"libvirt.org/go/libvirtxml"
)

func TestSyncHeadArgs(t *testing.T) {
	x := &libvirtxml.Domain{
		Devices: &libvirtxml.DomainDeviceList{Graphics: []libvirtxml.DomainGraphic{{VNC: &libvirtxml.DomainGraphicVNC{}}}},
		QEMUCommandline: &libvirtxml.DomainQEMUCommandline{Args: []libvirtxml.DomainQEMUCommandlineArg{
			{Value: "-device"}, {Value: "pvpanic"}, // someone else's argument stays
		}},
	}
	if !syncHeadArgs(x, 3) {
		t.Fatal("nothing added for 3 heads")
	}
	args := x.QEMUCommandline.Args
	if len(args) != 6 || args[0].Value != "-device" || args[2].Value != "-vnc" || args[3].Value != headArg(1) || args[5].Value != headArg(2) {
		t.Fatalf("args = %+v", args)
	}
	if liveHeads(x) != 3 {
		t.Errorf("liveHeads = %d", liveHeads(x))
	}
	if syncHeadArgs(x, 3) {
		t.Error("changed again for the same count")
	}
	syncHeadArgs(x, 1)
	if len(x.QEMUCommandline.Args) != 2 || liveHeads(x) != 1 {
		t.Errorf("after going back to one monitor: %+v", x.QEMUCommandline.Args)
	}
	// Without VNC (e.g. SPICE only) there is nothing to serve the extra heads with.
	y := &libvirtxml.Domain{Devices: &libvirtxml.DomainDeviceList{}}
	if syncHeadArgs(y, 2) || y.QEMUCommandline != nil {
		t.Error("head servers added without a VNC display")
	}
	// Removing the last of our arguments drops an empty <qemu:commandline>.
	z := &libvirtxml.Domain{Devices: x.Devices}
	syncHeadArgs(z, 2)
	syncHeadArgs(z, 1)
	if z.QEMUCommandline != nil {
		t.Errorf("empty commandline kept: %+v", z.QEMUCommandline)
	}
}

func TestExpandCPUSet(t *testing.T) {
	got := expandCPUSet("0-3,^2,8")
	if len(got) != 4 || got[0] != 0 || got[2] != 3 || got[3] != 8 {
		t.Errorf("expand = %v", got)
	}
	if expandCPUSet("") != nil {
		t.Error("empty set has CPUs")
	}
}

func TestSameXML(t *testing.T) {
	if !sameXML("<a>\n  <b/>\n</a>\n", "<a>\n<b/>\n\n</a>") {
		t.Error("indentation counted as a change")
	}
	if sameXML("<a><b/></a>", "<a><c/></a>") {
		t.Error("a change was missed")
	}
}
