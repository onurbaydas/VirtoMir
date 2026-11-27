# data "virtomir_machine"

A machine or template made outside this configuration, by `name` or `id` (its UUID).

```hcl
data "virtomir_machine" "db" {
  name = "db-01"
}

# e.g. for a DNS record
output "db_address" {
  value = data.virtomir_machine.db.addresses[0]
}
```

Attributes: `id`, `name`, `state` (`running`, `shutoff`, …), `vcpus`, `memory_mb`, `autostart`,
`template`, `project` (its project's ID; empty for a global machine) and `addresses`: the IP
addresses of its network adapters with their prefix (`192.168.122.31/24`), from the guest agent
or the network's DHCP leases. They are empty while the machine is off.
