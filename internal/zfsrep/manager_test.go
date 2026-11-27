package zfsrep

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestDeleteKeepsSnapshots(t *testing.T) {
	f := newFake()
	m, _ := open(t, f)
	st, err := m.Put(Spec{SourcePool: "tank/vms", SourceVolume: "web", TargetHost: "root@backup", TargetPool: "dr", IntervalMinutes: 5})
	if err != nil {
		t.Fatal(err)
	}
	syncOnce(t, m, st.Spec.ID)
	before := slices.Clone(f.hosts[""]["tank/vms/web"])

	if err := m.Delete(st.Spec.ID); err != nil {
		t.Fatal(err)
	}
	if len(m.List()) != 0 {
		t.Errorf("still listed: %+v", m.List())
	}
	if got := f.hosts[""]["tank/vms/web"]; !slices.Equal(got, before) || len(got) == 0 {
		t.Errorf("source snapshots %v, want them kept (%v)", got, before)
	}
	if len(f.hosts["root@backup"]["dr/web"]) == 0 {
		t.Error("target snapshots removed")
	}
	if err := m.Delete(st.Spec.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete of an unknown ID = %v", err)
	}
	m2, err := Open(m.path, f, nil)
	if err != nil || len(m2.List()) != 0 {
		t.Errorf("deletion not saved: %+v, %v", m2.List(), err)
	}
}

func TestRunStartsOnlyDueEnabledAndStops(t *testing.T) {
	f := newFake()
	f.hosts[""]["tank/vms/db"] = nil
	f.hosts[""]["tank/vms/old"] = nil
	m, now := open(t, f)
	due, _ := m.Put(Spec{SourcePool: "tank/vms", SourceVolume: "web", TargetHost: "root@backup", TargetPool: "dr", IntervalMinutes: 5, Enabled: true})
	off, _ := m.Put(Spec{SourcePool: "tank/vms", SourceVolume: "db", TargetHost: "root@backup", TargetPool: "dr", IntervalMinutes: 5})
	recent, _ := m.Put(Spec{SourcePool: "tank/vms", SourceVolume: "old", TargetHost: "root@backup", TargetPool: "dr", IntervalMinutes: 60, Enabled: true})
	// recent ran a minute ago: not due for another 59 minutes.
	m.mu.Lock()
	m.find(recent.Spec.ID).LastAttempt = now.Add(-time.Minute)
	m.mu.Unlock()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.pipes)
		f.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}

	if len(f.pipes) != 1 {
		t.Fatalf("%d passes, want 1: %v", len(f.pipes), f.pipes)
	}
	for _, s := range m.List() {
		switch s.Spec.ID {
		case due.Spec.ID:
			if s.LastSnapshot == "" {
				t.Errorf("due replication did not run: %+v", s)
			}
		case off.Spec.ID, recent.Spec.ID:
			if s.LastSnapshot != "" {
				t.Errorf("%s ran: %+v", s.Spec.SourceVolume, s)
			}
		}
	}
}
