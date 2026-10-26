package jobs

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestPanickingJobFails(t *testing.T) {
	var buf bytes.Buffer
	m := New(context.Background())
	m.Log = slog.New(slog.NewTextHandler(&buf, nil))
	release := make(chan struct{})
	j := m.Start("backup", "Backup web", func(_ context.Context, report func(string)) (string, error) {
		<-release
		var counts map[string]int
		counts["x"]++ // assignment to entry in nil map
		return "", nil
	})
	if n := m.Running(); n != 1 {
		t.Fatalf("running = %d", n)
	}
	close(release)
	got := wait(t, m, j.ID)
	if got.State != Failed || !strings.HasPrefix(got.Message, "internal error: ") || got.Finished == nil {
		t.Fatalf("job = %+v", got)
	}
	if !strings.Contains(buf.String(), "job panicked") || !strings.Contains(buf.String(), "stack=") {
		t.Fatalf("log = %s", buf.String())
	}
	if n := m.Running(); n != 0 {
		t.Fatalf("running = %d", n)
	}
}
