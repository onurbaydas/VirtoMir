package systemd

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestUpgradeCommands(t *testing.T) {
	defer func(f func(string) (string, error)) { LookPath = f }(LookPath)
	for _, tc := range []struct {
		have   string
		reboot bool
		want   []string
	}{
		{"apt-get", false, []string{"apt-get update", "--force-confold", "upgrade -y", "DEBIAN_FRONTEND=noninteractive"}},
		{"dnf", true, []string{"dnf upgrade -y", "&& systemctl reboot"}},
	} {
		LookPath = func(name string) (string, error) {
			if name == tc.have {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		}
		var got []string
		u := &Upgrader{Run: func(_ context.Context, name string, args ...string) (string, error) {
			got = append([]string{name}, args...)
			return "", nil
		}}
		unit, err := u.Upgrade(t.Context(), tc.reboot)
		if err != nil || !strings.HasPrefix(unit, "virtomir-osupgrade-") {
			t.Fatalf("%s: %q %v", tc.have, unit, err)
		}
		line := strings.Join(got, " ")
		if got[0] != "systemd-run" || !strings.Contains(line, "--wait") || strings.Contains(line, "reboot") != tc.reboot {
			t.Errorf("%s: %s", tc.have, line)
		}
		for _, w := range tc.want {
			if !strings.Contains(line, w) {
				t.Errorf("%s: %q missing from %s", tc.have, w, line)
			}
		}
	}
	LookPath = func(string) (string, error) { return "", errors.New("no") }
	if _, err := (&Upgrader{}).Upgrade(t.Context(), false); err == nil || !strings.Contains(err.Error(), "neither apt-get nor dnf") {
		t.Errorf("no package manager: %v", err)
	}
}

func TestUpgradeFailureNamesTheUnit(t *testing.T) {
	defer func(f func(string) (string, error)) { LookPath = f }(LookPath)
	LookPath = func(string) (string, error) { return "/usr/bin/x", nil }
	u := &Upgrader{Run: func(context.Context, string, ...string) (string, error) { return "", errors.New("exit status 100") }}
	if _, err := u.Upgrade(t.Context(), true); err == nil || !strings.Contains(err.Error(), "journalctl -u virtomir-osupgrade-") {
		t.Errorf("err = %v", err)
	}
}
