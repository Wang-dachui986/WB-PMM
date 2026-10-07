using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Net.NetworkInformation;
using System.Net.Sockets;
using System.Runtime.InteropServices;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Win32.SafeHandles;

namespace WBPMM;

/// <summary>WinForms-context owner of the exact two child processes and their private control channels.</summary>
internal sealed class DesktopController : IAsyncDisposable
{
    private readonly AppPaths _paths;
    private readonly SafeLog _log;
    private readonly SynchronizationContext? _ui;
    private ManagedHelper? _panel;
    private ManagedHelper? _gateway;
    private PanelClient? _client;
    private GatewaySnapshot? _committed;
    private string? _panelSecret;
    private string? _panelControlKey;
    private string? _gatewayManagementKey;
    private int? _panelPort;
    private int? _gatewayPort;
    private string _panelState = "未启动";
    private string _gatewayState = "stopped";
    private string? _busy;
    private DateTimeOffset _updatedAt = DateTimeOffset.UtcNow;
    private DateTimeOffset? _gatewayStartedAt;
    private int? _healthy;
    private int? _total;
    private ulong _generation;
    private ulong _lifecycleEpoch;
    private bool _sleeping;
    private bool _shutdownComplete;
    private CancellationTokenSource? _healthPoll;
    private ServiceFlow? _service;
    private MaintenanceFlow? _maintenance;
    private PendingAttempt? _attempt;
    private ImportPreview? _preview;
    private ProxyAttempt? _proxy;
    private (string ID, string Action, string Document)? _lastApplied;
    private readonly HashSet<string> _retired = new(StringComparer.Ordinal);
    private static readonly HttpClient LocalHttp = new(new HttpClientHandler { UseProxy = false, AllowAutoRedirect = false, UseCookies = false })
    { Timeout = Timeout.InfiniteTimeSpan };

    internal DesktopController(AppPaths paths, SafeLog log)
    {
        _paths = paths; _log = log; _ui = SynchronizationContext.Current;
    }

    internal string? PanelOrigin => _panelPort is int port && _panel?.IsRunning == true && PanelCookie is not null ? $"http://127.0.0.1:{port}" : null;
    internal string? InstanceId { get; private set; }
    internal string? PanelCookie { get; private set; }
    internal bool IsShuttingDown { get; private set; }
    internal event Action? StatusChanged;
    internal event Action? PanelChanged;
    internal event Action<string, string>? LifecycleChanged;
    internal Func<string, string, CancellationToken, Task<bool>>? ConfirmAsync { get; set; }
    internal Func<CancellationToken, Task<string?>>? ChooseAuthFileAsync { get; set; }
    internal Action<string>? CopyText { get; set; }
    internal Action<string>? OpenExternal { get; set; }
    internal Func<string, CancellationToken, Task>? RemoveAccountNoteAsync { get; set; }
    internal Func<string, string, CancellationToken, Task<string>>? ChooseStopModeAsync { get; set; }

    internal async Task InitializeAsync(bool autoStartApi, CancellationToken ct)
    {
        try { _paths.Prepare(); }
        catch (Exception error) { _log.Record("app", "directory_preparation_failed", hresult: error.HResult); throw; }
        try { _committed = GatewayConfiguration.EnsureInitial(_paths); }
        catch (BridgeFailure) { _log.Record("app", "configuration_failed"); _gatewayState = "failed"; Publish(); }
        catch (Exception error) { _log.Record("app", "configuration_failed", hresult: error.HResult); throw; }
        await StartPanelAsync(ct);
        if (autoStartApi && _committed is not null) await StartGatewayAsync(ct);
    }

    internal JsonObject GetStatus()
    {
        var status = new JsonObject {
            ["gateway_state"] = _gatewayState, ["panel_state"] = _panelState, ["instance_id"] = InstanceId ?? "",
            ["updated_at"] = _updatedAt.ToString("O", CultureInfo.InvariantCulture), ["busy"] = _busy is not null,
            ["lifecycle_epoch"] = _lifecycleEpoch.ToString(CultureInfo.InvariantCulture),
            ["lan_enabled"] = _committed?.AllowLan ?? false,
            ["lan_api_base_urls"] = new JsonArray(LanBaseUrls().Select(url => (JsonNode?)JsonValue.Create(url)).ToArray())
        };
        if (_panelPort is int pp) status["panel_port"] = pp;
        var port = _gatewayPort ?? _committed?.Port;
        if (port is int p) { status["api_port"] = p; status["api_base_url"] = $"http://127.0.0.1:{p}/v1"; }
        if (_gatewayStartedAt is DateTimeOffset started && _gateway?.IsRunning == true)
            status["uptime_seconds"] = Math.Max(0, (long)(DateTimeOffset.UtcNow - started).TotalSeconds);
        if (_healthy is int healthy) status["healthy_accounts"] = healthy;
        if (_total is int total) status["total_accounts"] = total;
        if (_maintenance is { } flow)
        {
            status["maintenance_generation"] = flow.Generation.ToString(CultureInfo.InvariantCulture);
            status["maintenance_action"] = flow.Action; status["maintenance_phase"] = flow.Phase;
        }
        return status;
    }

    private IEnumerable<string> LanBaseUrls()
    {
        if (_committed?.AllowLan != true || _gateway?.IsRunning != true || _gatewayPort is not int port) yield break;
        var seen = new HashSet<string>(StringComparer.Ordinal);
        NetworkInterface[] interfaces;
        try { interfaces = NetworkInterface.GetAllNetworkInterfaces(); }
        catch { yield break; }
        foreach (var nic in interfaces)
        {
            if (nic.OperationalStatus != OperationalStatus.Up || nic.NetworkInterfaceType is NetworkInterfaceType.Loopback or NetworkInterfaceType.Tunnel or NetworkInterfaceType.Ppp) continue;
            if (nic.Name.Contains("utun", StringComparison.OrdinalIgnoreCase)) continue;
            UnicastIPAddressInformationCollection addresses;
            try { addresses = nic.GetIPProperties().UnicastAddresses; }
            catch { continue; }
            foreach (var address in addresses)
            {
                var ip = address.Address;
                if (ip.AddressFamily != AddressFamily.InterNetwork) continue;
                var octets = ip.GetAddressBytes();
                if (!(octets[0] == 10 || octets[0] == 172 && octets[1] is >= 16 and <= 31 || octets[0] == 192 && octets[1] == 168)) continue;
                var raw = ip.ToString();
                if (seen.Add(raw)) yield return $"http://{raw}:{port}/v1";
            }
        }
    }

    private void Publish()
    {
        _updatedAt = DateTimeOffset.UtcNow;
        StatusChanged?.Invoke();
    }

    private void NotifyUI(Action action)
    {
        if (_ui is null || SynchronizationContext.Current == _ui) action();
        else _ui.Post(_ => action(), null);
    }

    private bool Begin(string name)
    {
        if (IsShuttingDown) throw new BridgeFailure("shutting_down", "应用正在退出");
        if (_busy is not null) throw new BridgeFailure("busy", "服务操作正在进行");
        _busy = name; Publish(); return true;
    }

    private void End(string name)
    {
        if (_busy == name) { _busy = null; Publish(); }
    }

    private static string Secret() => Convert.ToHexString(RandomNumberGenerator.GetBytes(32)).ToLowerInvariant();

    private async Task<bool> StartPanelAsync(CancellationToken ct)
    {
        try
        {
            var snapshot = GatewayConfiguration.Read(_paths);
            _committed = snapshot;
            if (_panel?.IsRunning == true && _panelPort is int existing) return await LoginPanelAsync(_panel, existing, ct);
            InstanceId = Convert.ToHexString(RandomNumberGenerator.GetBytes(8)).ToLowerInvariant();
            _panelSecret = Secret(); _panelControlKey = Secret();
            _panelState = "正在启动"; PanelCookie = null; _panelPort = null; Publish(); PanelChanged?.Invoke();
            var helper = new ManagedHelper("wbgui", Path.Combine(_paths.Helpers, "wbgui.exe"), _paths.Root, _log);
            helper.Exited += code => NotifyUI(() => PanelExited(helper, code));
            _panel = helper;
            var init = new JsonObject {
                ["protocol"] = 1, ["instance_id"] = InstanceId, ["username"] = "desktop", ["password"] = _panelSecret,
                ["panel_control_key"] = _panelControlKey, ["gateway_url"] = $"http://127.0.0.1:{snapshot.Port}",
                ["auth_dir"] = _paths.Auths, ["config_file"] = _paths.GatewayConfig, ["backup_dir"] = _paths.Backups,
                ["dangerous_ops"] = false
            };
            if (!string.IsNullOrEmpty(snapshot.ApiKey)) init["gateway_api_key"] = snapshot.ApiKey;
            var ready = await helper.StartAsync(["--desktop"], init, true, InstanceId, ct);
            if (_panel != helper || !helper.IsRunning || ready is null || Text(ready["service"]) != "wbgui" ||
                Integer(ready["protocol"]) != 1 || Text(ready["instance_id"]) != InstanceId ||
                Integer(ready["pid"]) != helper.Pid || Integer(ready["port"]) is < 1024 or > 65535)
                throw new BridgeFailure("operation_failed", "面板握手与当前进程不匹配");
            _panelPort = (int)Integer(ready["port"]);
            return await LoginPanelAsync(helper, _panelPort.Value, ct);
        }
        catch (Exception error)
        {
            _log.Record("panel", "panel_startup_failed", hresult: error.HResult);
            _log.SaveSnapshot();
            _panelState = "启动失败"; PanelCookie = null; _client?.Dispose(); _client = null; Publish(); PanelChanged?.Invoke();
            if (_panel is { } partial && partial.IsRunning) { try { await partial.StopAsync(CancellationToken.None); } catch { } }
            return false;
        }
    }

