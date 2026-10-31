using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Linq;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;

namespace WBPMM;

internal sealed record GatewaySnapshot(string Listen, string Host, int Port, bool AllowLan,
    string ApiKey, string Revision, string ProxyUrl);

internal static class GatewayConfiguration
{
    private const long MaximumBytes = 10 * 1024 * 1024;
    private static readonly string[] DisabledSchedules =
        ["travel_enabled", "activity_enabled", "keepalive_enabled", "school_enabled", "cat_enabled"];

    internal static GatewaySnapshot EnsureInitial(AppPaths paths, int port = 7863)
    {
        if (File.Exists(paths.GatewayConfig)) return Read(paths);
        if (port is < 1024 or > 65535) throw new BridgeFailure("CONFIG_INVALID", "API 端口无效");
        var schedule = new JsonObject { ["checkin_enabled"] = true, ["checkin_hours"] = new JsonArray(9, 21) };
        foreach (var name in DisabledSchedules) schedule[name] = false;
        var doc = new JsonObject {
            ["listen"] = $"127.0.0.1:{port}", ["api_key"] = "wbd_sk_" + Convert.ToHexString(RandomNumberGenerator.GetBytes(32)).ToLowerInvariant(),
            ["auth_dir"] = paths.Auths, ["state_file"] = Path.Combine(paths.Root, "state.json"),
            ["schedule"] = schedule, ["admin"] = new JsonObject { ["enabled"] = false }
        };
        try { AppPaths.AtomicWrite(paths.GatewayConfig, Encoding.UTF8.GetBytes(doc.ToJsonString()), overwrite: false); }
        catch (IOException) { if (!File.Exists(paths.GatewayConfig)) throw new BridgeFailure("CONFIG_STORAGE", "无法创建网关配置"); }
        return Read(paths);
    }

