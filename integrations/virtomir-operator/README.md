# VirtoMir operator

Manage [VirtoMir](https://github.com/onurbaydas/VirtoMir) machines and projects from Kubernetes, the same way as everything else in a cluster: `kubectl apply`, GitOps (Argo CD, Flux), Helm.

- **VirtoMirMachine** is a machine. It is made from a cloud image or deployed from a template (linked or full). The operator keeps it running or stopped as the spec says, and reports its state and addresses. If the machine is deleted outside Kubernetes, the operator makes it again. Deleting the object deletes the machine and its disks, unless `deletionPolicy: Retain`.
- **VirtoMirProject** is a project (multi-tenancy): its storage pools, networks, limits and members. The operator keeps it as the spec says.

A machine of the same name that already exists in VirtoMir is adopted, not created again. The operator applies the size (`vcpus`, `memoryMiB`, `diskGiB`) only when it creates the machine. Change a running machine's size in VirtoMir.

## Install

The operator talks to one VirtoMir host, and to the other cluster members through it, with an API token of an admin.

```sh
kubectl apply -f config/crd/virtomir.io_crds.yaml
kubectl create namespace virtomir-system
kubectl -n virtomir-system create secret generic virtomir \
  --from-literal=url=https://hv1:8420 --from-literal=token=vmt_... --from-literal=insecure=true
kubectl apply -f config/deploy/operator.yaml
kubectl apply -f config/samples/samples.yaml
kubectl get virtomirmachines
```

There is no published image yet. Build it with `docker build -t ghcr.io/onurbaydas/virtomir-operator:latest .`, or run the operator outside the cluster:

```sh
VIRTOMIR_URL=https://hv1:8420 VIRTOMIR_TOKEN=vmt_... VIRTOMIR_INSECURE=true go run . --kubeconfig ~/.kube/config
```

## Machine spec

| Field | Meaning |
|-------|---------|
| `name` | The name in VirtoMir. The default is the object's name. |
| `host` | The cluster member to create it on. |
| `template`, `linked` | Deploy from a template (name or UUID), as a linked clone or a full copy. |
| `baseImage` | Or make it from a cloud image on the host. |
| `vcpus`, `memoryMiB`, `diskGiB`, `storagePool`, `network`, `project` | The size, the place and the project. |
| `cloudInit` | `user` and `sshKeys` for the first boot. |
| `powerState` | `Running` (default) or `Stopped`. |
| `deletionPolicy` | `Delete` (default) or `Retain`. |
