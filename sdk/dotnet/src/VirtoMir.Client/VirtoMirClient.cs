using System.Net;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Text.Json.Serialization;

namespace VirtoMir.Client;

/// <summary>Power and lifecycle actions a machine accepts.</summary>
public enum PowerAction
{
    /// <summary>Starts the machine.</summary>
    Start,
    /// <summary>ACPI power button; the guest decides.</summary>
    Shutdown,
    /// <summary>ACPI reboot request.</summary>
    Reboot,
    /// <summary>Pulls the plug.</summary>
    ForceOff,
    /// <summary>Freezes the vCPUs.</summary>
    Pause,
    /// <summary>Continues a paused machine.</summary>
    Resume,
    /// <summary>Hardware reset button.</summary>
    Reset,
    /// <summary>Writes RAM to disk and stops the machine.</summary>
    Save,
}

/// <summary>A machine as the list shows it.</summary>
public sealed record Machine(
    string Uuid,
    string Name,
    string State,
    int Vcpus,
    long MaxMemoryKiB,
    bool Autostart,
    bool Template)
{
    /// <summary>Whether the state counts as on (running, paused, …).</summary>
    [JsonIgnore]
    public bool IsRunning => !string.IsNullOrEmpty(State) && State is not ("shutoff" or "crashed" or "pmsuspended");
}

/// <summary>A snapshot of a machine; Created is in unix seconds.</summary>
public sealed record Snapshot(
    string Name,
    string? Description,
    string? Parent,
    string State,
    long Created,
    bool Current,
    bool Memory,
    string Location);

/// <summary>An error answer of the VirtoMir API.</summary>
public sealed class VirtoMirException : Exception
{
    /// <summary>Makes the error of one answer.</summary>
    public VirtoMirException(int status, string message) : base($"VirtoMir API {status}: {message}")
    {
        Status = status;
    }

    /// <summary>The HTTP status of the answer.</summary>
    public int Status { get; }

    /// <summary>The answer was 404.</summary>
    public bool IsNotFound => Status == 404;
}

/// <summary>Client for the VirtoMir API (/api/v1). Sign in with an API token made on the Account page.</summary>
public sealed class VirtoMirClient
{
    private static readonly JsonSerializerOptions Json = new(JsonSerializerDefaults.Web);

    private readonly HttpClient _http;
    private readonly string _baseUrl;
    private readonly string? _token;

    /// <summary>baseUrl looks like https://hv1:8420. Pass an HttpClient of your own for a self-signed host certificate.</summary>
    public VirtoMirClient(string baseUrl, string? token = null, HttpClient? http = null)
    {
        if (!Uri.TryCreate(baseUrl, UriKind.Absolute, out var u) || (u.Scheme != "http" && u.Scheme != "https") || string.IsNullOrEmpty(u.Host))
        {
            throw new ArgumentException($"url must look like https://host:8420, not \"{baseUrl}\"", nameof(baseUrl));
        }
        _baseUrl = baseUrl.TrimEnd('/');
        _token = token;
        _http = http ?? new HttpClient { Timeout = TimeSpan.FromMinutes(2) };
    }

    /// <summary>Route every call to this cluster member: /api/v1/hosts/&lt;Host&gt;/…</summary>
    public string? Host { get; set; }

    private static string Seg(string s) => Uri.EscapeDataString(s);

    private string PathFor(string p) =>
        !string.IsNullOrEmpty(Host) && p.StartsWith("/api/v1/", StringComparison.Ordinal)
            ? "/api/v1/hosts/" + Seg(Host) + p["/api/v1".Length..]
            : p;

    private static string Wire(PowerAction a) => a switch
    {
        PowerAction.ForceOff => "force-off",
        _ => a.ToString().ToLowerInvariant(),
    };

    /// <summary>Sends one request; body (if not null) is sent as JSON. Returns the answer, or null when it is empty.</summary>
    public async Task<JsonNode?> RequestAsync(HttpMethod method, string path, object? body = null, CancellationToken ct = default)
    {
        using var req = new HttpRequestMessage(method, _baseUrl + PathFor(path));
        req.Headers.Accept.Add(new MediaTypeWithQualityHeaderValue("application/json"));
        if (!string.IsNullOrEmpty(_token))
        {
            req.Headers.Authorization = new AuthenticationHeaderValue("Bearer", _token);
        }
        if (body is not null)
        {
            req.Content = new StringContent(JsonSerializer.Serialize(body, Json), Encoding.UTF8, "application/json");
        }
        using var res = await _http.SendAsync(req, ct).ConfigureAwait(false);
        var text = await res.Content.ReadAsStringAsync(ct).ConfigureAwait(false);
        if ((int)res.StatusCode >= 300)
        {
            var msg = text.Trim();
            try
            {
                if (JsonNode.Parse(text)?["error"] is JsonValue e && e.TryGetValue<string>(out var s) && s.Length > 0)
                {
                    msg = s;
                }
            }
            catch (JsonException)
            {
                // not JSON
            }
            throw new VirtoMirException((int)res.StatusCode, msg.Length > 0 ? msg : res.ReasonPhrase ?? "");
        }
        return string.IsNullOrWhiteSpace(text) ? null : JsonNode.Parse(text);
    }

