package virt

import (
	"strings"
	"testing"
)

func TestSetDisplay(t *testing.T) {
	x := fixture(t)
	x.Devices.Channels = append(x.Devices.Channels, vdagentChannel())
	if err := setDisplay(x, "spice"); err != nil {
		t.Fatal(err)
	}
	doc, _ := x.Marshal()
	for _, want := range []string{`<graphics type="spice" port="-1" autoport="yes" listen="127.0.0.1"`, `<image compression="auto_glz">`, `<channel type="spicevmc">`, `name="com.redhat.spice.0"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("spice: missing %s", want)
		}
	}
	if strings.Contains(doc, "qemu-vdagent") || strings.Contains(doc, `type="vnc"`) || strings.Count(doc, "com.redhat.spice.0") != 1 {
		t.Errorf("leftovers:\n%s", doc)
	}
	if displayProtocol(x) != "spice" {
		t.Error("protocol not read back")
	}
	// Back to VNC: the agent becomes qemu-vdagent again (the clipboard stays).
	if err := setDisplay(x, "vnc"); err != nil {
		t.Fatal(err)
	}
	doc, _ = x.Marshal()
	if !strings.Contains(doc, `<graphics type="vnc"`) || !strings.Contains(doc, "qemu-vdagent") || strings.Contains(doc, "spicevmc") {
		t.Errorf("vnc:\n%s", doc)
	}
	if err := setDisplay(x, "rdp"); err == nil {
		t.Error("unknown protocol accepted")
	}
}
