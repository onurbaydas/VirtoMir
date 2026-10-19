//go:build unix

package dr

import (
	"os/exec"
	"syscall"
)

// killProcessGroup kills a whole hook process group on timeout: a hook's
// shell may have spawned children that hold the output pipes open, and
// killing only the shell would leave Run waiting on them past the deadline.
func killProcessGroup(pid int) {
	if pid <= 1 {
		return
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil || pgid <= 1 {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// startHookProc starts the hook shell in its own process group, so its
// children can be killed together with it on timeout.
func startHookProc(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}
