using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Reflection;
using System.Runtime.InteropServices;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Win32;

namespace WBPMM;

internal sealed class NativeUtilities
{
    private const string RunKey = @"Software\Microsoft\Windows\CurrentVersion\Run";
    private const string RunName = "WB-PMM";
    private const string DiagnosticName = "workbuddy-diagnostics.json";
    private const int MaximumSettingsBytes = 128 * 1024;
    private readonly AppPaths paths;
    private readonly SafeLog log;
    private readonly SemaphoreSlim gate = new(1, 1);
    private readonly Dictionary<string, string> notes = new(StringComparer.Ordinal);
    private bool firstRunCompleted;
    private string? activeDocumentId;
    private Preview? preview;
    private bool savingPreview;

    private sealed record Preview(string Id, string Owner, DateTimeOffset Expires, byte[] Data);

    public NativeUtilities(AppPaths paths, SafeLog log)
    {
        this.paths = paths;
        this.log = log;
        LoadSettings();
    }

    internal void RecordStartupFailure(string stage, Exception error)
    {
        // Record only numeric error codes, never exception messages containing credentials or paths.
        log.Record("app", stage == "WebView2" ? "webview_startup_failed" : "desktop_startup_failed", hresult: error.HResult);
        log.SaveSnapshot();
    }

    internal string StartupDiagnosticText() => new JsonObject
    {
        ["app_version"] = typeof(NativeUtilities).Assembly.GetName().Version?.ToString(),
        ["os_version"] = Environment.OSVersion.VersionString,
        ["process_architecture"] = System.Runtime.InteropServices.RuntimeInformation.ProcessArchitecture.ToString(),
        ["log"] = JsonNode.Parse(log.DiagnosticText())
    }.ToJsonString();

    internal void OpenLogDirectory() => OpenDirectory(new JsonObject { ["kind"] = "logs" });

    public AppPaths Paths => paths;
    public bool AutoStartApi { get; private set; }
    public bool LaunchAtLogin { get; private set; }
    public string Theme { get; private set; } = "system";
    public Func<string, string, CancellationToken, Task<bool>>? ConfirmAsync { get; set; }
    public Func<string, CancellationToken, Task<string?>>? ChooseSaveFileAsync { get; set; }
    public Func<JsonObject>? StatusProvider { get; set; }

    internal async Task RemoveAccountNoteAsync(string uid, CancellationToken ct)
    {
        if (!notes.ContainsKey(uid)) return;
        _ = await UpdatePreferencesAsync(new JsonObject {
            ["note"] = new JsonObject { ["uid"] = uid, ["value"] = "" }
        }, ct);
    }

    public void SetActiveDocument(string documentId)
    {
        activeDocumentId = documentId;
        preview = null;
    }

    public void RetireDocument(string documentId)
    {
        if (activeDocumentId != documentId) return;
        activeDocumentId = null;
        preview = null;
    }

    public async Task<JsonObject> ExecuteAsync(string action, JsonObject payload, CancellationToken ct)
    {
        ct.ThrowIfCancellationRequested();
        if (payload.Count > 5 || Encoding.UTF8.GetByteCount(payload.ToJsonString()) > 4096)
            throw InvalidPayload();
        return action switch
        {
            "preferences.get" => RequireEmpty(payload, Preferences()),
            "preferences.update" => await UpdatePreferencesAsync(payload, ct),
            "logs.tail" => Tail(payload),
            "diagnostics.export" => await ExportAsync(payload, ct),
            "desktop.openDirectory" => OpenDirectory(payload),
            "logs.clearArchives" => await ClearArchivesAsync(payload, ct),
            _ => throw new BridgeFailure("unsupported_action", "不支持的原生命令")
        };
    }

    private void LoadSettings()
    {
        if (!File.Exists(paths.SettingsFile)) return;
        try
        {
            var doc = JsonNode.Parse(AppPaths.ReadPrivateFile(paths.SettingsFile, MaximumSettingsBytes)) as JsonObject
                ?? throw new JsonException();
            var theme = StringValue(doc["theme"]);
            if (theme is "system" or "light" or "dark") Theme = theme;
            AutoStartApi = BooleanValue(doc["autoStartGatewayOnLaunch"]) ?? false;
            LaunchAtLogin = BooleanValue(doc["autoStartGatewayAtLogin"]) ?? false;
            firstRunCompleted = BooleanValue(doc["hasCompletedFirstRun"]) ?? false;
            if (doc["notes"] is JsonObject saved)
                foreach (var entry in saved)
                    if (notes.Count < 200 && ValidUid(entry.Key) && StringValue(entry.Value) is { } value &&
                        ValidNote(value)) notes[entry.Key] = value;
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException or JsonException or InvalidOperationException or ArgumentException or BridgeFailure)
        {
            throw new BridgeFailure("settings_invalid", "应用设置文件无效或不安全；原文件已保留");
        }
    }

