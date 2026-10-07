using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Threading;
using System.Threading.Tasks;

namespace WBPMM;

internal sealed class PanelConfigFailure(string code, bool uncertain) : Exception(code)
{
    internal string Code { get; } = code;
    internal bool Uncertain { get; } = uncertain;
}

internal sealed record PanelConfigView(string Revision, int Port, bool AllowLan, JsonObject Public);
internal sealed record PreparedConfig(string BackupId, string Revision, string CandidateRevision);

/// <summary>Only loopback HTTP and fixed panel routes. No proxy, redirects, shared cookies or raw error bodies.</summary>
internal sealed class PanelClient : IDisposable
{
    private readonly HttpClient _http;
    private readonly int _port;
    private const string CookieName = "wbgui_session";
    private const int ConfigLimit = 64 * 1024;
    private const int AccountLimit = 2 * 1024 * 1024;

    internal PanelClient(int port)
    {
        if (port is < 1024 or > 65535) throw new BridgeFailure("invalid_payload", "面板端口无效");
        _port = port;
        _http = new HttpClient(new HttpClientHandler { UseProxy = false, AllowAutoRedirect = false, UseCookies = false })
        { BaseAddress = new Uri($"http://127.0.0.1:{port}/"), Timeout = Timeout.InfiniteTimeSpan };
    }

    public void Dispose() => _http.Dispose();

    internal async Task<string> LoginAsync(string username, string password, CancellationToken ct)
    {
        using var request = Make(HttpMethod.Post, "api/login", new JsonObject { ["username"] = username, ["password"] = password });
        using var response = await SendRawAsync(request, ct);
        if (response.StatusCode != HttpStatusCode.OK) throw new BridgeFailure("operation_failed", "面板拒绝内部会话");
        _ = await ReadJsonAsync(response, ConfigLimit, ct);
        if (!response.Headers.TryGetValues("Set-Cookie", out var headers)) throw new BridgeFailure("operation_failed", "面板未返回会话 Cookie");
        foreach (var header in headers)
        {
            var first = header.Split(';', 2)[0].Trim();
            if (!first.StartsWith(CookieName + "=", StringComparison.Ordinal)) continue;
            var value = first[(CookieName.Length + 1)..];
            if (value.Length is < 16 or > 4096 || value.Any(c => c is <= ' ' or >= '\u007f' or ';' or ',')) break;
            if (header.Split(';').Any(part => part.Trim().Equals("Secure", StringComparison.OrdinalIgnoreCase))) break;
            return value;
        }
        throw new BridgeFailure("operation_failed", "面板会话 Cookie 无效");
    }

    internal async Task<bool> MaintenanceAsync(string operation, string instance, ulong generation, string controlKey,
        string? action, IReadOnlyList<string>? uids, DateTimeOffset? expiry, CancellationToken ct)
    {
        var body = new JsonObject { ["operation"] = operation, ["instance_id"] = instance, ["generation"] = generation };
        if (action is not null && expiry is not null)
        {
            body["action"] = action;
            body["uids"] = new JsonArray((uids ?? []).Select(uid => (JsonNode?)JsonValue.Create(uid)).ToArray());
            // Go time.Time does not unescape JSON's \u002B in a +00:00 offset; UTC Z round-trips.
            body["expires_at"] = expiry.Value.UtcDateTime.ToString("O", CultureInfo.InvariantCulture);
        }
        var reply = await AccountAsync(HttpMethod.Post, "api/desktop/maintenance", body, null, controlKey, null, null, ct);
        if (!Bool(reply["ok"]) || Text(reply["instance_id"]) != instance || Number<ulong>(reply["generation"]) != generation)
            throw new BridgeFailure("operation_failed", "面板维护回执无效");
        return operation == "begin" || Bool(reply["drained"]);
    }

    internal async Task<Uri> AuthorizationUrlAsync(string sessionId, string instance, ulong generation, string controlKey, CancellationToken ct)
    {
        var reply = await AccountAsync(HttpMethod.Get, "api/desktop/login/" + sessionId, null, null, controlKey, instance, generation, ct);
        if (Text(reply["session_id"]) != sessionId ||
            !DateTimeOffset.TryParse(Text(reply["expires_at"]), CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out var expiry) ||
            expiry <= DateTimeOffset.UtcNow || !Uri.TryCreate(Text(reply["auth_url"]), UriKind.Absolute, out var uri) ||
            uri.Scheme != Uri.UriSchemeHttps || !new[] { "copilot.tencent.com", "www.workbuddy.ai" }.Contains(uri.Host, StringComparer.Ordinal) ||
            !string.IsNullOrEmpty(uri.UserInfo) || uri.Port != 443)
            throw new BridgeFailure("invalid_authorization_url", "授权地址未通过原生校验");
        return uri;
    }

