# Terraform provider for VirtoMir

Manage machines, networks and storage pools of a [VirtoMir](https://github.com/onurbaydas/VirtoMir) host (or any member of a VirtoMir cluster) with Terraform or OpenTofu.

```hcl
terraform {
  required_providers {
    virtomir = { source = "onurbaydas/virtomir" }
  }
}

provider "virtomir" {
  url      = "https://hv1:8420"
  token    = var.virtomir_token # Account → API tokens, admin role
  insecure = true               # the host's self-signed certificate
}

resource "virtomir_machine" "web" {
  name      = "web-1"
  template  = "deb13-tpl" # or: pool + disk_gb, base_image, iso, disk_path
  linked    = true
  vcpus     = 2
  memory_mb = 2048
  cloud_init = {
    user     = "ops"
    ssh_keys = [file("~/.ssh/id_ed25519.pub")]
  }
}
```

More in [examples/](examples/) and [docs/](docs/).

| | |
|---|---|
| `virtomir_machine` | a machine from scratch (new disk, cloud image, ISO, existing disk) or from a template; `vcpus`, `memory_mb`, `running` and `autostart` change in place, the rest replaces it |
| `virtomir_network` | NAT, routed, isolated (IPv4 and/or IPv6) or bridged network |
| `virtomir_pool` | directory, NFS, iSCSI, LVM or ZFS storage pool (destroy only forgets it) |
| `data.virtomir_host` | host name, libvirt/QEMU version, CPUs, memory |

The provider reads `VIRTOMIR_URL`, `VIRTOMIR_TOKEN` and `VIRTOMIR_INSECURE=1` when the provider block leaves them out. In a cluster, `host = "<member>"` manages that member through `url`.

## Build and use without the registry

```sh
go build -o terraform-provider-virtomir .
mkdir -p ~/.terraform.d/plugins-dev && mv terraform-provider-virtomir ~/.terraform.d/plugins-dev/
cat >> ~/.terraformrc <<'EOF'   # ~/.tofurc for OpenTofu
provider_installation {
  dev_overrides { "onurbaydas/virtomir" = "/home/you/.terraform.d/plugins-dev" }
  direct {}
}
EOF
```

`go test ./...` runs the client tests. Publishing to the Terraform Registry needs this directory in a repository of its own named `terraform-provider-virtomir` with GPG-signed releases (goreleaser); until then use the build above.

License: MPL-2.0 (see [LICENSE](LICENSE)); VirtoMir itself is AGPL-3.0-or-later.