    private async Task<bool> LoginPanelAsync(ManagedHelper helper, int port, CancellationToken ct)
    {
        try
        {
            using var client = new PanelClient(port);
            var cookie = await client.LoginAsync("desktop", _panelSecret!, ct);
            if (_panel != helper || !helper.IsRunning || _panelPort != port || IsShuttingDown) return false;
            _client?.Dispose(); _client = new PanelClient(port); PanelCookie = cookie; _panelState = "已就绪";
            Publish(); PanelChanged?.Invoke(); return true;
        }
        catch (Exception error)
        {
            _log.Record("panel", "panel_session_failed", hresult: error.HResult);
            _log.SaveSnapshot();
            PanelCookie = null; _panelState = "管理连接已过期"; Publish(); PanelChanged?.Invoke(); return false;
        }
    }

    private void PanelExited(ManagedHelper helper, int code)
    {
        if (_panel != helper || IsShuttingDown) return;
        _panel = null; _panelPort = null; PanelCookie = null; _panelSecret = null; _panelControlKey = null;
        _client?.Dispose(); _client = null; _panelState = "启动失败"; Publish(); PanelChanged?.Invoke();
        _log.Record("panel", "process_exited", code);
        if (_service is { } flow) flow.Cancel.Cancel();
        if (_maintenance is { } maintenance) _ = FinishMaintenanceAsync(maintenance);
    }

    private async Task<bool> ReconnectPanelAsync(CancellationToken ct)
    {
        if (_panel?.IsRunning == true && _panelPort is int port) return await LoginPanelAsync(_panel, port, ct);
        if (_panel is { } old) { try { await old.StopAsync(ct); } catch { return false; } }
        return await StartPanelAsync(ct);
    }

    private async Task<bool> StartGatewayAsync(CancellationToken ct)
    {
        if (_gateway?.IsRunning == true || _gatewayState is "starting" or "stopping" or "maintenance") return false;
        ManagedHelper? helper = null;
        try
        {
            var snapshot = GatewayConfiguration.Read(_paths);
            if (_committed?.Revision != snapshot.Revision || string.IsNullOrWhiteSpace(snapshot.ApiKey)) throw new BridgeFailure("CONFIG_CONFLICT", "配置已被外部修改");
            var portResult = ProbePort(snapshot.Host, snapshot.Port);
            if (portResult != "free") throw new BridgeFailure(portResult == "occupied" ? "port_occupied" : "port_unavailable", "API 端口不可用");
            _gatewayState = "starting"; _gatewayManagementKey = Secret(); _gatewayPort = null; _healthy = null; _total = null; Publish();
            helper = new ManagedHelper("wb2api", Path.Combine(_paths.Helpers, "wb2api.exe"), _paths.Root, _log);
            helper.Exited += code => NotifyUI(() => GatewayExited(helper, code));
            _gateway = helper;
            await helper.StartAsync(["-desktop", "-config", _paths.GatewayConfig], new JsonObject { ["management_key"] = _gatewayManagementKey }, false, InstanceId ?? "", ct);
            if (!helper.IsRunning || _gateway != helper) throw new BridgeFailure("operation_failed", "API 进程未运行");
            _gatewayPort = snapshot.Port; _gatewayStartedAt = DateTimeOffset.UtcNow;
            var deadline = DateTimeOffset.UtcNow.AddSeconds(10);
            while (DateTimeOffset.UtcNow < deadline)
            {
                ct.ThrowIfCancellationRequested();
                if (_gateway != helper || !helper.IsRunning) break;
                var health = await ProbeHealthAsync(snapshot.Port, ct);
                if (health is { } found)
                {
                    _healthy = found.Healthy; _total = found.Total;
                    _gatewayState = found.Healthy > 0 ? "running_ready" : "running_unavailable";
                    Publish(); StartHealthPolling(helper, snapshot.Port); return true;
                }
                await Task.Delay(200, ct);
            }
            throw new BridgeFailure("operation_failed", "API 探活超时");
        }
        catch
        {
            _gatewayState = "failed"; Publish();
            if (helper is not null && helper.IsRunning) { try { await helper.StopAsync(CancellationToken.None); } catch { } }
            if (_gateway == helper && helper?.IsRunning != true) { _gateway = null; _gatewayPort = null; _gatewayManagementKey = null; }
            return false;
        }
    }

    private void GatewayExited(ManagedHelper helper, int code)
    {
        if (_gateway != helper || IsShuttingDown || _gatewayState == "stopping") return;
        _gateway = null; _gatewayPort = null; _gatewayManagementKey = null; _healthy = null; _total = null;
        _healthPoll?.Cancel(); _gatewayState = "failed"; Publish(); _log.Record("gateway", "process_exited", code);
    }

    private async Task<bool> StopGatewayAsync(CancellationToken ct, TimeSpan? grace = null)
    {
        _gatewayState = "stopping"; _healthPoll?.Cancel(); Publish();
        var helper = _gateway;
        try { if (helper is not null) await helper.StopAsync(ct, grace); }
        catch { _gatewayState = "failed"; Publish(); return false; }
        if (helper?.IsRunning == true) { _gatewayState = "failed"; Publish(); return false; }
        if (_gateway == helper) _gateway = null;
        _gatewayPort = null; _gatewayManagementKey = null; _healthy = null; _total = null; _gatewayStartedAt = null;
        _gatewayState = "stopped"; Publish(); return true;
    }

    private static string ProbePort(string host, int port)
    {
        try
        {
            using var listener = new Socket(AddressFamily.InterNetwork, SocketType.Stream, ProtocolType.Tcp);
            listener.ExclusiveAddressUse = true; listener.Bind(new IPEndPoint(IPAddress.Parse(host), port));
            return "free";
        }
        catch (SocketException ex) when (ex.SocketErrorCode == SocketError.AddressAlreadyInUse) { return "occupied"; }
        catch { return "unavailable"; }
    }

    private async Task<(int Healthy, int Total)?> ProbeHealthAsync(int port, CancellationToken ct)
    {
        try
        {
            var (status, json) = await LocalRequestAsync(port, HttpMethod.Get, "healthz", null, null, 3, ct);
            if (status is not (HttpStatusCode.OK or HttpStatusCode.ServiceUnavailable) || Text(json["service"]) != "workbuddy2api") return null;
            var healthy = Integer(json["healthy"]); var total = Integer(json["total"]);
            if (healthy < 0 || total < 0 || healthy > total || total > int.MaxValue) return null;
            return ((int)healthy, (int)total);
        }
        catch { return null; }
    }

    private void StartHealthPolling(ManagedHelper helper, int port)
    {
        _healthPoll?.Cancel(); var source = new CancellationTokenSource(); _healthPoll = source;
        _ = PollHealthAsync(helper, port, source.Token);
    }

    private async Task PollHealthAsync(ManagedHelper helper, int port, CancellationToken ct)
    {
        while (!ct.IsCancellationRequested)
        {
            try { await Task.Delay(TimeSpan.FromSeconds(5), ct); } catch { return; }
            var health = await ProbeHealthAsync(port, ct);
            NotifyUI(() => {
                if (ct.IsCancellationRequested || _sleeping || _gateway != helper || !helper.IsRunning || _gatewayPort != port || IsShuttingDown || _gatewayState is "maintenance" or "starting" or "stopping") return;
                _healthy = health?.Healthy; _total = health?.Total;
                _gatewayState = health?.Healthy > 0 ? "running_ready" : "running_unavailable"; Publish();
            });
        }
    }

    private static async Task<(HttpStatusCode Status, JsonObject Body)> LocalRequestAsync(int port, HttpMethod method, string route,
        string? managementKey, JsonObject? body, int seconds, CancellationToken ct)
    {
        using var deadline = CancellationTokenSource.CreateLinkedTokenSource(ct);
        deadline.CancelAfter(TimeSpan.FromSeconds(seconds));
        using var request = new HttpRequestMessage(method, $"http://127.0.0.1:{port}/{route}");
        if (managementKey is not null) request.Headers.TryAddWithoutValidation("X-WorkBuddy-Management-Key", managementKey);
        if (body is not null) request.Content = new StringContent(body.ToJsonString(), Encoding.UTF8, "application/json");
        using var response = await LocalHttp.SendAsync(request, HttpCompletionOption.ResponseHeadersRead, deadline.Token);
        if (response.Content.Headers.ContentLength is long length && length > 65536) throw new BridgeFailure("operation_failed", "本机响应过大");
        await using var stream = await response.Content.ReadAsStreamAsync(deadline.Token);
        using var memory = new MemoryStream(); var buffer = new byte[4096];
        while (true)
        {
            var count = await stream.ReadAsync(buffer, deadline.Token); if (count == 0) break;
            if (memory.Length + count > 65536) throw new BridgeFailure("operation_failed", "本机响应过大");
            memory.Write(buffer, 0, count);
        }
        return (response.StatusCode, JsonNode.Parse(memory.ToArray()) as JsonObject ?? throw new BridgeFailure("operation_failed", "本机响应无效"));
    }

