# virtomir_machine

A virtual machine, made in one of these ways:

| How | Attributes |
|---|---|
| new empty disk | `pool`, `disk_gb` (with `iso` to install from) |
| copy of a cloud image | `base_image`, `pool`, `disk_gb`, `cloud_init` |
| existing disk as it is | `disk_path` |
| from a template | `template` (name or UUID), `linked`, `cloud_init` |

## Schema

Required: `name`, `vcpus`, `memory_mb` (MiB).

Optional: `os` (`linux`, `windows`, `other`; default `linux`), `firmware` (`uefi`, `bios`), `pool`, `disk_gb`, `disk_path`, `base_image`, `iso`, `network` or `bridge` (one adapter), `template`, `linked`, `cloud_init` { `user`, `password`, `ssh_keys`, `hostname`, `packages` }, `running` (default `true`), `autostart` (default `false`), `delete_disks` (default `true`).

Read-only: `id` (UUID), `state`.

`vcpus`, `memory_mb`, `running` and `autostart` change in place (vCPUs and memory live where the guest allows, otherwise from the next start); every other attribute replaces the machine. Import with `terraform import virtomir_machine.web <name or UUID>`.
