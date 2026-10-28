package virt

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestDriverSocket(t *testing.T) {
	dir := t.TempDir()
	qemu := filepath.Join(dir, "virtqemud-sock")
	old := driverSockets
	driverSockets = map[string]string{"qemu": qemu, "lxc": filepath.Join(dir, "virtlxcd-sock")}
	t.Cleanup(func() { driverSockets = old })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	c := NewClient(DefaultSocket, log)
	if s := c.socketFor("qemu"); s != DefaultSocket {
		t.Errorf("monolithic libvirtd: %s", s)
	}
	if err := os.WriteFile(qemu, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if s := c.socketFor("qemu"); s != qemu {
		t.Errorf("modular daemons: %s, want %s", s, qemu)
	}
	if s := c.socketFor("lxc"); s != driverSockets["lxc"] {
		t.Errorf("modular daemons without virtlxcd (no LXC driver): %s, want the missing socket, not virtproxyd", s)
	}
	if s := NewClient("/run/custom-sock", log).socketFor("qemu"); s != "/run/custom-sock" {
		t.Errorf("-libvirt-socket given: %s", s)
	}
}
