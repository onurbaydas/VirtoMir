package gpool

import (
	"errors"
	"testing"
	"time"
)

func TestAllocate(t *testing.T) {
	h := host()
	got, err := Allocate(Pool{ID: "l40s", Model: "L40S", Kind: KindWhole}, h, 1)
	if err != nil || len(got) != 1 || got[0].PCI != "0000:41:00.0" {
		t.Fatalf("whole: %+v %v (the free card, not the used or the unready one)", got, err)
	}
	if _, err := Allocate(Pool{ID: "l40s", Model: "L40S", Kind: KindWhole}, h, 2); !errors.Is(err, ErrNoCapacity) {
		t.Errorf("two whole cards from one free: %v", err)
	}
	got, err = Allocate(Pool{ID: "t4", Kind: KindMDev, Profile: "nvidia-222"}, h, 3)
	if err != nil || len(got) != 3 || got[0].MDevUUID != "m-free" || got[1].Parent != "0000:81:00.0" || got[1].Type != "nvidia-222" {
		t.Fatalf("mdev: %+v %v (the unused slice first, then new ones)", got, err)
	}
	if _, err := Allocate(Pool{ID: "a100", Kind: KindMIG, Profile: "1g.5gb"}, h, 1); !errors.Is(err, ErrInvalid) {
		t.Errorf("MIG handed out: %v", err)
	}
	if _, err := Allocate(Pool{ID: "l40s", Model: "L40S", Kind: KindWhole, PerHost: 1}, h, 2); !errors.Is(err, ErrInvalid) {
		t.Errorf("per-host cap: %v", err)
	}
}

func TestCandidatesAndReservations(t *testing.T) {
	h1, h2 := host(), host()
	h2.HostID, h2.HostName, h2.PCI = "h2", "hv-02", nil // both L40S of 41 and 42 free there
	s, _ := Open("")
	pool, _ := s.Create(Pool{ID: "l40s", Name: "L40S", Model: "L40S", Kind: KindWhole})
	got := Candidates(pool, []HostHardware{h1, h2}, 1, func(host string) int { return s.Reserved("l40s", host) })
	if len(got) != 2 || got[0].HostID != "h2" || got[0].Available != 2 {
		t.Fatalf("candidates %+v", got)
	}
	if err := s.Reserve("vm-1", "l40s", "h2", 2); err != nil {
		t.Fatal(err)
	}
	got = Candidates(pool, []HostHardware{h1, h2}, 1, func(host string) int { return s.Reserved("l40s", host) })
	if len(got) != 1 || got[0].HostID != "h1" {
		t.Errorf("after reserving h2's cards: %+v", got)
	}
	s.Release("vm-1")
	if s.Reserved("l40s", "h2") != 0 {
		t.Error("release")
	}
	defer func(f func() time.Time) { now = f }(now)
	_ = s.Reserve("vm-2", "l40s", "h2", 1)
	now = func() time.Time { return time.Now().Add(time.Hour) }
	if s.Reserved("l40s", "h2") != 0 {
		t.Error("an old reservation did not expire")
	}
	if err := s.Reserve("vm-3", "nope", "h2", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("reserve in an unknown pool: %v", err)
	}
}
