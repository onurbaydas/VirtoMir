package api

import (
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestPlainError(t *testing.T) {
	cases := []struct {
		err    error
		status int
		says   string
	}{
		{fmt.Errorf("connect to libvirt at /run/libvirt/libvirt-sock: %w", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}), http.StatusServiceUnavailable, "cannot reach libvirt"},
		{fmt.Errorf("write backup: %w", &os.PathError{Op: "write", Path: "/b/x.qcow2", Err: syscall.ENOSPC}), http.StatusInsufficientStorage, "Not enough space"},
		{fmt.Errorf("open: %w", &os.PathError{Op: "open", Path: "/nope", Err: fs.ErrNotExist}), http.StatusNotFound, "/nope does not exist"},
		{fmt.Errorf("mkdir: %w", &os.PathError{Op: "mkdir", Path: "/root/x", Err: fs.ErrPermission}), http.StatusForbidden, "Permission denied on /root/x"},
		{fmt.Errorf("mkdir: %w", &os.PathError{Op: "mkdir", Path: "/home/me/backups", Err: syscall.EROFS}), http.StatusForbidden, "ProtectHome=false"},
		{fmt.Errorf("mkdir: %w", &os.PathError{Op: "mkdir", Path: "/mnt/ro/x", Err: syscall.EROFS}), http.StatusForbidden, "/mnt/ro/x is read-only"},
		{fmt.Errorf("qemu-img convert: exit status 1: qemu-img: Could not open '/x.qcow2': Permission denied"), http.StatusInternalServerError, "Something went wrong: Could not open '/x.qcow2': Permission denied"},
		{fmt.Errorf("boom"), http.StatusInternalServerError, "Something went wrong: boom"},
	}
	for _, c := range cases {
		status, msg := plainError(c.err)
		if status != c.status || !strings.Contains(msg, c.says) {
			t.Errorf("%v: %d %q", c.err, status, msg)
		}
	}
	if _, msg := plainError(fmt.Errorf("qemu-img convert: exit status 1: qemu-img: x")); strings.Contains(msg, "exit status") {
		t.Errorf("tool noise kept: %q", msg)
	}
	if _, msg := plainError(fmt.Errorf("qemu-img convert: exit status 1: qemu-img: x")); strings.Contains(msg, "qemu-img") {
		t.Errorf("tool name kept: %q", msg)
	}
	if _, msg := plainError(fmt.Errorf("mkdir: %w", &os.PathError{Op: "mkdir", Path: "/mnt/ro/x", Err: syscall.EROFS})); strings.Contains(msg, "ProtectHome") {
		t.Errorf("ProtectHome mentioned outside /home: %q", msg)
	}
	long := strings.Repeat("a", 1000)
	if _, msg := plainError(fmt.Errorf("%s", long)); len([]rune(msg)) > 401+len("Something went wrong: ") {
		t.Errorf("not shortened: %d", len(msg))
	}
}
