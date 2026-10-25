# Packer plugin for VirtoMir

Build [VirtoMir](https://github.com/onurbaydas/VirtoMir) templates with Packer. The builder:

1. makes a machine from a cloud image on a VirtoMir host, with a user and a temporary SSH key in its cloud-init,
2. waits for its address and runs your provisioners over SSH,
3. shuts it down and turns it into a template.

VirtoMir then deploys linked or full clones of that template from the UI, the API, Terraform or Ansible.

```hcl
source "virtomir" "debian13" {
  url           = "https://hv1:8420"
  token         = var.token          # Account → API tokens, admin role
  insecure      = true
  template_name = "debian-13-base"
  base_image    = "/var/lib/libvirt/images/debian-13-genericcloud-amd64.qcow2"
  storage_pool  = "images"
}
```

See [example/debian13.pkr.hcl](example/debian13.pkr.hcl) for a full build.

## Settings

| Setting | Meaning |
|---------|---------|
| `url`, `token` | VirtoMir and an API token of an admin (or `VIRTOMIR_URL`, `VIRTOMIR_TOKEN`). |
| `insecure` | Accept the host's self-signed certificate (or `VIRTOMIR_INSECURE=true`). |
| `host` | The cluster member to build on. The default is the host the URL names. |
| `template_name` | The name of the template the build makes (required). |
| `base_image` | The path of a cloud image on the host (required). |
| `storage_pool`, `disk_gib` | Where the disk goes and how big it is (default 20). |
| `vcpus`, `memory_mib` | The machine's size during the build (default 2 and 2048). |
| `network` | The libvirt network (default `default`). Packer must reach the machine's address on it. |
| `keep_machine` | Leave a plain machine instead of a template. |
| `ip_wait_timeout`, `shutdown_timeout` | How long to wait for the address (default 10m) and for the shutdown (default 5m, then it is switched off). |
| `ssh_username`, `ssh_password`, `ssh_private_key_file`, … | The usual SSH communicator settings. Without a key or password, Packer generates a temporary key. |

The cloud image must run cloud-init, and should have the QEMU guest agent or a DHCP lease that VirtoMir can see. Otherwise VirtoMir does not learn the machine's address.

## Build

```sh
go build -o packer-plugin-virtomir .
packer plugins install --path ./packer-plugin-virtomir github.com/onurbaydas/virtomir
```

`config.hcl2spec.go` is generated with `go generate ./...` (packer-sdc). The packer-sdc of SDK v0.6.12 cannot type-check Go 1.27 code. After generating, change the types of `IPWaitTimeout` and `ShutdownTimeout` in `FlatConfig` from `invalid type` to `string`, as for the SDK's own durations.
