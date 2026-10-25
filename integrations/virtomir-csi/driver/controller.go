package driver

import (
	"context"
	"errors"
	"regexp"
	"slices"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// StorageClass parameters.
const (
	ParamPool   = "pool"   // the storage pool (required)
	ParamHost   = "host"   // the member whose pool it is ("" = the host the URL names)
	ParamFormat = "format" // qcow2 (default) or raw (raw for LVM, iSCSI and Ceph pools)
)

var volNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

func toStatus(err error) error {
	if errors.Is(err, errNotFound) {
		return status.Error(codes.NotFound, err.Error())
	}
	return status.Error(codes.Unavailable, err.Error())
}

func (d *Driver) ControllerGetCapabilities(context.Context, *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	var caps []*csi.ControllerServiceCapability
	for _, t := range []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
	} {
		caps = append(caps, &csi.ControllerServiceCapability{Type: &csi.ControllerServiceCapability_Rpc{Rpc: &csi.ControllerServiceCapability_RPC{Type: t}}})
	}
	return &csi.ControllerGetCapabilitiesResponse{Capabilities: caps}, nil
}

func (d *Driver) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	name := req.GetName()
	if !volNameRE.MatchString(name) {
		return nil, status.Errorf(codes.InvalidArgument, "volume name %q is not usable", name)
	}
	if err := singleNode(req.GetVolumeCapabilities()); err != nil {
		return nil, err
	}
	p := req.GetParameters()
	id := volumeID{host: p[ParamHost], pool: p[ParamPool], name: name}
	if id.pool == "" {
		return nil, status.Error(codes.InvalidArgument, "the StorageClass names no pool (parameters.pool)")
	}
	format := p[ParamFormat]
	if format == "" {
		format = "qcow2"
	}
	if format != "qcow2" && format != "raw" {
		return nil, status.Errorf(codes.InvalidArgument, "format is qcow2 or raw, not %q", format)
	}
	want := req.GetCapacityRange().GetRequiredBytes()
	if want == 0 {
		want = 1 << 30
	}
	if lim := req.GetCapacityRange().GetLimitBytes(); lim > 0 && lim < want {
		return nil, status.Error(codes.OutOfRange, "the limit is below the required size")
	}
	gib := (want + 1<<30 - 1) >> 30
	vols, err := d.API.volumes(ctx, id.host, id.pool)
	if err != nil {
		return nil, toStatus(err)
	}
	if i := slices.IndexFunc(vols, func(v Volume) bool { return v.Name == name }); i >= 0 {
		// Asked again (the provisioner retries): the same volume, if it is big enough.
		if vols[i].CapacityBytes < want {
			return nil, status.Errorf(codes.AlreadyExists, "volume %s exists with %d bytes, %d asked", name, vols[i].CapacityBytes, want)
		}
		return &csi.CreateVolumeResponse{Volume: &csi.Volume{VolumeId: id.String(), CapacityBytes: vols[i].CapacityBytes}}, nil
	}
	if err := d.API.createVolume(ctx, id.host, id.pool, name, gib, format); err != nil {
		return nil, toStatus(err)
	}
	return &csi.CreateVolumeResponse{Volume: &csi.Volume{VolumeId: id.String(), CapacityBytes: gib << 30}}, nil
}

func (d *Driver) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	id, err := parseVolumeID(req.GetVolumeId())
	if err != nil {
		return &csi.DeleteVolumeResponse{}, nil // not ours: nothing to delete
	}
	if err := d.API.deleteVolume(ctx, id.host, id.pool, id.name); err != nil && !errors.Is(err, errNotFound) {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &csi.DeleteVolumeResponse{}, nil
}

func (d *Driver) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	id, err := parseVolumeID(req.GetVolumeId())
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	if req.GetNodeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "no node ID")
	}
	if err := singleNode([]*csi.VolumeCapability{req.GetVolumeCapability()}); err != nil {
		return nil, err
	}
	host, err := d.API.findMachine(ctx, req.GetNodeId())
	if err != nil {
		return nil, toStatus(err)
	}
	ser := serial(id.String())
	devs, err := d.API.devices(ctx, host, req.GetNodeId())
	if err != nil {
		return nil, toStatus(err)
	}
	if !slices.ContainsFunc(devs, func(x device) bool { return x.Serial == ser }) {
		if err := d.API.attach(ctx, host, req.GetNodeId(), id.pool, id.name, ser); err != nil {
			return nil, toStatus(err)
		}
	}
	return &csi.ControllerPublishVolumeResponse{PublishContext: map[string]string{"serial": ser}}, nil
}

func (d *Driver) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	host, err := d.API.findMachine(ctx, req.GetNodeId())
	if errors.Is(err, errNotFound) {
		return &csi.ControllerUnpublishVolumeResponse{}, nil // the machine is gone, and its disk with it
	}
	if err != nil {
		return nil, toStatus(err)
	}
	devs, err := d.API.devices(ctx, host, req.GetNodeId())
	if err != nil {
		return nil, toStatus(err)
	}
	ser := serial(req.GetVolumeId())
	for _, x := range devs {
		if x.Serial == ser && x.Target != "" {
			if err := d.API.detach(ctx, host, req.GetNodeId(), x.Target); err != nil {
				return nil, toStatus(err)
			}
		}
	}
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

func (d *Driver) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	id, err := parseVolumeID(req.GetVolumeId())
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	vols, err := d.API.volumes(ctx, id.host, id.pool)
	if err != nil {
		return nil, toStatus(err)
	}
	if !slices.ContainsFunc(vols, func(v Volume) bool { return v.Name == id.name }) {
		return nil, status.Errorf(codes.NotFound, "no volume %s", id)
	}
	if err := singleNode(req.GetVolumeCapabilities()); err != nil {
		return &csi.ValidateVolumeCapabilitiesResponse{Message: err.Error()}, nil
	}
	return &csi.ValidateVolumeCapabilitiesResponse{Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{VolumeCapabilities: req.GetVolumeCapabilities()}}, nil
}
