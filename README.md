# VirtoMir

**A Detailed Control Deck for Virtual Environments.**

VirtoMir is a modern web control panel for **KVM / QEMU / libvirt** that tries to be two things at once:

- **Effortless** — create and run a VM without knowing much about virtualization.
- **Terrifyingly detailed** — every libvirt setting is reachable, editable and, yes, breakable.

It installs on the Linux host you already have (Debian, Ubuntu, Fedora, …) as a single binary. No dedicated distribution, no database, no agents.

> 🚧 **Status: early release (0.6).** Used and tested on single hosts and small clusters; high availability has not been tried on real BMC hardware yet. Test it before you trust it with production.

![Overview](docs/screenshots/overview.png)

**Try it:** click through the [online demo](https://onurbaydas.github.io/VirtoMir/demo/) (made-up hosts, nothing to install), or get a throwaway test host in a VM with one command: [Try VirtoMir without touching your servers](docs/try-in-a-vm.md).

## Why?

| Tool | Gap |
|---|---|
| Cockpit (machines) | Easy, but shallow — advanced settings send you back to `virsh` / XML |
| virt-manager | Detailed, but a desktop app with weak monitoring and no web access |
| Proxmox VE | Powerful, but it *is* the operating system |

VirtoMir sits in the middle: as easy as Cockpit, as deep as raw libvirt XML, as polished as a modern SaaS dashboard.

## Features

### Machines

- Live overview of host and machines, updated every second
- New-machine wizard: install from an ISO, start from a **cloud image with cloud-init** (user, password, SSH keys), or boot an existing disk. Linux, Windows (UEFI + Secure Boot + TPM 2.0) and "other" profiles with sensible defaults
- Start, shut down, reboot, pause, force off, hard reset, **save to disk** (resume exactly where it was) — one by one or **in bulk**
- Rename, **clone** (full copies, new identity, runs in the background), delete (optionally with its own disks — never shared ones)
- **Templates**: turn a prepared machine into a template and create new machines from it in seconds — **linked clones** (store only their own changes) or full copies, with optional cloud-init
- **Console in the browser**: graphical over VNC (noVNC) or **SPICE** (spice-html5: resolution follows the window, shared clipboard with the guest agent) and serial, also full-screen
- **Settings form** for CPU (topology, model), memory (balloon), boot order, firmware — with a **"review changes" XML diff** before saving; vCPUs and memory are applied live where possible
- **Tuning**: CPU and emulator pinning, huge pages, NUMA memory policy
- **XML editor** for everything else, validated by libvirt
- **Configuration history**: every version of a machine's definition — also changes made with `virsh` — with a diff and one-click restore
- Hardware: add/remove disks and network adapters (hot-plug), insert/eject ISOs, **grow disks**
- **PCI/USB passthrough** with an IOMMU group view and safety checks (devices the host needs are refused, with the reason)
- More devices from the Hardware tab: **shared folders** (virtiofs), **vGPU slices** (mediated devices, created on the Hardware page), **clipboard sharing**, video model and **up to 4 monitors**, watchdog, TPM 2.0, random number generator — hot-plugged where possible, otherwise from the next start (and it says which)
- Expert **QEMU command line** (extra arguments and environment), clearly marked as such
- **Import**: OVA appliances, a libvirt domain XML, or a disk image already on the host
- **Snapshots** as a tree: take, revert, delete
- **Backups**: live (no downtime) or offline, restore as a new machine; **automatic snapshots and backups** on a schedule with retention
- **Off-site copies** with restic — encrypted and deduplicated, to S3 (or MinIO, Wasabi, Ceph RGW …), Backblaze B2, Azure, Google Cloud Storage, SFTP, a REST server or a disk; every scheduled backup can be copied right after it is made, with its own retention, and brought back with one click
- **Firewall per machine**: in/out rules by protocol, port and IPv4/IPv6 address, default policies and anti-spoofing — enforced on the host (libvirt network filters), so the guest cannot switch it off; applied live
- **As code**: the new-machine form shows its exact API request (curl, Ansible) and an equivalent `virt-install`

### Monitoring

- Per machine: CPU, per-vCPU load, disk throughput / IOPS / latency, network traffic, guest-reported memory
- Host CPU, memory, physical disk and network throughput
- **History** for 1 h, 6 h, 24 h, 7 days and 30 days — kept by VirtoMir itself, no Prometheus needed
- …and a **Prometheus endpoint** (`/api/v1/metrics`) for those who have one; the whole API is described by **OpenAPI** (`/api/v1/openapi.json`)
- **Alerts** with sensible defaults (host overloaded, machine crashed, pool filling up, libvirt down) — to a webhook, Slack, Discord, ntfy or e-mail
- **Host hardware**: vendor, model, BIOS, temperatures, fans and power from the host's own sensors; GPUs with load, memory, temperature and power (NVIDIA, AMD)
- **Busiest machines** right now, and "would N more machines fit?" from live numbers (with an N+1 check in a cluster)
- **Reports**: capacity, machine inventory and backup compliance over the whole cluster — HTML (prints to PDF), CSV or JSON, on demand or on a schedule

### Storage & networks

- Storage pools and volumes, **ISO / cloud image upload** with progress, grow volumes
- Virtual networks (NAT, isolated, routed, bridged) with **IPv4 and IPv6** (SLAAC, DHCPv6, NAT66), DHCP leases with machine names, **reserved addresses**
- **Host wiring** at a glance: physical ports, bonds, VLANs, bridges and which machines are plugged in
- **Create bridges, bonds and VLAN interfaces on the host** — through netplan, NetworkManager or systemd-networkd, whichever the host uses; a bridge can take over the host's own address. Every change must be confirmed within two minutes, or the host undoes it by itself (so a change that cuts you off heals itself)
- **Packet capture** on any host interface (tcpdump): watch it live or download a `.pcap` for Wireshark
- **Download images from a URL** straight into a pool, verified against a SHA-256; find **unused disk images**
- **NFS, iSCSI and Ceph RBD storage**, and one pool on every host of a cluster in a single step
- **LVM and ZFS**: use an existing volume group or zpool, or build a new volume group from empty disks (a disk with data on it is refused, never overwritten)
- **VLANs** on Open vSwitch bridges (per network or per adapter), and one network on every host in a single step

### Several hosts

- Install VirtoMir on each host and join them with a **one-time join code** — no shared database, no extra services; if one host is down, the others carry on
- Manage every host from any of them: **cluster overview** with live host cards and all machines in one list, host switcher, consoles and live charts of any host
- **Move machines between hosts**: stopped ones are copied, running ones are **live-migrated** — without copying disks on shared NFS storage, or copying them live
- Alerts, activity and backups of all hosts in one place, history charts of every host side by side; roles apply on every host
- Alert rules, notifications and the backup location saved **the same on every host** in one step
- A **CPU model every host supports**, so machines can live-migrate between different CPUs
- **High availability**: protected machines are restarted on another host when theirs dies — with quorum (and an optional witness), and only after the dead host is certainly off: through its BMC (IPMI or Redfish), its hardware watchdog, or an admin's confirmation. Priorities, preferred hosts, a "what if this host died" preview
- **Maintenance**: move every running machine off a host in one go
- **Resource pools**: nested limits on vCPUs, memory and disk space per storage pool, enforced on every create, resize and move

### Security

- Sign in with your Linux account (**PAM**); only root and members of the `libvirt` group
- **Roles** from Linux groups: admin (`libvirt`), operator (`virtomir-operator`: run machines, console, snapshots, backups) and viewer (`virtomir-viewer`)
- **LDAP / Active Directory** and **OpenID Connect** ("Sign in with …") besides local accounts, roles from directory groups
- **Two-step sign-in** with any authenticator app (TOTP), with recovery codes; admins can reset it
- **Quotas and self-service**: operators create and manage their own machines within a limit on machines, vCPUs, memory and disk space
- **API tokens** for scripts and automation, limited to their owner's role
- HTTPS by default (self-signed certificate created on first start, or your own)
- Session timeouts, lockout after failed sign-ins, CSRF protection, **activity log** of every change (CSV export)
- **Updates** from the GitHub releases in the UI (Account → Updates): downloaded, checked against the published SHA-256 and installed with apt

| | |
|---|---|
| ![Machine performance](docs/screenshots/machine-performance.png) | ![Machine settings](docs/screenshots/settings.png) |
| ![Machine firewall](docs/screenshots/firewall.png) | ![Snapshots](docs/screenshots/snapshots.png) |
| ![High availability](docs/screenshots/ha.png) | ![Resource pools](docs/screenshots/pools.png) |
| ![Reports](docs/screenshots/reports.png) | ![New machine](docs/screenshots/new-machine.png) |
| ![Storage](docs/screenshots/storage.png) | ![Networks](docs/screenshots/networks.png) |
| ![Light theme](docs/screenshots/light-theme.png) | ![Updates](docs/screenshots/updates.png) |

<p align="center"><img src="docs/screenshots/mobile.png" alt="On a phone" width="260"></p>

- **English and Turkish** (Türkçe) user interface — the browser's language by default, switchable in the sidebar and on the Account page

<sub>Screenshots from VirtoMir 0.4 on a test host (Ubuntu 26.04, libvirt 12) with real, small machines; the host name is shown as hv-01. Dark, light and "match system" themes; works on phones too.</sub>

## What it does on your host

Before you install it on a hypervisor you care about — or [try it in a throwaway VM](docs/try-in-a-vm.md) first:

- **Runs as root**, as one service (`virtomird`), because it talks to the system libvirt socket and checks passwords through PAM.
- **Listens on port 8420 (HTTPS)** — on all addresses when installed from a package or with `install.sh`, only on `127.0.0.1` when the binary is started by hand. Change it in `/etc/default/virtomird` (`/etc/sysconfig/virtomird` on Fedora/RHEL), e.g. to `127.0.0.1:8420` behind your own reverse proxy.
- **Keeps its own state in `/var/lib/virtomir`** (certificate, history, settings, activity log). No database and no agents inside your machines.
- **Never touches your existing machines on its own.** It reads what libvirt has and changes only what you ask for, directly or through something you set up (schedules, high availability); changes made through VirtoMir are in the activity log.
- **Removes cleanly:** `sudo apt purge virtomir` (or `sudo ./install.sh --uninstall --purge`) deletes the service and `/var/lib/virtomir`; your machines, disks and networks stay.
- **Security problems:** please report them privately, see [SECURITY.md](SECURITY.md).

## Install

### Signed package repository (apt / dnf)

Releases are published to a signed repository, so updates come with `apt upgrade` / `dnf upgrade`:

```sh
# Debian / Ubuntu
sudo install -d -m 0755 /etc/apt/keyrings
sudo curl -fsSL -o /etc/apt/keyrings/virtomir.asc https://onurbaydas.github.io/VirtoMir/virtomir.asc
echo "deb [signed-by=/etc/apt/keyrings/virtomir.asc] https://onurbaydas.github.io/VirtoMir/apt stable main" | sudo tee /etc/apt/sources.list.d/virtomir.list
sudo apt update && sudo apt install virtomir

# Fedora / RHEL 10 (experimental)
sudo curl -fsSL -o /etc/yum.repos.d/virtomir.repo https://onurbaydas.github.io/VirtoMir/virtomir.repo
sudo dnf install virtomir
```

The repository is built by `scripts/build-repos.sh` in the release workflow (`InRelease` and `repomd.xml` signed, every `.rpm` signed).

### Debian / Ubuntu package

```sh
sudo apt install ./virtomir_0.6.0_amd64.deb   # or _arm64.deb; pulls libvirt and QEMU if missing
```

It starts `virtomird` right away on port 8420 (self-signed HTTPS); the first admin to sign in is walked through what the host still needs (storage, a network, a backup location, a first image). Sign in with a Linux account in the `libvirt` group (admins), `virtomir-operator` or `virtomir-viewer`. Flags (listen address, own certificate) go in `/etc/default/virtomird`, then `sudo systemctl restart virtomird`. `sudo apt purge virtomir` also deletes `/var/lib/virtomir`; your machines are never touched. Later versions install from the UI (Account → Updates). Build the package yourself with `scripts/build-deb.sh <version>` on Debian/Ubuntu (Go, Node, gcc, libpam0g-dev).

### Fedora / RHEL package (experimental)

```sh
sudo dnf install ./virtomir-0.6.0-1.x86_64.rpm   # or .aarch64.rpm; Fedora 40+ / RHEL 10
```

Same service and port; flags go in `/etc/sysconfig/virtomird`. Update with `dnf`, not from the UI. Built with `scripts/build-rpm.sh <version>`.

### Tarball

Requirements: a Linux host with **libvirt + QEMU/KVM** running and **systemd**.

```sh
# Debian / Ubuntu: libvirt itself, if you don't have it yet
sudo apt install qemu-system-x86 libvirt-daemon-system ovmf swtpm

# Download a release, unpack it and run the installer
tar xzf virtomir_v0.6.0_linux_amd64.tar.gz
sudo ./virtomir_v0.6.0_linux_amd64/install.sh
```

The installer puts `virtomird` in `/usr/local/bin`, adds a PAM service (`/etc/pam.d/virtomir`) and a systemd unit, starts it and prints the address — usually `https://<host>:8420/`. Your browser asks you to accept the self-signed certificate once.

- Sign in as `root` or as a user in the `libvirt` group (`sudo usermod -aG libvirt <user>`).
- Only local access, behind your own reverse proxy: `sudo ./install.sh --listen 127.0.0.1:8420`
- Upgrade: run the installer of the new release. Remove: `sudo ./install.sh --uninstall` (your VMs are not touched).
- From GitHub directly: `sudo ./install.sh --download` (uses the `gh` CLI for private repositories).

### Configuration

`virtomird` flags (edit `ExecStart` in `/etc/systemd/system/virtomird.service`):

| Flag | Default | Meaning |
|---|---|---|
| `-listen` | `127.0.0.1:8420` (installer: `0.0.0.0:8420`) | address and port |
| `-auth` | `pam` | `none` disables sign-in — development only |
| `-auth-group` | `libvirt` | besides root, who may sign in as admin |
| `-operator-group` | `virtomir-operator` | members may run machines but not change them |
| `-viewer-group` | `virtomir-viewer` | members may look but change nothing |
| `-tls` | `auto` | `auto` (self-signed in `-state-dir`), `files` (`-tls-cert`, `-tls-key`), `off` (behind a TLS proxy) |
| `-state-dir` | `/var/lib/virtomir` | certificate, metric history, alerts, schedules, activity log, tokens, cluster membership, configuration history, quotas, LDAP/OIDC settings; default backup location (`backups/`) |
| `-name` | hostname | how this host is called in a cluster |
| `-libvirt-socket` | `/var/run/libvirt/libvirt-sock` | |

## Build from source

```sh
git clone https://github.com/onurbaydas/VirtoMir && cd VirtoMir
(cd web && npm ci && npm run build)                            # web UI, embedded into the binary
CGO_ENABLED=1 go build -tags pam -o virtomird ./cmd/virtomird  # needs gcc + libpam0g-dev
# or everything incl. a release tarball:
sh scripts/build-release.sh v0.6.0-dev
```

Without `-tags pam` the binary builds anywhere (also cross-compiled from Windows/macOS), but can only run with `-auth none`.

Tags `v*` build and publish a release through GitHub Actions (`.github/workflows/release.yml`).

## Development

- Go (daemon, `cmd/` + `internal/`) and Svelte 5 + TypeScript (UI, `web/`).
- `go test ./...` and `cd web && npm run check` run anywhere, no libvirt needed.
- `scripts/dev-run.ps1` builds on Windows and runs on a Hyper-V dev host created by `dev/hyperv/New-VirtoMirDevHost.ps1` (Debian 13 with nested KVM).
- Work on the UI without any host: `cd web && npm run dev:demo` (the same pretend cluster as the [online demo](https://onurbaydas.github.io/VirtoMir/demo/)).
- How to contribute: [CONTRIBUTING.md](CONTRIBUTING.md). VirtoMir is developed with the help of AI coding agents; [AGENTS.md](AGENTS.md) is their entry point, and the best map of the code for people too.

## Roadmap

Ideas and questions: [Discussions](https://github.com/onurbaydas/VirtoMir/discussions). Found a bug or tried it on your hardware? [Open an issue](https://github.com/onurbaydas/VirtoMir/issues/new/choose) — test reports from setups we cannot reach (real BMCs, Ceph, GPUs, other distributions) help the most.

## License

VirtoMir is free software: **GNU Affero General Public License v3.0 or later** — see [LICENSE](LICENSE). You may use, study, change and share it; if you offer a changed VirtoMir to others over a network, you must offer them its source code too.

Copyright © 2026 Onur Baydas. For a commercial license without the AGPL's obligations, contact the author through GitHub.
