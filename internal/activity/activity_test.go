package activity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAddListPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Add(Entry{User: "onur", Method: "POST", Path: "/api/v1/domains/u1/actions/start", Status: 202})
	_ = l.Add(Entry{User: "ayse", Method: "DELETE", Path: "/api/v1/domains/u2", Status: 409, Error: "Stop it first"})

	got := l.List(10, Filter{})
	if len(got) != 2 || got[0].User != "ayse" {
		t.Fatalf("newest first expected: %+v", got)
	}
	if f := l.List(10, Filter{Failed: true}); len(f) != 1 || f[0].Error != "Stop it first" {
		t.Errorf("failed filter: %+v", f)
	}
	if f := l.List(10, Filter{Target: "u1"}); len(f) != 1 || f[0].User != "onur" {
		t.Errorf("target filter: %+v", f)
	}

	l2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if again := l2.List(10, Filter{}); len(again) != 2 || again[1].Path != "/api/v1/domains/u1/actions/start" {
		t.Errorf("not persisted: %+v", again)
	}
}

func TestRingLimit(t *testing.T) {
	l, _ := Open("")
	for i := range Keep + 5 {
		_ = l.Add(Entry{Status: 200 + i%2})
	}
	if n := len(l.List(Keep*2, Filter{})); n != Keep {
		t.Errorf("kept %d entries", n)
	}
}

func TestHashChaining(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 5; i++ {
		err := l.Add(Entry{
			User:   fmt.Sprintf("user%d", i),
			Method: "POST",
			Path:   fmt.Sprintf("/api/v1/test/%d", i),
			Status: 200,
		})
		if err != nil {
			t.Fatalf("add entry %d: %v", i, err)
		}
	}

	// List returns newest first
	entries := l.List(10, Filter{})
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(entries))
	}

	// Reverse to oldest first for sequential verification
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}

	for i, e := range entries {
		expectedSeq := int64(i + 1)
		if e.Seq != expectedSeq {
			t.Errorf("entry %d: expected seq %d, got %d", i, expectedSeq, e.Seq)
		}
		if len(e.Hash) != 64 {
			t.Errorf("entry %d: expected 64-char sha256 hash, got %q", i, e.Hash)
		}
		if i == 0 {
			if e.PrevHash != GenesisHash {
				t.Errorf("first entry prevHash expected %s, got %s", GenesisHash, e.PrevHash)
			}
		} else {
			if e.PrevHash != entries[i-1].Hash {
				t.Errorf("entry %d prevHash expected %s, got %s", i, entries[i-1].Hash, e.PrevHash)
			}
		}
		expectedHash := ComputeHash(e)
		if e.Hash != expectedHash {
			t.Errorf("entry %d hash expected %s, got %s", i, expectedHash, e.Hash)
		}
	}
}

func TestVerifyIntegrityIntact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 5; i++ {
		_ = l.Add(Entry{
			User:   "admin",
			Method: "PUT",
			Path:   fmt.Sprintf("/api/v1/config/%d", i),
			Status: 200,
		})
	}

	res, err := l.Verify()
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected log to be valid, got invalid: %s", res.Reason)
	}
	if res.VerifiedCount != 5 {
		t.Errorf("expected 5 verified entries, got %d", res.VerifiedCount)
	}
	if res.FirstSeq != 1 {
		t.Errorf("expected firstSeq 1, got %d", res.FirstSeq)
	}
	if res.LastSeq != 5 {
		t.Errorf("expected lastSeq 5, got %d", res.LastSeq)
	}
	if res.HeadHash != l.lastHash {
		t.Errorf("expected headHash %s, got %s", l.lastHash, res.HeadHash)
	}
}

func TestDetectTamperedField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 10; i++ {
		_ = l.Add(Entry{
			User:   fmt.Sprintf("user%d", i),
			Method: "POST",
			Path:   fmt.Sprintf("/api/v1/resource/%d", i),
			Status: 200,
		})
	}

	// Read all lines from disk
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 10 {
		t.Fatalf("expected 10 lines on disk, got %d", len(lines))
	}

	// Alter line 5 (0-indexed 4): change status from 200 to 500
	var e Entry
	if err := json.Unmarshal([]byte(lines[4]), &e); err != nil {
		t.Fatal(err)
	}
	e.Status = 500 // Tamper status without updating hash
	tamperedBytes, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	lines[4] = string(tamperedBytes)

	// Write back to file
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	res, err := l.Verify()
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if res.Valid {
		t.Fatal("expected verify to fail on tampered field, but got valid")
	}
	if res.TamperedIndex != 4 {
		t.Errorf("expected TamperedIndex 4, got %d", res.TamperedIndex)
	}
	if !strings.Contains(strings.ToLower(res.Reason), "hash mismatch") {
		t.Errorf("expected reason to explain hash mismatch, got %q", res.Reason)
	}
}

func TestDetectDeletedEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 6; i++ {
		_ = l.Add(Entry{
			User:   fmt.Sprintf("user%d", i),
			Method: "POST",
			Path:   fmt.Sprintf("/api/v1/item/%d", i),
			Status: 200,
		})
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 6 {
		t.Fatalf("expected 6 lines, got %d", len(lines))
	}

	// Delete line 3 (index 2)
	lines = append(lines[:2], lines[3:]...)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	res, err := l.Verify()
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if res.Valid {
		t.Fatal("expected verify to fail on deleted entry, but got valid")
	}
	if res.TamperedIndex != 2 {
		t.Errorf("expected TamperedIndex 2, got %d", res.TamperedIndex)
	}
	reasonLower := strings.ToLower(res.Reason)
	if !strings.Contains(reasonLower, "sequence") && !strings.Contains(reasonLower, "prevhash") {
		t.Errorf("expected reason to detect sequence or prevHash breakage, got %q", res.Reason)
	}
}

func TestPersistenceResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 5; i++ {
		_ = l.Add(Entry{
			User:   fmt.Sprintf("user%d", i),
			Method: "POST",
			Path:   fmt.Sprintf("/api/v1/step/%d", i),
			Status: 200,
		})
	}

	entry5Hash := l.lastHash
	if entry5Hash == "" {
		t.Fatal("entry 5 hash was empty")
	}

	// Re-open log from disk
	l2, err := Open(path)
	if err != nil {
		t.Fatalf("re-open failed: %v", err)
	}

	// Add entry 6
	err = l2.Add(Entry{
		User:   "user6",
		Method: "POST",
		Path:   "/api/v1/step/6",
		Status: 200,
	})
	if err != nil {
		t.Fatalf("add entry 6 failed: %v", err)
	}

	entries := l2.List(1, Filter{})
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry from list, got %d", len(entries))
	}
	entry6 := entries[0]
	if entry6.Seq != 6 {
		t.Errorf("expected entry 6 Seq=6, got %d", entry6.Seq)
	}
	if entry6.PrevHash != entry5Hash {
		t.Errorf("expected entry 6 PrevHash %q to match entry 5 Hash %q", entry6.PrevHash, entry5Hash)
	}

	// Verify whole resumed chain is valid
	res, err := l2.Verify()
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected valid chain after resume, got reason: %s", res.Reason)
	}
	if res.VerifiedCount != 6 {
		t.Errorf("expected 6 verified entries, got %d", res.VerifiedCount)
	}
}

func TestExportJSONLandCSV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 3; i++ {
		_ = l.Add(Entry{
			User:   "exportuser",
			Method: "GET",
			Path:   fmt.Sprintf("/api/v1/export/%d", i),
			Status: 200,
		})
	}

	// Test CSV export
	var csvBuf bytes.Buffer
	if err := l.Export(&csvBuf, "csv"); err != nil {
		t.Fatalf("export csv failed: %v", err)
	}
	csvStr := csvBuf.String()
	if !strings.Contains(csvStr, "# virtoMir audit log export; chain_valid=true") {
		t.Errorf("csv missing header comment: %s", csvStr)
	}
	if !strings.Contains(csvStr, "seq,prevHash,hash,time,user,token,remote,method,path,status,error") {
		t.Errorf("csv missing column headers: %s", csvStr)
	}
	if !strings.Contains(csvStr, "exportuser") {
		t.Errorf("csv missing entry content: %s", csvStr)
	}

	// Test JSONL export
	var jsonlBuf bytes.Buffer
	if err := l.Export(&jsonlBuf, "jsonl"); err != nil {
		t.Fatalf("export jsonl failed: %v", err)
	}
	jsonlLines := strings.Split(strings.TrimSpace(jsonlBuf.String()), "\n")
	if len(jsonlLines) != 4 { // 1 metadata header + 3 entries
		t.Fatalf("expected 4 jsonl lines (1 header + 3 entries), got %d: %v", len(jsonlLines), jsonlLines)
	}
	if !strings.Contains(jsonlLines[0], `"_metadata"`) || !strings.Contains(jsonlLines[0], `"valid":true`) {
		t.Errorf("jsonl line 0 missing metadata: %s", jsonlLines[0])
	}
	for i := 1; i <= 3; i++ {
		var e Entry
		if err := json.Unmarshal([]byte(jsonlLines[i]), &e); err != nil {
			t.Errorf("jsonl line %d invalid JSON: %v", i, err)
		}
		if e.Seq != int64(i) || e.User != "exportuser" {
			t.Errorf("jsonl line %d entry mismatch: %+v", i, e)
		}
	}
}

func TestLegacyUnhashedOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	// Write legacy entries without seq, prevHash, hash
	legacy := `{"time":"2026-10-18T10:00:00Z","user":"alice","method":"GET","path":"/api/v1/domains","status":200}` + "\n" +
		`{"time":"2026-10-18T10:01:00Z","user":"bob","method":"POST","path":"/api/v1/domains/1/actions/start","status":202}` + "\n"
	if err := os.WriteFile(path, []byte(legacy), 0o640); err != nil {
		t.Fatal(err)
	}

	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if l.lastSeq != 2 {
		t.Errorf("expected legacy lastSeq=2, got %d", l.lastSeq)
	}
	if l.lastHash != GenesisHash {
		t.Errorf("expected legacy lastHash=%s, got %s", GenesisHash, l.lastHash)
	}

	// Verify on pure legacy file before any new entries
	res, err := l.Verify()
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected legacy-only log to be valid without false alarm, got reason: %s", res.Reason)
	}
	if res.VerifiedCount != 0 {
		t.Errorf("expected 0 verified chained entries for legacy-only log, got %d", res.VerifiedCount)
	}

	// Now add a new entry, it should chain from GenesisHash at seq 3
	_ = l.Add(Entry{User: "carol", Method: "GET", Path: "/api/v1/networks", Status: 200})
	got := l.List(1, Filter{})
	if got[0].Seq != 3 {
		t.Errorf("expected new entry seq 3, got %d", got[0].Seq)
	}
	if got[0].PrevHash != GenesisHash {
		t.Errorf("expected new entry prevHash %s, got %s", GenesisHash, got[0].PrevHash)
	}

	// Verify on legacy log after chained entry is appended
	res2, err := l.Verify()
	if err != nil {
		t.Fatalf("verify error after add: %v", err)
	}
	if !res2.Valid {
		t.Fatalf("expected chain to be valid after adding chained entry to legacy log: %s", res2.Reason)
	}
	if res2.VerifiedCount != 1 {
		t.Errorf("expected 1 verified chained entry, got %d", res2.VerifiedCount)
	}
	if res2.FirstSeq != 3 {
		t.Errorf("expected firstSeq 3, got %d", res2.FirstSeq)
	}
	if res2.LastSeq != 3 {
		t.Errorf("expected lastSeq 3, got %d", res2.LastSeq)
	}
}

func TestVerifyLegacyWithMultipleChainedAndTampering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy_mixed.jsonl")
	legacy := `{"time":"2026-10-18T10:00:00Z","user":"alice","method":"GET","path":"/api/v1/domains","status":200}` + "\n" +
		`{"time":"2026-10-18T10:01:00Z","user":"bob","method":"POST","path":"/api/v1/domains/1/actions/start","status":202}` + "\n"
	if err := os.WriteFile(path, []byte(legacy), 0o640); err != nil {
		t.Fatal(err)
	}

	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 3; i++ {
		_ = l.Add(Entry{
			User:   fmt.Sprintf("user%d", i),
			Method: "POST",
			Path:   fmt.Sprintf("/api/v1/res/%d", i),
			Status: 200,
		})
	}

	res, err := l.Verify()
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected valid log: %s", res.Reason)
	}
	if res.VerifiedCount != 3 {
		t.Errorf("expected 3 verified entries, got %d", res.VerifiedCount)
	}
	if res.FirstSeq != 3 {
		t.Errorf("expected firstSeq 3, got %d", res.FirstSeq)
	}
	if res.LastSeq != 5 {
		t.Errorf("expected lastSeq 5, got %d", res.LastSeq)
	}

	// Tamper with a chained entry (line 4 / index 3, which is seq 4)
	content, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	var e Entry
	_ = json.Unmarshal([]byte(lines[3]), &e)
	e.Status = 500
	tamperedBytes, _ := json.Marshal(e)
	lines[3] = string(tamperedBytes)
	_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o640)

	resTampered, err := l.Verify()
	if err != nil {
		t.Fatalf("verify error on tampered: %v", err)
	}
	if resTampered.Valid {
		t.Fatal("expected verify to detect tampering in chained entry after legacy prefix")
	}
	if resTampered.TamperedIndex != 3 {
		t.Errorf("expected tampered index 3, got %d", resTampered.TamperedIndex)
	}
	if resTampered.TamperedSeq != 4 {
		t.Errorf("expected tampered seq 4, got %d", resTampered.TamperedSeq)
	}
}

func TestVerifyRotatedLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.jsonl")

	// Create path.1 with entries 1 to 5
	var lines1 []string
	var lastH string
	for i := 1; i <= 5; i++ {
		e := Entry{
			Seq:      int64(i),
			PrevHash: GenesisHash,
			Time:     time.Date(2026, 10, 19, 1, 0, i, 0, time.UTC),
			User:     fmt.Sprintf("user%d", i),
			Method:   "GET",
			Path:     fmt.Sprintf("/api/v1/item/%d", i),
			Status:   200,
		}
		if i > 1 {
			e.PrevHash = lastH
		}
		e.Hash = ComputeHash(e)
		lastH = e.Hash
		b, _ := json.Marshal(e)
		lines1 = append(lines1, string(b))
	}
	if err := os.WriteFile(path+".1", []byte(strings.Join(lines1, "\n")+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	// Create path with entries 6 to 10
	var lines2 []string
	for i := 6; i <= 10; i++ {
		e := Entry{
			Seq:      int64(i),
			PrevHash: lastH,
			Time:     time.Date(2026, 10, 19, 1, 0, i, 0, time.UTC),
			User:     fmt.Sprintf("user%d", i),
			Method:   "GET",
			Path:     fmt.Sprintf("/api/v1/item/%d", i),
			Status:   200,
		}
		e.Hash = ComputeHash(e)
		lastH = e.Hash
		b, _ := json.Marshal(e)
		lines2 = append(lines2, string(b))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines2, "\n")+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if l.lastSeq != 10 {
		t.Errorf("expected lastSeq 10, got %d", l.lastSeq)
	}
	if l.lastHash != lastH {
		t.Errorf("expected lastHash %s, got %s", lastH, l.lastHash)
	}

	res, err := l.Verify()
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected rotated log across .1 and active file to be valid, got: %s", res.Reason)
	}
	if res.VerifiedCount != 10 {
		t.Errorf("expected 10 verified entries, got %d", res.VerifiedCount)
	}
	if res.FirstSeq != 1 {
		t.Errorf("expected firstSeq 1, got %d", res.FirstSeq)
	}
	if res.LastSeq != 10 {
		t.Errorf("expected lastSeq 10, got %d", res.LastSeq)
	}
}