    private static string Text(JsonNode? node) { try { return node?.GetValue<string>() ?? ""; } catch { return ""; } }
    private static long Integer(JsonNode? node) { try { return node?.GetValue<long>() ?? -1; } catch { return -1; } }

    internal async Task<JsonObject> ExecuteAsync(string action, JsonObject payload, string requestId, string documentId, CancellationToken ct)
    {
        if (string.IsNullOrEmpty(documentId) || _retired.Contains(documentId)) throw new BridgeFailure("stale_document", "页面会话已失效");
        switch (action)
        {
            case "desktop.getStatus":
                BridgeValidation.Empty(payload);
                return new JsonObject { ["protocol"] = 1, ["status"] = GetStatus() };
            case "desktop.getBridgeInfo":
                BridgeValidation.Empty(payload);
                return new JsonObject { ["protocol"] = 1, ["commands"] = new JsonArray(CommandNames.Select(name => (JsonNode?)JsonValue.Create(name)).ToArray()) };
            case "desktop.checkReadiness": BridgeValidation.Empty(payload); return await CheckReadinessAsync(ct);
            case "desktop.checkProxy": return await CheckProxyAsync(payload, requestId, documentId, ct);
            case "gateway.start":
            case "desktop.reconnect":
            case "stats.reset":
                BridgeValidation.Empty(payload);
                Begin(action);
                try
                {
                    var ok = action switch {
                        "gateway.start" => await StartGatewayAsync(ct),
                        "desktop.reconnect" => await ReconnectPanelAsync(ct),
                        _ => await ResetStatisticsAsync(documentId, ct)
                    };
                    if (!ok) throw new BridgeFailure("operation_failed", "操作未完成，请查看原生状态");
                    return new JsonObject();
                }
                finally { End(action); }
            case "gateway.stop":
            case "gateway.restart":
            case "gateway.applyConfig":
            case "api.rotateKey": return await ServiceCommandAsync(action, payload, requestId, documentId, ct);
            case "api.copyKey":
                BridgeValidation.Empty(payload); Begin(action);
                try
                {
                    var snapshot = GatewayConfiguration.Read(_paths);
                    if (snapshot.Revision != _committed?.Revision || string.IsNullOrWhiteSpace(snapshot.ApiKey)) throw new BridgeFailure("CONFIG_CONFLICT", "配置已经变化");
                    (CopyText ?? throw new BridgeFailure("operation_failed", "剪贴板未就绪"))(snapshot.ApiKey);
                    return new JsonObject { ["copied"] = true };
                }
                finally { End(action); }
            case "account.maintenance": return await MaintenanceCommandAsync(payload, requestId, documentId, ct);
            case "auth.openBrowser": return await OpenAuthorizationAsync(payload, documentId, ct);
            case "files.importAuth": return await ImportCommandAsync(payload, requestId, documentId, ct);
            case "account.setEnabled": return await SetAccountEnabledAsync(payload, documentId, ct);
            case "account.delete": return await DeleteAccountAsync(payload, documentId, ct);
            default: throw new BridgeFailure("unsupported_action", "命令尚未开放");
        }
    }

    private static readonly string[] CommandNames = [
        "desktop.getStatus", "desktop.getBridgeInfo", "desktop.checkReadiness", "desktop.checkProxy", "gateway.start",
        "gateway.stop", "gateway.restart", "desktop.reconnect", "stats.reset", "account.maintenance", "auth.openBrowser",
        "files.importAuth", "account.setEnabled", "account.delete", "gateway.applyConfig", "api.rotateKey", "api.copyKey",
        "logs.tail", "preferences.get", "preferences.update", "diagnostics.export", "desktop.openDirectory", "logs.clearArchives"
    ];

    private async Task<JsonObject> CheckReadinessAsync(CancellationToken ct)
    {
        var result = new JsonObject { ["checked_at"] = DateTimeOffset.UtcNow.ToString("O", CultureInfo.InvariantCulture) };
        if (_service is not null) { result["ready"] = false; result["config_result"] = "busy"; result["checks"] = new JsonArray(); return result; }
        var checks = new JsonArray(); var allOkay = true;
        foreach (var name in new[] { "wbgui", "wb2api" })
        {
            var file = Path.Combine(_paths.Helpers, name + ".exe");
            bool okay;
            try { okay = File.Exists(file) && !File.GetAttributes(file).HasFlag(FileAttributes.ReparsePoint); }
            catch { okay = false; }
            checks.Add(new JsonObject { ["check"] = name, ["result"] = okay ? "ok" : "helper_missing" }); allOkay &= okay;
        }
        foreach (var (name, path) in new[] { ("data", _paths.Root), ("logs", _paths.Logs), ("auths", _paths.Auths),
            ("backups", _paths.Backups), ("run", _paths.Run) })
        {
            var okay = await PrivateWriteCheckAsync(path, ct);
            checks.Add(new JsonObject { ["check"] = name, ["result"] = okay ? "ok" : "directory_unavailable" }); allOkay &= okay;
        }
        result["checks"] = checks;
        try
        {
            var snapshot = GatewayConfiguration.Read(_paths);
            if (snapshot.Revision != _committed?.Revision) { result["config_result"] = "configuration_changed"; result["ready"] = false; return result; }
            var owned = _gateway?.IsRunning == true && _gatewayPort == snapshot.Port;
            result["revision"] = snapshot.Revision; result["port"] = snapshot.Port;
            result["port_result"] = owned ? "owned" : ProbePort(snapshot.Host, snapshot.Port);
            result["config_result"] = string.IsNullOrWhiteSpace(snapshot.ApiKey) ? "key_missing" : "ok";
            result["ready"] = allOkay && (owned || Text(result["port_result"]) == "free") && Text(result["config_result"]) == "ok";
        }
        catch { result["config_result"] = "configuration_unavailable"; result["ready"] = false; }
        return result;
    }

    private static async Task<bool> PrivateWriteCheckAsync(string path, CancellationToken ct)
    {
        try
        {
            var info = new DirectoryInfo(path);
            if (!info.Exists || info.Attributes.HasFlag(FileAttributes.ReparsePoint)) return false;
            var candidate = Path.Combine(path, ".readiness-" + Guid.NewGuid().ToString("N"));
            try
            {
                await using var stream = new FileStream(candidate, FileMode.CreateNew, FileAccess.Write, FileShare.None, 1, FileOptions.DeleteOnClose | FileOptions.WriteThrough);
                await stream.WriteAsync(new byte[] { 0 }, ct); await stream.FlushAsync(ct);
                return true;
            }
            finally { try { File.Delete(candidate); } catch { } }
        }
        catch { return false; }
    }

    private async Task<JsonObject> CheckProxyAsync(JsonObject payload, string id, string document, CancellationToken ct)
    {
        if (Text(payload["operation"]) == "cancel_pending")
        {
            BridgeValidation.Keys(payload, "operation", "request_id"); var old = BridgeValidation.RequestID(payload["request_id"]);
            if (_proxy is { } attempt && attempt.ID == old && attempt.Document == document) attempt.Cancel.Cancel();
            return new JsonObject { ["cancelled"] = true };
        }
        BridgeValidation.Keys(payload, "revision"); var revision = BridgeValidation.Revision(payload["revision"]);
        if (_proxy is not null || _busy is not null || _sleeping) throw new BridgeFailure("busy", "检查正在进行");
        var snapshot = GatewayConfiguration.Read(_paths);
        if (snapshot.Revision != revision || _committed?.Revision != revision) throw new BridgeFailure("revision_conflict", "配置版本已改变");
        var checkedAt = DateTimeOffset.UtcNow.ToString("O", CultureInfo.InvariantCulture);
        if (snapshot.ProxyUrl.Length == 0) return new JsonObject { ["revision"] = revision, ["checked_at"] = checkedAt, ["result"] = "not_applicable", ["tcp_only"] = true };
        if (!GatewayConfiguration.TryProxyEndpoint(snapshot.ProxyUrl, out var host, out var port)) throw new BridgeFailure("invalid_configuration", "代理地址无效");
        var running = new ProxyAttempt(id, document, revision); _proxy = running;
        using var linked = CancellationTokenSource.CreateLinkedTokenSource(ct, running.Cancel.Token);
        linked.CancelAfter(TimeSpan.FromSeconds(5));
        var result = "unreachable";
        try
        {
            using var tcp = new TcpClient();
            await tcp.ConnectAsync(host, port, linked.Token);
            result = "reachable";
        }
        catch (OperationCanceledException) when (running.Cancel.IsCancellationRequested || ct.IsCancellationRequested) { throw new BridgeFailure("cancelled", "检查已取消"); }
        catch (OperationCanceledException) { throw new BridgeFailure("expired", "检查超时"); }
        catch (SocketException) { result = "unreachable"; }
        finally { if (_proxy == running) _proxy = null; running.Cancel.Dispose(); }
        if (_retired.Contains(document)) throw new BridgeFailure("cancelled", "页面已切换");
        if (GatewayConfiguration.Read(_paths).Revision != revision || _committed?.Revision != revision) throw new BridgeFailure("revision_conflict", "配置版本已改变");
        return new JsonObject { ["revision"] = revision, ["checked_at"] = checkedAt, ["result"] = result, ["tcp_only"] = true };
    }

