# Terraform provider for VirtoMir

[![Terraform Registry](https://img.shields.io/badge/Terraform_Registry-onurbaydas%2Fvirtomir-7B42BC?logo=terraform&logoColor=white)](https://registry.terraform.io/providers/onurbaydas/virtomir/latest)

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

`terraform init` installs it from the [Terraform Registry](https://registry.terraform.io/providers/onurbaydas/virtomir/latest), where the documentation is too. More in [examples/](examples/) and [docs/](docs/).

This provider is developed in [VirtoMir](https://github.com/onurbaydas/VirtoMir/tree/main/integrations/terraform-provider-virtomir) and copied to [terraform-provider-virtomir](https://github.com/onurbaydas/terraform-provider-virtomir) with every release; open issues and pull requests there, in VirtoMir.

| | |
|---|---|
| `virtomir_machine` | a machine from scratch (new disk, cloud image, ISO, existing disk) or from a template; `vcpus`, `memory_mb`, `running` and `autostart` change in place, the rest replaces it |
| `virtomir_network` | NAT, routed, isolated (IPv4 and/or IPv6) or bridged network |
| `virtomir_pool` | directory, NFS, iSCSI, LVM or ZFS storage pool (destroy only forgets it) |
| `data.virtomir_host` | host name, libvirt/QEMU version, CPUs, memory |
| `data.virtomir_machine` | a machine or template by name or UUID: state, size, project, IP addresses |
| `data.virtomir_network` | a network by name: mode, bridge, IPv4 network |
| `data.virtomir_pool` | a storage pool by name: type, path, capacity and free space |
| `data.virtomir_project` | a project by ID: pools, networks, machines and limits |

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

`go test ./...` runs the unit tests. `TF_ACC=1 go test ./internal/provider -run TestAcc` runs Terraform itself
against a fake host, and with `VIRTOMIR_URL`, `VIRTOMIR_TOKEN` (and `VIRTOMIR_INSECURE=1`) against a real one
(`VIRTOMIR_ACC_MACHINE` and `VIRTOMIR_ACC_PROJECT` name a machine and a project to read too). Publishing to the Terraform Registry needs this directory in a repository of its own named `terraform-provider-virtomir` with GPG-signed releases (goreleaser); until then use the build above.

License: MPL-2.0 (see [LICENSE](LICENSE)); VirtoMir itself is AGPL-3.0-or-later.
