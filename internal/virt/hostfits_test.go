package virt

import (
	"strings"
	"testing"
)

func TestCPUsFit(t *testing.T) {
	for _, c := range []struct {
		vcpus uint
		host  int32
		ok    bool
	}{
		{4, 20, true},
		{20, 20, true}, // every thread
		{21, 20, false},
		{512, 0, true}, // host unknown: no guess
	} {
		if err := cpusFit(c.vcpus, c.host); (err == nil) != c.ok {
			t.Errorf("cpusFit(%d, %d) = %v, want ok=%v", c.vcpus, c.host, err, c.ok)
		}
	}
	if err := cpusFit(64, 20); err == nil || !strings.Contains(err.Error(), "20 CPU threads") {
		t.Errorf("message should name the host's threads: %v", err)
	}
}

func TestSettingsFit(t *testing.T) {
	const hostKiB, hostCPUs = 16 << 20, 8 // 16 GiB, 8 threads
	old := SettingsView{Settings: Settings{VCPUs: 2, MemoryMiB: 2048}}
	for _, c := range []struct {
		name  string
		old   SettingsView
		vcpus uint
		mem   uint64
		ok    bool
	}{
		{"within the host", old, 8, 16 << 10, true},
		{"more memory than the host", old, 2, 32 << 10, false},
		{"more vCPUs than the host", old, 16, 2048, false},
		// Moved here from a larger host: unchanged values stay, other edits go through.
		{"unchanged oversize", SettingsView{Settings: Settings{VCPUs: 32, MemoryMiB: 64 << 10}}, 32, 64 << 10, true},
	} {
		err := settingsFit(c.old, Settings{VCPUs: c.vcpus, MemoryMiB: c.mem}, hostKiB, hostCPUs)
		if (err == nil) != c.ok {
			t.Errorf("%s: settingsFit = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestRestoreNeed(t *testing.T) {
	chains := [][]imageInfo{
		{{ActualSize: 3 << 30, VirtualSize: 40 << 30}},                                               // full backup
		{{ActualSize: 1 << 30, VirtualSize: 20 << 30}, {ActualSize: 2 << 30, VirtualSize: 20 << 30}}, // incremental chain
	}
	if got, want := restoreNeed(chains, false), uint64(6<<30); got != want {
		t.Errorf("file pool: need %d, want %d (what the chains hold)", got, want)
	}
	if got, want := restoreNeed(chains, true), uint64(60<<30); got != want {
		t.Errorf("block pool: need %d, want %d (whole disks)", got, want)
	}
}

func TestParseMemAvailable(t *testing.T) {
	const meminfo = "MemTotal:       32603764 kB\nMemFree:         1234567 kB\nMemAvailable:   20480000 kB\nBuffers: 1 kB\n"
	if got := parseMemAvailable(meminfo); got != 20480000 {
		t.Errorf("parseMemAvailable = %d, want 20480000", got)
	}
	if got := parseMemAvailable("MemTotal: 1 kB\n"); got != 0 {
		t.Errorf("no MemAvailable line: %d, want 0", got)
	}
}
