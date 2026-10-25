package gpool

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStoreCRUD(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gpools.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	l40, err := s.Create(Pool{ID: "l40s", Name: "L40S whole", Model: "L40S", Kind: KindWhole})
	if err != nil || l40.Created.IsZero() {
		t.Fatalf("create: %+v %v", l40, err)
	}
	if _, err := s.Create(Pool{ID: "l40s", Name: "again", Model: "x", Kind: KindWhole}); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate: %v", err)
	}
	for _, bad := range []Pool{
		{ID: "A", Name: "x", Model: "x", Kind: KindWhole},
		{ID: "ok-id", Name: " ", Model: "x", Kind: KindWhole},
		{ID: "ok-id", Name: "x", Kind: "half"},
		{ID: "ok-id", Name: "x", Kind: KindMIG},
		{ID: "ok-id", Name: "x", Kind: KindWhole},
	} {
		if _, err := s.Create(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted %+v: %v", bad, err)
		}
	}
	if _, err := s.Create(Pool{ID: "a100-1g", Name: "A100 1g", Model: "A100", Kind: KindMIG, Profile: "1g.10gb"}); err != nil {
		t.Fatal(err)
	}
	l40.Description = "render nodes"
	if _, err := s.Update(l40); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("a100-1g"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("a100-1g"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}

	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := r.Get("l40s"); !ok || got.Description != "render nodes" || len(r.List()) != 1 || r.Shared().Version != 4 {
		t.Errorf("reloaded %+v, %d pools, version %d", got, len(r.List()), r.Shared().Version)
	}
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}

	other, _ := Open("")
	if ok, _ := other.Adopt(r.Shared()); !ok || len(other.List()) != 1 {
		t.Error("newer pools not adopted")
	}
	if ok, _ := other.Adopt(Shared{Version: 1}); ok {
		t.Error("older pools adopted")
	}
}