    private async Task<bool> ResetStatisticsAsync(string document, CancellationToken ct)
    {
        var helper = _gateway; var port = _gatewayPort; var key = _gatewayManagementKey;
        if (helper?.IsRunning != true || port is null || key is null || _gatewayState is not ("running_ready" or "running_unavailable")) return false;
        try
        {
            var (status, body) = await LocalRequestAsync(port.Value, HttpMethod.Post, "v1/stats/reset", key, null, 5, ct);
            return !_retired.Contains(document) && _gateway == helper && helper.IsRunning && _gatewayPort == port && _gatewayManagementKey == key &&
                (int)status is >= 200 and < 300 && BoolValue(body["ok"]);
        }
        catch { return false; }
    }

    private static bool BoolValue(JsonNode? value) { try { return value?.GetValue<bool>() == true; } catch { return false; } }

    private async Task<JsonObject> ManagementAsync(ManagedHelper helper, int port, string key, string route, JsonObject body, CancellationToken ct)
    {
        var (status, reply) = await LocalRequestAsync(port, HttpMethod.Post, route, key, body, 5, ct);
        if (_gateway != helper || !helper.IsRunning || _gatewayPort != port || _gatewayManagementKey != key || status != HttpStatusCode.OK)
            throw new BridgeFailure("operation_failed", "网关管理回执无效");
        return reply;
    }

    private async Task<JsonObject> ServiceCommandAsync(string action, JsonObject payload, string id, string document, CancellationToken ct)
    {
        if (Text(payload["operation"]) == "cancel_pending")
        {
            BridgeValidation.Keys(payload, "operation", "request_id");
            var original = BridgeValidation.RequestID(payload["request_id"]);
            if (_lastApplied is { } applied && applied.ID == original && applied.Action == action && applied.Document == document)
                return new JsonObject { ["cancelled"] = false, ["applied"] = true };
            if (_service is not { } active || active.Action != action || active.ID != original)
                return new JsonObject { ["cancelled"] = false, ["outcome_unknown"] = true };
            if (active.Document != document) throw new BridgeFailure("stale_document", "操作属于另一页面");
            active.Cancel.Cancel();
            if (active.Work is not null) { try { await active.Work; } catch { } }
            if (_service == active) throw new BridgeFailure("cleanup_failed", "原生清理尚未确认");
            return new JsonObject { ["cancelled"] = !active.FinishAcknowledged, ["applied"] = active.FinishAcknowledged };
        }
        string? revision = null; JsonObject? patch = null;
        if (action is "gateway.stop" or "gateway.restart") BridgeValidation.Empty(payload);
        else if (action == "api.rotateKey") { BridgeValidation.Keys(payload, "revision"); revision = BridgeValidation.Revision(payload["revision"]); }
        else { patch = BridgeValidation.Patch(payload); revision = BridgeValidation.Revision(payload["revision"]); }
        Begin(action);
        try
        {
            var old = revision is null ? _committed : GatewayConfiguration.Read(_paths);
            if (old is null || old.Revision != _committed?.Revision || revision is not null && old.Revision != revision)
                throw new BridgeFailure("CONFIG_CONFLICT", "配置版本已改变");
            if (_panel?.IsRunning != true || _panelPort is null || _panelControlKey is null || PanelCookie is null || _client is null)
                throw new BridgeFailure("operation_failed", "管理面板尚未就绪");
            var gateway = _gateway;
            if (gateway is not null && (!gateway.IsRunning || _gatewayPort is null || _gatewayManagementKey is null))
                throw new BridgeFailure("operation_failed", "API 进程状态未确认");
            var flow = new ServiceFlow(id, action, document, InstanceId!, old, _panel, _panelPort.Value, PanelCookie, _panelControlKey,
                gateway, _gatewayPort, _gatewayManagementKey, gateway?.IsRunning == true);
            if (gateway is not null) flow.Generation = NextGeneration();
            _service = flow;
            flow.Work = RunServiceAsync(flow, revision, patch, ct);
            return await flow.Work;
        }
        catch { if (_service is null) End(action); throw; }
    }

    private async Task<JsonObject> RunServiceAsync(ServiceFlow flow, string? revision, JsonObject? patch, CancellationToken caller)
    {
        using var linked = CancellationTokenSource.CreateLinkedTokenSource(caller, flow.Cancel.Token);
        linked.CancelAfter(TimeSpan.FromSeconds(900));
        try
        {
            CheckService(flow, linked.Token);
            var mode = await ChooseModeAsync(flow.Action, flow.WasRunning, linked.Token);
            if (mode == "cancel") throw new BridgeFailure("cancelled", "已取消");
            CheckService(flow, linked.Token);
            if (revision is not null)
            {
                flow.Phase = "preparing"; flow.PrepareUncertain = true;
                try
                {
                    using var client = new PanelClient(flow.PanelPort);
                    flow.Prepared = await client.PrepareConfigAsync(InstanceId!, flow.ControlKey, revision, patch, linked.Token);
                    flow.PrepareUncertain = false;
                }
                catch (PanelConfigFailure ex) { flow.PrepareUncertain = ex.Uncertain; throw; }
                CheckService(flow, linked.Token);
            }
            await StopForServiceAsync(flow, mode, linked.Token);
            CheckService(flow, linked.Token);
            if (flow.Prepared is { } prepared)
            {
                flow.Phase = "committing"; flow.CommitStarted = true; flow.CommitUncertain = true;
                PanelConfigView view;
                try
                {
                    using var client = new PanelClient(flow.PanelPort);
                    view = await client.CommitConfigAsync(InstanceId!, flow.ControlKey, prepared.BackupId, linked.Token);
                    flow.CommitUncertain = false;
                }
                catch (PanelConfigFailure ex) { flow.CommitUncertain = ex.Uncertain; throw; }
                CheckService(flow, linked.Token);
                var snapshot = GatewayConfiguration.Read(_paths);
                if (view.Revision != prepared.CandidateRevision || snapshot.Revision != view.Revision || snapshot.Port != view.Port || snapshot.AllowLan != view.AllowLan)
                    throw new BridgeFailure("CONFIG_CONFLICT", "写入配置与落盘内容不一致");
                _committed = snapshot; Publish();
                if (flow.WasRunning)
                {
                    flow.Phase = "starting";
                    if (!await StartGatewayAsync(linked.Token)) throw new BridgeFailure("operation_failed", "API 未能恢复运行");
                    flow.Candidate = _gateway;
                }
                CheckService(flow, linked.Token);
                flow.Phase = "finishing"; flow.FinishStarted = true;
                await FinishConfigWithRetryAsync(flow, prepared.BackupId, CancellationToken.None);
                flow.FinishAcknowledged = true;
                _lastApplied = (flow.ID, flow.Action, flow.Document);
                CompleteService(flow);
                return new JsonObject { ["config"] = view.Public.DeepClone(), ["applied"] = true };
            }
            if (flow.Action == "gateway.restart")
            {
                flow.Phase = "starting"; flow.RestoredOld = true;
                if (!await StartGatewayAsync(linked.Token)) throw new BridgeFailure("operation_failed", "API 未能重启");
                CheckService(flow, linked.Token);
            }
            CompleteService(flow);
            return new JsonObject { ["ok"] = true };
        }
        catch (Exception ex)
        {
            var cleaned = await CleanupServiceAsync(flow);
            if (cleaned) CompleteService(flow);
            else { flow.Phase = "blocked"; _gatewayState = "failed"; Publish(); }
            if (!cleaned) throw new BridgeFailure("cleanup_failed", "操作结果尚未安全确认，保护锁已保留");
            if (ex is PanelConfigFailure cf) throw new BridgeFailure(cf.Code, "配置操作未完成");
            if (ex is BridgeFailure bf) throw bf;
            if (ex is OperationCanceledException) throw new BridgeFailure("cancelled", "操作已取消");
            throw new BridgeFailure("operation_failed", "操作未完成");
        }
    }

    private void CheckService(ServiceFlow flow, CancellationToken ct)
    {
        ct.ThrowIfCancellationRequested();
        if (_service != flow || IsShuttingDown || _retired.Contains(flow.Document) || DateTimeOffset.UtcNow >= flow.Expires)
            throw new BridgeFailure("cancelled", "操作已取消");
        if (_panel != flow.Panel || flow.Panel.IsRunning != true || _panelPort != flow.PanelPort ||
            PanelCookie != flow.Cookie || _panelControlKey != flow.ControlKey || InstanceId != flow.Instance)
            throw new BridgeFailure("stale_document", "管理连接已经变化");
    }

