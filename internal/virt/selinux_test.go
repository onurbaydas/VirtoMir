package virt

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestNeedsVMLabel(t *testing.T) {
	for label, want := range map[string]bool{
		"system_u:object_r:var_t:s0":        true,
		"system_u:object_r:default_t:s0":    true,
		"system_u:object_r:virt_image_t:s0": false,
		"":                                  false, // unknown: left alone
	} {
		if got := needsVMLabel(label); got != want {
			t.Errorf("needsVMLabel(%q) = %v, want %v", label, got, want)
		}
	}
}

func TestLabelForVMs(t *testing.T) {
	oldOn, oldLabel, oldRun := selinuxOn, fileLabel, labelRunner
	t.Cleanup(func() { selinuxOn, fileLabel, labelRunner = oldOn, oldLabel, oldRun })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var ran []string
	semanage := true
	labelRunner = func(_ context.Context, name string, args ...string) error {
		ran = append(ran, name+" "+strings.Join(args, " "))
		if name == "semanage" && !semanage {
			return context.Canceled // not installed
		}
		return nil
	}
	selinuxOn = func() bool { return true }
	fileLabel = func(string) string { return "system_u:object_r:var_t:s0" }

	LabelForVMs(log, "/srv/vms/")
	want := []string{"semanage fcontext -a -t virt_image_t /srv/vms(/.*)?", "restorecon -R /srv/vms"}
	if strings.Join(ran, "|") != strings.Join(want, "|") {
		t.Errorf("ran %q, want %q", ran, want)
	}

	ran, semanage = nil, false
	LabelForVMs(log, "/srv/vms")
	if last := ran[len(ran)-1]; last != "chcon -R -t virt_image_t /srv/vms" {
		t.Errorf("without semanage the last command is %q, want chcon", last)
	}

	for _, dir := range []string{"/srv", "/", "relative/dir", ""} {
		ran = nil
		LabelForVMs(log, dir)
		if len(ran) > 0 {
			t.Errorf("%q was relabelled: %q", dir, ran)
		}
	}
	fileLabel = func(string) string { return "system_u:object_r:virt_image_t:s0" }
	ran = nil
	LabelForVMs(log, "/srv/vms")
	if len(ran) > 0 {
		t.Errorf("a folder already labelled was touched: %q", ran)
	}
	selinuxOn = func() bool { return false }
	fileLabel = func(string) string { return "system_u:object_r:var_t:s0" }
	LabelForVMs(log, "/srv/vms")
	if len(ran) > 0 {
		t.Errorf("a host without SELinux was touched: %q", ran)
	}
}
