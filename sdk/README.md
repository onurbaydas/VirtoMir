# VirtoMir client libraries

Small clients for the VirtoMir API (`/api/v1`), published to GitHub Packages with every release (`.github/workflows/packages.yml`). Each one covers machines, power actions, autostart and snapshots, sends a token made on the **Account** page as `Authorization: Bearer`, can route calls to a cluster member (`/api/v1/hosts/<id>/…`), and has a plain request method for every other endpoint.

| Language | Package | Folder |
|----------|---------|--------|
| TypeScript / JavaScript | npm `@onurbaydas/virtomir` | [typescript](typescript) |
| Java 17+ | Maven `io.github.onurbaydas:virtomir-client` | [java](java) |
| .NET 8+ | NuGet `VirtoMir.Client` | [dotnet](dotnet) |
| Ruby 3+ | gem `virtomir` | [ruby](ruby) |

Go users can use the client in `integrations/terraform-provider-virtomir/internal/client` as a model. The Kubernetes CSI driver and operator images are on ghcr.io (`integrations/virtomir-csi`, `integrations/virtomir-operator`).

The libraries are MPL-2.0, like the other integrations, so they can be used in closed-source tools.