    private async Task<string> ChooseModeAsync(string action, bool wasRunning, CancellationToken ct)
    {
        using var confirm = CancellationTokenSource.CreateLinkedTokenSource(ct);
        confirm.CancelAfter(TimeSpan.FromSeconds(120));
        var title = action switch {
            "account.maintenance" => "开始账号维护？", "api.rotateKey" => "轮换 API 密钥？",
            "gateway.applyConfig" => "应用网关配置？", "gateway.restart" => "重启 API 服务？",
            _ => "停止 API 服务？"
        };
        var detail = wasRunning
            ? "等待完成会先停止接收新请求；立即中止会中断在途请求。"
            : "API 当前停止，操作完成后仍保持停止。";
        try
        {
            if (ChooseStopModeAsync is { } choose)
            {
                var mode = await choose(title, detail, confirm.Token);
                return mode is "wait" or "abort" ? mode : "cancel";
            }
            var accepted = ConfirmAsync is { } callback && await callback(title, detail, confirm.Token);
            return accepted ? "wait" : "cancel";
        }
        catch (OperationCanceledException) { return "cancel"; }
    }

    private async Task StopForServiceAsync(ServiceFlow flow, string mode, CancellationToken ct)
    {
        if (!flow.WasRunning || flow.Gateway is null) return;
        if (mode == "wait")
        {
            flow.Phase = "waiting"; flow.DrainRequested = true;
            bool drained;
            try { drained = await DrainAsync(flow.Gateway, flow.GatewayPort!.Value, flow.GatewayKey!, flow.Generation, "begin", ct); }
            catch (BridgeFailure ex) when (ex.Code == "not_supported") { flow.DrainRequested = false; throw; }
            while (!drained)
            {
                CheckService(flow, ct);
                await Task.Delay(200, ct);
                drained = await DrainAsync(flow.Gateway, flow.GatewayPort!.Value, flow.GatewayKey!, flow.Generation, "status", ct);
            }
        }
        CheckService(flow, ct);
        flow.Phase = "stopping";
        if (!await StopGatewayAsync(ct, mode == "abort" ? TimeSpan.Zero : TimeSpan.FromSeconds(10)))
            throw new BridgeFailure("operation_failed", "API 未能停止");
        flow.GatewayStopped = true;
        CheckService(flow, ct);
    }

    private async Task<bool> DrainAsync(ManagedHelper helper, int port, string key, ulong generation, string operation, CancellationToken ct)
    {
        if (_gateway != helper || !helper.IsRunning || _gatewayPort != port || _gatewayManagementKey != key)
            throw new BridgeFailure("operation_failed", "API 进程身份已改变");
        var (status, reply) = await LocalRequestAsync(port, HttpMethod.Post, "admin/desktop/drain", key,
            new JsonObject { ["operation"] = operation, ["generation"] = generation }, 5, ct);
        if (status is HttpStatusCode.NotFound or HttpStatusCode.NotImplemented) throw new BridgeFailure("not_supported", "网关不支持等待完成");
        if (_gateway != helper || !helper.IsRunning || _gatewayPort != port || _gatewayManagementKey != key ||
            status != HttpStatusCode.OK || !BoolValue(reply["ok"]) || Integer(reply["generation"]) != (long)generation ||
            BoolValue(reply["accepting"]) != (operation == "end")) throw new BridgeFailure("operation_failed", "排空回执无效");
        return operation == "end" || BoolValue(reply["drained"]);
    }

    private async Task FinishConfigWithRetryAsync(ServiceFlow flow, string backup, CancellationToken ct)
    {
        using var client = new PanelClient(flow.PanelPort);
        try { await client.FinishConfigAsync(flow.Instance, flow.ControlKey, backup, ct); }
        catch (PanelConfigFailure ex) when (ex.Uncertain)
        { await client.FinishConfigAsync(flow.Instance, flow.ControlKey, backup, ct); }
    }

    private async Task<bool> CleanupServiceAsync(ServiceFlow flow)
    {
        if (_service != flow || IsShuttingDown) return false;
        flow.Phase = "cleaning";
        try
        {
            if (flow.Candidate is { } candidate && candidate.IsRunning)
            {
                if (_gateway != candidate || !await StopGatewayAsync(CancellationToken.None) || candidate.IsRunning) return false;
            }
            if (flow.FinishStarted || flow.PrepareUncertain) return false;
            if (flow.Prepared is { } prepared)
            {
                if (_panel != flow.Panel || !flow.Panel.IsRunning || _panelPort != flow.PanelPort || _panelControlKey != flow.ControlKey ||
                    PanelCookie != flow.Cookie || InstanceId != flow.Instance) return false;
                using var client = new PanelClient(flow.PanelPort);
                var current = await client.ConfigViewAsync(flow.Cookie, CancellationToken.None);
                if (flow.CommitStarted && current.Revision == prepared.CandidateRevision)
                {
                    if (_gateway?.IsRunning == true) return false;
                    var restored = await client.RestoreConfigAsync(flow.Instance, flow.ControlKey, prepared.BackupId, prepared.CandidateRevision, CancellationToken.None);
                    if (restored.Revision != flow.Old.Revision || GatewayConfiguration.Read(_paths).Revision != flow.Old.Revision) return false;
                    _committed = flow.Old; Publish();
                }
                else if (current.Revision == flow.Old.Revision && !flow.CommitUncertain)
                {
                    try { await FinishConfigWithRetryAsync(flow, prepared.BackupId, CancellationToken.None); }
                    catch (PanelConfigFailure ex) when (flow.CommitStarted && !ex.Uncertain && ex.Code == "CONFIG_TRANSACTION") { }
                }
                else return false;
            }
            if (!flow.GatewayStopped && flow.WasRunning && _gateway?.IsRunning != true && flow.Gateway?.IsRunning != true)
                flow.GatewayStopped = true;
            if (flow.DrainRequested && !flow.GatewayStopped && flow.Gateway is { } oldGateway)
                if (!await DrainAsync(oldGateway, flow.GatewayPort!.Value, flow.GatewayKey!, flow.Generation, "end", CancellationToken.None)) return false;
            if (!flow.GatewayStopped && flow.WasRunning && (_gateway != flow.Gateway || flow.Gateway?.IsRunning != true)) return false;
            if (!flow.GatewayStopped && flow.WasRunning && _gatewayState == "failed")
            {
                var health = await ProbeHealthAsync(flow.GatewayPort!.Value, CancellationToken.None);
                if (health is null) return false;
                _healthy = health.Value.Healthy; _total = health.Value.Total;
                _gatewayState = health.Value.Healthy > 0 ? "running_ready" : "running_unavailable"; Publish();
            }
            if (flow.GatewayStopped && flow.WasRunning && _gateway?.IsRunning != true)
            {
                if (flow.RestoredOld) return flow.Prepared is null && _gateway is null;
                if (GatewayConfiguration.Read(_paths).Revision != flow.Old.Revision) return false;
                flow.RestoredOld = true; _committed = flow.Old; _gatewayState = "stopped"; Publish();
                return await StartGatewayAsync(CancellationToken.None);
            }
            return true;
        }
        catch { return false; }
    }

    private void CompleteService(ServiceFlow flow)
    {
        if (_service != flow) return;
        _service = null; flow.Cancel.Dispose(); End(flow.Action); Publish();
    }

    private ulong NextGeneration()
    {
        if (_generation == ulong.MaxValue) throw new BridgeFailure("operation_failed", "维护序号耗尽");
        return ++_generation;
    }

    private async Task<JsonObject> MaintenanceCommandAsync(JsonObject payload, string requestId, string document, CancellationToken ct)
    {
        var operation = Text(payload["operation"]);
        switch (operation)
        {
            case "begin":
                BridgeValidation.Keys(payload, "operation", "action", "uids");
                var action = BridgeValidation.Text(payload["action"], 1, 32);
                if (payload["uids"] is not JsonArray values) throw BridgeValidation.Invalid();
                var uids = values.Select(BridgeValidation.UID).ToArray();
                if (uids.Distinct(StringComparer.Ordinal).Count() != uids.Length ||
                    action switch { "login" => uids.Length != 0, "reauthorize" or "delete" => uids.Length != 1,
                        "refresh" or "credits" => uids.Length is < 1 or > 20, _ => true }) throw BridgeValidation.Invalid();
                return await BeginMaintenanceAsync(action == "reauthorize" ? "login" : action, uids, requestId, document, false, ct);
            case "cancel_pending":
                BridgeValidation.Keys(payload, "operation", "request_id");
                return await CancelAttemptAsync(BridgeValidation.RequestID(payload["request_id"]), document, false);
            case "end":
            case "cancel":
                BridgeValidation.Keys(payload, "operation", "instance_id", "generation");
                var instance = BridgeValidation.Text(payload["instance_id"]);
                var generation = BridgeValidation.Generation(payload["generation"]);
                var flow = _maintenance;
                if (flow is null || flow.Instance != instance || flow.Generation != generation || flow.Document != document)
                    throw new BridgeFailure("stale_maintenance", "维护许可已过期");
                if (!await FinishMaintenanceAsync(flow)) throw new BridgeFailure("operation_failed", "维护清理未确认");
                return new JsonObject { ["drained"] = true };
            default: throw BridgeValidation.Invalid();
        }
    }

