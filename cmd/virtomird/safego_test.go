package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestRunSafeSurvivesAPanic(t *testing.T) {
	restartBackoff = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	runSafe(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), "test", func(context.Context) {
		calls++
		if calls == 1 {
			panic("boom")
		}
	})
	if calls != 2 {
		t.Fatalf("run was called %d times, want 2 (restarted once after the panic)", calls)
	}
}
