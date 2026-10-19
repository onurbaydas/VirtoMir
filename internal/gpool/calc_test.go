package gpool

import (
	"testing"

	"github.com/onurbaydas/virtomir/internal/hostinfo"
	"github.com/onurbaydas/virtomir/internal/virt"
)

func host() HostHardware {
	return HostHardware{
		HostID: "h1", HostName: "hv-01",
		GPUs: []hostinfo.GPU{
			{Address: "0000:41:00.0", Name: "NVIDIA L40S", PassThroughReady: true, Telemetry: "nvidia-smi"},
			{Address: "0000:42:00.0", Name: "NVIDIA L40S", PassThroughReady: true},
			{Address: "0000:43:00.0", Name: "NVIDIA L40S"}, // no IOMMU group: cannot be passed through
			{Address: "0000:81:00.0", Name: "Tesla T4", MDevTypes: []hostinfo.MDevTypeInfo{{ID: "nvidia-222", Available: 6}}},
			{Address: "0000:c1:00.0", Name: "NVIDIA A100-PCIE-40GB", MIGProfiles: []hostinfo.MIGProfile{{ID: "1g.5gb", Count: 7, Free: 5}, {ID: "3g.20gb", Count: 2, Free: 2}}},
		},
		PCI: []virt.HostPCI{
			{Address: "0000:42:00.0", Product: "AD102GL [L40S]", UsedBy: []string{"render-01"}},
			{Address: "0000:90:00.0", Product: "Not a GPU"},
		},
		MDevs: []virt.HostMDev{
			{UUID: "m-used", Parent: "0000:81:00.0", Type: "nvidia-222", UsedBy: []string{"vdi-01"}},
			{UUID: "m-free", Parent: "0000:81:00.0", Type: "nvidia-222"},
			{UUID: "m-other", Parent: "0000:81:00.0", Type: "nvidia-223"},
		},
	}
}

func TestAvailability(t *testing.T) {
	for _, tc := range []struct {
		pool              Pool
		total, used, free int
	}{
		{Pool{ID: "l40s", Model: "L40S", Kind: KindWhole}, 3, 1, 1},
		{Pool{ID: "t4", Model: "T4", Kind: KindMDev, Profile: "nvidia-222"}, 8, 1, 7},
		{Pool{ID: "any-t4", Kind: KindMDev, Profile: "nvidia-222"}, 8, 1, 7},
		{Pool{ID: "a100", Model: "a100", Kind: KindMIG, Profile: "1g.5gb"}, 7, 2, 5},
		{Pool{ID: "h100", Model: "H100", Kind: KindWhole}, 0, 0, 0},
	} {
		r := Availability(tc.pool, host())
		if r.Total != tc.total || r.Used != tc.used || r.Free != tc.free || r.HostName != "hv-01" {
			t.Errorf("%s: %+v, want total %d used %d free %d", tc.pool.ID, r, tc.total, tc.used, tc.free)
		}
	}
	if r := Availability(Pool{ID: "l40s", Model: "L40S", Kind: KindWhole}, host()); r.Telemetry != "nvidia-smi" {
		t.Errorf("telemetry %q", r.Telemetry)
	}
}

func TestClusterAvailability(t *testing.T) {
	h2 := host()
	h2.HostID, h2.HostName, h2.PCI = "h2", "hv-02", nil
	got := ClusterAvailability([]Pool{{ID: "l40s", Model: "L40S", Kind: KindWhole}, {ID: "h100", Model: "H100", Kind: KindWhole}}, []HostHardware{host(), h2})
	if len(got["l40s"]) != 2 || got["l40s"][1].Free != 2 || len(got["h100"]) != 0 {
		t.Errorf("%+v", got)
	}
}
