# VirtoMir.Client

A small .NET client for the [VirtoMir](https://github.com/onurbaydas/VirtoMir) API (`/api/v1`): machines, power, autostart and snapshots, with `RequestAsync()` for everything else. .NET 8+.

## Install

The package is on GitHub Packages. Add the feed once (a GitHub token with `read:packages` is needed, even for public packages):

```
dotnet nuget add source https://nuget.pkg.github.com/onurbaydas/index.json --name virtomir --username <github-user> --password <token> --store-password-in-clear-text
dotnet add package VirtoMir.Client
```

## Use

Make an API token on the **Account** page of VirtoMir.

```csharp
using VirtoMir.Client;

var vm = new VirtoMirClient("https://hv1:8420", Environment.GetEnvironmentVariable("VIRTOMIR_TOKEN"));
foreach (var m in await vm.ListMachinesAsync()) Console.WriteLine($"{m.Name} {m.State}");

if (await vm.FindMachineAsync("web") is { } web)
{
    await vm.CreateSnapshotAsync(web.Uuid, "before-upgrade");
    await vm.PowerAsync(web.Uuid, PowerAction.Reboot);
}

var pools = await vm.RequestAsync(HttpMethod.Get, "/api/v1/pools");
```

Set `Host = "hv2"` to send every call to a cluster member through this host. For a self-signed host certificate, pass an `HttpClient` whose handler trusts it. Errors are `VirtoMirException` with `Status` and the API's message.
