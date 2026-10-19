package virt

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/digitalocean/go-libvirt"
	"github.com/onurbaydas/virtomir/internal/signature"
	"libvirt.org/go/libvirtxml"
)

// ContainerImage is a system image a container can start from, published by the Linux
// Containers project (images.linuxcontainers.org).
type ContainerImage struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	dist    string
	release string
}

// ContainerImages are the images VirtoMir offers.
var ContainerImages = []ContainerImage{
	{Key: "debian-13", Label: "Debian 13 (trixie)", dist: "debian", release: "trixie"},
	{Key: "ubuntu-24.04", Label: "Ubuntu 24.04 LTS", dist: "ubuntu", release: "noble"},
	{Key: "alpine-3.22", Label: "Alpine Linux 3.22", dist: "alpine", release: "3.22"},
}

const (
	imageServer = "https://images.linuxcontainers.org"
	// idBase is where container root lands on the host (user namespace): uid/gid 0 – 65535
	// inside are 100000 – 165535 outside, so root in a container is nobody on the host.
	idBase  = 100000
	idCount = 65536
)

func (c *Client) containerDir() string {
	if c.ContainerDir != "" {
		return c.ContainerDir
	}
	return "/var/lib/virtomir/containers"
}

// ensureContainerDir makes the container directory passable (0711) for the containers'
// own root user, which is uid 100000 on the host: without it a container cannot start.
func (c *Client) ensureContainerDir() error {
	dir := c.containerDir()
	if err := os.MkdirAll(dir, 0o711); err != nil {
		return err
	}
	return os.Chmod(dir, 0o711)
}

// imageHTTP fetches images (tests replace it).
var imageHTTP = &http.Client{Timeout: 30 * time.Minute}

func imageArch() string {
	if runtime.GOARCH == "arm" {
		return "armhf"
	}
	return runtime.GOARCH
}

// latestImage finds the newest build of img for this architecture: its directory on the
// image server.
func latestImage(ctx context.Context, img ContainerImage) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, imageServer+"/meta/1.0/index-system", nil)
	resp, err := imageHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("image index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("image index: %s", resp.Status)
	}
	// dist;release;arch;variant;build;path
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 8<<20))
	for sc.Scan() {
		f := strings.Split(sc.Text(), ";")
		if len(f) == 6 && f[0] == img.dist && f[1] == img.release && f[2] == imageArch() && f[3] == "default" {
			return strings.Trim(f[5], "/"), nil
		}
	}
	return "", fmt.Errorf("the image server has no %s build for %s", img.Label, imageArch())
}

// fetchImage returns the cached rootfs tarball of img's newest build, downloading and
// checking it against the build's signed SHA256SUMS first if needed.
func (c *Client) fetchImage(ctx context.Context, img ContainerImage, report func(string)) (string, error) {
	dir, err := latestImage(ctx, img)
	if err != nil {
		return "", err
	}
	if err := c.ensureContainerDir(); err != nil {
		return "", err
	}
	cache := filepath.Join(c.containerDir(), "images")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(cache, strings.ReplaceAll(dir, "/", "_")+".tar.xz")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	want, err := imageSum(ctx, dir)
	if err != nil {
		return "", err
	}
	report("Downloading " + img.Label + "…")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, imageServer+"/"+dir+"/rootfs.tar.xz", nil)
	resp, err := imageHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", img.Label, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", img.Label, resp.Status)
	}
	tmp, err := os.CreateTemp(cache, ".download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, 2<<30)); err != nil {
		tmp.Close()
		return "", fmt.Errorf("download %s: %w", img.Label, err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return "", fmt.Errorf("the %s download is damaged (SHA-256 %s, the server lists %s)", img.Label, got, want)
	}
	return path, os.Rename(tmp.Name(), path)
}

// imageSum is the SHA-256 of a build's rootfs.tar.xz, from its SHA256SUMS once that is
// proven signed by the image server's key (SHA256SUMS.asc).
func imageSum(ctx context.Context, dir string) (string, error) {
	sums, err := imageFile(ctx, dir, "SHA256SUMS")
	if err != nil {
		return "", fmt.Errorf("image checksums: %w", err)
	}
	sig, err := imageFile(ctx, dir, "SHA256SUMS.asc")
	if err != nil {
		return "", fmt.Errorf("image checksum signature: %w", err)
	}
	if err := signature.LXCImages().Verify(bytes.NewReader(sums), bytes.NewReader(sig)); err != nil {
		return "", fmt.Errorf("the image's checksums are not signed by the Linux Containers image server, so it was not downloaded: %w", err)
	}
	return rootfsSum(sums)
}

