# VirtoMir features

Everything VirtoMir does, in one list. The [README](../README.md) has the short version, screenshots and how to install it.

## How the interface is organised

What you use every day on one host is always in front: **Overview, Machines, Storage, Networks, Backups, Alerts and Hardware**. Everything else is one click deeper, never gone:

- The sidebar's **Operations** group (insights, reports, compliance, content library, activity), **Datacenter** group (cluster, Ceph, SDN, physical network, Kubernetes, bare metal; open by default once hosts are joined) and **Self-service** group (projects, catalog) fold open on demand. Experimental pages are marked *beta*. The command palette (Ctrl+K) reaches every page.
- A machine has six tabs: **Overview, Console, Settings, Hardware, Snapshots, Backups**. Its firewall, change history, timeline and XML are under **Advanced**.
- The new-machine wizard asks only what every machine needs. Projects, resource pools, GPU pools, SDN subnets, IP pools and storage policies appear when they are set up.
- Host-wide settings (updates, sign-in with LDAP/OIDC, quotas, integrations, the host setup) are on **Host settings**, the gear in the sidebar footer (admins only). Your own role, two-step sign-in and API tokens are on **Account**.

## Features

### Machines

- Live overview of host and machines, updated every second
- **Nothing to install by hand**: a feature that needs a tool the host lacks (restic, FRR, dnsmasq + iPXE, cephadm + podman) shows an **Install it** button (in a cluster: **Install on every host**, on each member that lacks it); VirtoMir runs apt or dnf itself and shows the progress as a task
- New-machine wizard: install from an ISO, start from a **cloud image with cloud-init** (user, password, SSH keys), or boot an existing disk. Linux, Windows (UEFI + Secure Boot + TPM 2.0) and "other" profiles with sensible defaults
- Start, shut down, reboot, pause, force off, hard reset, **save to disk** (resume exactly where it was) — one by one or **in bulk**
- Rename, **clone** (full copies, new identity, runs in the background), delete (optionally with its own disks — never shared ones)
- **Templates**: turn a machine into a template — a running one with the guest agent is made anonymous (machine ID and SSH host keys, or sysprep on Windows), shut down and converted by VirtoMir itself — and create new machines from it in seconds — **linked clones** (store only their own changes) or full copies, with optional cloud-init
- **Console in the browser**: graphical over VNC (noVNC) or **SPICE** (spice-html5: resolution follows the window, shared clipboard with the guest agent) and serial, also full-screen
- **Settings form** for CPU (topology, model), memory (balloon), boot order, firmware — with a **"review changes" XML diff** before saving; vCPUs and memory are applied live where possible
- **Tuning**: CPU and emulator pinning, huge pages, NUMA memory policy
- **XML editor** for everything else, validated by libvirt
- **Configuration history**: every version of a machine's definition, recorded from libvirt's events — also changes made with `virsh`, live-only changes on running machines, and a warning when `/etc/libvirt/qemu/*.xml` was edited but not loaded — with a diff and one-click restore
- **Timeline**: everything that happened to a machine in one list — what was done through VirtoMir and by whom, changes made outside it, when it started, stopped, crashed or was paused and why (a full pool, a kernel panic, a shutdown from inside the guest), its snapshots, backups and alerts — with a jump to the exact change. The answer to "it worked yesterday: what changed?"
- **Hardware**: add/remove disks and network adapters (hot-plug), insert/eject ISOs, **grow disks**
- **Storage QoS & SLAs**: guarantee and cap I/O bandwidth (`read_bytes_sec`, `write_bytes_sec`) and IOPS (`read_iops_sec`, `write_iops_sec`, burst ceilings) per virtual disk using QEMU `<iotune>`, applied live without guest downtime
- **Confidential Computing**: machines with encrypted memory (**AMD SEV / SEV-SNP**, **Intel TDX**) on hosts whose CPU and firmware offer it; VirtoMir reads what the host supports from libvirt and only offers that
- **System Containers**: Linux system containers on libvirt LXC next to the virtual machines — Debian, Ubuntu or Alpine from images.linuxcontainers.org (checked against SHA-256), unprivileged (container root is an unprivileged user on the host), on the same virtual networks, with live CPU and memory, the text console, start, shut down, reboot and delete (needs `libvirt-daemon-driver-lxc`)
- **PCI/USB passthrough** with an IOMMU group view and safety checks (devices the host needs are refused, with the reason); when the IOMMU is off, one click puts it on the kernel command line of this host or of every host (GRUB, grubby or systemd-boot; VirtoMir checks the generated boot entry before it asks for the reboot), then a reboot; a host whose kernel has it on already is left alone
- More devices from the Hardware tab: **shared folders** (virtiofs), **vGPU slices** (mediated devices, created on the Hardware page), **clipboard sharing**, video model and **up to 4 monitors**, watchdog, TPM 2.0, random number generator — hot-plugged where possible, otherwise from the next start (and it says which)
- Expert **QEMU command line** (extra arguments and environment), clearly marked as such
- **Import**: OVA appliances, a libvirt domain XML, or a disk image already on the host
- **From VMware** (experimental): read the inventory of a vCenter or a standalone ESXi host and convert its machines with `virt-v2v` — not tested on real VMware hardware yet
- **Snapshots** as a tree: take, revert, delete
- **Backups**: live (no downtime) or offline, restore as a new machine; **automatic snapshots and backups** on a schedule with retention; the Backups page lists the machines without automatic backups (one click protects them) and the automatic backups whose last run failed
- **Off-site copies** with restic — encrypted and deduplicated, to S3 (or MinIO, Wasabi, Ceph RGW …), Backblaze B2, Azure, Google Cloud Storage, SFTP, a REST server or a disk; every scheduled backup can be copied right after it is made, with its own retention, and brought back with one click
- **Firewall per machine**: in/out rules by protocol, port and IPv4/IPv6 address, default policies and anti-spoofing — enforced on the host (libvirt network filters), so the guest cannot switch it off; applied live
- **As code**: the new-machine form shows its exact API request (curl, Ansible) and an equivalent `virt-install`

