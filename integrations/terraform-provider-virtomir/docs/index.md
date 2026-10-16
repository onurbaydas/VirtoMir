# VirtoMir Provider

Manages machines, networks and storage pools through the VirtoMir API.

## Example

```hcl
provider "virtomir" {
  url      = "https://hv1:8420"
  token    = var.virtomir_token
  insecure = true
}
```

## Schema

- `url` (String) Address of a VirtoMir host. Default: `VIRTOMIR_URL`.
- `token` (String, Sensitive) API token; creating things needs the admin role. Default: `VIRTOMIR_TOKEN`.
- `insecure` (Boolean) Accept a self-signed certificate. Default: `VIRTOMIR_INSECURE=1`.
- `host` (String) In a cluster, the member to manage through `url`.