    private async Task<JsonObject> CancelAttemptAsync(string id, string document, bool importing)
    {
        if (importing && _preview is { } preview && preview.RequestID == id && preview.Document == document)
        { _preview = null; return new JsonObject { ["cancelled"] = true, ["drained"] = true }; }
        var attempt = _attempt;
        if (attempt is null || attempt.ID != id || attempt.Document != document || attempt.Importing != importing)
            throw new BridgeFailure("stale_maintenance", "待取消操作已结束");
        attempt.Cancel.Cancel();
        if (_maintenance is { } flow && flow.Attempt == attempt && !await FinishMaintenanceAsync(flow))
            throw new BridgeFailure("operation_failed", "维护清理未确认");
        await attempt.Completed.Task;
        return new JsonObject { ["cancelled"] = true, ["drained"] = true };
    }

    private async Task<JsonObject> BeginMaintenanceAsync(string action, string[] uids, string requestId, string document, bool importing, CancellationToken ct)
    {
        Begin("account.maintenance");
        var attempt = new PendingAttempt(requestId, document, importing); _attempt = attempt;
        using var linked = CancellationTokenSource.CreateLinkedTokenSource(ct, attempt.Cancel.Token);
        linked.CancelAfter(TimeSpan.FromSeconds(120));
        MaintenanceFlow? flow = null;
        try
        {
            var panel = _panel; var port = _panelPort; var cookie = PanelCookie; var key = _panelControlKey;
            if (panel?.IsRunning != true || port is null || cookie is null || key is null || InstanceId is null)
                throw new BridgeFailure("operation_failed", "管理面板未就绪");
            var wasRunning = _gateway?.IsRunning == true;
            var mode = await ChooseModeAsync("account.maintenance", wasRunning, linked.Token);
            if (mode == "cancel" || _retired.Contains(document)) throw new BridgeFailure("cancelled", "已取消");
            if (_panel != panel || !panel.IsRunning || _panelPort != port || PanelCookie != cookie || _panelControlKey != key)
                throw new BridgeFailure("operation_failed", "管理面板已改变");
            flow = new MaintenanceFlow(document, attempt, action, uids, InstanceId, panel, port.Value, key,
                _gateway, _gatewayPort, _gatewayManagementKey, wasRunning, NextGeneration());
            _maintenance = flow; Publish();
            using var callerCancellation = ct.Register(() => flow.Cancel.Cancel());
            flow.Preparation = PrepareMaintenanceAsync(flow, mode);
            var granted = await flow.Preparation;
            if (!granted || flow.Cancel.IsCancellationRequested || _retired.Contains(document) || IsShuttingDown)
                throw new BridgeFailure("operation_failed", "无法进入账号维护");
            flow.Expiry = ExpireMaintenanceLaterAsync(flow);
            return new JsonObject { ["instance_id"] = flow.Instance, ["generation"] = flow.Generation.ToString(CultureInfo.InvariantCulture),
                ["action"] = flow.Action, ["uids"] = new JsonArray(flow.UIDs.Select(uid => (JsonNode?)JsonValue.Create(uid)).ToArray()),
                ["expires_at"] = flow.Expires.ToString("O", CultureInfo.InvariantCulture) };
        }
        catch (OperationCanceledException) { if (flow is not null) await FinishMaintenanceAsync(flow); else End("account.maintenance"); throw new BridgeFailure("cancelled", "维护已取消"); }
        catch
        {
            if (flow is not null) await FinishMaintenanceAsync(flow); else End("account.maintenance");
            throw;
        }
        finally
        {
            attempt.Completed.TrySetResult();
            if (_attempt == attempt && _maintenance?.Attempt != attempt) _attempt = null;
        }
    }

    private bool IsCurrent(MaintenanceFlow flow)
        => _maintenance == flow && !flow.Cancel.IsCancellationRequested && !_retired.Contains(flow.Document) &&
           !IsShuttingDown && DateTimeOffset.UtcNow < flow.Expires && _panel == flow.Panel && flow.Panel.IsRunning &&
           _panelPort == flow.PanelPort && _panelControlKey == flow.ControlKey && InstanceId == flow.Instance;

    private async Task<bool> PrepareMaintenanceAsync(MaintenanceFlow flow, string mode)
    {
        try
        {
            if (!IsCurrent(flow)) return false;
            if (flow.WasRunning && mode == "wait")
            {
                flow.DrainRequested = true; flow.Phase = "waiting"; Publish();
                var drained = await DrainAsync(flow.Gateway!, flow.GatewayPort!.Value, flow.GatewayKey!, flow.Generation, "begin", flow.Cancel.Token);
                while (!drained)
                {
                    if (!IsCurrent(flow)) return false;
                    await Task.Delay(200, flow.Cancel.Token);
                    drained = await DrainAsync(flow.Gateway!, flow.GatewayPort!.Value, flow.GatewayKey!, flow.Generation, "status", flow.Cancel.Token);
                }
            }
            if (!IsCurrent(flow)) return false;
            flow.Phase = "stopping"; Publish();
            if (!await StopGatewayAsync(flow.Cancel.Token, mode == "abort" ? TimeSpan.Zero : TimeSpan.FromSeconds(10))) return false;
            flow.GatewayStopped = true;
            if (!IsCurrent(flow) || _gateway is not null) return false;
            _gatewayState = "maintenance"; flow.Phase = "granting"; Publish();
            flow.PermitRequested = true; // A lost reply may still mean a live permit.
            using var client = new PanelClient(flow.PanelPort);
            var granted = await client.MaintenanceAsync("begin", flow.Instance, flow.Generation, flow.ControlKey,
                flow.Action, flow.UIDs, flow.Expires, flow.Cancel.Token);
            if (!granted || !IsCurrent(flow)) return false;
            flow.Phase = "active"; Publish(); return true;
        }
        catch (BridgeFailure ex) when (ex.Code == "not_supported") { flow.DrainRequested = false; return false; }
        catch { return false; }
    }

    private async Task ExpireMaintenanceLaterAsync(MaintenanceFlow flow)
    {
        try { await Task.Delay(flow.Expires - DateTimeOffset.UtcNow, flow.Cancel.Token); }
        catch { return; }
        await InvokeUIAsync(async () => { if (_maintenance == flow) await FinishMaintenanceAsync(flow); });
    }

    private Task InvokeUIAsync(Func<Task> work)
    {
        if (_ui is null || SynchronizationContext.Current == _ui) return work();
        var result = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        _ui.Post(async _ => { try { await work(); result.TrySetResult(); } catch (Exception ex) { result.TrySetException(ex); } }, null);
        return result.Task;
    }

    private Task<bool> FinishMaintenanceAsync(MaintenanceFlow flow)
    {
        if (_maintenance != flow) return Task.FromResult(true);
        flow.Cancel.Cancel(); flow.Phase = "ending"; Publish();
        return flow.Cleanup ??= CleanupMaintenanceAsync(flow);
    }

    private async Task<bool> CleanupMaintenanceAsync(MaintenanceFlow flow)
    {
        if (flow.Preparation is not null) { try { await flow.Preparation; } catch { } }
        if (_maintenance != flow) return true;
        var drained = !flow.PermitRequested || !flow.Panel.IsRunning;
        if (!drained)
        {
            try
            {
                using var client = new PanelClient(flow.PanelPort);
                drained = await client.MaintenanceAsync("end", flow.Instance, flow.Generation, flow.ControlKey, null, null, null, CancellationToken.None);
            }
            catch { }
        }
        if (!drained)
        {
            try { await flow.Panel.StopAsync(CancellationToken.None); drained = !flow.Panel.IsRunning; }
            catch { drained = false; }
            if (drained && _panel == flow.Panel)
            {
                _panel = null; _panelPort = null; PanelCookie = null; _panelSecret = null; _panelControlKey = null;
                _client?.Dispose(); _client = null; _panelState = "启动失败"; PanelChanged?.Invoke(); Publish();
            }
        }
        if (!drained)
        { flow.Phase = "blocked"; flow.Cleanup = null; Publish(); return false; }
        if (flow.DrainRequested && !flow.GatewayStopped && flow.Gateway?.IsRunning == true)
        {
            try { if (!await DrainAsync(flow.Gateway, flow.GatewayPort!.Value, flow.GatewayKey!, flow.Generation, "end", CancellationToken.None)) throw new Exception(); }
            catch { flow.Phase = "blocked"; flow.Cleanup = null; Publish(); return false; }
        }
        if (!flow.GatewayStopped && flow.WasRunning && _gateway?.IsRunning != true && flow.Gateway?.IsRunning != true)
            flow.GatewayStopped = true;
        if (!flow.GatewayStopped && flow.WasRunning && (_gateway != flow.Gateway || flow.Gateway?.IsRunning != true))
        { flow.Phase = "blocked"; flow.Cleanup = null; Publish(); return false; }
        if (!flow.GatewayStopped && flow.WasRunning && flow.Gateway?.IsRunning == true && _gateway == flow.Gateway && _gatewayState == "failed")
        {
            var health = await ProbeHealthAsync(flow.GatewayPort!.Value, CancellationToken.None);
            if (health is null) { flow.Phase = "blocked"; flow.Cleanup = null; Publish(); return false; }
            _healthy = health.Value.Healthy; _total = health.Value.Total;
            _gatewayState = health.Value.Healthy > 0 ? "running_ready" : "running_unavailable"; Publish();
        }
        var restored = true;
        if (flow.GatewayStopped && _gateway?.IsRunning != true && _gatewayState == "maintenance")
        { _gatewayState = "stopped"; Publish(); }
        if (flow.WasRunning && _gateway?.IsRunning != true && !IsShuttingDown)
        { _gatewayState = "stopped"; Publish(); restored = await StartGatewayAsync(CancellationToken.None); }
        if (_maintenance == flow) { _maintenance = null; if (_attempt == flow.Attempt) _attempt = null; End("account.maintenance"); Publish(); }
        return restored;
    }