// rootfsSum finds rootfs.tar.xz in a SHA256SUMS file.
func rootfsSum(sums []byte) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[1] == "rootfs.tar.xz" && len(f[0]) == 64 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", errors.New("the image's SHA256SUMS lists no rootfs.tar.xz")
}

// imageFile downloads a small file of an image build.
func imageFile(ctx context.Context, dir, name string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, imageServer+"/"+dir+"/"+name, nil)
	resp, err := imageHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// prepareRootfs unpacks tarball into rootfs, names the system, sets root's password and
// hands every file to the container's ID range.
func prepareRootfs(ctx context.Context, tarball, rootfs, hostname, password string) error {
	if err := os.MkdirAll(rootfs, 0o755); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, "tar", "-xJf", tarball, "-C", rootfs, "--numeric-owner", "--xattrs", "--xattrs-include=*").CombinedOutput(); err != nil {
		return fmt.Errorf("unpack the image: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if err := os.WriteFile(filepath.Join(rootfs, "etc", "hostname"), []byte(hostname+"\n"), 0o644); err != nil {
		return err
	}
	if err := consoleOnly(filepath.Join(rootfs, "etc", "inittab")); err != nil {
		return err
	}
	if password != "" {
		if err := setRootPassword(rootfs, password); err != nil {
			return fmt.Errorf("set the root password: %w", err)
		}
	}
	return shiftIDs(rootfs, idBase)
}

// consoleOnly turns off the gettys a busybox inittab (Alpine) starts on tty1 – tty4: a
// libvirt container has only /dev/console, and the respawning gettys would fill it with
// errors. Other systems (systemd) have no inittab.
func consoleOnly(inittab string) error {
	raw, err := os.ReadFile(inittab)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "tty") && strings.Contains(l, "getty") {
			lines[i] = "#" + l + " # VirtoMir: no ttys in a container; the console has a getty"
		}
	}
	return os.WriteFile(inittab, []byte(strings.Join(lines, "\n")), 0o644)
}

// containerXML is the libvirt-lxc definition of a container on rootfs: unprivileged (user
// namespace), its own network namespace, a console on a pty.
func containerXML(s CreateSpec, rootfs string) *libvirtxml.Domain {
	x := &libvirtxml.Domain{
		Type:          "lxc",
		Name:          s.Name,
		Memory:        &libvirtxml.DomainMemory{Value: uint(s.MemoryMiB), Unit: "MiB"},
		CurrentMemory: &libvirtxml.DomainCurrentMemory{Value: uint(s.MemoryMiB), Unit: "MiB"},
		VCPU:          &libvirtxml.DomainVCPU{Placement: "static", Value: uint(s.VCPUs)},
		OS: &libvirtxml.DomainOS{
			Type: &libvirtxml.DomainOSType{Type: "exe"},
			Init: "/sbin/init",
		},
		IDMap: &libvirtxml.DomainIDMap{
			UIDs: []libvirtxml.DomainIDMapRange{{Start: 0, Target: idBase, Count: idCount}},
			GIDs: []libvirtxml.DomainIDMapRange{{Start: 0, Target: idBase, Count: idCount}},
		},
		Features:   &libvirtxml.DomainFeatureList{PrivNet: &libvirtxml.DomainFeature{}},
		Clock:      &libvirtxml.DomainClock{Offset: "utc"},
		OnPoweroff: "destroy", OnReboot: "restart", OnCrash: "destroy",
		Devices: &libvirtxml.DomainDeviceList{
			Filesystems: []libvirtxml.DomainFilesystem{{
				AccessMode: "passthrough",
				Source:     &libvirtxml.DomainFilesystemSource{Mount: &libvirtxml.DomainFilesystemSourceMount{Dir: rootfs}},
				Target:     &libvirtxml.DomainFilesystemTarget{Dir: "/"},
			}},
			Consoles: []libvirtxml.DomainConsole{{
				Source: &libvirtxml.DomainChardevSource{Pty: &libvirtxml.DomainChardevSourcePty{}},
				Target: &libvirtxml.DomainConsoleTarget{Type: "lxc", Port: new(uint)},
			}},
		},
	}
	if n := s.Network; n != nil {
		ifc := libvirtxml.DomainInterface{}
		switch {
		case n.Network != "":
			ifc.Source = &libvirtxml.DomainInterfaceSource{Network: &libvirtxml.DomainInterfaceSourceNetwork{Network: n.Network}}
		case n.Bridge != "":
			ifc.Source = &libvirtxml.DomainInterfaceSource{Bridge: &libvirtxml.DomainInterfaceSourceBridge{Bridge: n.Bridge}}
		}
		if ifc.Source != nil {
			x.Devices.Interfaces = []libvirtxml.DomainInterface{ifc}
		}
	}
	return x
}