    internal async Task<string?> ExistingRegionAsync(string uid, string cookie, CancellationToken ct)
    {
        var reply = await AccountAsync(HttpMethod.Get, "api/accounts", null, cookie, null, null, null, ct);
        if (reply["accounts"] is not JsonArray accounts) throw new BridgeFailure("operation_failed", "账号列表响应无效");
        foreach (var entry in accounts)
        {
            if (entry is not JsonObject account || Text(account["uid"]) != uid || !Bool(account["has_file"])) continue;
            return Region(Text(account["domain"]));
        }
        return null;
    }

    internal static string Region(string domain) => domain switch
    {
        "" or "copilot.tencent.com" => "cn",
        "workbuddy.ai" or "www.workbuddy.ai" => "global",
        _ => throw new BridgeFailure("region_conflict", "凭证账号区域不受支持")
    };

    internal async Task ImportCredentialAsync(byte[] raw, bool overwrite, string cookie, string instance, ulong generation, CancellationToken ct)
    {
        var reply = await AccountAsync(HttpMethod.Post, "api/accounts/import",
            new JsonObject { ["raw_json"] = new UTF8Encoding(false, true).GetString(raw), ["overwrite"] = overwrite }, cookie, null, instance, generation, ct);
        if (!Bool(reply["ok"])) throw new BridgeFailure("operation_failed", "导入回执无效");
    }

    internal async Task DeleteAccountAsync(string uid, string cookie, string instance, ulong generation, CancellationToken ct)
    {
        var escaped = Uri.EscapeDataString(uid);
        var reply = await AccountAsync(HttpMethod.Delete, $"api/accounts/{escaped}?confirm={escaped}", null, cookie, null, instance, generation, ct);
        if (!Bool(reply["ok"])) throw new BridgeFailure("operation_failed", "删除回执无效");
    }

    internal async Task<PanelConfigView> ConfigViewAsync(string cookie, CancellationToken ct)
        => ParseConfig(await ConfigAsync(null, null, cookie, ct));

    internal async Task<PreparedConfig> PrepareConfigAsync(string instance, string key, string revision, JsonObject? patch, CancellationToken ct)
    {
        var body = new JsonObject { ["operation"] = "prepare", ["instance_id"] = instance, ["revision"] = revision };
        if (patch is null) body["rotate_key"] = true;
        else body["patch"] = patch.DeepClone();
        var reply = await ConfigAsync(body, key, null, ct);
        var backup = Text(reply["backup_id"]);
        var candidate = Text(reply["candidate_revision"]);
        if (backup.Length is < 1 or > 128 || Text(reply["revision"]) != revision || !ValidRevision(candidate))
            throw new PanelConfigFailure("CONFIG_RESPONSE", true);
        return new PreparedConfig(backup, revision, candidate);
    }

    internal async Task<PanelConfigView> CommitConfigAsync(string instance, string key, string backup, CancellationToken ct)
        => ParseConfig(RequireConfig(await ConfigAsync(new JsonObject { ["operation"] = "commit", ["instance_id"] = instance, ["backup_id"] = backup }, key, null, ct)));

    internal async Task<PanelConfigView> RestoreConfigAsync(string instance, string key, string backup, string currentRevision, CancellationToken ct)
        => ParseConfig(RequireConfig(await ConfigAsync(new JsonObject { ["operation"] = "restore", ["instance_id"] = instance,
            ["backup_id"] = backup, ["current_revision"] = currentRevision }, key, null, ct)));

    internal async Task FinishConfigAsync(string instance, string key, string backup, CancellationToken ct)
        => _ = await ConfigAsync(new JsonObject { ["operation"] = "finish", ["instance_id"] = instance, ["backup_id"] = backup }, key, null, ct);

    private async Task<JsonObject> ConfigAsync(JsonObject? body, string? key, string? cookie, CancellationToken ct)
    {
        using var request = Make(body is null ? HttpMethod.Get : HttpMethod.Post, body is null ? "api/config" : "api/desktop/config", body,
            cookie, key);
        try
        {
            using var response = await SendRawAsync(request, ct);
            var reply = await ReadJsonAsync(response, ConfigLimit, ct);
            if (response.StatusCode != HttpStatusCode.OK)
            {
                var code = Text(reply["code"]);
                var known = new[] { "CONFIG_INVALID", "CONFIG_CONFLICT", "CONFIG_BUSY", "CONFIG_TRANSACTION", "CONFIG_BACKUP_BUDGET", "CONFIG_CANCELLED", "CONFIG_STORAGE" };
                throw new PanelConfigFailure(known.Contains(code, StringComparer.Ordinal) ? code : "CONFIG_RESPONSE", !known.Contains(code, StringComparer.Ordinal));
            }
            if (body is not null && !Bool(reply["ok"])) throw new PanelConfigFailure("CONFIG_RESPONSE", true);
            return reply;
        }
        catch (PanelConfigFailure) { throw; }
        catch (OperationCanceledException) when (ct.IsCancellationRequested) { throw; }
        catch { throw new PanelConfigFailure("CONFIG_TRANSPORT", true); }
    }

