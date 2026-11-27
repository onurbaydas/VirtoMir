# data "virtomir_network"

A virtual network, by `name`.

```hcl
data "virtomir_network" "lan" {
  name = "default"
}
```

Attributes: `mode` (`nat`, `route`, `isolated`, `bridge`, …), `bridge`, `subnet` (the gateway
with its prefix, `192.168.122.1/24`), `ipv4_cidr` (the network itself, `192.168.122.0/24`),
`subnet6` and `active`.
