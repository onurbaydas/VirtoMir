package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

func wait(t *testing.T, m *Manager, id string) Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if j, _ := m.Get(id); j.State != Running {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return Job{}
}

func TestJobLifecycle(t *testing.T) {
	m := New(context.Background())
	release := make(chan struct{})
	j := m.Start("clone", "Clone web", func(_ context.Context, report func(string)) (string, error) {
		report("copying")
		<-release
		return "new-uuid", nil
	})
	if j.State != Running || j.ID == "" {
		t.Fatalf("start = %+v", j)
	}
	time.Sleep(20 * time.Millisecond)
	if got, _ := m.Get(j.ID); got.Message != "copying" {
		t.Errorf("progress = %q", got.Message)
	}
	close(release)
	if done := wait(t, m, j.ID); done.State != Succeeded || done.Result != "new-uuid" || done.Finished == nil {
		t.Errorf("done = %+v", done)
	}
}

func TestJobFailure(t *testing.T) {
	m := New(context.Background())
	j := m.Start("clone", "x", func(context.Context, func(string)) (string, error) { return "", errors.New("disk full") })
	if done := wait(t, m, j.ID); done.State != Failed || done.Message != "disk full" {
		t.Errorf("done = %+v", done)
	}
}

func TestTrimKeepsRunning(t *testing.T) {
	m := New(context.Background())
	block := make(chan struct{})
	defer close(block)
	running := m.Start("long", "x", func(context.Context, func(string)) (string, error) { <-block; return "", nil })
	for range Keep + 10 {
		j := m.Start("quick", "x", func(context.Context, func(string)) (string, error) { return "", nil })
		wait(t, m, j.ID)
	}
	if _, ok := m.Get(running.ID); !ok {
		t.Error("running job was trimmed")
	}
	if n := len(m.List()); n > Keep+1 {
		t.Errorf("kept %d jobs", n)
	}
}