    internal static PanelConfigView ParseConfig(JsonObject value)
    {
        try
        {
            var revision = Text(value["revision"]);
            var e = value["editable"] as JsonObject ?? throw new Exception();
            var upstream = e["upstream"] as JsonObject ?? throw new Exception();
            var pool = e["pool"] as JsonObject ?? throw new Exception();
            var sticky = e["session_sticky"] as JsonObject ?? throw new Exception();
            var desktop = e["desktop"] as JsonObject ?? throw new Exception();
            var summary = value["safe_summary"] as JsonObject ?? throw new Exception();
            var port = Number<int>(e["port"]);
            var allowLan = e.ContainsKey("allow_lan") ? Boolean(e["allow_lan"]) : false;
            var host = Text(summary["listen_host"]);
            var checkinEnabled = false;
            var hours = new JsonArray(9, 21);
            if (e.ContainsKey("schedule"))
            {
                var schedule = e["schedule"] as JsonObject ?? throw new Exception();
                if (schedule.Count != 2 || !schedule.ContainsKey("checkin_enabled") || !schedule.ContainsKey("checkin_hours")) throw new Exception();
                checkinEnabled = Boolean(schedule["checkin_enabled"]);
                hours = schedule["checkin_hours"] as JsonArray ?? throw new Exception();
                if (!ValidHours(hours)) throw new Exception();
            }
            if (!ValidRevision(revision) || port is < 1024 or > 65535 || host != (allowLan ? "0.0.0.0" : "127.0.0.1") ||
                Boolean(summary["schedules_disabled"]) == checkinEnabled ||
                Number<int>(upstream["timeout_seconds"]) is < 1 or > 86400 ||
                Number<int>(upstream["header_timeout_seconds"]) is < 1 or > 86400 ||
                Number<int>(upstream["idle_timeout_seconds"]) is < 1 or > 86400 ||
                Number<long>(pool["max_in_flight"]) is < 0 or > 9007199254740991 ||
                Text(sticky["ttl"]).Length is < 1 or > 128 || !desktop.ContainsKey("proxy_url") ||
                StringValue(desktop["proxy_url"]).Length > 2048) throw new Exception();
            var projection = new JsonObject {
                ["revision"] = revision, ["key_configured"] = Boolean(value["key_configured"]),
                ["safe_summary"] = new JsonObject { ["listen_host"] = host, ["schedules_disabled"] = !checkinEnabled },
                ["editable"] = new JsonObject {
                    ["port"] = port, ["allow_lan"] = allowLan,
                    ["upstream"] = new JsonObject { ["timeout_seconds"] = Number<int>(upstream["timeout_seconds"]),
                        ["header_timeout_seconds"] = Number<int>(upstream["header_timeout_seconds"]), ["idle_timeout_seconds"] = Number<int>(upstream["idle_timeout_seconds"]) },
                    ["pool"] = new JsonObject { ["max_in_flight"] = Number<long>(pool["max_in_flight"]) },
                    ["session_sticky"] = new JsonObject { ["enabled"] = Boolean(sticky["enabled"]), ["ttl"] = Text(sticky["ttl"]) },
                    ["desktop"] = new JsonObject { ["proxy_url"] = StringValue(desktop["proxy_url"]) },
                    ["schedule"] = new JsonObject { ["checkin_enabled"] = checkinEnabled, ["checkin_hours"] = hours.DeepClone() }
                }
            };
            return new PanelConfigView(revision, port, allowLan, projection);
        }
        catch { throw new PanelConfigFailure("CONFIG_RESPONSE", true); }
    }

