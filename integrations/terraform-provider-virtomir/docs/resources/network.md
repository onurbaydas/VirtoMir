# virtomir_network

A libvirt network. `mode`: `nat` (default), `route`, `isolated` — with `subnet` (IPv4, `dhcp` default `true`) and/or `subnet6` (a /64, SLAAC, `dhcp6` optional; NAT66 in nat mode) — or `bridge` with `bridge = "br0"` (an existing host bridge). Every change replaces the network; a network still used by machines cannot be destroyed.
