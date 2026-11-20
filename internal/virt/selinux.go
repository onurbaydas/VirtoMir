package virt

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// On an SELinux host libvirt's daemons and QEMU may only use files labelled virt_image_t.
// A pool or backup folder outside the places the policy knows (/var/lib/libvirt/images,
// /srv/virtomir, /var/lib/virtomir/backups) is var_t or default_t: every disk operation is
// denied (enforcing) or reported as an SELinux alert (permissive, Fedora). VirtoMir labels
// such a folder itself, with a rule that survives a relabel of the whole system.

// selinuxOn reports whether SELinux is active on this host; tests replace it.
var selinuxOn = func() bool {
	_, err := os.Stat("/sys/fs/selinux/enforce")
	return err == nil
}

// fileLabel is the SELinux label of path ("" when unknown); tests replace it.
var fileLabel = func(path string) string {
	return xattrLabel(path)
}

// labelRunner runs one labelling command; tests replace it.
var labelRunner = func(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

// relabelFrom are the labels of folders nothing else claims (a new folder under /srv, a
// mount point, a folder in a home directory). A folder with any other label (system
// configuration, a home directory itself, a service's data) is never relabelled.
var relabelFrom = []string{"var_t", "default_t", "unlabeled_t", "mnt_t", "user_home_t"}

// needsVMLabel reports whether a folder with this label must be relabelled for VM images.
func needsVMLabel(label string) bool {
	f := strings.Split(label, ":")
	return len(f) >= 3 && slices.Contains(relabelFrom, f[2])
}

// LabelForVMs gives dir (and everything below it) the virt_image_t label when SELinux is on
// and the folder does not have it yet: a persistent rule through semanage, then restorecon;
// without semanage, chcon (lost on a full relabel, but correct until then). Errors are only
// logged: the folder still works where SELinux allows it.
func LabelForVMs(log *slog.Logger, dir string) {
	if dir == "" || !filepath.IsAbs(dir) || !selinuxOn() {
		return
	}
	dir = filepath.Clean(dir)
	// Never a system folder: only a folder of its own holds images.
	switch dir {
	case "/", "/home", "/root", "/srv", "/var", "/var/lib", "/mnt", "/media", "/opt", "/tmp", "/usr", "/etc":
		return
	}
	if !needsVMLabel(fileLabel(dir)) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// semanage reads the path as a regular expression: a dot or bar in a folder name must not
	// widen the rule to other files.
	rule := regexp.QuoteMeta(dir) + "(/.*)?"
	err := labelRunner(ctx, "semanage", "fcontext", "-a", "-t", "virt_image_t", rule)
	if err != nil {
		// The rule may exist with another type: modify it.
		err = labelRunner(ctx, "semanage", "fcontext", "-m", "-t", "virt_image_t", rule)
	}
	if err == nil {
		err = labelRunner(ctx, "restorecon", "-R", dir)
	} else {
		err = labelRunner(ctx, "chcon", "-R", "-t", "virt_image_t", dir)
	}
	if err != nil {
		log.Warn("could not give a folder the SELinux label for VM images", "dir", dir, "err", err)
		return
	}
	log.Info("folder labelled for VM images (SELinux)", "dir", dir)
}

// LabelDirPools labels the folders of this host's directory pools (see LabelForVMs).
func (c *Client) LabelDirPools() {
	if !selinuxOn() {
		return
	}
	pools, err := c.ListPools()
	if err != nil {
		return
	}
	for _, p := range pools {
		if p.Type == "dir" && p.Path != "" {
			LabelForVMs(c.log, p.Path)
		}
	}
}