func TestVerifyRotatedLogPrunedGenesis(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.jsonl")

	// Simulate log where rotation dropped earliest entries (e.g. entries 1..49 were in an earlier .1)
	// path.1 starts at seq 50 with a valid SHA-256 PrevHash
	prevAnchor := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	lastH := prevAnchor

	var lines1 []string
	for i := 50; i <= 55; i++ {
		e := Entry{
			Seq:      int64(i),
			PrevHash: lastH,
			Time:     time.Date(2026, 10, 19, 2, 0, i, 0, time.UTC),
			User:     fmt.Sprintf("user%d", i),
			Method:   "POST",
			Path:     fmt.Sprintf("/api/v1/action/%d", i),
			Status:   200,
		}
		e.Hash = ComputeHash(e)
		lastH = e.Hash
		b, _ := json.Marshal(e)
		lines1 = append(lines1, string(b))
	}
	if err := os.WriteFile(path+".1", []byte(strings.Join(lines1, "\n")+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	var lines2 []string
	for i := 56; i <= 60; i++ {
		e := Entry{
			Seq:      int64(i),
			PrevHash: lastH,
			Time:     time.Date(2026, 10, 19, 2, 0, i, 0, time.UTC),
			User:     fmt.Sprintf("user%d", i),
			Method:   "POST",
			Path:     fmt.Sprintf("/api/v1/action/%d", i),
			Status:   200,
		}
		e.Hash = ComputeHash(e)
		lastH = e.Hash
		b, _ := json.Marshal(e)
		lines2 = append(lines2, string(b))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines2, "\n")+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	res, err := l.Verify()
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected rotated log with pruned genesis to be valid, got: %s", res.Reason)
	}
	if res.VerifiedCount != 11 {
		t.Errorf("expected 11 verified entries, got %d", res.VerifiedCount)
	}
	if res.FirstSeq != 50 {
		t.Errorf("expected firstSeq 50, got %d", res.FirstSeq)
	}
	if res.LastSeq != 60 {
		t.Errorf("expected lastSeq 60, got %d", res.LastSeq)
	}

	// Now tamper with entry in path.1 (line 3, which is seq 52, index 2)
	lines1Tampered := make([]string, len(lines1))
	copy(lines1Tampered, lines1)
	var e52 Entry
	_ = json.Unmarshal([]byte(lines1Tampered[2]), &e52)
	e52.User = "hacker"
	tamperedBytes, _ := json.Marshal(e52)
	lines1Tampered[2] = string(tamperedBytes)
	_ = os.WriteFile(path+".1", []byte(strings.Join(lines1Tampered, "\n")+"\n"), 0o640)

	resTampered, err := l.Verify()
	if err != nil {
		t.Fatalf("verify error on tampered rotated: %v", err)
	}
	if resTampered.Valid {
		t.Fatal("expected verify to detect tampering in rotated file")
	}
	if resTampered.TamperedIndex != 2 {
		t.Errorf("expected tampered index 2, got %d", resTampered.TamperedIndex)
	}
	if resTampered.TamperedSeq != 52 {
		t.Errorf("expected tampered seq 52, got %d", resTampered.TamperedSeq)
	}
}

func TestVerifyRotatedLogGapDetection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.jsonl")

	prevAnchor := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	lastH := prevAnchor

	var lines1 []string
	for i := 50; i <= 55; i++ {
		e := Entry{
			Seq:      int64(i),
			PrevHash: lastH,
			Time:     time.Date(2026, 10, 19, 2, 0, i, 0, time.UTC),
			User:     fmt.Sprintf("user%d", i),
			Method:   "POST",
			Path:     fmt.Sprintf("/api/v1/action/%d", i),
			Status:   200,
		}
		e.Hash = ComputeHash(e)
		lastH = e.Hash
		b, _ := json.Marshal(e)
		lines1 = append(lines1, string(b))
	}
	_ = os.WriteFile(path+".1", []byte(strings.Join(lines1, "\n")+"\n"), 0o640)

	// In active file, skip seq 56 and start at seq 57
	var lines2 []string
	for i := 57; i <= 60; i++ {
		e := Entry{
			Seq:      int64(i),
			PrevHash: lastH,
			Time:     time.Date(2026, 10, 19, 2, 0, i, 0, time.UTC),
			User:     fmt.Sprintf("user%d", i),
			Method:   "POST",
			Path:     fmt.Sprintf("/api/v1/action/%d", i),
			Status:   200,
		}
		e.Hash = ComputeHash(e)
		lastH = e.Hash
		b, _ := json.Marshal(e)
		lines2 = append(lines2, string(b))
	}
	_ = os.WriteFile(path, []byte(strings.Join(lines2, "\n")+"\n"), 0o640)

	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	res, err := l.Verify()
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if res.Valid {
		t.Fatal("expected verify to fail when gap exists between .1 and active file")
	}
	if res.TamperedIndex != 6 {
		t.Errorf("expected TamperedIndex 6 (boundary), got %d", res.TamperedIndex)
	}
	if res.TamperedSeq != 57 {
		t.Errorf("expected TamperedSeq 57, got %d", res.TamperedSeq)
	}
}

func TestRotateKeepsSeveralOldFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	add := func(n int) {
		for i := 0; i < n; i++ {
			if err := l.Add(Entry{User: "u", Method: "POST", Path: "/x", Status: 200}); err != nil {
				t.Fatal(err)
			}
		}
	}
	add(2)
	if err := rotate(path); err != nil {
		t.Fatal(err)
	}
	add(2)
	if err := rotate(path); err != nil {
		t.Fatal(err)
	}
	add(2)

	// The newest file is path, then .1, .2: the chain still verifies across all three.
	for _, p := range []string{path, path + ".1", path + ".2"} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
	}
	res, err := l.Verify()
	if err != nil || !res.Valid || res.VerifiedCount != 6 {
		t.Fatalf("verify across rotated files = %+v, %v", res, err)
	}
	var buf bytes.Buffer
	if err := l.Export(&buf, "jsonl"); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(buf.String(), `"user"`); n != 6 {
		t.Errorf("export has %d entries, want 6", n)
	}
	re, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := re.Add(Entry{User: "u", Path: "/y", Status: 200}); err != nil {
		t.Fatal(err)
	}
	if res, _ := re.Verify(); !res.Valid || res.VerifiedCount != 7 {
		t.Errorf("after reopen = %+v", res)
	}

	// Rotating more often than OldFiles drops only the oldest.
	for i := 0; i < OldFiles+2; i++ {
		add(1)
		if err := rotate(path); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= OldFiles; i++ {
		if _, err := os.Stat(path + "." + fmt.Sprint(i)); err != nil {
			t.Errorf("%s.%d missing", path, i)
		}
	}
	if _, err := os.Stat(path + "." + fmt.Sprint(OldFiles+1)); err == nil {
		t.Errorf("more than %d old files kept", OldFiles)
	}
}
