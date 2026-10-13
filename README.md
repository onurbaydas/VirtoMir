# VirtoMir

**A Detailed Control Deck for Virtual Environments.**

VirtoMir is a modern web control panel for **KVM / QEMU / libvirt** that aims to be two things at once:

- **Effortless** — you should barely need to know anything about virtualization to create and run a VM.
- **Terrifyingly detailed** — every libvirt / QEMU setting is reachable, editable and, yes, breakable.

It installs on top of the Linux host you already have — no dedicated distribution required.

> 🚧 **Status:** pre-alpha, under active development. Not ready for production hosts yet.

## Why?

| Tool | Gap |
|---|---|
| Cockpit (machines) | Easy, but shallow — advanced settings send you back to `virsh` / XML |
| virt-manager | Detailed, but a dated desktop app with weak monitoring and no web access |
| Proxmox VE | Powerful, but it *is* the operating system |

VirtoMir wants to sit in the middle: as easy as Cockpit, as deep as raw libvirt XML, as polished as a modern SaaS dashboard.

## Planned highlights

- Beautiful, live **monitoring** (per-vCPU, disk I/O, network, host resources)
- A **console that just works** in the browser
- Two-way **form ↔ XML** editing for every device and setting
- Hardware insight: IOMMU groups, PCI/USB passthrough, NUMA, CPU pinning, hugepages
- Snapshots, storage pools, virtual networks, multi-host

## For AI agents & contributors

Start with [AGENTS.md](AGENTS.md). Project memory lives in [`.ai/`](.ai/README.md).