### Monitoring

- Per machine: CPU, per-vCPU load, disk throughput / IOPS / latency, network traffic, guest-reported memory
- Host CPU, memory, physical disk and network throughput
- **History** for 1 h, 6 h, 24 h, 7 days and 30 days — kept by VirtoMir itself, no Prometheus needed
- …and a **Prometheus endpoint** (`/api/v1/metrics`) for those who have one, with ready-made **Grafana dashboards** (host overview, one machine; also on grafana.com as [26096](https://grafana.com/grafana/dashboards/26096-virtomir-overview/) and [26097](https://grafana.com/grafana/dashboards/26097-virtomir-machine/)) and **Prometheus alert rules** in [`contrib/`](../contrib/README.md); the whole API is described by **OpenAPI** (`/api/v1/openapi.json`)
- **Alerts** with sensible defaults (host overloaded, machine crashed, pool filling up, libvirt down) — to a webhook, Slack, Discord, ntfy or e-mail
- **Host hardware**: vendor, model, BIOS, temperatures, fans and power from the host's own sensors; GPUs with load, memory, temperature and power (NVIDIA, AMD)
- **Busiest machines** right now, and "would N more machines fit?" from live numbers (with an N+1 check in a cluster)
- **Reports**: capacity, machine inventory and backup compliance over the whole cluster — HTML (prints to PDF), CSV or JSON, on demand or on a schedule
- **Insights**: capacity forecasts (when a host or pool runs full), right-sizing suggestions for machines that ask for more than they use, and alerts when a machine's metrics leave their usual range
- **Compliance**: security checks of every host (sVirt confinement, SSH root login, host firewall, kernel pointer hiding, core dumps, time sync) with the FIPS mode shown, and a tamper-evident activity log that can be verified
- **Transparent Memory Deduplication (KSM)**: monitor and control Kernel Samepage Merging (`/sys/kernel/mm/ksm`), automatically sharing identical memory pages across virtual machines and displaying live saved GiB
- **Distributed SIEM / Syslog Forwarding**: stream audit trail and security events in real time to enterprise SIEM platforms (Splunk, Graylog, ELK) in standard **RFC 5424** format over UDP, TCP, or TLS

### Storage & networks

- Storage pools and volumes, **ISO / cloud image upload** with progress, grow volumes
- **Bridged networks on any host interface** (wired, Wi-Fi, bonds, VLANs, internal bridges): a wired port gets a bridge made for it; on **Wi-Fi** machines are bridged the way VirtualBox and VMware Workstation do it: they get their address from the LAN's own DHCP server and are seen on the LAN by it, while their frames go out with the host's MAC address (IPv4 only; the host watches its gateway and removes the bridge if it ever loses it)
- Virtual networks (NAT, isolated, routed, bridged) with **IPv4 and IPv6** (SLAAC, DHCPv6, NAT66), DHCP leases with machine names, **reserved addresses**
- **Cross-Host VXLAN Overlay (SDN)**: one layer-2 network spanning several hosts, tunnelled over their existing network (UDP 4789) without switch changes; created cluster-wide, every member gets the others as peers and one host is the gateway (NAT, DHCP); VirtoMir keeps the tunnel devices in place, also after a reboot
- **Software-defined networking on OVN** *(experimental, not yet run on a real OVN cluster)*: per-project **VPCs** with subnets, OVN-native DHCP and a distributed router, static routes, an **external gateway** with SNAT, **floating IPs**, **security groups** (stateful rules, default deny, rules between groups), **load balancers** with TCP/UDP health checks and **IPsec** for the Geneve tunnels; a machine's adapter can be put on a VPC subnet when it is created or later
- **Asynchronous ZFS Storage Replication**: datasets and zvols replicated to another host on a schedule with incremental `zfs send -i | ssh … zfs receive`, old replication snapshots pruned on both sides; a copy of the data elsewhere without a SAN. VirtoMir sets up the root SSH key on the target itself: over the cluster for a member, with root's password there (asked once, never kept) for any other host
- **Host wiring** at a glance: physical ports, bonds, VLANs, bridges and which machines are plugged in
- **Create bridges, bonds and VLAN interfaces on the host** — through netplan, NetworkManager or systemd-networkd, whichever the host uses; a bridge can take over the host's own address. Every change must be confirmed within two minutes, or the host undoes it by itself (so a change that cuts you off heals itself)
- **Packet capture** on any host interface (tcpdump): watch it live or download a `.pcap` for Wireshark
- **Download images from a URL** straight into a pool, verified against a SHA-256; find **unused disk images**
- **NFS, iSCSI and Ceph RBD storage**, and one pool on every host of a cluster in a single step
- **Network storage found by itself**: NFS exports, SMB shares and iSCSI targets on the LAN, Fibre Channel ports and SAN LUNs (multipath switched on when a LUN has several paths) are listed; one click connects them (iSCSI CHAP, SMB sign-in, mounts that survive a reboot) and makes a pool
- **Pools of a chosen size from free space**: carved from an empty disk, a volume group's free extents or a zpool (a dataset with a quota), formatted and mounted below `/srv/virtomir`; removing the pool gives the space back
- **Datastore browser** for every pool of files: browse, upload, download, copy, move, rename and delete, never outside the pool, and never a file a machine uses
- **Content library**: ISO images, cloud images and templates kept in versions, synced to the other hosts and deployed as new machines from there
- **LVM and ZFS**: use an existing volume group or zpool, or build a new volume group from empty disks (a disk with data on it is refused, never overwritten)
- **VLANs** on Open vSwitch bridges (per network or per adapter), and one network on every host in a single step
- **Hyper-converged Ceph** *(experimental)*: bootstrap a Ceph cluster on the VirtoMir hosts with cephadm, add members and their disks as OSDs (only empty disks: anything with partitions, a file system, an LVM/ZFS/RAID label, held open by the kernel or used by a storage pool is refused), watch health, capacity, placement groups and the OSD tree, create replicated or erasure-coded RBD pools that become storage pools on every host, and set `noout` while a host is serviced; Ceph health raises alerts
- **Storage policies**: name the pool new disks go to by intent ("fast", "cold"), with a cluster-wide default
- **Storage efficiency**: what ZFS and Ceph compression and deduplication save, and their settings (dedup only after confirming its memory cost); compact a stopped machine's disk image (virt-sparsify); new disks pass the guest's TRIM to the host
- **Storage tiers**: label pools NVMe, SSD or HDD and move a disk to a tier, live
- **NVMe over TCP** *(experimental)*: discover and connect storage targets (reconnected after a reboot) and turn their namespaces into pools
- **IP pools**: fixed addresses for machines made from cloud images (set through cloud-init), the same pools on every host so addresses never collide, reservations for anything else
- **Dynamic DNS**: machines with a pool address get A/AAAA and PTR records (RFC 2136 updates signed with TSIG — BIND, Knot, PowerDNS, Windows DNS), removed when the machine is deleted
- **BGP and EVPN through FRR** *(experimental)*: announce a host's networks to your routers, and its VXLAN overlays with EVPN; VirtoMir writes only its own section of FRR's configuration and shows the sessions
- **SR-IOV** *(experimental)*: set the virtual functions of a network card (kept across reboots) and make a network of it, where every machine gets its own virtual function

### Several hosts

- Install VirtoMir on each host and join them with a **one-time join code** — no shared database, no extra services; if one host is down, the others carry on
- Manage every host from any of them: **cluster overview** with live host cards and all machines in one list, host switcher, consoles and live charts of any host
- **Single-Pane Fleet Deck & DRS**: every machine of the cluster in one list, and a Distributed Resource Scheduler that measures the load skew, honours affinity/anti-affinity rules, recommends live migrations you apply with one click — or, fully automated, carries them out itself — and suggests the best host for a new machine
- **Move machines between hosts**: stopped ones are copied, running ones are **live-migrated** — without copying disks on shared NFS storage, or copying them live
- Alerts, activity and backups of all hosts in one place, history charts of every host side by side; roles apply on every host
- Alert rules, notifications and the backup location saved **the same on every host** in one step
- A **CPU model every host supports**, so machines can live-migrate between different CPUs
- **High availability**: protected machines are restarted on another host when theirs dies — with quorum (and an optional witness), and only after the dead host is certainly off: through its BMC (IPMI or Redfish), its watchdog (VirtoMir loads the kernel's software watchdog where there is no hardware one), or an admin's confirmation. Priorities, preferred hosts, a "what if this host died" preview
- **Maintenance**: move every running machine off a host in one go
- **Rolling cluster upgrade**: pre-checks (quorum, room to evacuate, versions), then one host at a time — machines live-migrate away, VirtoMir (and optionally the OS packages, with a reboot) is updated, the host comes back and takes its machines again; pause, resume or abort between steps
- **GPU pools**: the cluster's GPUs grouped by how machines get them (whole card by passthrough, a vGPU type, a MIG profile); a new machine asks for "2 of l40s" and is placed on the member that has them free, within its project's GPU quota
- **Resource pools**: nested limits on vCPUs, memory and disk space per storage pool, enforced on every create, resize and move
- **Placement rules**: keep machines together or apart, or on hosts with a label (rack, zone) — as a must or a should; checked on create, move, DRS and HA
- **Disaster recovery** *(experimental)*: protection groups replicated to another host, recovery plans with boot order, test failover on clones, real failover and failback

### Projects & self-service

- **Projects**: tenants with members and roles, limits on machines, vCPUs, memory, disk and GPUs, and the pools and networks they may use; operators of a project manage only its machines
- **Self-service catalog**: blueprints people order machines from, with approval, leases that expire (and can be extended) and showback prices

### Security

- Sign in with your Linux account (**PAM**); only root and members of the `libvirt` group
- **Roles** from Linux groups: admin (`libvirt`), operator (`virtomir-operator`: run machines, console, snapshots, backups) and viewer (`virtomir-viewer`)
- **LDAP / Active Directory** and **OpenID Connect** ("Sign in with …") besides local accounts, roles from directory groups
- **Two-step sign-in** with any authenticator app (TOTP), with recovery codes; admins can reset it
- **Quotas and self-service**: operators create and manage their own machines within a limit on machines, vCPUs, memory and disk space
- **API tokens** for scripts and automation, limited to their owner's role
- HTTPS by default (self-signed certificate created on first start, or your own)
- Session timeouts, lockout after failed sign-ins, CSRF protection, **activity log** of every change (CSV export)
- **Updates** in the UI (Host settings → Updates): from the GitHub releases, or from `main` (the rolling `edge` build of every push); downloaded, checked against the published SHA-256 and its OpenPGP signature, and installed with apt or dnf. Automatic updates can be switched off; "Check for updates" asks now, and the Update button lights up when there is one

### Automation

- The whole **REST API**, described by OpenAPI, with API tokens
- An **Ansible collection** (machines, networks, storage pools, projects, placement rules, snapshots; check mode and idempotent) in [`integrations/ansible`](../integrations/ansible)
- A **Terraform provider** (machines, networks, pools, projects, placement rules) on the [Terraform Registry](https://registry.terraform.io/providers/onurbaydas/virtomir/latest) (`source = "onurbaydas/virtomir"`), developed in [`integrations/terraform-provider-virtomir`](../integrations/terraform-provider-virtomir)
- **Client libraries** for TypeScript (npm), Java (Maven), .NET (NuGet) and Ruby (gem) on GitHub Packages, in [`sdk`](../sdk); the Kubernetes **CSI driver** and **operator** images on ghcr.io

### Languages

- **English and Turkish** (Türkçe) user interface — the browser's language by default, switchable in the sidebar and on the Account page
