package reports

import (
	"strings"
	"testing"
	"time"
)

func TestReports_GenerationAndFormats(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// 1. Capacity report (HTML and CSV)
	capData := CapacityData{
		ClusterName:      "VirtCluster-01",
		TotalHosts:       3,
		TotalVCPUs:       48,
		AllocatedVCPUs:   32,
		TotalMemoryGiB:   192.0,
		AllocatedMemGiB:  128.0,
		TotalStorageGiB:  8192.0,
		AllocatedDiskGiB: 4096.0,
		RunningVMs:       14,
		TotalVMs:         18,
		ForecastDays:     120,
	}

	htmlMeta, err := store.GenerateCapacityReport(capData, FormatHTML)
	if err != nil {
		t.Fatalf("GenerateCapacityReport HTML: %v", err)
	}
	content, _, err := store.GetContent(htmlMeta.ID)
	if err != nil {
		t.Fatalf("GetContent HTML: %v", err)
	}
	if !strings.Contains(string(content), "Cluster Capacity") {
		t.Errorf("HTML content missing title")
	}

	csvMeta, err := store.GenerateCapacityReport(capData, FormatCSV)
	if err != nil {
		t.Fatalf("GenerateCapacityReport CSV: %v", err)
	}
	csvContent, _, err := store.GetContent(csvMeta.ID)
	if err != nil {
		t.Fatalf("GetContent CSV: %v", err)
	}
	if !strings.Contains(string(csvContent), "VirtCluster-01") {
		t.Errorf("CSV content missing cluster name")
	}

	// 2. Inventory report
	invItems := []InventoryItem{
		{
			UUID:        "uuid-vm-1",
			Name:        "web-prod-01",
			State:       "running",
			Host:        "hv-01",
			VCPUs:       4,
			MemoryMiB:   8192,
			DisksGiB:    100,
			IPAddresses: "192.168.77.50",
			OS:          "Debian 13",
		},
	}
	invMeta, err := store.GenerateInventoryReport(invItems, FormatHTML)
	if err != nil {
		t.Fatalf("GenerateInventoryReport: %v", err)
	}
	if invMeta.SizeBytes <= 0 {
		t.Errorf("expected positive size")
	}

	// 3. Backup SLA report
	slaItems := []BackupSLAItem{
		{
			VMName:       "web-prod-01",
			LastBackup:   time.Now().Add(-2 * time.Hour),
			Status:       "compliant",
			BackupSizeMB: 2048,
			Target:       "/var/lib/virtomir/backups",
		},
	}
	slaMeta, err := store.GenerateBackupSLAReport(slaItems, FormatHTML)
	if err != nil {
		t.Fatalf("GenerateBackupSLAReport: %v", err)
	}
	if slaMeta.SizeBytes <= 0 {
		t.Errorf("expected positive size")
	}

	// 4. Listing and deletion
	list := store.List()
	if len(list) != 4 {
		t.Fatalf("expected 4 reports, got %d", len(list))
	}

	err = store.Delete(htmlMeta.ID)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	listAfter := store.List()
	if len(listAfter) != 3 {
		t.Errorf("expected 3 reports after deletion, got %d", len(listAfter))
	}
}
