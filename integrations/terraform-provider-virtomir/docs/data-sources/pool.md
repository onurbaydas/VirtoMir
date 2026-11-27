# data "virtomir_pool"

A storage pool, by `name`.

```hcl
data "virtomir_pool" "images" {
  name = "default"
}
```

Attributes: `type` (`dir`, `netfs`, `logical`, `zfs`, `rbd`, …), `path`, `active`,
`capacity_bytes`, `allocated_bytes` (in use) and `available_bytes` (free).
