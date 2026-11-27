# Bridging machines to Wi-Fi

VirtoMir can put machines straight on the LAN of a **Wi-Fi card**, the way VirtualBox and
VMware Workstation do: in **Networks → New network**, choose *Bridged* and pick the Wi-Fi
interface. The machines then sit next to your laptop and phone on the same network. This page
explains how that works, because it is not the same as bridging to a wired card, and what it
means for you.

## How it works

A wired bridge passes every machine's frames on unchanged. A Wi-Fi access point does not
allow that: it only accepts frames from the MAC address of the card that joined it. So
VirtoMir translates:

- Frames **leaving** a machine go out with the **host's MAC address** as their sender (also
  inside ARP). Frames **coming back** are delivered to the right machine by their **IP
  address**, which VirtoMir learns from what each machine sends.
- Machines get their addresses from the **LAN's own DHCP server** (your router), as on a
  wired bridge. A DHCP request carries the machine's own MAC inside it, and VirtoMir sends it
  with the broadcast flag set so the router's answer reaches the host.
- The host itself can reach every machine: each address it learns gets a route into the bridge.

Under the hood a bridge (`wbr0`, `wbr1`, …) holds the machines, and a small relay in
virtomird copies frames between it and the Wi-Fi card. The bridges are saved in
`/var/lib/virtomir/wifibridges.json` and come back after a restart or a reboot.

## What to expect

- **One MAC for all machines on the air.** An IP scan, or your router's list of connected
  devices / ARP table, shows the **host's** MAC address for every machine.
- **DHCP still knows each machine.** Leases and reservations on the router stay keyed on each
  machine's **own** MAC, because the DHCP request carries it. A fixed address reserved for a
  machine in the router keeps working.
- **IPv4 only.** IPv6 is not passed on yet: it would need neighbour discovery to be
  translated too. Machines get no IPv6 address over a Wi-Fi bridge.
- **One Wi-Fi card per bridge.** Each Wi-Fi bridge uses one card. Put the machines that should
  share a LAN on the same bridged network; two Wi-Fi bridges on one card are not tested.
- **Protocols other than IPv4 and ARP** (for example IPX, or raw Ethernet tools) do not pass.
- VirtoMir remembers up to 1024 machines and 1024 addresses per bridge, so a machine that
  makes up senders cannot exhaust the host.

## The safety net

Translating the host's own Wi-Fi traffic wrongly could cut the host off the network, and you
off the host. So while a bridge runs, the host pings its own **default gateway** through the
Wi-Fi card every 5 seconds. If three pings in a row get no answer, VirtoMir **removes the
bridge** to give the host its connection back, and logs:

> the host stopped reaching its gateway while the Wi-Fi bridge ran; the bridge was taken down
> to give the host its connection back

Addresses that are never a machine's (the host's own, its gateway, anything outside the
card's networks) are never learned, whoever claims them.

## Troubleshooting

**A machine gets no address.**
- Check that the router's DHCP server is on and has free addresses. VirtoMir does not hand out
  addresses on a Wi-Fi bridge; the router does.
- Some access points (guest networks, "client isolation", enterprise Wi-Fi with 802.1X) drop
  traffic between clients or limit how many addresses one client may have. Try a normal home
  network or ask the network's admin.
- As a test, set a fixed address of the LAN inside the machine's operating system: if that
  works, the problem is DHCP, not the bridge.
- `journalctl -u virtomird` shows `wifi bridge: machine on the LAN` with the address as each
  machine is learned.

**The bridge disappeared.** The safety net took it down because the host stopped reaching its
gateway (look for the message above in `journalctl -u virtomird`). Fix the Wi-Fi connection,
then create the network again.

**Removing a bridge.** Delete the network in **Networks**. Its bridge, relay and firewall set
are taken down, and it is no longer started after a reboot.