    private JsonObject Preferences()
    {
        var saved = new JsonObject();
        foreach (var (uid, value) in notes) saved[uid] = value;
        var assembly = Assembly.GetEntryAssembly();
        var version = assembly?.GetName().Version;
        var info = assembly?.GetCustomAttribute<AssemblyInformationalVersionAttribute>()?.InformationalVersion;
        var about = new JsonObject
        {
            ["app_version"] = SafeVersion(info) ?? SafeVersion(version?.ToString(3)) ?? "unversioned",
            ["app_build"] = SafeVersion(version?.Revision.ToString()) ?? "unversioned",
            ["gateway_commit"] = "d1023f373065468d67791faf7e2235768af1e056",
            ["panel_commit"] = "9413e70bc694a1e76c416d00cf70f5c9869bc3ca",
            ["license"] = "MIT", ["architecture"] = RuntimeInformation.ProcessArchitecture.ToString().ToLowerInvariant(),
            ["build_time"] = "unrecorded", ["os_version"] = Environment.OSVersion.Version.ToString()
        };
        return new JsonObject
        {
            ["theme"] = Theme, ["autoStartGatewayOnLaunch"] = AutoStartApi,
            ["autoStartGatewayAtLogin"] = LaunchAtLogin,
            ["loginItemStatus"] = LoginItemStatus(), ["hasCompletedFirstRun"] = firstRunCompleted,
            ["notes"] = saved, ["about"] = about
        };
    }

    private async Task<JsonObject> UpdatePreferencesAsync(JsonObject payload, CancellationToken ct)
    {
        if (payload.Count is 0 or > 6 || payload.Any(item => item.Key is not
                ("theme" or "autoStartGatewayOnLaunch" or "autoStartGatewayAtLogin" or "hasCompletedFirstRun" or "note" or "openLoginItems")))
            throw InvalidPayload();
        if (payload.ContainsKey("openLoginItems"))
        {
            if (payload.Count != 1 || BooleanValue(payload["openLoginItems"]) != true) throw InvalidPayload();
            try
            {
                Process.Start(new ProcessStartInfo("ms-settings:startupapps") { UseShellExecute = true });
                return Preferences();
            }
            catch { throw new BridgeFailure("open_failed", "无法打开 Windows 启动应用设置"); }
        }
        var theme = Theme;
        var auto = AutoStartApi;
        var login = LaunchAtLogin;
        var first = firstRunCompleted;
        var changedNotes = new Dictionary<string, string>(notes, StringComparer.Ordinal);
        if (payload.ContainsKey("theme"))
        {
            theme = StringValue(payload["theme"]) ?? throw InvalidPayload();
            if (theme is not ("system" or "light" or "dark")) throw InvalidPayload();
        }
        if (payload.ContainsKey("autoStartGatewayOnLaunch")) auto = BooleanValue(payload["autoStartGatewayOnLaunch"]) ?? throw InvalidPayload();
        if (payload.ContainsKey("autoStartGatewayAtLogin")) login = BooleanValue(payload["autoStartGatewayAtLogin"]) ?? throw InvalidPayload();
        if (payload.ContainsKey("hasCompletedFirstRun")) first = BooleanValue(payload["hasCompletedFirstRun"]) ?? throw InvalidPayload();
        if (payload.ContainsKey("note"))
        {
            if (payload["note"] is not JsonObject note || note.Count != 2 || !note.ContainsKey("uid") || !note.ContainsKey("value")) throw InvalidPayload();
            var uid = StringValue(note["uid"]);
            var value = StringValue(note["value"]);
            if (uid is null || value is null || !ValidUid(uid) || !ValidNote(value)) throw InvalidPayload();
            if (value.Length == 0) changedNotes.Remove(uid);
            else if (changedNotes.ContainsKey(uid) || changedNotes.Count < 200) changedNotes[uid] = value;
            else throw InvalidPayload();
        }
        var next = new JsonObject
        {
            ["theme"] = theme, ["autoStartGatewayOnLaunch"] = auto,
            ["autoStartGatewayAtLogin"] = login, ["hasCompletedFirstRun"] = first
        };
        var saved = new JsonObject();
        foreach (var (uid, value) in changedNotes) saved[uid] = value;
        next["notes"] = saved;
        await gate.WaitAsync(ct);
        try
        {
            var oldRun = ReadRunValue();
            if (payload.ContainsKey("autoStartGatewayAtLogin")) SetRunValue(login);
            try { AppPaths.AtomicWrite(paths.SettingsFile, Encoding.UTF8.GetBytes(next.ToJsonString())); }
            catch
            {
                if (payload.ContainsKey("autoStartGatewayAtLogin")) RestoreRunValue(oldRun);
                throw new BridgeFailure("save_failed", "无法保存应用设置");
            }
            Theme = theme; AutoStartApi = auto; LaunchAtLogin = login; firstRunCompleted = first;
            notes.Clear();
            foreach (var (uid, value) in changedNotes) notes[uid] = value;
            return Preferences();
        }
        finally { gate.Release(); }
    }

