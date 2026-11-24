using System.Net;
using System.Text;
using VirtoMir.Client;
using Xunit;

namespace VirtoMir.Client.Tests;

internal sealed class Stub : HttpMessageHandler
{
    public List<string> Seen { get; } = new();
    public List<string?> Auth { get; } = new();
    public List<string> Bodies { get; } = new();
    public HttpStatusCode Status { get; set; } = HttpStatusCode.OK;
    public string Reply { get; set; } = "";

    protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage req, CancellationToken ct)
    {
        Seen.Add($"{req.Method} {req.RequestUri!.PathAndQuery}");
        Auth.Add(req.Headers.Authorization?.ToString());
        Bodies.Add(req.Content is null ? "" : await req.Content.ReadAsStringAsync(ct));
        return new HttpResponseMessage(Status) { Content = new StringContent(Reply, Encoding.UTF8, "application/json") };
    }
}

public class VirtoMirClientTests
{
    private const string U = "6f1c2a5e-0000-4000-8000-000000000001";

    [Fact]
    public async Task ListsMachinesWithTheToken()
    {
        var stub = new Stub { Reply = "{\"domains\":[{\"uuid\":\"" + U + "\",\"name\":\"web\",\"state\":\"running\",\"vcpus\":2,\"maxMemoryKiB\":2097152,\"autostart\":true,\"extra\":1}]}" };
        var c = new VirtoMirClient("http://hv1:8420/", "tok", new HttpClient(stub));
        var list = await c.ListMachinesAsync();
        Assert.Single(list);
        Assert.Equal("web", list[0].Name);
        Assert.True(list[0].IsRunning);
        Assert.Equal("Bearer tok", stub.Auth[0]);
        Assert.Equal("GET /api/v1/domains", stub.Seen[0]);
        stub.Reply = "{\"domains\":null}";
        Assert.Empty(await c.ListMachinesAsync());
        Assert.Null(await c.FindMachineAsync("web"));
    }

    [Fact]
    public async Task RoutesThroughAClusterMember()
    {
        var stub = new Stub { Status = HttpStatusCode.NoContent };
        var c = new VirtoMirClient("http://hv1:8420", "tok", new HttpClient(stub)) { Host = "hv2" };
        await c.PowerAsync(U, PowerAction.ForceOff);
        await c.SetAutostartAsync(U, false);
        await c.RevertSnapshotAsync(U, "before upgrade", safety: false);
        await c.DeleteMachineAsync(U, removeStorage: true);
        Assert.Equal(new[]
        {
            $"POST /api/v1/hosts/hv2/domains/{U}/actions/force-off",
            $"PUT /api/v1/hosts/hv2/domains/{U}/autostart",
            $"POST /api/v1/hosts/hv2/domains/{U}/snapshots/before%20upgrade/revert?safety=false",
            $"DELETE /api/v1/hosts/hv2/domains/{U}?storage=remove",
        }, stub.Seen);
        Assert.Equal("{\"enabled\":false}", stub.Bodies[1]);
    }

    [Fact]
    public async Task CreatesASnapshot()
    {
        var stub = new Stub { Status = HttpStatusCode.Created, Reply = "{\"name\":\"before-upgrade\"}" };
        var c = new VirtoMirClient("http://hv1:8420", null, new HttpClient(stub));
        Assert.Equal("before-upgrade", await c.CreateSnapshotAsync(U, "before-upgrade"));
    }

    [Fact]
    public async Task AnErrorAnswerCarriesTheMessage()
    {
        var stub = new Stub { Status = HttpStatusCode.NotFound, Reply = "{\"error\":\"machine not found\"}" };
        var c = new VirtoMirClient("http://hv1:8420", null, new HttpClient(stub));
        var e = await Assert.ThrowsAsync<VirtoMirException>(() => c.GetMachineAsync(U));
        Assert.True(e.IsNotFound);
        Assert.Contains("machine not found", e.Message);
    }

    [Fact]
    public void RefusesABadUrl()
    {
        Assert.Throws<ArgumentException>(() => new VirtoMirClient("hv1:8420"));
    }
}