    private MaintenanceFlow ActiveMaintenance(string document, string action)
    {
        var flow = _maintenance;
        if (flow is null || flow.Document != document || flow.Action != action || flow.Phase != "active" || !IsCurrent(flow))
            throw new BridgeFailure("stale_maintenance", "维护许可已失效");
        return flow;
    }

    private async Task<JsonObject> OpenAuthorizationAsync(JsonObject payload, string document, CancellationToken ct)
    {
        BridgeValidation.Keys(payload, "session_id"); var session = BridgeValidation.HexSession(payload["session_id"]);
        var flow = ActiveMaintenance(document, "login");
        using var client = new PanelClient(flow.PanelPort);
        var url = await client.AuthorizationUrlAsync(session, flow.Instance, flow.Generation, flow.ControlKey, ct);
        if (ActiveMaintenance(document, "login") != flow) throw new BridgeFailure("stale_maintenance", "维护许可已失效");
        try { (OpenExternal ?? throw new BridgeFailure("operation_failed", "浏览器未就绪"))(url.AbsoluteUri); }
        catch (BridgeFailure) { throw; }
        catch { throw new BridgeFailure("operation_failed", "无法打开授权地址"); }
        return new JsonObject { ["opened"] = true };
    }

    private async Task<JsonObject> DeleteAccountAsync(JsonObject payload, string document, CancellationToken ct)
    {
        BridgeValidation.Keys(payload, "uid"); var uid = BridgeValidation.UID(payload["uid"]);
        var flow = ActiveMaintenance(document, "delete");
        if (flow.UIDs.Length != 1 || flow.UIDs[0] != uid || PanelCookie is not string cookie)
            throw new BridgeFailure("stale_maintenance", "维护许可与账号不匹配");
        using var client = new PanelClient(flow.PanelPort);
        await client.DeleteAccountAsync(uid, cookie, flow.Instance, flow.Generation, ct);
        if (ActiveMaintenance(document, "delete") != flow) throw new BridgeFailure("stale_maintenance", "维护许可已失效");
        var message = "账号凭证已删除";
        if (RemoveAccountNoteAsync is { } removeNote)
        {
            try { await removeNote(uid, ct); }
            catch { message = "账号凭证已删除，本地备注清理未完成"; _log.Record("app", "app_warning"); }
        }
        return new JsonObject { ["ok"] = true, ["message"] = message };
    }

    private async Task<JsonObject> SetAccountEnabledAsync(JsonObject payload, string document, CancellationToken ct)
    {
        BridgeValidation.Keys(payload, "uid", "enabled"); var uid = BridgeValidation.UID(payload["uid"]);
        var enabled = BridgeValidation.Bool(payload["enabled"]);
        Begin("account.setEnabled");
        try
        {
            var gateway = _gateway; var port = _gatewayPort; var key = _gatewayManagementKey;
            if (gateway?.IsRunning != true || port is null || key is null) throw new BridgeFailure("operation_failed", "API 未运行");
            var result = await ManagementAsync(gateway, port.Value, key, $"admin/accounts/{uid}/{(enabled ? "enable" : "disable")}", new JsonObject(), ct);
            if (_retired.Contains(document) || Text(result["uid"]) != uid || BoolValue(result["manual_disabled"]) != !enabled)
                throw new BridgeFailure("operation_failed", "账号状态回执无效");
            return new JsonObject { ["ok"] = true };
        }
        finally { End("account.setEnabled"); }
    }

    private async Task<JsonObject> ImportCommandAsync(JsonObject payload, string id, string document, CancellationToken ct)
    {
        var operation = Text(payload["operation"]);
        switch (operation)
        {
            case "preview":
                BridgeValidation.Keys(payload, "operation");
                Begin("files.importAuth");
                var attempt = new PendingAttempt(id, document, true); _attempt = attempt; _preview = null;
                using (var linked = CancellationTokenSource.CreateLinkedTokenSource(ct, attempt.Cancel.Token))
                {
                    linked.CancelAfter(TimeSpan.FromSeconds(120));
                    try
                    {
                        var panel = _panel; var port = _panelPort; var cookie = PanelCookie; var instance = InstanceId;
                        var path = await (ChooseAuthFileAsync ?? throw new BridgeFailure("operation_failed", "文件选择器未就绪"))(linked.Token);
                        if (path is null || _retired.Contains(document)) throw new BridgeFailure("cancelled", "已取消");
                        var credential = ReadCredential(path);
                        if (panel?.IsRunning != true || port is null || cookie is null || instance is null) throw new BridgeFailure("operation_failed", "管理面板未就绪");
                        using var client = new PanelClient(port.Value);
                        var existing = await client.ExistingRegionAsync(credential.UID, cookie, linked.Token);
                        if (_panel != panel || _panelPort != port || PanelCookie != cookie || InstanceId != instance || _retired.Contains(document))
                            throw new BridgeFailure("cancelled", "管理连接已变化");
                        if (existing is not null && existing != credential.Region) throw new BridgeFailure("region_conflict", "账号区域冲突");
                        var preview = new ImportPreview(Guid.NewGuid().ToString(), id, document, instance, credential, existing is not null);
                        _preview = preview;
                        _ = ExpirePreviewLaterAsync(preview);
                        return new JsonObject { ["preview_id"] = preview.ID, ["uid"] = credential.UID, ["region"] = credential.Region,
                            ["expires_at"] = credential.ExpiresAt, ["overwrite_required"] = preview.OverwriteRequired };
                    }
                    catch (OperationCanceledException) { throw new BridgeFailure("cancelled", "导入预览已取消"); }
                    finally { attempt.Completed.TrySetResult(); if (_attempt == attempt) _attempt = null; End("files.importAuth"); }
                }
            case "confirm":
                BridgeValidation.Keys(payload, "operation", "preview_id", "overwrite");
                var previewID = BridgeValidation.GuidID(payload["preview_id"]);
                var overwrite = BridgeValidation.Bool(payload["overwrite"]);
                var saved = _preview;
                if (saved is null || saved.ID != previewID || saved.Document != document || saved.Instance != InstanceId || saved.Expires < DateTimeOffset.UtcNow)
                    throw new BridgeFailure("stale_maintenance", "导入预览已过期");
                if (saved.OverwriteRequired && !overwrite) throw new BridgeFailure("overwrite_required", "需确认覆盖凭证");
                _preview = null;
                await BeginMaintenanceAsync("import", [saved.Credential.UID], id, document, true, ct);
                var flow = ActiveMaintenance(document, "import");
                try
                {
                    if (PanelCookie is not string cookie) throw new BridgeFailure("operation_failed", "管理连接已失效");
                    using var client = new PanelClient(flow.PanelPort);
                    await client.ImportCredentialAsync(saved.Credential.Raw, overwrite, cookie, flow.Instance, flow.Generation, ct);
                    if (ActiveMaintenance(document, "import") != flow) throw new BridgeFailure("stale_maintenance", "维护许可已失效");
                    if (!await FinishMaintenanceAsync(flow)) throw new BridgeFailure("operation_failed", "导入后清理失败");
                    return new JsonObject { ["ok"] = true };
                }
                catch { await FinishMaintenanceAsync(flow); throw; }
            case "cancel":
                BridgeValidation.Keys(payload, "operation", "preview_id");
                var cancelID = BridgeValidation.GuidID(payload["preview_id"]);
                if (_preview is null || _preview.ID != cancelID || _preview.Document != document)
                    throw new BridgeFailure("stale_maintenance", "导入预览已过期");
                _preview = null; return new JsonObject { ["cancelled"] = true };
            case "cancel_pending":
                BridgeValidation.Keys(payload, "operation", "request_id");
                return await CancelAttemptAsync(BridgeValidation.RequestID(payload["request_id"]), document, true);
            default: throw BridgeValidation.Invalid();
        }
    }

    private async Task ExpirePreviewLaterAsync(ImportPreview preview)
    {
        await Task.Delay(preview.Expires - DateTimeOffset.UtcNow);
        await InvokeUIAsync(() => { if (_preview == preview) _preview = null; return Task.CompletedTask; });
    }

