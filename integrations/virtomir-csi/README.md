# VirtoMir CSI driver

Persistent volumes for Kubernetes clusters that run on [VirtoMir](https://github.com/onurbaydas/VirtoMir) machines. A volume is a volume of a VirtoMir storage pool. It is attached to the node's machine as a virtio disk while the machine runs, then formatted and mounted inside the node.

- The **controller** (one per cluster) creates and deletes volumes and attaches and detaches them through the VirtoMir API. It needs an API token of an admin.
- The **node plugin** (on every node) finds the disk by its serial number (`/dev/disk/by-id/virtio-vmcsi-…`), formats it (ext4 by default, or xfs) and mounts it. The node ID is the machine's UUID, which libvirt gives the machine as its SMBIOS system UUID.

The access modes are ReadWriteOnce and ReadWriteOncePod, as filesystem or raw block volumes. A disk belongs to one machine at a time, so ReadWriteMany is not possible. Expansion and snapshots are not supported yet.

## Install

Build the image (there is no published image yet):

```sh
docker build -t ghcr.io/onurbaydas/virtomir-csi:latest integrations/virtomir-csi
```

Create the namespace and the secret, then apply the driver and a StorageClass:

```sh
kubectl create namespace virtomir-csi
kubectl -n virtomir-csi create secret generic virtomir \
  --from-literal=url=https://hv1:8420 \
  --from-literal=token=vmt_... \
  --from-literal=insecure=true   # the host's self-signed certificate
kubectl apply -f deploy/virtomir-csi.yaml
kubectl apply -f deploy/storageclass.yaml   # set parameters.pool first
```

The controller must reach the VirtoMir API from inside the cluster. On a NAT network that is the host's address on the network, for example `https://192.168.122.1:8420`.

## StorageClass parameters

| Parameter | Meaning |
|-----------|---------|
| `pool` | The VirtoMir storage pool for the volumes (required). |
| `host` | The cluster member whose pool it is. The default is the host the URL names. |
| `format` | `qcow2` (default) or `raw`. Use `raw` for LVM, iSCSI and Ceph RBD pools. |

When the nodes run on several members, the pool must be shared (NFS, Ceph RBD or iSCSI) and have the same name on every member, because a volume can only be attached where its pool is.

## Volume IDs

A volume ID is `host/pool/name`, where `-` stands for the host the URL names, for example `-/images/pvc-e6ea39b0-…`. The disk serial is derived from the ID, so the controller and the node find the same disk without asking each other.
