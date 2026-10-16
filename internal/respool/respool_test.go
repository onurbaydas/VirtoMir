package respool

import (
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "respools.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestHierarchyLimits(t *testing.T) {
	s := open(t)
	prod, err := s.Create(ResourcePool{Name: "Production", CPULimit: 16, MemoryLimitMiB: 32768,
		StorageLimits: map[string]uint64{"nfs": 1 << 40}})
	if err != nil || prod.ID != "production" {
		t.Fatalf("create = %+v, %v", prod, err)
	}
	if _, err := s.Create(ResourcePool{Name: "production"}); err == nil {
		t.Error("duplicate name accepted")
	}
	for _, bad := range []ResourcePool{
		{Name: "a", ParentID: "production", CPULimit: 32, MemoryLimitMiB: 1024, StorageLimits: map[string]uint64{"nfs": 1}},
		{Name: "b", ParentID: "production", CPULimit: 0, MemoryLimitMiB: 1024, StorageLimits: map[string]uint64{"nfs": 1}},
		{Name: "c", ParentID: "production", CPULimit: 4, MemoryLimitMiB: 1024},
		{Name: "d", ParentID: "nope"},
	} {
		if _, err := s.Create(bad); err == nil {
			t.Errorf("accepted child %+v", bad)
		}
	}
	db, err := s.Create(ResourcePool{Name: "DB team", ParentID: "production", CPULimit: 8, MemoryLimitMiB: 16384,
		StorageLimits: map[string]uint64{"nfs": 500 << 30}})
	if err != nil {
		t.Fatal(err)
	}
	// A parent cannot be moved under its own child.
	p := prod
	p.ParentID = db.ID
	if err := s.Update(p); err == nil || !strings.Contains(err.Error(), "inside itself") {
		t.Errorf("cycle: %v", err)
	}

	use := map[string]MachineUse{
		"vm1": {VCPUs: 4, MemoryMiB: 8192, StorageBytes: map[string]uint64{"nfs": 100 << 30}},
		"vm2": {VCPUs: 6, MemoryMiB: 4096},
	}
	lookup := func(u string) (MachineUse, bool) { m, ok := use[u]; return m, ok }
	fits := func(d Demand) func([]ResourcePool) error {
		return func(chain []ResourcePool) error { return Check(chain, s.Usages(lookup), d) }
	}
	if err := s.Assign(db.ID, "VM1", fits(Demand{VCPUs: 4, MemoryMiB: 8192, StoragePool: "nfs", StorageBytes: 100 << 30})); err != nil {
		t.Fatal(err)
	}
	if s.PoolOf("vm1") != db.ID {
		t.Fatal("vm1 not in db")
	}
	// vm2 does not fit the child's 8 vCPUs next to vm1 …
	if err := s.Assign(db.ID, "vm2", fits(Demand{VCPUs: 6})); err == nil || !strings.Contains(err.Error(), "DB team") {
		t.Errorf("over child limit: %v", err)
	}
	// … but fits the parent.
	if err := s.Assign(prod.ID, "vm2", fits(Demand{VCPUs: 6})); err != nil {
		t.Fatal(err)
	}
	u := s.Usages(lookup)
	if u["production"].VCPUs != 10 || u["production"].Machines != 2 || u["db-team"].VCPUs != 4 {
		t.Errorf("usage = %+v", u)
	}
	if u["production"].StorageBytes["nfs"] != 100<<30 {
		t.Errorf("storage = %+v", u["production"].StorageBytes)
	}
	// Growing vm1 by 8 vCPUs breaks the child limit; growing by 2 fits both.
	if err := Check(s.Chain(db.ID), u, Demand{VCPUs: 8}); err == nil {
		t.Error("child limit not enforced")
	}
	if err := Check(s.Chain(db.ID), u, Demand{VCPUs: 2}); err != nil {
		t.Error(err)
	}
	// Storage: 450 GiB more on nfs exceeds DB team's 500 GiB.
	if err := Check(s.Chain(db.ID), u, Demand{StoragePool: "nfs", StorageBytes: 450 << 30}); err == nil {
		t.Error("storage limit not enforced")
	}
	if err := s.Delete(prod.ID); err == nil {
		t.Error("deleted a pool with machines and children")
	}
	s.Forget("vm1")
	if s.PoolOf("vm1") != "" {
		t.Error("forgotten machine still in a pool")
	}
	// Survives a restart.
	s2, err := Open(s.path)
	if err != nil || len(s2.List()) != 2 || s2.PoolOf("vm2") != prod.ID {
		t.Fatalf("reopen: %v %+v", err, s2.List())
	}
}
