package activity

import (
	"path/filepath"
	"testing"
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