    private static Credential ReadCredential(string path)
    {
        try
        {
            using var handle = CreateFileW(path, 0x80000000, 1, IntPtr.Zero, 3, 0x00200000, IntPtr.Zero);
            if (handle.IsInvalid || !GetFileInformationByHandle(handle, out var info) ||
                (info.Attributes & (uint)(FileAttributes.ReparsePoint | FileAttributes.Directory)) != 0 ||
                info.NumberOfLinks != 1 || info.FileSizeHigh != 0 || info.FileSizeLow is 0 or > 1048576)
                throw new BridgeFailure("invalid_file", "凭证文件无效");
            using var stream = new FileStream(handle, FileAccess.Read);
            using var memory = new MemoryStream(); var buffer = new byte[8192];
            while (true)
            {
                var count = stream.Read(buffer, 0, buffer.Length); if (count == 0) break;
                if (memory.Length + count > 1048576) throw new BridgeFailure("invalid_file", "凭证文件过大");
                memory.Write(buffer, 0, count);
            }
            var raw = memory.ToArray();
            var json = JsonNode.Parse(new UTF8Encoding(false, true).GetString(raw)) as JsonObject ?? throw new Exception();
            var auth = json.ContainsKey("auth") ? json["auth"] as JsonObject ?? throw new Exception() : json;
            var account = json.ContainsKey("auth") ? json["account"] as JsonObject ?? throw new Exception() : json;
            var uid = BridgeValidation.UID(account["uid"]);
            if (BridgeValidation.Text(auth["accessToken"], 1, 1_048_576).Trim().Length == 0) throw new Exception();
            if (auth.ContainsKey("refreshToken")) BridgeValidation.Text(auth["refreshToken"], 0, 1_048_576);
            var domain = auth.ContainsKey("domain") ? BridgeValidation.Text(auth["domain"], 0, 255) : "";
            var region = PanelClient.Region(domain);
            long expires = 0;
            if (auth.ContainsKey("expiresAt"))
            {
                var number = auth["expiresAt"]?.ToJsonString() ?? throw new Exception();
                if (!long.TryParse(number, NumberStyles.AllowLeadingSign, CultureInfo.InvariantCulture, out expires) ||
                    number != expires.ToString(CultureInfo.InvariantCulture)) throw new Exception();
            }
            return new Credential(raw, uid, region, expires);
        }
        catch (BridgeFailure) { throw; }
        catch { throw new BridgeFailure("invalid_file", "凭证文件无效"); }
    }

    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true, EntryPoint = "CreateFileW")]
    private static extern SafeFileHandle CreateFileW(string name, uint access, uint share, IntPtr security, uint disposition, uint flags, IntPtr template);

    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool GetFileInformationByHandle(SafeFileHandle file, out ByHandleInformation information);

    [StructLayout(LayoutKind.Sequential)]
    private struct ByHandleInformation
    {
        internal uint Attributes, CreationTimeLow, CreationTimeHigh, LastAccessTimeLow, LastAccessTimeHigh,
            LastWriteTimeLow, LastWriteTimeHigh, VolumeSerialNumber, FileSizeHigh, FileSizeLow,
            NumberOfLinks, FileIndexHigh, FileIndexLow;
    }

    internal async Task RetireDocumentAsync(string documentId)
    {
        _retired.Add(documentId);
        if (_proxy?.Document == documentId) _proxy.Cancel.Cancel();
        if (_preview?.Document == documentId) _preview = null;
        if (_attempt?.Document == documentId) _attempt.Cancel.Cancel();
        if (_service?.Document == documentId && _service is { } service)
        {
            service.Cancel.Cancel();
            if (service.Work is not null) { try { await service.Work; } catch { } }
        }
        if (_maintenance?.Document == documentId) await FinishMaintenanceAsync(_maintenance);
    }

    internal async Task HandleLifecycleAsync(bool sleeping)
    {
        if (_sleeping == sleeping) return;
        _sleeping = sleeping; _lifecycleEpoch++;
        if (sleeping) { _proxy?.Cancel.Cancel(); _healthPoll?.Cancel(); }
        else if (_gateway?.IsRunning == true && _gatewayPort is int port) StartHealthPolling(_gateway, port);
        LifecycleChanged?.Invoke(sleeping ? "sleep" : "wake", _lifecycleEpoch.ToString(CultureInfo.InvariantCulture));
        Publish(); await Task.CompletedTask;
    }

    internal async Task ShutdownAsync()
    {
        if (_shutdownComplete) return;
        IsShuttingDown = true; _proxy?.Cancel.Cancel(); _service?.Cancel.Cancel(); _attempt?.Cancel.Cancel(); _preview = null;
        _healthPoll?.Cancel();
        if (_maintenance is { } flow) flow.Cancel.Cancel();
        var gateway = _gateway; var panel = _panel;
        using var deadline = new CancellationTokenSource(TimeSpan.FromSeconds(20));
        if (gateway is not null) { try { await gateway.StopAsync(deadline.Token); } catch { } }
        if (panel is not null) { try { await panel.StopAsync(deadline.Token); } catch { } }
        if (gateway?.IsRunning == true || panel?.IsRunning == true)
            throw new BridgeFailure("stop_failed", "未能确认内置服务已经退出");
        _gateway = null; _panel = null; _gatewayPort = null; _panelPort = null; _gatewayManagementKey = null;
        _panelSecret = null; _panelControlKey = null; PanelCookie = null; _client?.Dispose(); _client = null;
        _gatewayState = "stopped"; _panelState = "未启动"; Publish(); PanelChanged?.Invoke();
        if (gateway is not null) await gateway.DisposeAsync();
        if (panel is not null) await panel.DisposeAsync();
        _shutdownComplete = true;
    }

    public async ValueTask DisposeAsync()
    {
        await ShutdownAsync();
        _healthPoll?.Dispose();
    }

    private sealed class ServiceFlow(string id, string action, string document, string instance, GatewaySnapshot old, ManagedHelper panel, int panelPort,
        string cookie, string controlKey, ManagedHelper? gateway, int? gatewayPort, string? gatewayKey, bool wasRunning)
    {
        internal readonly string ID = id, Action = action, Document = document, Instance = instance;
        internal readonly GatewaySnapshot Old = old;
        internal readonly ManagedHelper Panel = panel;
        internal readonly int PanelPort = panelPort;
        internal readonly string Cookie = cookie, ControlKey = controlKey;
        internal readonly ManagedHelper? Gateway = gateway;
        internal readonly int? GatewayPort = gatewayPort;
        internal readonly string? GatewayKey = gatewayKey;
        internal readonly bool WasRunning = wasRunning;
        internal readonly DateTimeOffset Expires = DateTimeOffset.UtcNow.AddSeconds(900);
        internal readonly CancellationTokenSource Cancel = new();
        internal ulong Generation;
        internal string Phase = "confirming";
        internal PreparedConfig? Prepared;
        internal ManagedHelper? Candidate;
        internal Task<JsonObject>? Work;
        internal bool PrepareUncertain, CommitStarted, CommitUncertain, FinishStarted, FinishAcknowledged;
        internal bool DrainRequested, GatewayStopped, RestoredOld;
    }

    private sealed class PendingAttempt(string id, string document, bool importing)
    {
        internal readonly string ID = id, Document = document;
        internal readonly bool Importing = importing;
        internal readonly CancellationTokenSource Cancel = new();
        internal readonly TaskCompletionSource Completed = new(TaskCreationOptions.RunContinuationsAsynchronously);
    }

    private sealed class MaintenanceFlow(string document, PendingAttempt attempt, string action, string[] uids, string instance,
        ManagedHelper panel, int panelPort, string controlKey, ManagedHelper? gateway, int? gatewayPort, string? gatewayKey,
        bool wasRunning, ulong generation)
    {
        internal readonly string Document = document, Action = action, Instance = instance, ControlKey = controlKey;
        internal readonly string[] UIDs = uids;
        internal readonly PendingAttempt Attempt = attempt;
        internal readonly ManagedHelper Panel = panel;
        internal readonly int PanelPort = panelPort;
        internal readonly ManagedHelper? Gateway = gateway;
        internal readonly int? GatewayPort = gatewayPort;
        internal readonly string? GatewayKey = gatewayKey;
        internal readonly bool WasRunning = wasRunning;
        internal readonly ulong Generation = generation;
        internal readonly DateTimeOffset Expires = DateTimeOffset.UtcNow.AddSeconds(900);
        internal readonly CancellationTokenSource Cancel = new();
        internal string Phase = "confirming";
        internal bool DrainRequested, GatewayStopped, PermitRequested;
        internal Task<bool>? Preparation, Cleanup;
        internal Task? Expiry;
    }

    private sealed class ProxyAttempt(string id, string document, string revision)
    {
        internal readonly string ID = id, Document = document, Revision = revision;
        internal readonly CancellationTokenSource Cancel = new();
    }

    private sealed record Credential(byte[] Raw, string UID, string Region, long ExpiresAt);
    private sealed class ImportPreview(string id, string requestId, string document, string instance, Credential credential, bool overwrite)
    {
        internal readonly string ID = id, RequestID = requestId, Document = document, Instance = instance;
        internal readonly Credential Credential = credential;
        internal readonly bool OverwriteRequired = overwrite;
        internal readonly DateTimeOffset Expires = DateTimeOffset.UtcNow.AddSeconds(120);
    }
}
