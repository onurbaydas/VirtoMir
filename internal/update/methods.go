package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// How this virtomird was installed decides what the updater downloads and how it installs it.
const (
	MethodDeb    = "deb"    // /usr/bin/virtomird from the .deb (apt)
	MethodRPM    = "rpm"    // /usr/bin/virtomird from the .rpm (dnf)
	MethodBinary = "binary" // tarball, install.sh or a binary run from anywhere: replaced in place
)

// detectMethod looks at the running binary and the package managers.
func detectMethod() string {
	exe, _ := os.Executable()
	if exe == "/usr/bin/virtomird" {
		if out, err := exec.Command("dpkg-query", "-W", "-f=${Status}", "virtomir").Output(); err == nil && strings.Contains(string(out), "install ok installed") {
			return MethodDeb
		}
		if exec.Command("rpm", "-q", "virtomir").Run() == nil {
			return MethodRPM
		}
	}
	return MethodBinary
}

// canInstall reports whether this host can update itself with its install method.
func canInstall() (bool, string) {
	if runtime.GOOS != "linux" {
		return false, "updates install on Linux hosts only"
	}
	switch detectMethod() {
	case MethodDeb, MethodRPM:
		if _, err := exec.LookPath("systemd-run"); err != nil {
			return false, "systemd-run is missing"
		}
		return true, ""
	}
	exe, err := runningBinary()
	if err != nil {
		return false, err.Error()
	}
	// The new binary is written next to the running one and renamed over it.
	f, err := os.CreateTemp(filepath.Dir(exe), ".virtomird-update-*")
	if err != nil {
		return false, "cannot write next to " + exe + ": " + err.Error()
	}
	f.Close()
	os.Remove(f.Name())
	return true, ""
}

func runningBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find the running binary: %w", err)
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe, nil
}

// assetFor names the release file a host with this install method downloads.
func assetFor(method string, rel *Release) string {
	switch method {
	case MethodRPM:
		return fmt.Sprintf("virtomir-%s-1.%s.rpm", rel.Version, rpmArch())
	case MethodBinary:
		return fmt.Sprintf("virtomir_%s_linux_%s.tar.gz", rel.Tag, runtime.GOARCH)
	}
	return fmt.Sprintf("virtomir_%s_%s.deb", rel.Version, debArch())
}

func rpmArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	}
	return runtime.GOARCH
}

func kindOf(method string) string {
	switch method {
	case MethodRPM:
		return ".rpm"
	case MethodBinary:
		return "tarball"
	}
	return ".deb"
}

func defaultRunRPMInstaller(ctx context.Context, rpmPath string) (string, error) {
	unit := fmt.Sprintf("virtomir-update-%d", time.Now().Unix())
	tool := "/usr/bin/dnf"
	args := []string{"install", "-y", rpmPath}
	if _, err := os.Stat(tool); err != nil {
		tool, args = "/usr/bin/rpm", []string{"-U", "--replacepkgs", rpmPath}
	}
	cmd := exec.CommandContext(ctx, "systemd-run", append([]string{"--unit=" + unit, "--collect", "--quiet", tool}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("start the upgrade: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return unit, nil
}

// installBinary puts the virtomird from the release tarball in place of the running binary
// (atomic rename; the running process keeps the old file) and restarts the daemon.
func (c *Checker) installBinary(tarPath string, rel *Release, report func(string)) (string, error) {
	exe, err := runningBinary()
	if err != nil {
		return "", err
	}
	if err := swapBinary(tarPath, exe); err != nil {
		return "", err
	}
	report("New binary in place (the previous one is kept as " + filepath.Base(exe) + ".old); restarting VirtoMir")
	restart := c.Restart
	if restart == nil {
		restart = defaultRestart
	}
	how, err := restart(exe)
	if err != nil {
		return "", fmt.Errorf("the new binary is in place but the restart failed (restart virtomird yourself): %w", err)
	}
	return fmt.Sprintf("Installed %s at %s; %s — reload the page in a few seconds", rel.Version, exe, how), nil
}

// swapBinary puts the virtomird from the tarball at exe, keeping the previous binary as
// <exe>.old (to go back by hand if the new one does not start).
func swapBinary(tarPath, exe string) error {
	tmp := exe + ".new"
	if err := extractBinary(tarPath, tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	old := exe + ".old"
	if err := os.Remove(old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		os.Remove(tmp)
		return fmt.Errorf("remove %s: %w", old, err)
	}
	// A hard link keeps the running binary's file under the .old name; copy where links
	// are not possible.
	if err := os.Link(exe, old); err != nil {
		if err := copyFile(exe, old); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("keep the previous binary as %s: %w", old, err)
		}
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", exe, err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// extractBinary copies the virtomird executable out of a release tarball into dst.
func extractBinary(tarPath, dst string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("read the tarball: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("the tarball has no virtomird binary")
		}
		if err != nil {
			return fmt.Errorf("read the tarball: %w", err)
		}
		if h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != "virtomird" {
			continue
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, io.LimitReader(tr, 512<<20)); err != nil {
			out.Close()
			return fmt.Errorf("extract virtomird: %w", err)
		}
		return out.Close()
	}
}

// defaultRestart restarts the daemon after the answer went out: through systemd when it runs
// as a service, otherwise by replacing this process with the new binary.
func defaultRestart(exe string) (string, error) {
	if unit := systemdUnit(); unit != "" {
		if _, err := exec.LookPath("systemd-run"); err == nil {
			cmd := exec.Command("systemd-run", "--on-active=3", "--collect", "--quiet",
				"--unit=virtomir-restart-"+strconv.FormatInt(time.Now().Unix(), 10), "systemctl", "restart", unit)
			if out, err := cmd.CombinedOutput(); err != nil {
				return "", fmt.Errorf("schedule a restart of %s: %v: %s", unit, err, strings.TrimSpace(string(out)))
			}
			return "restarting " + unit, nil
		}
	}
	go func() {
		time.Sleep(3 * time.Second)
		_ = execSelf(exe)
	}()
	return "restarting in place", nil
}

// systemdUnit is the .service this process runs in ("" when it is not a systemd service).
func systemdUnit() string {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	return unitFromCgroup(string(b))
}

func unitFromCgroup(s string) string {
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		parts := strings.Split(line, "/")
		for i := len(parts) - 1; i >= 0; i-- {
			if strings.HasSuffix(parts[i], ".service") {
				return parts[i]
			}
		}
	}
	return ""
}

// method is the install method, deb when no detector is set (older callers and tests).
func (c *Checker) method() string {
	if c.Method == nil {
		return MethodDeb
	}
	return c.Method()
}