    private async Task<JsonObject> AccountAsync(HttpMethod method, string path, JsonObject? body, string? cookie, string? controlKey,
        string? instance, ulong? generation, CancellationToken ct)
    {
        using var request = Make(method, path, body, cookie, controlKey, instance, generation);
        using var response = await SendRawAsync(request, ct);
        var reply = await ReadJsonAsync(response, AccountLimit, ct);
        if (response.StatusCode == HttpStatusCode.OK) return reply;
        throw Text(reply["code"]) switch
        {
            "CREDENTIAL_REGION_CONFLICT" => new BridgeFailure("region_conflict", "账号区域冲突"),
            "OVERWRITE_CONFIRMATION_REQUIRED" => new BridgeFailure("overwrite_required", "需确认覆盖凭证"),
            "MAINTENANCE_REQUIRED" => new BridgeFailure("stale_maintenance", "账号维护许可已失效"),
            _ => new BridgeFailure("operation_failed", "面板账号操作失败")
        };
    }

    private HttpRequestMessage Make(HttpMethod method, string path, JsonObject? body = null, string? cookie = null,
        string? controlKey = null, string? instance = null, ulong? generation = null)
    {
        var request = new HttpRequestMessage(method, path);
        request.Headers.TryAddWithoutValidation("Origin", $"http://127.0.0.1:{_port}");
        if (cookie is not null) request.Headers.TryAddWithoutValidation("Cookie", CookieName + "=" + cookie);
        if (controlKey is not null) request.Headers.TryAddWithoutValidation("X-WorkBuddy-Control-Key", controlKey);
        if (instance is not null) request.Headers.TryAddWithoutValidation("X-WorkBuddy-Panel-Instance", instance);
        if (generation is not null) request.Headers.TryAddWithoutValidation("X-WorkBuddy-Maintenance-Generation", generation.Value.ToString(CultureInfo.InvariantCulture));
        if (body is not null) request.Content = new StringContent(body.ToJsonString(), Encoding.UTF8, "application/json");
        return request;
    }

    private async Task<HttpResponseMessage> SendRawAsync(HttpRequestMessage request, CancellationToken ct)
    {
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(ct);
        timeout.CancelAfter(TimeSpan.FromSeconds(10));
        try { return await _http.SendAsync(request, HttpCompletionOption.ResponseHeadersRead, timeout.Token); }
        catch (OperationCanceledException) when (!ct.IsCancellationRequested) { throw new BridgeFailure("operation_failed", "本机面板请求超时"); }
        catch (HttpRequestException) { throw new BridgeFailure("operation_failed", "本机面板连接失败"); }
    }

    private static async Task<JsonObject> ReadJsonAsync(HttpResponseMessage response, int limit, CancellationToken ct)
    {
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(ct);
        timeout.CancelAfter(TimeSpan.FromSeconds(10));
        try
        {
            if (response.Content.Headers.ContentLength is long length && length > limit) throw new BridgeFailure("operation_failed", "本机响应过大");
            await using var stream = await response.Content.ReadAsStreamAsync(timeout.Token);
            using var memory = new MemoryStream();
            var buffer = new byte[8192];
            while (true)
            {
                var count = await stream.ReadAsync(buffer, timeout.Token);
                if (count == 0) break;
                if (memory.Length + count > limit) throw new BridgeFailure("operation_failed", "本机响应过大");
                memory.Write(buffer, 0, count);
            }
            try { return JsonNode.Parse(memory.ToArray()) as JsonObject ?? throw new JsonException(); }
            catch (JsonException) { throw new BridgeFailure("operation_failed", "本机响应格式无效"); }
        }
        catch (OperationCanceledException) when (!ct.IsCancellationRequested) { throw new BridgeFailure("operation_failed", "本机响应超时"); }
    }

    private static JsonObject RequireConfig(JsonObject reply) => reply["config"] as JsonObject ?? throw new PanelConfigFailure("CONFIG_RESPONSE", true);
    internal static bool ValidRevision(string? value) => value is { Length: 64 } && value.All(c => c is >= '0' and <= '9' or >= 'a' and <= 'f');
    private static bool ValidHours(JsonArray hours)
    {
        if (hours.Count is < 1 or > 24) return false;
        var seen = new HashSet<int>();
        foreach (var hour in hours)
            if (!int.TryParse(hour?.ToJsonString(), NumberStyles.None, CultureInfo.InvariantCulture, out var value) || value is < 0 or > 23 || !seen.Add(value)) return false;
        return true;
    }
    private static string Text(JsonNode? node) { try { return node?.GetValue<string>() ?? ""; } catch { return ""; } }
    private static string StringValue(JsonNode? node) => node?.GetValue<string>() ?? throw new Exception();
    private static bool Bool(JsonNode? node) { try { return node?.GetValue<bool>() == true; } catch { return false; } }
    private static bool Boolean(JsonNode? node) => node?.GetValue<bool>() ?? throw new Exception();
    private static T Number<T>(JsonNode? node) where T : struct => node?.GetValue<T>() ?? throw new Exception();
}
