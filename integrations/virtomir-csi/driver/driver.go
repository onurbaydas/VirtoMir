// Package driver is the VirtoMir CSI driver: Kubernetes volumes are volumes of VirtoMir
// storage pools, attached as virtio disks to the machines the nodes run in. The controller
// talks to the VirtoMir API; the node plugin runs inside each node, finds the disk by its
// serial and formats and mounts it.
package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Name is the driver's name in Kubernetes (CSIDriver, StorageClass provisioner).
const Name = "csi.virtomir.io"

// Version is set at build time.
var Version = "dev"

// Driver serves the CSI identity service and the controller and/or node services.
type Driver struct {
	csi.UnimplementedIdentityServer
	csi.UnimplementedControllerServer
	csi.UnimplementedNodeServer

	// API is set for the controller.
	API *API
	// NodeID is the machine UUID of this node (node plugin).
	NodeID string
	// Mounter does the node's block device work (tests fake it).
	Mounter Mounter
	// DevDir is where disks appear by serial (/dev/disk/by-id).
	DevDir string
	Log    *slog.Logger
}

// Serve listens on endpoint (unix:///csi/csi.sock) until ctx ends.
func (d *Driver) Serve(ctx context.Context, endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "unix" {
		return fmt.Errorf("the endpoint is unix:///path/to/csi.sock, not %q", endpoint)
	}
	path := u.Path
	if path == "" {
		path = u.Host
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	srv := grpc.NewServer(grpc.UnaryInterceptor(d.logCalls))
	csi.RegisterIdentityServer(srv, d)
	if d.API != nil {
		csi.RegisterControllerServer(srv, d)
	}
	if d.NodeID != "" {
		csi.RegisterNodeServer(srv, d)
	}
	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()
	d.Log.Info("serving", "endpoint", endpoint, "controller", d.API != nil, "node", d.NodeID)
	return srv.Serve(l)
}

func (d *Driver) logCalls(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	res, err := h(ctx, req)
	if err != nil && !strings.HasSuffix(info.FullMethod, "/Probe") {
		d.Log.Warn("call failed", "method", info.FullMethod, "err", err)
	}
	return res, err
}

// ---- identity ----------------------------------------------------------------------------

func (d *Driver) GetPluginInfo(context.Context, *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	return &csi.GetPluginInfoResponse{Name: Name, VendorVersion: Version}, nil
}

func (d *Driver) GetPluginCapabilities(context.Context, *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	return &csi.GetPluginCapabilitiesResponse{Capabilities: []*csi.PluginCapability{{
		Type: &csi.PluginCapability_Service_{Service: &csi.PluginCapability_Service{Type: csi.PluginCapability_Service_CONTROLLER_SERVICE}},
	}}}, nil
}

func (d *Driver) Probe(context.Context, *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	return &csi.ProbeResponse{}, nil
}

// singleNode reports whether every capability can be served by a disk on one machine.
func singleNode(caps []*csi.VolumeCapability) error {
	if len(caps) == 0 {
		return status.Error(codes.InvalidArgument, "no volume capabilities")
	}
	for _, c := range caps {
		switch c.GetAccessMode().GetMode() {
		case csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER, csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER,
			csi.VolumeCapability_AccessMode_SINGLE_NODE_MULTI_WRITER, csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY:
		default:
			return status.Errorf(codes.InvalidArgument, "a VirtoMir volume is a disk of one machine: access mode %s is not possible", c.GetAccessMode().GetMode())
		}
	}
	return nil
}
