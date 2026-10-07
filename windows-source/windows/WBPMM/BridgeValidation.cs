using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;
using System.Text;
using System.Text.Json.Nodes;

namespace WBPMM;

internal static class BridgeValidation
{
    internal static void Keys(JsonObject value, params string[] keys)
    {
        if (value.Count != keys.Length || value.Any(pair => !keys.Contains(pair.Key, StringComparer.Ordinal)))
            throw Invalid();
    }

    internal static void Empty(JsonObject value) => Keys(value);

    internal static string Text(JsonNode? node, int minimum = 1, int maximum = 128)
    {
        try
        {
            var value = node?.GetValue<string>() ?? throw Invalid();
            if (Encoding.UTF8.GetByteCount(value) < minimum || Encoding.UTF8.GetByteCount(value) > maximum) throw Invalid();
            return value;
        }
        catch (InvalidOperationException) { throw Invalid(); }
    }

    internal static bool Bool(JsonNode? node)
    {
        try { return node?.GetValue<bool>() ?? throw Invalid(); }
        catch (InvalidOperationException) { throw Invalid(); }
    }

    internal static string Revision(JsonNode? node)
    {
        var value = Text(node, 64, 64);
        if (!PanelClient.ValidRevision(value)) throw Invalid();
        return value;
    }

    internal static string UID(JsonNode? node)
    {
        var value = Text(node, 6, 128);
        if (value.Contains("..", StringComparison.Ordinal) || value.Any(c => !((c >= 'a' && c <= 'z') ||
            (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c is '.' or '_' or '-'))) throw Invalid();
        return value;
    }

    internal static string RequestID(JsonNode? node) => Text(node, 1, 128);

    internal static ulong Generation(JsonNode? node)
    {
        var raw = Text(node, 1, 20);
        if (!ulong.TryParse(raw, NumberStyles.None, CultureInfo.InvariantCulture, out var value) ||
            value == 0 || raw != value.ToString(CultureInfo.InvariantCulture)) throw Invalid();
        return value;
    }

    internal static string HexSession(JsonNode? node)
    {
        var value = Text(node, 32, 32);
        if (value.Any(c => c is not (>= '0' and <= '9' or >= 'a' and <= 'f'))) throw Invalid();
        return value;
    }

    internal static string GuidID(JsonNode? node)
    {
        var value = Text(node, 36, 36);
        if (!Guid.TryParse(value, out _)) throw Invalid();
        return value;
    }

    internal static JsonObject Patch(JsonObject payload)
    {
        Keys(payload, "revision", "patch");
        Revision(payload["revision"]);
        if (payload["patch"] is not JsonObject patch || patch.Count == 0 || Encoding.UTF8.GetByteCount(patch.ToJsonString()) > 16384 ||
            patch.Any(p => !new[] { "port", "allow_lan", "upstream", "pool", "session_sticky", "desktop", "schedule" }.Contains(p.Key, StringComparer.Ordinal))) throw Invalid();
        if (patch.ContainsKey("port")) Integer(patch["port"], 1024, 65535);
        if (patch.ContainsKey("allow_lan")) Bool(patch["allow_lan"]);
        if (patch.ContainsKey("upstream"))
        {
            var item = NonemptyObject(patch["upstream"], "timeout_seconds", "header_timeout_seconds", "idle_timeout_seconds");
            foreach (var pair in item) Integer(pair.Value, 1, 86400);
        }
        if (patch.ContainsKey("pool"))
        {
            if (patch["pool"] is not JsonObject item) throw Invalid();
            Keys(item, "max_in_flight"); Integer(item["max_in_flight"], 0, 9007199254740991);
        }
        if (patch.ContainsKey("session_sticky"))
        {
            var item = NonemptyObject(patch["session_sticky"], "enabled", "ttl");
            if (item.ContainsKey("enabled")) Bool(item["enabled"]);
            if (item.ContainsKey("ttl")) Text(item["ttl"], 1, 128);
        }
        if (patch.ContainsKey("desktop"))
        {
            if (patch["desktop"] is not JsonObject item) throw Invalid();
            Keys(item, "proxy_url"); Text(item["proxy_url"], 0, 2048);
        }
        if (patch.ContainsKey("schedule"))
        {
            var item = NonemptyObject(patch["schedule"], "checkin_enabled", "checkin_hours");
            if (item.ContainsKey("checkin_enabled")) Bool(item["checkin_enabled"]);
            if (item.ContainsKey("checkin_hours"))
            {
                if (item["checkin_hours"] is not JsonArray hours || hours.Count is < 1 or > 24) throw Invalid();
                var seen = new HashSet<long>();
                foreach (var hour in hours) { var number = Integer(hour, 0, 23); if (!seen.Add(number)) throw Invalid(); }
            }
        }
        return patch;
    }

    private static JsonObject NonemptyObject(JsonNode? node, params string[] allowed)
    {
        if (node is not JsonObject obj || obj.Count == 0 || obj.Any(p => !allowed.Contains(p.Key, StringComparer.Ordinal))) throw Invalid();
        return obj;
    }

    internal static long Integer(JsonNode? node, long minimum, long maximum)
    {
        var raw = node?.ToJsonString() ?? throw Invalid();
        if (!long.TryParse(raw, NumberStyles.None, CultureInfo.InvariantCulture, out var value) ||
            value < minimum || value > maximum || raw != value.ToString(CultureInfo.InvariantCulture)) throw Invalid();
        return value;
    }

    internal static BridgeFailure Invalid() => new("invalid_payload", "命令参数无效");
}
