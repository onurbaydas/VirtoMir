package virt

import (
	"testing"

	"github.com/digitalocean/go-libvirt"
)

func TestFormatVersion(t *testing.T) {
	cases := map[uint64]string{
		11003000: "11.3.0",
		10000013: "10.0.13",
		9010002:  "9.10.2",
		0:        "0.0.0",
	}
	for in, want := range cases {
		if got := formatVersion(in); got != want {
			t.Errorf("formatVersion(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatUUID(t *testing.T) {
	u := libvirt.UUID{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}
	if got, want := formatUUID(u), "12345678-9abc-def0-0123-456789abcdef"; got != want {
		t.Errorf("formatUUID = %q, want %q", got, want)
	}
}

func TestDomainStateFrom(t *testing.T) {
	cases := map[libvirt.DomainState]DomainState{
		libvirt.DomainNostate:     StateUnknown,
		libvirt.DomainRunning:     StateRunning,
		libvirt.DomainBlocked:     StateBlocked,
		libvirt.DomainPaused:      StatePaused,
		libvirt.DomainShutdown:    StateShuttingDown,
		libvirt.DomainShutoff:     StateShutoff,
		libvirt.DomainCrashed:     StateCrashed,
		libvirt.DomainPmsuspended: StateSuspended,
		99:                        StateUnknown,
	}
	for in, want := range cases {
		if got := domainStateFrom(in); got != want {
			t.Errorf("domainStateFrom(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestInt8sToString(t *testing.T) {
	in := []int8{'x', '8', '6', '_', '6', '4', 0, 'z'}
	if got := int8sToString(in); got != "x86_64" {
		t.Errorf("int8sToString = %q", got)
	}
}