// CreateContainer makes a system container from one of ContainerImages (a background job:
// the image may have to be downloaded first).
func (c *Client) CreateContainer(ctx context.Context, spec CreateSpec, report func(string)) (CreateResult, error) {
	spec.Kind = "container"
	if err := spec.normalize(); err != nil {
		return CreateResult{}, err
	}
	var img ContainerImage
	for _, i := range ContainerImages {
		if i.Key == spec.Image {
			img = i
		}
	}
	if img.Key == "" {
		return CreateResult{}, invalidf("Choose a container image.")
	}
	l, err := c.lxc()
	if err != nil {
		return CreateResult{}, err
	}
	if q, err := c.conn(); err == nil && c.nameTaken(q, spec.Name) {
		return CreateResult{}, refusedf("A machine named %s already exists.", spec.Name)
	}
	home := filepath.Join(c.containerDir(), spec.Name)
	if _, err := os.Stat(home); err == nil {
		return CreateResult{}, refusedf("%s already exists; remove it or choose another name.", home)
	}

	tarball, err := c.fetchImage(ctx, img, report)
	if err != nil {
		return CreateResult{}, err
	}
	report("Unpacking " + img.Label + "…")
	rootfs := filepath.Join(home, "rootfs")
	if err := prepareRootfs(ctx, tarball, rootfs, spec.Name, spec.RootPassword); err != nil {
		_ = os.RemoveAll(home)
		return CreateResult{}, err
	}
	doc, err := containerXML(spec, rootfs).Marshal()
	if err != nil {
		_ = os.RemoveAll(home)
		return CreateResult{}, err
	}
	d, err := l.DomainDefineXML(doc)
	if err != nil {
		_ = os.RemoveAll(home)
		return CreateResult{}, classify(err)
	}
	res := CreateResult{UUID: formatUUID(d.UUID)}
	m := vmMeta{Kind: "container"}
	_ = c.writeMeta(l, d, m)
	c.remember(res.UUID, m)
	c.log.Info("container created", "name", spec.Name, "image", img.Key)
	if spec.Start {
		report("Starting…")
		if err := l.DomainCreate(d); err != nil {
			res.StartError = classify(err).Error()
		}
	}
	return res, nil
}

// removeContainerFiles deletes a container's root file system (only below ContainerDir).
func (c *Client) removeContainerFiles(l *libvirt.Libvirt, d libvirt.Domain) error {
	raw, err := l.DomainGetXMLDesc(d, libvirt.DomainXMLInactive)
	if err != nil {
		return classify(err)
	}
	var x libvirtxml.Domain
	if err := x.Unmarshal(raw); err != nil || x.Devices == nil {
		return err
	}
	base := filepath.Clean(c.containerDir()) + string(filepath.Separator)
	for _, f := range x.Devices.Filesystems {
		if f.Target == nil || f.Target.Dir != "/" || f.Source == nil || f.Source.Mount == nil {
			continue
		}
		home := filepath.Dir(filepath.Clean(f.Source.Mount.Dir)) // <dir>/<name>/rootfs
		if strings.HasPrefix(home+string(filepath.Separator), base) && home+string(filepath.Separator) != base {
			return os.RemoveAll(home)
		}
	}
	return nil
}