    private JsonObject Tail(JsonObject payload)
    {
        if (payload.Any(item => item.Key is not ("source" or "run_id" or "cursor" or "limit")) ||
            StringValue(payload["source"]) is not ("app" or "panel" or "gateway")) throw InvalidPayload();
        if (payload.ContainsKey("run_id") && !Guid.TryParseExact(StringValue(payload["run_id"]), "D", out _)) throw InvalidPayload();
        if (payload.ContainsKey("cursor") && !ValidNumber(payload["cursor"], 0, 9_007_199_254_740_991)) throw InvalidPayload();
        if (payload.ContainsKey("limit") && !ValidNumber(payload["limit"], 1, 200)) throw InvalidPayload();
        return log.Tail(payload);
    }

    private async Task<JsonObject> ExportAsync(JsonObject payload, CancellationToken ct)
    {
        var operation = StringValue(payload["operation"]);
        if (operation == "preview")
        {
            if (payload.Count != 1 || activeDocumentId is null || savingPreview) throw InvalidPayload();
            var data = Encoding.UTF8.GetBytes(DiagnosticText());
            if (data.Length > 1024 * 1024) throw new BridgeFailure("save_failed", "诊断内容过大");
            preview = new Preview(Guid.NewGuid().ToString("D"), activeDocumentId, DateTimeOffset.UtcNow.AddMinutes(2), data);
            return new JsonObject
            {
                ["preview_id"] = preview.Id, ["bytes"] = data.Length,
                ["files"] = new JsonArray(DiagnosticName),
                ["sections"] = new JsonArray("应用与上游版本", "系统与架构", "服务状态与计数", "安全配置摘要", "最近安全事件")
            };
        }
        if (operation is not ("save" or "cancel") || payload.Count != 2 || !payload.ContainsKey("preview_id")) throw InvalidPayload();
        var id = StringValue(payload["preview_id"]);
        var current = preview;
        if (current is null || id != current.Id || current.Owner != activeDocumentId || current.Expires <= DateTimeOffset.UtcNow)
            throw new BridgeFailure("stale_preview", "诊断预览已失效，请重新预览");
        if (operation == "cancel") { preview = null; return new JsonObject { ["cancelled"] = true }; }
        if (savingPreview) throw new BridgeFailure("busy", "诊断保存正在进行");
        if (ChooseSaveFileAsync is null) throw new BridgeFailure("busy", "保存窗口不可用");
        savingPreview = true;
        try
        {
            var chosen = await ChooseSaveFileAsync(DiagnosticName, ct);
            if (current != preview || current.Owner != activeDocumentId) throw new BridgeFailure("stale_document", "页面已切换，请重新预览");
            preview = null;
            if (chosen is null) return new JsonObject { ["cancelled"] = true };
            ct.ThrowIfCancellationRequested();
            if (!Path.IsPathFullyQualified(chosen) || Path.GetExtension(chosen).ToLowerInvariant() != ".json" ||
                (File.Exists(chosen) && (File.GetAttributes(chosen) & FileAttributes.ReparsePoint) != 0))
                throw new IOException();
            var directory = Path.GetDirectoryName(chosen) ?? throw new IOException();
            var temp = Path.Combine(directory, ".wbpmm-export-" + Guid.NewGuid().ToString("N") + ".tmp");
            try
            {
                await File.WriteAllBytesAsync(temp, current.Data, ct);
                ct.ThrowIfCancellationRequested();
                if (activeDocumentId != current.Owner) throw new OperationCanceledException(ct);
                File.Move(temp, chosen, true);
            }
            finally { if (File.Exists(temp)) File.Delete(temp); }
            return new JsonObject { ["saved"] = true };
        }
        catch (OperationCanceledException) { throw; }
        catch (BridgeFailure) { throw; }
        catch { throw new BridgeFailure("save_failed", "无法保存诊断文件"); }
        finally { savingPreview = false; }
    }

