//go:build windows

package dr

import (
	"os/exec"
	"strconv"
)

// killProcessGroup kills a hook's whole process tree on Windows (where there
// are no process groups) via taskkill /T: a hook's shell may have spawned
// children that hold the output pipes open, and killing only the shell would
// leave Run waiting on them past the deadline.
func killProcessGroup(pid int) {
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}

// startHookProc starts the hook shell the plain way on Windows.
func startHookProc(cmd *exec.Cmd) error {
	return cmd.Start()
}
