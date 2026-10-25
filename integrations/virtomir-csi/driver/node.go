package driver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Mounter is what the node plugin does to block devices and mounts.
type Mounter interface {
	// Format makes a file system on dev unless it has one.
	Format(ctx context.Context, dev, fsType string) error
	Mount(ctx context.Context, src, dst, fsType string, opts []string) error
	Unmount(ctx context.Context, path string) error
	IsMounted(path string) (bool, error)
}

// maxVolumes leaves room on the machine's PCI bus for its own devices.
const maxVolumes = 24

func (d *Driver) NodeGetInfo(context.Context, *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{NodeId: d.NodeID, MaxVolumesPerNode: maxVolumes}, nil
}

func (d *Driver) NodeGetCapabilities(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{Capabilities: []*csi.NodeServiceCapability{{
		Type: &csi.NodeServiceCapability_Rpc{Rpc: &csi.NodeServiceCapability_RPC{Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME}},
	}}}, nil
}

// device waits for the disk with serial ser to appear (hot-plug takes a moment).
func (d *Driver) device(ctx context.Context, ser string) (string, error) {
	link := filepath.Join(d.DevDir, "virtio-"+ser)
	for deadline := time.Now().Add(time.Minute); ; {
		if dev, err := filepath.EvalSymlinks(link); err == nil {
			return dev, nil
		}
		if time.Now().After(deadline) {
			return "", status.Errorf(codes.Unavailable, "the disk %s did not appear in the node", link)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (d *Driver) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	staging := req.GetStagingTargetPath()
	vc := req.GetVolumeCapability()
	if staging == "" || vc == nil {
		return nil, status.Error(codes.InvalidArgument, "staging path and capability are required")
	}
	if vc.GetBlock() != nil {
		return &csi.NodeStageVolumeResponse{}, nil // a raw block volume is bound at publish
	}
	ser := req.GetPublishContext()["serial"]
	if ser == "" {
		ser = serial(req.GetVolumeId())
	}
	dev, err := d.device(ctx, ser)
	if err != nil {
		return nil, err
	}
	if ok, err := d.Mounter.IsMounted(staging); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	} else if ok {
		return &csi.NodeStageVolumeResponse{}, nil
	}
	fs := vc.GetMount().GetFsType()
	if fs == "" {
		fs = "ext4"
	}
	if err := d.Mounter.Format(ctx, dev, fs); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := os.MkdirAll(staging, 0o750); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := d.Mounter.Mount(ctx, dev, staging, fs, vc.GetMount().GetMountFlags()); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &csi.NodeStageVolumeResponse{}, nil
}

func (d *Driver) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	if err := d.unmount(ctx, req.GetStagingTargetPath(), false); err != nil {
		return nil, err
	}
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (d *Driver) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	target := req.GetTargetPath()
	vc := req.GetVolumeCapability()
	if target == "" || vc == nil {
		return nil, status.Error(codes.InvalidArgument, "target path and capability are required")
	}
	if ok, err := d.Mounter.IsMounted(target); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	} else if ok {
		return &csi.NodePublishVolumeResponse{}, nil
	}
	opts := []string{"bind"}
	if req.GetReadonly() {
		opts = append(opts, "ro")
	}
	src := req.GetStagingTargetPath()
	if vc.GetBlock() != nil {
		ser := req.GetPublishContext()["serial"]
		if ser == "" {
			ser = serial(req.GetVolumeId())
		}
		dev, err := d.device(ctx, ser)
		if err != nil {
			return nil, err
		}
		src = dev
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		f, err := os.OpenFile(target, os.O_CREATE, 0o640)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		_ = f.Close()
	} else if err := os.MkdirAll(target, 0o750); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := d.Mounter.Mount(ctx, src, target, "", opts); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &csi.NodePublishVolumeResponse{}, nil
}

func (d *Driver) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	if err := d.unmount(ctx, req.GetTargetPath(), true); err != nil {
		return nil, err
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// unmount unmounts path if it is mounted; remove also removes the mount point.
func (d *Driver) unmount(ctx context.Context, path string, remove bool) error {
	if path == "" {
		return status.Error(codes.InvalidArgument, "no path")
	}
	ok, err := d.Mounter.IsMounted(path)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	if ok {
		if err := d.Mounter.Unmount(ctx, path); err != nil {
			return status.Error(codes.Internal, err.Error())
		}
	}
	if remove {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return status.Error(codes.Internal, err.Error())
		}
	}
	return nil
}

// ExecMounter uses blkid, mkfs, mount and umount of the node.
type ExecMounter struct{}

func run(ctx context.Context, name string, args ...string) (string, int, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), ee.ExitCode(), nil
	}
	return string(out), 0, err
}

func (ExecMounter) Format(ctx context.Context, dev, fsType string) error {
	out, code, err := run(ctx, "blkid", "-o", "value", "-s", "TYPE", dev)
	if err != nil {
		return err
	}
	switch {
	case code == 0 && strings.TrimSpace(out) != "":
		return nil // it has a file system: never format again
	case code != 0 && code != 2: // 2: nothing found
		return fmt.Errorf("blkid %s: %s", dev, strings.TrimSpace(out))
	}
	if out, code, err := run(ctx, "mkfs."+fsType, dev); err != nil || code != 0 {
		return fmt.Errorf("mkfs.%s %s: %v %s", fsType, dev, err, strings.TrimSpace(out))
	}
	return nil
}

func (ExecMounter) Mount(ctx context.Context, src, dst, fsType string, opts []string) error {
	args := []string{}
	if fsType != "" {
		args = append(args, "-t", fsType)
	}
	if len(opts) > 0 {
		args = append(args, "-o", strings.Join(opts, ","))
	}
	if out, code, err := run(ctx, "mount", append(args, src, dst)...); err != nil || code != 0 {
		return fmt.Errorf("mount %s %s: %v %s", src, dst, err, strings.TrimSpace(out))
	}
	return nil
}

func (ExecMounter) Unmount(ctx context.Context, path string) error {
	if out, code, err := run(ctx, "umount", path); err != nil || code != 0 {
		return fmt.Errorf("umount %s: %v %s", path, err, strings.TrimSpace(out))
	}
	return nil
}

func (ExecMounter) IsMounted(path string) (bool, error) {
	return mountedIn("/proc/self/mountinfo", path)
}

// mountedIn reports whether path is a mount point in a mountinfo file.
func mountedIn(mountinfo, path string) (bool, error) {
	f, err := os.Open(mountinfo)
	if err != nil {
		return false, err
	}
	defer f.Close()
	clean := pathpkg.Clean(path)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// mount ID, parent ID, major:minor, root, mount point, …; spaces are \040.
		if len(fields) > 4 && strings.ReplaceAll(fields[4], `\040`, " ") == clean {
			return true, nil
		}
	}
	return false, sc.Err()
}
