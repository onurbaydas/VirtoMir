package virt

import (
	"strings"
	"testing"
)

func TestPCIBlocked(t *testing.T) {
	g := 7
	gpu := HostPCI{Address: "0000:01:00.0", Class: "0x030000", IOMMUGroup: &g, Group: []string{"0000:01:00.1", "0000:00:01.0"}}
	drivers := map[string]string{"0000:01:00.1": "snd_hda_intel", "0000:00:01.0": "pcieport"}
	bridge := func(a string) bool { return a == "0000:00:01.0" }

	if r := pciBlocked(gpu, false, drivers, bridge); !strings.Contains(r, "IOMMU is off") {
		t.Errorf("no IOMMU: %q", r)
	}
	// The GPU's audio function is still on a host driver: the group is not free.
	if r := pciBlocked(gpu, true, drivers, bridge); !strings.Contains(r, "0000:01:00.1 (snd_hda_intel)") || strings.Contains(r, "pcieport") {
		t.Errorf("busy group: %q", r)
	}
	drivers["0000:01:00.1"] = "vfio-pci"
	if r := pciBlocked(gpu, true, drivers, bridge); r != "" {
		t.Errorf("free group refused: %q", r)
	}
	nic := gpu
	nic.HostUse = "network interface eno1 has an address (192.168.1.2/24)"
	if r := pciBlocked(nic, true, drivers, bridge); !strings.Contains(r, "host needs it") {
		t.Errorf("host NIC: %q", r)
	}
	br := HostPCI{Class: "0x060400", IOMMUGroup: &g}
	if r := pciBlocked(br, true, drivers, bridge); !strings.Contains(r, "bridge") {
		t.Errorf("bridge: %q", r)
	}
	if r := pciBlocked(HostPCI{Class: "0x020000"}, true, drivers, bridge); !strings.Contains(r, "not in an IOMMU group") {
		t.Errorf("no group: %q", r)
	}
}

func TestParsePCIAddress(t *testing.T) {
	a, err := parsePCIAddress("0000:0a:1f.3")
	if err != nil || pciAddr(a) != "0000:0a:1f.3" {
		t.Fatalf("= %v, %v", a, err)
	}
	for _, bad := range []string{"", "0a:1f.3", "zzzz:00:00.0"} {
		if _, err := parsePCIAddress(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