    internal static GatewaySnapshot Read(AppPaths paths)
    {
        try
        {
            var bytes = AppPaths.ReadPrivateFile(paths.GatewayConfig, MaximumBytes);
            if (bytes.Length is 0 or > (int)MaximumBytes) throw new BridgeFailure("CONFIG_INVALID", "配置文件无效");
            RejectDuplicateKeys(bytes);
            var doc = JsonNode.Parse(bytes) as JsonObject ?? throw new BridgeFailure("CONFIG_INVALID", "配置格式无效");
            RejectCaseVariants(doc, ["listen", "api_key", "auth_dir", "state_file", "admin", "schedule", "desktop"]);
            var listen = Text(doc["listen"]);
            var (host, port) = ParseListen(listen);
            var key = Text(doc["api_key"]);
            if (Text(doc["auth_dir"]) != paths.Auths ||
                Text(doc["state_file"]) != Path.Combine(paths.Root, "state.json") ||
                doc["schedule"] is not JsonObject schedule)
                throw new BridgeFailure("CONFIG_INVALID", "配置字段无效");
            var stateFile = Path.Combine(paths.Root, "state.json");
            AppPaths.RejectReparseParents(stateFile);
            if (File.Exists(stateFile)) AppPaths.CheckPrivateFile(stateFile, long.MaxValue);
            RejectCaseVariants(schedule, ["checkin_enabled", "checkin_hours", .. DisabledSchedules]);
            if (Boolean(schedule["checkin_enabled"]) is null) throw new BridgeFailure("CONFIG_INVALID", "签到配置无效");
            foreach (var name in DisabledSchedules)
                if (Boolean(schedule[name]) != false) throw new BridgeFailure("CONFIG_INVALID", "桌面模式不支持的定时任务已开启");
            if (schedule["checkin_hours"] is JsonNode hours && !ValidHours(hours))
                throw new BridgeFailure("CONFIG_INVALID", "签到时间无效");
            if (doc["admin"] is JsonNode adminNode)
            {
                if (adminNode is not JsonObject admin) throw new BridgeFailure("CONFIG_INVALID", "管理配置无效");
                RejectCaseVariants(admin, ["enabled"]);
                if (admin.ContainsKey("enabled") && Boolean(admin["enabled"]) != false)
                    throw new BridgeFailure("CONFIG_INVALID", "桌面模式不能开放管理接口");
            }
            var proxy = "";
            if (doc["desktop"] is JsonNode desktopNode)
            {
                if (desktopNode is not JsonObject desktop) throw new BridgeFailure("CONFIG_INVALID", "代理配置无效");
                RejectCaseVariants(desktop, ["proxy_url"]);
                proxy = desktop.ContainsKey("proxy_url") ? Text(desktop["proxy_url"]) : "";
                if (proxy.Length > 0 && !TryProxyEndpoint(proxy, out _, out _))
                    throw new BridgeFailure("CONFIG_INVALID", "代理地址无效");
            }
            return new GatewaySnapshot(listen, host, port, host == "0.0.0.0", key,
                Convert.ToHexString(SHA256.HashData(bytes)).ToLowerInvariant(), proxy);
        }
        catch (BridgeFailure) { throw; }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException or JsonException or ArgumentException or InvalidOperationException)
        { throw new BridgeFailure("CONFIG_INVALID", "已有网关配置无效或不安全，原文件已保留"); }
    }

    internal static (string Host, int Port) ParseListen(string value)
    {
        foreach (var host in new[] { "127.0.0.1", "0.0.0.0" })
        {
            var prefix = host + ":";
            if (!value.StartsWith(prefix, StringComparison.Ordinal)) continue;
            var raw = value[prefix.Length..];
            if (int.TryParse(raw, NumberStyles.None, CultureInfo.InvariantCulture, out var port) &&
                port is >= 1024 and <= 65535 && raw == port.ToString(CultureInfo.InvariantCulture)) return (host, port);
        }
        throw new BridgeFailure("CONFIG_INVALID", "监听地址仅允许规范的本机或全网卡 IPv4 地址");
    }

    internal static bool TryProxyEndpoint(string value, out string host, out int port)
    {
        host = ""; port = 0;
        if (Encoding.UTF8.GetByteCount(value) > 2048 || !Uri.TryCreate(value, UriKind.Absolute, out var uri) ||
            uri.Scheme != Uri.UriSchemeHttp || string.IsNullOrEmpty(uri.Host) || !string.IsNullOrEmpty(uri.UserInfo) ||
            uri.AbsolutePath != "/" || uri.Query.Length > 0 || uri.Fragment.Length > 0 ||
            uri.Port is < 1 or > 65535 || !value.EndsWith(":" + uri.Port.ToString(CultureInfo.InvariantCulture), StringComparison.Ordinal)) return false;
        host = uri.Host; port = uri.Port; return true;
    }

    private static void RejectCaseVariants(JsonObject value, IEnumerable<string> protectedKeys)
    {
        var exact = new HashSet<string>(protectedKeys, StringComparer.Ordinal);
        var insensitive = new HashSet<string>(protectedKeys, StringComparer.OrdinalIgnoreCase);
        foreach (var key in value.Select(pair => pair.Key))
            if (insensitive.Contains(key) && !exact.Contains(key)) throw new BridgeFailure("CONFIG_INVALID", "配置字段大小写无效");
    }

    private static bool ValidHours(JsonNode node)
    {
        if (node is not JsonArray values || values.Count > 24) return false;
        var seen = new HashSet<int>();
        foreach (var value in values)
        {
            if (value is null || !int.TryParse(value.ToJsonString(), NumberStyles.None, CultureInfo.InvariantCulture, out var hour) ||
                hour is < 0 or > 23 || !seen.Add(hour)) return false;
        }
        return true;
    }

    private static string Text(JsonNode? value)
    {
        try { return value?.GetValue<string>() ?? throw new BridgeFailure("CONFIG_INVALID", "配置字段缺失"); }
        catch (InvalidOperationException) { throw new BridgeFailure("CONFIG_INVALID", "配置字段类型无效"); }
    }

    private static bool? Boolean(JsonNode? value)
    {
        try { return value?.GetValue<bool>(); }
        catch (InvalidOperationException) { return null; }
    }

    private static void RejectDuplicateKeys(byte[] bytes)
    {
        var reader = new Utf8JsonReader(bytes, new JsonReaderOptions { MaxDepth = 128 });
        var objects = new Stack<HashSet<string>?>();
        while (reader.Read())
        {
            switch (reader.TokenType)
            {
                case JsonTokenType.StartObject: objects.Push(new HashSet<string>(StringComparer.Ordinal)); break;
                case JsonTokenType.StartArray: objects.Push(null); break;
                case JsonTokenType.EndObject:
                case JsonTokenType.EndArray: objects.Pop(); break;
                case JsonTokenType.PropertyName:
                    if (objects.Count == 0 || objects.Peek() is not HashSet<string> keys || !keys.Add(reader.GetString()!))
                        throw new BridgeFailure("CONFIG_INVALID", "配置存在重复字段");
                    break;
            }
        }
    }
}
