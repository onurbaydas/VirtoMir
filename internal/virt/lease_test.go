package virt

import (
	"errors"
	"testing"
)

func TestLeases(t *testing.T) {
	var s leaseSet
	const u = "7C1F3E2A-55B4-4A8E-9D0C-1B2A3C4D5E6F"
	release, err := s.take("backup", true, machineKey(u))
	if err != nil {
		t.Fatal(err)
	}
	// The same machine, however its UUID is spelled.
	_, err = s.take("clone", true, machineKey("7c1f3e2a-55b4-4a8e-9d0c-1b2a3c4d5e6f"), nameKey("copy"))
	var busy *BusyError
	if !errors.As(err, &busy) || busy.Op != "backup" {
		t.Fatalf("second take: %v", err)
	}
	if err.Error() != "Another operation (backup) is running on this machine. Try again when it has finished." {
		t.Fatalf("message: %s", err)
	}
	// All or nothing: the failed take left "copy" free.
	if r, err := s.take("restore", false, nameKey("copy")); err != nil {
		t.Fatalf("name taken by a failed lease: %v", err)
	} else {
		r()
	}
	if err := s.mustStayOff(u); err == nil {
		t.Fatal("start allowed during an offline backup")
	}
	release()
	release() // twice is fine
	if err := s.mustStayOff(u); err != nil {
		t.Fatal(err)
	}
	r, err := s.take("snapshot revert", false, machineKey(u))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.mustStayOff(u); err != nil {
		t.Fatalf("a revert does not keep the machine off: %v", err)
	}
	r()
}
