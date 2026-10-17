# Try VirtoMir without touching your servers

VirtoMir runs as root next to libvirt, so it is reasonable not to install it on a hypervisor you
care about just to have a look. Pick one of these, from no effort to the real thing:

| Way | Time | What you get |
|---|---|---|
| [Online demo](#1-online-demo) | 0 min | The real interface on made-up hosts, in your browser. Nothing to install. |
| [Multipass](#2-multipass-one-command) | ~5 min | A throwaway Ubuntu VM with VirtoMir and KVM inside. |
| [Vagrant](#3-vagrant) | ~5 min | The same, with Vagrant (libvirt or VirtualBox). |
| [Any VM you already have](#4-any-other-vm) | ~5 min | Proxmox, Hyper-V, a cloud VM… one script. |

Everything below happens inside a VM you can delete afterwards. Your own machines are never touched.

## 1. Online demo

**https://onurbaydas.github.io/VirtoMir/demo/**

The same interface VirtoMir ships, talking to a pretend cluster of three hosts in your browser.
Start and stop machines, open the serial console, take snapshots, look at the cluster, HA and
history pages. Nothing leaves your browser, and a reload starts over. Things that need a real
host (the screen console, uploads, packet capture) say so.

## 2. Multipass (one command)

[Multipass](https://canonical.com/multipass/install) runs Ubuntu VMs on Linux, Windows and macOS.

```sh
git clone https://github.com/onurbaydas/VirtoMir && cd VirtoMir
sh dev/try/try.sh
```

It creates the VM `virtomir-try` (4 vCPUs, 6 GiB, 40 GiB disk; change with `--cpus`, `--memory`,
`--disk`), installs KVM, libvirt and VirtoMir in it, creates the user `virtomir` with a random
password, a storage pool, a network and a tiny CirrOS test image, then prints the address and the
password. Open the address, accept the self-signed certificate once and sign in.

Done? `sh dev/try/try.sh --delete` removes the VM and everything in it.

## 3. Vagrant

```sh
cd dev/try
vagrant up                                          # libvirt or VirtualBox provider
vagrant ssh -c 'sudo cat /root/virtomir-try-password'
```

VirtoMir is forwarded to **https://localhost:8420/**, user `virtomir`. `vagrant destroy -f` when done.

## 4. Any other VM

A fresh Debian 13 or Ubuntu 24.04 VM with nested virtualization (see below), then inside it:

```sh
curl -fsSLO https://raw.githubusercontent.com/onurbaydas/VirtoMir/main/dev/try/provision.sh
less provision.sh            # read it first: it runs as root
sudo sh provision.sh
```

It does what the Multipass route does and prints the address and password at the end.
On Windows with Hyper-V, `dev/hyperv/New-VirtoMirDevHost.ps1` builds a complete Debian dev host
with nested KVM in one command (that one is meant for development, not just trying).

## Nested virtualization

VirtoMir starts real KVM machines, so the test VM needs virtualization inside it. Without it the
UI works but new machines will not start; the scripts warn when `/dev/kvm` is missing.

- **Linux host (Multipass, Vagrant libvirt, virt-manager)**: check
  `cat /sys/module/kvm_intel/parameters/nested` (or `kvm_amd`). `Y` or `1` means on (recent kernels usually have it on already). To turn it on:
  ```sh
  echo "options kvm_intel nested=1" | sudo tee /etc/modprobe.d/kvm-nested.conf   # kvm_amd on AMD
  sudo modprobe -r kvm_intel && sudo modprobe kvm_intel                          # with no VMs running
  ```
- **Windows / Hyper-V**: with the VM off,
  `Set-VMProcessor -VMName <name> -ExposeVirtualizationExtensions $true`.
- **Proxmox VE**: set the VM's CPU type to `host`, and check the node's `nested` parameter as for a Linux host above.
- **VirtualBox**: 6.1 or newer, `VBoxManage modifyvm <name> --nested-hw-virt on` (the Vagrantfile does this).
- **macOS**: Apple Silicon Macs cannot run x86 KVM guests in a VM. Use the online demo, or a
  Linux machine or cloud VM.
- **Cloud VMs**: support differs by provider and instance type; many only offer it on certain
  families or on bare metal. Check your provider's documentation for "nested virtualization".

## What to try

A good first hour, roughly in this order:

1. The first-run page: does it find what the host has and offer what it lacks?
2. Create a machine from the CirrOS image (**New machine → Existing disk**), start it, open the console.
3. Take a snapshot, change something, revert.
4. Change vCPUs or memory in **Settings**, look at the XML diff before saving.
5. Back it up, restore it as a new machine.
6. Watch **Overview** and the machine's charts for a while; switch to the 1 h range.
7. Break something on purpose: stop libvirt (`sudo systemctl stop libvirtd`) and see what the UI says.

## Tell us how it went

Whatever happened — it worked, something was confusing, something broke — please open a
**[test report](https://github.com/onurbaydas/VirtoMir/issues/new?template=test_report.yml)**.
It takes two minutes and asks for exactly what helps: your distribution, libvirt version and what
you tried. Questions and ideas are welcome in
[Discussions](https://github.com/onurbaydas/VirtoMir/discussions).
