package respool

import (
	"path/filepath"
	"testing"
)

func TestRespool_CRUDAndAssignment(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "respools.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Create root pool
	root, err := store.Create(ResourcePool{
		Name:           "Production",
		CPULimit:       16,
		MemoryLimitMiB: 32768,
		StoragePoolQuotas: map[string]uint64{
			"default": 1024 * 1024 * 1024 * 1024, // 1TB
		},
	})
	if err != nil {
		t.Fatalf("Create root pool: %v", err)
	}

	// Create child pool
	child, err := store.Create(ResourcePool{
		Name:           "Databases",
		ParentID:       root.ID,
		CPULimit:       8,
		MemoryLimitMiB: 16384,
	})
	if err != nil {
		t.Fatalf("Create child pool: %v", err)
	}

	// Assign VM
	err = store.AssignVM(child.ID, "vm-db-01")
	if err != nil {
		t.Fatalf("AssignVM: %v", err)
	}

	found := store.GetPoolForVM("vm-db-01")
	if found == nil || found.ID != child.ID {
		t.Fatalf("expected VM in child pool, got %+v", found)
	}

	// Cannot delete pool with assigned VMs
	err = store.Delete(child.ID)
	if err == nil {
		t.Errorf("expected error deleting pool with assigned VMs")
	}

	// Cannot delete root pool with children
	err = store.Delete(root.ID)
	if err == nil {
		t.Errorf("expected error deleting parent pool")
	}

	// Quota check
	usage := PoolUsage{
		PoolID:        child.ID,
		UsedVCPUs:     6,
		UsedMemoryMiB: 12288,
	}
	// Adding 4 vCPUs should exceed limit 8 (6+4 = 10 > 8)
	err = store.CheckQuota(child.ID, usage, 4, 0, "", 0)
	if err == nil {
		t.Errorf("expected quota exceeded error for vCPUs")
	}

	// Adding 2 vCPUs should pass (6+2 = 8 <= 8)
	err = store.CheckQuota(child.ID, usage, 2, 0, "", 0)
	if err != nil {
		t.Errorf("expected quota check to pass, got: %v", err)
	}

	// Unassign VM and delete
	err = store.UnassignVM("vm-db-01")
	if err != nil {
		t.Fatalf("UnassignVM: %v", err)
	}
	err = store.Delete(child.ID)
	if err != nil {
		t.Fatalf("Delete child: %v", err)
	}
	err = store.Delete(root.ID)
	if err != nil {
		t.Fatalf("Delete root: %v", err)
	}
}