    private static List<T> ListOf<T>(JsonNode? n, string field) =>
        n?[field] is JsonArray a ? a.Deserialize<List<T>>(Json) ?? new List<T>() : new List<T>();

    // ---- machines ---------------------------------------------------------------------

    /// <summary>All machines on the host.</summary>
    public async Task<List<Machine>> ListMachinesAsync(CancellationToken ct = default) =>
        ListOf<Machine>(await RequestAsync(HttpMethod.Get, "/api/v1/domains", null, ct).ConfigureAwait(false), "domains");

    /// <summary>The machine page's full model of one machine.</summary>
    public Task<JsonNode?> GetMachineAsync(string uuid, CancellationToken ct = default) =>
        RequestAsync(HttpMethod.Get, $"/api/v1/domains/{Seg(uuid)}", null, ct);

    /// <summary>Finds a machine by name or UUID; null when there is none.</summary>
    public async Task<Machine?> FindMachineAsync(string nameOrUuid, CancellationToken ct = default) =>
        (await ListMachinesAsync(ct).ConfigureAwait(false))
            .FirstOrDefault(m => m.Name == nameOrUuid || string.Equals(m.Uuid, nameOrUuid, StringComparison.OrdinalIgnoreCase));

    /// <summary>Sends a power action.</summary>
    public Task PowerAsync(string uuid, PowerAction action, CancellationToken ct = default) =>
        RequestAsync(HttpMethod.Post, $"/api/v1/domains/{Seg(uuid)}/actions/{Wire(action)}", null, ct);

    /// <summary>Starts the machine with the host, or not.</summary>
    public Task SetAutostartAsync(string uuid, bool enabled, CancellationToken ct = default) =>
        RequestAsync(HttpMethod.Put, $"/api/v1/domains/{Seg(uuid)}/autostart", new { enabled }, ct);

    /// <summary>Deletes a machine; with removeStorage its disks go too.</summary>
    public Task DeleteMachineAsync(string uuid, bool removeStorage = false, CancellationToken ct = default) =>
        RequestAsync(HttpMethod.Delete, $"/api/v1/domains/{Seg(uuid)}" + (removeStorage ? "?storage=remove" : ""), null, ct);

    // ---- snapshots --------------------------------------------------------------------

    /// <summary>The machine's snapshots.</summary>
    public async Task<List<Snapshot>> ListSnapshotsAsync(string uuid, CancellationToken ct = default) =>
        ListOf<Snapshot>(await RequestAsync(HttpMethod.Get, $"/api/v1/domains/{Seg(uuid)}/snapshots", null, ct).ConfigureAwait(false), "snapshots");

    /// <summary>Takes a snapshot and returns its name (an empty name lets VirtoMir pick one).</summary>
    public async Task<string> CreateSnapshotAsync(string uuid, string name = "", string description = "", CancellationToken ct = default)
    {
        var r = await RequestAsync(HttpMethod.Post, $"/api/v1/domains/{Seg(uuid)}/snapshots", new { name, description }, ct).ConfigureAwait(false);
        return r?["name"]?.GetValue<string>() ?? "";
    }

    /// <summary>Goes back to a snapshot. With safety the current state is snapshotted first.</summary>
    public Task<JsonNode?> RevertSnapshotAsync(string uuid, string name, bool safety = true, CancellationToken ct = default) =>
        RequestAsync(HttpMethod.Post, $"/api/v1/domains/{Seg(uuid)}/snapshots/{Seg(name)}/revert" + (safety ? "" : "?safety=false"), null, ct);

    /// <summary>Deletes a snapshot.</summary>
    public Task DeleteSnapshotAsync(string uuid, string name, CancellationToken ct = default) =>
        RequestAsync(HttpMethod.Delete, $"/api/v1/domains/{Seg(uuid)}/snapshots/{Seg(name)}", null, ct);
}
