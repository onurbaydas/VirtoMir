//go:build unix

package dr

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestDefaultHookRunnerTimeout verifies the hook runner cuts a hook off at
// its deadline even when a background child of the shell holds the output
// pipe open: the hook runs in its own process group and the whole group is
// killed. (On Windows there are no process groups and MSYS sh re-parents
// background children off the pipe, so the scenario cannot be reproduced.)
func TestDefaultHookRunnerTimeout(t *testing.T) {
	hr := NewDefaultHookRunner()
	if len(hr.Shell) == 0 {
		t.Skip("no sh or bash on this host; the hook runner cannot be tested")
	}

	// The child's stdout stays on the hook's pipe (no redirection), so the
	// shell's death alone would not close it.
	start := time.Now()
	_, err := hr.RunHook(context.Background(), "sleep 30 &", nil, 500*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v, want a timeout error", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the timeout did not cut the hook off in time: %s", elapsed)
	}
}

// TestKillProcessGroupSafeWhenReaped verifies that killProcessGroup safely returns
// without signalling when called with invalid or already-reaped PIDs.
func TestKillProcessGroupSafeWhenReaped(t *testing.T) {
	killProcessGroup(0)
	killProcessGroup(1)
	killProcessGroup(-1)
	killProcessGroup(99999999)
}

