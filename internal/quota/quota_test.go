package quota

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestForAndCheck(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "quotas.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set([]Rule{
		{Kind: "group", Name: "students", Machines: 2, VCPUs: 4, MemoryMiB: 4096},
		{Kind: "group", Name: "staff", Machines: 5, VCPUs: 16, MemoryMiB: 32768},
		{Kind: "user", Name: "ayse", Machines: 1, VCPUs: 1, MemoryMiB: 512},
	}); err != nil {
		t.Fatal(err)
	}
	if r, ok := s.For("ayse", []string{"staff"}); !ok || r.Machines != 1 {
		t.Errorf("user rule must win: %+v", r)
	}
	if r, ok := s.For("ali", []string{"students", "staff"}); !ok || r.Name != "staff" {
		t.Errorf("most generous group: %+v", r)
	}
	if _, ok := s.For("veli", []string{"other"}); ok {
		t.Error("no rule must mean no quota")
	}
	r, _ := s.For("ali", []string{"students"})
	if err := Check(r, Usage{Machines: 1, VCPUs: 2, MemoryMiB: 2048, DiskGiB: 900}, 1, 2, 2048, 500); err != nil {
		t.Errorf("exactly at the limit: %v", err)
	}
	err = Check(r, Usage{Machines: 2, VCPUs: 4, MemoryMiB: 4096}, 1, 1, 512, 0)
	if err == nil || !strings.Contains(err.Error(), "machines (2 of 2 used)") {
		t.Errorf("over: %v", err)
	}
	// A disk limit, when set, counts; nil (older rules) means none.
	disk := uint64(100)
	r.DiskGiB = &disk
	if err := Check(r, Usage{DiskGiB: 80}, 0, 0, 0, 20); err != nil {
		t.Errorf("disk at the limit: %v", err)
	}
	if err := Check(r, Usage{DiskGiB: 80}, 0, 0, 0, 21); err == nil || !strings.Contains(err.Error(), "disk space (80 of 100 GiB used, this needs 21)") {
		t.Errorf("disk over: %v", err)
	}
	// Persisted.
	s2, _ := Open(s.path)
	if len(s2.Rules()) != 3 {
		t.Errorf("reloaded %d rules", len(s2.Rules()))
	}
	if err := s.Set([]Rule{{Kind: "team", Name: "x"}}); err == nil {
		t.Error("bad kind accepted")
	}
	if err := s.Set([]Rule{{Kind: "user", Name: "a"}, {Kind: "user", Name: "a"}}); err == nil {
		t.Error("duplicate accepted")
	}
}