    private string DiagnosticText()
    {
        var safeStatus = new JsonObject();
        var status = StatusProvider?.Invoke();
        if (status is not null)
        {
            foreach (var key in new[] { "gateway_state", "panel_state", "api_port", "panel_port",
                         "healthy_accounts", "total_accounts", "lan_enabled" })
                if (status[key] is JsonNode value) safeStatus[key] = value.DeepClone();
        }
        var configuration = new JsonObject
        {
            ["theme"] = Theme, ["auto_start_gateway"] = AutoStartApi,
            ["launch_at_login"] = LaunchAtLogin
        };
        var logs = JsonNode.Parse(log.DiagnosticText()) ?? new JsonObject();
        return new JsonObject
        {
            ["schema"] = 1, ["about"] = Preferences()["about"]?.DeepClone(),
            ["status"] = safeStatus, ["safe_configuration"] = configuration, ["logs"] = logs
        }.ToJsonString();
    }

    private JsonObject OpenDirectory(JsonObject payload)
    {
        var kind = StringValue(payload["kind"]);
        if (payload.Count != 1 || kind is not ("data" or "logs")) throw InvalidPayload();
        var target = kind == "data" ? paths.Root : paths.Logs;
        try
        {
            AppPaths.RejectReparseParents(target);
            if (!Directory.Exists(target)) return new JsonObject { ["opened"] = false };
            Process.Start(new ProcessStartInfo(target) { UseShellExecute = true });
            return new JsonObject { ["opened"] = true };
        }
        catch { return new JsonObject { ["opened"] = false }; }
    }

    private async Task<JsonObject> ClearArchivesAsync(JsonObject payload, CancellationToken ct)
    {
        if (payload.Count != 0) throw InvalidPayload();
        if (ConfirmAsync is null) throw new BridgeFailure("busy", "确认窗口不可用");
        var owner = activeDocumentId ?? throw new BridgeFailure("stale_document", "页面会话已失效");
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(ct);
        timeout.CancelAfter(TimeSpan.FromSeconds(25));
        bool accepted;
        try { accepted = await ConfirmAsync("清理安全日志归档？", "将清理全部旧安全日志快照。当前运行的日志和业务数据会保留。", timeout.Token); }
        catch (OperationCanceledException) { return new JsonObject { ["cleared"] = 0, ["cancelled"] = true }; }
        if (!accepted || timeout.IsCancellationRequested || activeDocumentId != owner)
            return new JsonObject { ["cleared"] = 0, ["cancelled"] = true };
        try { return new JsonObject { ["cleared"] = log.ClearArchives() }; }
        catch { throw new BridgeFailure("cleanup_failed", "无法清理日志归档"); }
    }

    private string LoginItemStatus()
    {
        try { return ReadRunValue() == ExpectedRunValue() ? "enabled" : "notRegistered"; }
        catch { return "failed"; }
    }

    private static string ExpectedRunValue() => "\"" + Environment.ProcessPath + "\" --background";
    private static string? ReadRunValue()
    {
        using var key = Registry.CurrentUser.OpenSubKey(RunKey, false);
        return key?.GetValue(RunName) as string;
    }
    private static void SetRunValue(bool enabled)
    {
        using var key = Registry.CurrentUser.CreateSubKey(RunKey, true) ?? throw new BridgeFailure("save_failed", "无法设置开机启动");
        if (enabled) key.SetValue(RunName, ExpectedRunValue(), RegistryValueKind.String);
        else key.DeleteValue(RunName, false);
    }
    private static void RestoreRunValue(string? value)
    {
        using var key = Registry.CurrentUser.CreateSubKey(RunKey, true);
        if (value is null) key?.DeleteValue(RunName, false);
        else key?.SetValue(RunName, value, RegistryValueKind.String);
    }

    private static JsonObject RequireEmpty(JsonObject payload, JsonObject response) => payload.Count == 0 ? response : throw InvalidPayload();
    private static BridgeFailure InvalidPayload() => new("invalid_payload", "请求参数无效");
    private static string? StringValue(JsonNode? node) { try { return node?.GetValue<string>(); } catch { return null; } }
    private static bool? BooleanValue(JsonNode? node) { try { return node?.GetValue<bool>(); } catch { return null; } }
    private static bool ValidNumber(JsonNode? node, long min, long max) => node is not null &&
        long.TryParse(node.ToJsonString(), out var value) && value >= min && value <= max;
    private static bool ValidUid(string value) => Encoding.UTF8.GetByteCount(value) is >= 6 and <= 128 &&
        !value.Contains("..", StringComparison.Ordinal) && value.All(c => c is >= 'A' and <= 'Z' or >= 'a' and <= 'z' or >= '0' and <= '9' or '.' or '_' or '-');
    private static bool ValidNote(string value) => !value.Contains('\0') && Encoding.UTF8.GetByteCount(value) <= 256;
    private static string? SafeVersion(string? value) => value is { Length: > 0 and <= 64 } &&
        value.All(c => char.IsAsciiLetterOrDigit(c) || c is '+' or '-' or '.' or '_') ? value : null;
}
