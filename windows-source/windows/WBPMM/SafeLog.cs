using System.Text;
using System.Text.Json.Nodes;

namespace WBPMM;

internal sealed class SafeLog
{
    private readonly AppPaths paths;
    private readonly object sync = new();
    private readonly List<JsonObject> entries = [];
    private long sequence;
    public string RunId { get; } = Guid.NewGuid().ToString("D");
    private static readonly HashSet<string> Events = new(StringComparer.Ordinal)
    {
        "process_started", "process_exited", "instance_started", "shutdown_completed", "configuration_ready",
        "configuration_failed", "panel_ready", "panel_startup_failed", "panel_session_ready", "panel_session_failed",
        "gateway_exited", "panel_exited", "port_occupied", "operation_rejected", "handshake_rejected",
        "directory_preparation_failed", "webview_startup_failed", "desktop_startup_failed", "app_error", "app_warning", "app_info", "log_archives_cleared"
    };

    public SafeLog(AppPaths paths) => this.paths = paths;

    public void Record(string source, string eventName, int? exitCode = null, int? hresult = null)
    {
        if (source is not ("app" or "panel" or "gateway")) source = "app";
        if (!Events.Contains(eventName)) eventName = "app_info";
        lock (sync)
        {
            var entry = new JsonObject
            {
                ["sequence"] = ++sequence, ["timestamp"] = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds() / 1000d,
                ["source"] = source, ["level"] = eventName.Contains("failed") || eventName == "app_error" ? "error" : "info",
                ["event"] = eventName
            };
            if (exitCode is not null) entry["exit_code"] = exitCode;
            if (hresult is not null) entry["hresult"] = $"0x{hresult.Value:X8}";
            entries.Add(entry);
            if (entries.Count > 2000) entries.RemoveRange(0, entries.Count - 2000);
        }
    }

    public JsonObject Tail(JsonObject payload)
    {
        lock (sync)
        {
            var cursor = payload["cursor"]?.GetValue<long>() ?? 0;
            var reset = payload["run_id"]?.GetValue<string>() != RunId ||
                (entries.Count > 0 && cursor < entries[0]["sequence"]!.GetValue<long>() - 1);
            if (reset) cursor = 0;
            var source = payload["source"]?.GetValue<string>() ?? "app";
            var limit = Math.Clamp(payload["limit"]?.GetValue<int>() ?? 200, 1, 200);
            var matches = entries.Where(item => item["sequence"]!.GetValue<long>() > cursor &&
                item["source"]!.GetValue<string>() == source).Take(limit).ToArray();
            return new JsonObject
            {
                ["run_id"] = RunId, ["cursor"] = matches.Length == limit ? matches[^1]["sequence"]!.GetValue<long>() : sequence,
                ["reset"] = reset, ["entries"] = new JsonArray(matches.Select(item => item.DeepClone()).ToArray())
            };
        }
    }

    public string DiagnosticText()
    {
        lock (sync) return new JsonObject
        {
            ["run_id"] = RunId, ["entries"] = new JsonArray(entries.Select(item => item.DeepClone()).ToArray())
        }.ToJsonString();
    }

    // A bounded snapshot on exit preserves diagnostics without polling writes to disk.
    public void SaveSnapshot()
    {
        try
        {
            var data = Encoding.UTF8.GetBytes(DiagnosticText());
            if (data.Length <= 1024 * 1024)
                AppPaths.AtomicWrite(Path.Combine(paths.Logs, "run-" + RunId + ".json"), data);
        }
        catch { /* Diagnostics must remain available in memory even when the snapshot cannot be saved. */ }
    }

    public int ClearArchives()
    {
        var count = 0;
        foreach (var file in Directory.EnumerateFiles(paths.Logs, "run-*.json"))
        {
            var basename = Path.GetFileNameWithoutExtension(file);
            if (!Guid.TryParseExact(basename[4..], "D", out _) || basename == "run-" + RunId) continue;
            AppPaths.CheckPrivateFile(file, 1024 * 1024);
            File.Delete(file); count++;
        }
        return count;
    }
}
