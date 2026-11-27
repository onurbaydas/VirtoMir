# data "virtomir_project"

A project, by `id`.

```hcl
data "virtomir_project" "team" {
  id = "team-a"
}
```

Attributes: `name`, `description`, `storage_pools` and `networks` (the ones its machines may
use), `machines` (how many it has now) and `limits` with `machines`, `vcpus`, `memory_mib` and
`disk_gib` (0: unlimited).
