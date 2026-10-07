using System;
using System.Collections.Generic;
using System.ComponentModel;
using System.Drawing;
using System.IO;
using System.Linq;
using System.Runtime.InteropServices;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Threading;
using System.Threading.Tasks;
using System.Windows.Forms;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.WinForms;
using Microsoft.Win32;

namespace WBPMM;

internal sealed class MainForm : Form
{
    private static readonly HashSet<string> UtilityActions =
    ["logs.tail", "preferences.get", "preferences.update", "diagnostics.export", "desktop.openDirectory", "logs.clearArchives"];
    private static readonly HashSet<string> CoreActions =
    ["desktop.checkReadiness", "desktop.checkProxy", "desktop.getStatus", "desktop.getBridgeInfo", "gateway.start",
     "gateway.stop", "gateway.restart", "desktop.reconnect", "stats.reset", "account.maintenance", "auth.openBrowser",
     "files.importAuth", "account.setEnabled", "account.delete", "gateway.applyConfig", "api.rotateKey", "api.copyKey"];
    private readonly DesktopController controller;
    private readonly NativeUtilities utilities;
    private readonly WebView2 web = new() { Dock = DockStyle.Fill, Visible = false };
    private readonly Panel failure = new() { Dock = DockStyle.Fill, Visible = false };
    private readonly Label failureMessage = new() { AutoSize = false, TextAlign = ContentAlignment.MiddleCenter, Dock = DockStyle.Fill };
    private readonly Label status = new() { AutoSize = true, Text = "正在启动本机服务…", Padding = new Padding(8, 4, 8, 4) };
    private readonly Button start = new() { Text = "启动 API", AutoSize = true };
    private readonly Button stop = new() { Text = "停止 API", AutoSize = true };
    private readonly Button restart = new() { Text = "重启 API", AutoSize = true };
    private readonly NotifyIcon tray = new();
    private readonly CancellationTokenSource lifetime = new();
    private readonly Dictionary<string, CancellationTokenSource> pending = new(StringComparer.Ordinal);
    private string? documentId;
    private string? injectedScriptId;
    private string? loadedOrigin;
    private string? loadedCookie;
    private string? approvedNavigation;
    private ulong? activeNavigationId;
    private string? profileDirectory;
    private bool initialized;
    private bool connecting;
    private bool controllerReady;
    private bool exiting;
    private bool loading;
    private bool closeNoticeShown;
    private ulong eventEpoch;

    [DesignerSerializationVisibility(DesignerSerializationVisibility.Hidden)]
    public bool StartInBackground { get; set; }

    public MainForm(DesktopController controller, NativeUtilities utilities)
    {
        this.controller = controller;
        this.utilities = utilities;
        Text = "WB-PMM";
        MinimumSize = new Size(780, 560);
        Size = new Size(1160, 780);
        StartPosition = FormStartPosition.CenterScreen;
        TryLoadBrandIcon();
        BuildLayout();
        BuildTray();
        WireController();
        Shown += async (_, _) => await InitializeAsync();
        FormClosing += OnFormClosing;
        FormClosed += (_, _) => CleanUpForm();
        SystemEvents.PowerModeChanged += OnPowerModeChanged;
    }

    public void ShowMainWindow()
    {
        if (InvokeRequired) { BeginInvoke(new Action(ShowMainWindow)); return; }
        if (IsDisposed) return;
        Show();
        WindowState = FormWindowState.Normal;
        ShowInTaskbar = true;
        Activate();
    }

    public Task RequestExitAsync()
    {
        if (InvokeRequired)
        {
            var completion = new TaskCompletionSource<bool>(TaskCreationOptions.RunContinuationsAsynchronously);
            BeginInvoke((Action)(async () =>
            {
                try { await ExitAsync(throwOnFailure: true); completion.TrySetResult(true); }
                catch (Exception error) { completion.TrySetException(error); }
            }));
            return completion.Task;
        }
        return ExitAsync(throwOnFailure: true);
    }

    private void BuildLayout()
    {
        var header = new FlowLayoutPanel
        {
            Dock = DockStyle.Top, Height = 47, FlowDirection = FlowDirection.LeftToRight,
            WrapContents = false, Padding = new Padding(8, 7, 8, 5)
        };
        var name = new Label { Text = "WB-PMM", Font = new Font(Font, FontStyle.Bold), AutoSize = true,
            Padding = new Padding(3, 5, 12, 0) };
        header.Controls.Add(name);
        header.Controls.Add(status);
        header.Controls.Add(start);
        header.Controls.Add(stop);
        header.Controls.Add(restart);
        start.Click += async (_, _) => await ExecuteHeaderActionAsync("gateway.start");
        stop.Click += async (_, _) => await ExecuteHeaderActionAsync("gateway.stop");
        restart.Click += async (_, _) => await ExecuteHeaderActionAsync("gateway.restart");
        var center = new Panel { Dock = DockStyle.Fill };
        var failureContent = new TableLayoutPanel { Dock = DockStyle.Fill, RowCount = 3, ColumnCount = 1,
            Padding = new Padding(32) };
        failureContent.RowStyles.Add(new RowStyle(SizeType.Percent, 42));
        failureContent.RowStyles.Add(new RowStyle(SizeType.Absolute, 72));
        failureContent.RowStyles.Add(new RowStyle(SizeType.Percent, 58));
        var retry = new Button { Text = "重试连接", AutoSize = true, Anchor = AnchorStyles.None };
        retry.Click += async (_, _) => await RetryAsync();
        var quit = new Button { Text = "完全退出", AutoSize = true };
        quit.Click += async (_, _) => await ExitAsync();
        var actions = new FlowLayoutPanel { AutoSize = true, Anchor = AnchorStyles.None, WrapContents = false };
        actions.Controls.Add(retry);
        actions.Controls.Add(quit);
        var logs = new Button { Text = "打开应用日志", AutoSize = true };
        logs.Click += (_, _) => utilities.OpenLogDirectory();
        actions.Controls.Add(logs);
        var copyError = new Button { Text = "复制诊断信息", AutoSize = true };
        copyError.Click += (_, _) =>
        {
            var detail = utilities.StartupDiagnosticText();
            try { Clipboard.SetText(detail); copyError.Text = "已复制诊断"; }
            catch { MessageBox.Show(this, detail, "诊断信息（可截图）"); }
        };
        actions.Controls.Add(copyError);
        failureMessage.Font = new Font(Font.FontFamily, 11, FontStyle.Regular);
        failureContent.Controls.Add(failureMessage, 0, 0);
        failureContent.Controls.Add(actions, 0, 1);
        failure.Controls.Add(failureContent);
        center.Controls.Add(web);
        center.Controls.Add(failure);
        Controls.Add(center);
        Controls.Add(header);
        SetServiceButtons(false);
        ShowFailure("正在连接本机管理面板…");
    }

    private void BuildTray()
    {
        var menu = new ContextMenuStrip();
        menu.Items.Add("打开 WB-PMM", null, (_, _) => ShowMainWindow());
        menu.Items.Add(new ToolStripSeparator());
        menu.Items.Add("启动 API", null, async (_, _) => await ExecuteHeaderActionAsync("gateway.start"));
        menu.Items.Add("停止 API", null, async (_, _) => await ExecuteHeaderActionAsync("gateway.stop"));
        menu.Items.Add("重启 API", null, async (_, _) => await ExecuteHeaderActionAsync("gateway.restart"));
        menu.Items.Add("复制本机 API 地址", null, (_, _) => CopyApiAddress());
        menu.Items.Add(new ToolStripSeparator());
        menu.Items.Add("完全退出", null, async (_, _) => await ExitAsync());
        tray.Icon = Icon ?? SystemIcons.Application;
        tray.Text = "WB-PMM 本机 API";
        tray.ContextMenuStrip = menu;
        tray.Visible = true;
        tray.DoubleClick += (_, _) => ShowMainWindow();
    }

    private void WireController()
    {
        controller.ConfirmAsync = ConfirmAsync;
        controller.ChooseAuthFileAsync = ChooseAuthFileAsync;
        controller.ChooseStopModeAsync = ChooseStopModeAsync;
        controller.CopyText = value => Clipboard.SetText(value);
        controller.OpenExternal = OpenApprovedExternal;
        controller.RemoveAccountNoteAsync = utilities.RemoveAccountNoteAsync;
        controller.StatusChanged += OnStatusChanged;
        controller.PanelChanged += OnPanelChanged;
        controller.LifecycleChanged += OnLifecycleChanged;
        utilities.ConfirmAsync = ConfirmAsync;
        utilities.ChooseSaveFileAsync = ChooseSaveFileAsync;
        utilities.StatusProvider = controller.GetStatus;
    }

    private async Task InitializeAsync()
    {
        if (initialized) return;
        initialized = true;
        if (StartInBackground) { Hide(); ShowInTaskbar = false; }
        connecting = true;
        var stage = "WebView2";
        try
        {
            await EnsureWebViewAsync();
            stage = "本机服务";
            await controller.InitializeAsync(StartInBackground ? utilities.LaunchAtLogin : utilities.AutoStartApi, lifetime.Token);
            controllerReady = true;
            RefreshStatus();
            await LoadPanelAsync(force: true);
            RefreshStatus();
        }
        catch (OperationCanceledException) when (lifetime.IsCancellationRequested) { }
        catch (Exception error) { ShowStartupFailure(stage, error); }
        finally { connecting = false; }
    }

    private void ShowStartupFailure(string stage, Exception error)
    {
        utilities.RecordStartupFailure(stage, error);
        status.Text = stage + "启动失败";
        SetServiceButtons(false);
        ShowFailure($"{stage}启动失败（0x{error.HResult:X8}）。\n请点击「复制诊断信息」并发送给我们；也可以直接截图错误码。");
    }

    private async Task EnsureWebViewAsync()
    {
        if (web.CoreWebView2 is not null) return;
        var root = utilities.Paths.Run;
        Directory.CreateDirectory(root);
        profileDirectory = Path.Combine(root, "webview-" + Guid.NewGuid().ToString("N"));
        var env = await CoreWebView2Environment.CreateAsync(userDataFolder: profileDirectory);
        await web.EnsureCoreWebView2Async(env);
        var core = web.CoreWebView2 ?? throw new BridgeFailure("webview_unavailable", "WebView2 初始化失败");
        core.Settings.AreDevToolsEnabled = false;
        core.Settings.AreDefaultContextMenusEnabled = false;
        core.Settings.AreHostObjectsAllowed = false;
        core.Settings.IsStatusBarEnabled = false;
        core.Settings.IsWebMessageEnabled = true;
        core.NavigationStarting += OnNavigationStarting;
        core.NavigationCompleted += OnNavigationCompleted;
        core.FrameNavigationStarting += (_, e) => e.Cancel = true;
        core.NewWindowRequested += (_, e) => e.Handled = true;
        core.DownloadStarting += (_, e) => e.Cancel = true;
        core.WebMessageReceived += OnWebMessageReceived;
        core.ProcessFailed += (_, _) => BeginInvoke((Action)(async () => { await RetireDocumentAsync(); ShowFailure("管理面板进程已退出，请重试连接。"); }));
        core.AddWebResourceRequestedFilter("*", CoreWebView2WebResourceContext.All);
        core.WebResourceRequested += (_, e) =>
        {
            if (AllowedPanelUrl(e.Request.Uri)) return;
            e.Response = core.Environment.CreateWebResourceResponse(null, 403, "Forbidden", "Content-Type: text/plain");
        };
    }

    private async Task LoadPanelAsync(bool force = false)
    {
        if (loading || web.CoreWebView2 is null) return;
        var origin = controller.PanelOrigin;
        var cookie = controller.PanelCookie;
        if (origin is null || cookie is null || !ValidOrigin(origin))
        {
            await RetireDocumentAsync();
            ShowFailure("本机管理面板尚未就绪，请重试连接。");
            return;
        }
        if (!force && origin == loadedOrigin && cookie == loadedCookie && documentId is not null) return;
        loading = true;
        try
        {
            await RetireDocumentAsync();
            if (injectedScriptId is not null)
            {
                web.CoreWebView2.RemoveScriptToExecuteOnDocumentCreated(injectedScriptId);
                injectedScriptId = null;
            }
            var id = Guid.NewGuid().ToString("D");
            var script = "(() => { if (window.top !== window) return; const id = " + JsonSerializer.Serialize(id) + ";" +
                "Object.defineProperty(window, '__workbuddyDocumentID', {value:id, writable:false, configurable:false});" +
                "Object.defineProperty(window, 'webkit', {value:{messageHandlers:{workbuddyNative:{postMessage(body){window.chrome.webview.postMessage(body)}}}}, configurable:false});" +
                "window.chrome.webview.addEventListener('message', event => window.__workbuddyBridgeReceive?.(event.data));" +
                "})();";
            injectedScriptId = await web.CoreWebView2.AddScriptToExecuteOnDocumentCreatedAsync(script);
            var manager = web.CoreWebView2.CookieManager;
            var session = manager.CreateCookie("wbgui_session", cookie, "127.0.0.1", "/");
            session.IsHttpOnly = true;
            session.IsSecure = false;
            session.SameSite = CoreWebView2CookieSameSiteKind.Strict;
            manager.AddOrUpdateCookie(session);
            documentId = id;
            utilities.SetActiveDocument(id);
            loadedOrigin = origin;
            loadedCookie = cookie;
            approvedNavigation = origin + "/";
            web.CoreWebView2.Navigate(approvedNavigation);
        }
        catch (OperationCanceledException) when (lifetime.IsCancellationRequested) { }
        catch { ShowFailure("管理面板无法载入。请重试连接。"); }
        finally { loading = false; }
    }

    private void OnNavigationStarting(object? sender, CoreWebView2NavigationStartingEventArgs e)
    {
        if (approvedNavigation == e.Uri)
        {
            approvedNavigation = null;
            activeNavigationId = e.NavigationId;
            return;
        }
        e.Cancel = true;
        if (!AllowedPanelUrl(e.Uri)) return;
        BeginInvoke((Action)(async () => await LoadPanelAsync(force: true)));
    }

    private async void OnNavigationCompleted(object? sender, CoreWebView2NavigationCompletedEventArgs e)
    {
        if (activeNavigationId != e.NavigationId) return;
        activeNavigationId = null;
        if (!e.IsSuccess)
        {
            await RetireDocumentAsync();
            ShowFailure("管理面板连接失败，请重试连接。");
            return;
        }
        failure.Visible = false;
        web.Visible = true;
        web.BringToFront();
    }

    private async void OnWebMessageReceived(object? sender, CoreWebView2WebMessageReceivedEventArgs e)
    {
        if (documentId is null || !AllowedPanelUrl(e.Source)) return;
        string raw;
        try { raw = e.WebMessageAsJson; }
        catch { return; }
        if (Encoding.UTF8.GetByteCount(raw) > 64 * 1024) return;
        JsonObject request;
        try { request = JsonNode.Parse(raw) as JsonObject ?? throw new JsonException(); }
        catch { return; }
        if (request.Count != 4 || !request.ContainsKey("id") || !request.ContainsKey("action") ||
            !request.ContainsKey("payload") || !request.ContainsKey("document_id")) return;
        var id = StringValue(request["id"]);
        var action = StringValue(request["action"]);
        var owner = StringValue(request["document_id"]);
        if (id is null || id.Length is < 12 or > 96 || !id.StartsWith("desktop-", StringComparison.Ordinal) ||
            !id.Skip(8).All(c => char.IsAsciiDigit(c) || c == '-') || action is null || owner != documentId ||
            request["payload"] is not JsonObject payload || !CoreActions.Contains(action) && !UtilityActions.Contains(action)) return;
        if (pending.Count >= 64 || pending.ContainsKey(id)) return;
        var op = CancellationTokenSource.CreateLinkedTokenSource(lifetime.Token);
        pending[id] = op;
        try
        {
            JsonObject data = UtilityActions.Contains(action)
                ? await utilities.ExecuteAsync(action, payload, op.Token)
                : await controller.ExecuteAsync(action, payload, id, owner, op.Token);
            Reply(id, owner, true, data, null, null);
        }
        catch (BridgeFailure ex) { Reply(id, owner, false, null, ex.Code, ex.Message); }
        catch (OperationCanceledException) { Reply(id, owner, false, null, "cancelled", "操作已取消"); }
        catch { Reply(id, owner, false, null, "operation_failed", "原生操作失败，请刷新状态后重试"); }
        finally
        {
            pending.Remove(id);
            op.Dispose();
        }
    }

    private void Reply(string id, string owner, bool ok, JsonObject? data, string? code, string? message)
    {
        if (owner != documentId || web.CoreWebView2 is null) return;
        var response = new JsonObject { ["id"] = id, ["ok"] = ok };
        if (ok) response["data"] = data?.DeepClone() ?? new JsonObject();
        else response["error"] = new JsonObject { ["code"] = code, ["message"] = message };
        web.CoreWebView2.PostWebMessageAsJson(response.ToJsonString());
    }

    private async Task RetireDocumentAsync()
    {
        var old = documentId;
        documentId = null;
        activeNavigationId = null;
        loadedOrigin = null;
        loadedCookie = null;
        if (old is null) return;
        utilities.RetireDocument(old);
        foreach (var token in pending.Values.ToArray()) token.Cancel();
        try { await controller.RetireDocumentAsync(old); }
        catch { /* Controller retains its uncertain cleanup lock. */ }
    }

    private async Task RetryAsync()
    {
        if (exiting || connecting) return;
        connecting = true;
        var stage = "WebView2";
        SetServiceButtons(false);
        ShowFailure("正在重新连接本机管理面板…");
        try
        {
            if (web.CoreWebView2 is null) await EnsureWebViewAsync();
            stage = "本机服务";
            if (!controllerReady)
            {
                await controller.InitializeAsync(StartInBackground ? utilities.LaunchAtLogin : utilities.AutoStartApi, lifetime.Token);
                controllerReady = true;
            }
            else if (controller.PanelOrigin is null)
                await controller.ExecuteAsync("desktop.reconnect", new JsonObject(), "native-" + Guid.NewGuid().ToString("N"),
                    documentId ?? Guid.NewGuid().ToString("D"), lifetime.Token);
            await LoadPanelAsync(force: true);
            RefreshStatus();
        }
        catch (OperationCanceledException) when (lifetime.IsCancellationRequested) { }
        catch (Exception error) { ShowStartupFailure(stage, error); }
        finally { connecting = false; }
    }

    private async Task ExecuteHeaderActionAsync(string action)
    {
        if (exiting || documentId is null) { ShowMainWindow(); return; }
        SetServiceButtons(false);
        try
        {
            await controller.ExecuteAsync(action, new JsonObject(), "native-" + Guid.NewGuid().ToString("N"),
                documentId, lifetime.Token);
        }
        catch (BridgeFailure ex) { MessageBox.Show(this, ex.Message, "服务操作", MessageBoxButtons.OK, MessageBoxIcon.Warning); }
        catch { MessageBox.Show(this, "服务操作失败，请刷新状态后重试。", "服务操作", MessageBoxButtons.OK, MessageBoxIcon.Warning); }
        finally { RefreshStatus(); }
    }

    private void OnStatusChanged()
    {
        if (InvokeRequired) { BeginInvoke(new Action(OnStatusChanged)); return; }
        RefreshStatus();
        SendEvent("workbuddy:gateway-status", new JsonObject());
    }
    private void OnPanelChanged()
    {
        if (InvokeRequired) { BeginInvoke(new Action(OnPanelChanged)); return; }
        _ = LoadPanelAsync();
    }
    private void OnLifecycleChanged(string kind, string epoch)
    {
        if (InvokeRequired) { BeginInvoke((Action)(() => OnLifecycleChanged(kind, epoch))); return; }
        if (kind is not ("sleep" or "wake") || !ulong.TryParse(epoch, out _)) return;
        SendEvent("workbuddy:lifecycle", new JsonObject { ["kind"] = kind });
    }
    private void SendEvent(string name, JsonObject detail)
    {
        if (documentId is null || web.CoreWebView2 is null) return;
        detail["document_id"] = documentId;
        detail["epoch"] = (++eventEpoch).ToString();
        var id = JsonSerializer.Serialize(documentId);
        var script = "if(window.__workbuddyDocumentID===" + id + ")window.dispatchEvent(new CustomEvent(" +
            JsonSerializer.Serialize(name) + ",{detail:" + detail.ToJsonString() + "}));";
        _ = web.CoreWebView2.ExecuteScriptAsync(script);
    }
    private void RefreshStatus()
    {
        JsonObject state;
        try { state = controller.GetStatus(); }
        catch { status.Text = "状态不可用"; SetServiceButtons(false); return; }
        var gateway = StringValue(state["gateway_state"]) ?? "stopped";
        status.Text = gateway switch
        {
            "running_ready" => "API 运行中", "running_unavailable" => "API 运行中 · 暂无可用账号",
            "starting" => "API 启动中", "stopping" => "API 停止中", "maintenance" => "API 维护中",
            "failed" => "API 故障", _ => "API 已停止"
        };
        var busy = gateway is "starting" or "stopping" or "maintenance";
        start.Enabled = !busy && gateway is ("stopped" or "failed");
        stop.Enabled = !busy && gateway is ("running_ready" or "running_unavailable");
        restart.Enabled = stop.Enabled;
    }
    private void SetServiceButtons(bool enabled) { start.Enabled = enabled; stop.Enabled = enabled; restart.Enabled = enabled; }
    private void CopyApiAddress()
    {
        try
        {
            var value = StringValue(controller.GetStatus()["api_base_url"]);
            if (!string.IsNullOrEmpty(value)) Clipboard.SetText(value);
        }
        catch { }
    }
    private void ShowFailure(string text)
    {
        failureMessage.Text = text;
        failure.Visible = true;
        web.Visible = false;
        failure.BringToFront();
    }
    private static bool ValidOrigin(string value) => Uri.TryCreate(value, UriKind.Absolute, out var uri) &&
        uri.Scheme == Uri.UriSchemeHttp && uri.Host == "127.0.0.1" && uri.Port is >= 1024 and <= 65535 &&
        uri.AbsolutePath == "/" && uri.Query.Length == 0 && uri.Fragment.Length == 0 && uri.UserInfo.Length == 0 &&
        value == $"http://127.0.0.1:{uri.Port}";
    private bool AllowedPanelUrl(string value)
    {
        if (!Uri.TryCreate(value, UriKind.Absolute, out var uri) || controller.PanelOrigin is not { } origin ||
            !ValidOrigin(origin)) return false;
        return uri.Scheme == Uri.UriSchemeHttp && uri.Host == "127.0.0.1" &&
            uri.Port == new Uri(origin).Port && uri.UserInfo.Length == 0;
    }
    private static string? StringValue(JsonNode? value) { try { return value?.GetValue<string>(); } catch { return null; } }

    private async Task<bool> ConfirmAsync(string title, string text, CancellationToken ct)
    {
        var answer = await ShowDecisionAsync(title, text, ["取消", "确认"], ct);
        return answer == 1;
    }
    private async Task<string> ChooseStopModeAsync(string title, string text, CancellationToken ct)
    {
        var answer = await ShowDecisionAsync(title, text, ["取消", "等待完成", "中止请求"], ct);
        return answer switch { 1 => "wait", 2 => "abort", _ => "cancel" };
    }
    private async Task<int> ShowDecisionAsync(string title, string text, string[] options, CancellationToken ct)
    {
        ct.ThrowIfCancellationRequested();
        var completion = new TaskCompletionSource<int>(TaskCreationOptions.RunContinuationsAsynchronously);
        using var dialog = new Form { Text = title, Width = 460, Height = 210, StartPosition = FormStartPosition.CenterParent,
            MinimizeBox = false, MaximizeBox = false, FormBorderStyle = FormBorderStyle.FixedDialog, ShowInTaskbar = false };
        var label = new Label { Text = text, Dock = DockStyle.Fill, Padding = new Padding(18), AutoEllipsis = true };
        var buttons = new FlowLayoutPanel { Dock = DockStyle.Bottom, Height = 50, FlowDirection = FlowDirection.RightToLeft };
        for (var i = options.Length - 1; i >= 0; i--)
        {
            var value = i;
            var button = new Button { Text = options[i], AutoSize = true };
            button.Click += (_, _) => { completion.TrySetResult(value); dialog.Close(); };
            buttons.Controls.Add(button);
        }
        dialog.Controls.Add(label); dialog.Controls.Add(buttons);
        dialog.FormClosed += (_, _) => completion.TrySetResult(0);
        dialog.Shown += (_, _) => { if (ct.IsCancellationRequested) dialog.Close(); };
        using var cancellation = ct.Register(() =>
        {
            completion.TrySetCanceled(ct);
            if (!dialog.IsDisposed && dialog.IsHandleCreated) dialog.BeginInvoke(dialog.Close);
        });
        if (ct.IsCancellationRequested) ct.ThrowIfCancellationRequested();
        dialog.ShowDialog(this);
        return await completion.Task;
    }
    private Task<string?> ChooseAuthFileAsync(CancellationToken ct)
    {
        ct.ThrowIfCancellationRequested();
        const string title = "选择账号授权 JSON 文件";
        using var dialog = new OpenFileDialog { Title = title, Filter = "JSON 文件 (*.json)|*.json",
            CheckFileExists = true, Multiselect = false, RestoreDirectory = true };
        using var cancellation = CancelFileDialogOnRequest(GetCurrentThreadId(), Handle, title, ct);
        var answer = dialog.ShowDialog(this) == DialogResult.OK ? dialog.FileName : null;
        ct.ThrowIfCancellationRequested();
        return Task.FromResult(answer);
    }
    private Task<string?> ChooseSaveFileAsync(string name, CancellationToken ct)
    {
        ct.ThrowIfCancellationRequested();
        const string title = "保存安全诊断";
        using var dialog = new SaveFileDialog { Title = title, FileName = name,
            Filter = "JSON 文件 (*.json)|*.json", DefaultExt = "json", AddExtension = true,
            OverwritePrompt = true, RestoreDirectory = true };
        using var cancellation = CancelFileDialogOnRequest(GetCurrentThreadId(), Handle, title, ct);
        var answer = dialog.ShowDialog(this) == DialogResult.OK ? dialog.FileName : null;
        ct.ThrowIfCancellationRequested();
        return Task.FromResult(answer);
    }
    private static IDisposable CancelFileDialogOnRequest(uint uiThread, IntPtr owner, string title, CancellationToken ct)
    {
        var timer = new System.Threading.Timer(_ =>
        {
            EnumThreadWindows(uiThread, (window, _) =>
            {
                var caption = new StringBuilder(256);
                var className = new StringBuilder(64);
                GetWindowText(window, caption, caption.Capacity);
                GetClassName(window, className, className.Capacity);
                var thread = GetWindowThreadProcessId(window, out var process);
                if (thread == uiThread && process == Environment.ProcessId && GetWindow(window, 4) == owner &&
                    className.ToString() == "#32770" && caption.ToString() == title)
                    PostMessage(window, 0x0010, IntPtr.Zero, IntPtr.Zero);
                return true;
            }, IntPtr.Zero);
        }, null, Timeout.Infinite, Timeout.Infinite);
        var registration = ct.Register(() => timer.Change(0, 100));
        return new DialogCancellation(registration, timer);
    }
    private sealed class DialogCancellation(CancellationTokenRegistration registration, System.Threading.Timer timer) : IDisposable
    {
        public void Dispose() { registration.Dispose(); timer.Dispose(); }
    }
    private static void OpenApprovedExternal(string value)
    {
        // DesktopController validates the OAuth host before this hook.
        if (!Uri.TryCreate(value, UriKind.Absolute, out var uri) || uri.Scheme != Uri.UriSchemeHttps ||
            uri.Port != 443 || uri.UserInfo.Length != 0 || uri.Host is not ("copilot.tencent.com" or "www.workbuddy.ai"))
            throw new BridgeFailure("invalid_authorization_url", "授权地址未通过原生校验");
        System.Diagnostics.Process.Start(new System.Diagnostics.ProcessStartInfo(uri.AbsoluteUri) { UseShellExecute = true });
    }
    private async void OnPowerModeChanged(object? sender, PowerModeChangedEventArgs e)
    {
        if (e.Mode is not (PowerModes.Suspend or PowerModes.Resume)) return;
        if (InvokeRequired)
        {
            if (!IsDisposed && IsHandleCreated) BeginInvoke((Action)(() => OnPowerModeChanged(sender, e)));
            return;
        }
        try { await controller.HandleLifecycleAsync(e.Mode == PowerModes.Suspend); }
        catch { /* Core records the safe lifecycle event. */ }
    }
    private void OnFormClosing(object? sender, FormClosingEventArgs e)
    {
        if (exiting) return;
        e.Cancel = true;
        Hide(); ShowInTaskbar = false;
        if (!closeNoticeShown)
        {
            closeNoticeShown = true;
            tray.ShowBalloonTip(3500, "WB-PMM 仍在运行", "可从系统托盘重新打开，或选择“完全退出”。", ToolTipIcon.Info);
        }
    }
    private async Task ExitAsync(bool throwOnFailure = false)
    {
        if (exiting) return;
        exiting = true;
        SetServiceButtons(false);
        try
        {
            await RetireDocumentAsync();
            await controller.ShutdownAsync();
            await controller.DisposeAsync();
            lifetime.Cancel();
            tray.Visible = false;
            Close();
        }
        catch
        {
            exiting = false;
            ShowMainWindow();
            MessageBox.Show(this, "完全退出未完成，请查看安全日志后重试。", "退出失败", MessageBoxButtons.OK, MessageBoxIcon.Warning);
            if (throwOnFailure) throw new BridgeFailure("shutdown_failed", "完全退出未完成");
        }
    }
    private void CleanUpForm()
    {
        SystemEvents.PowerModeChanged -= OnPowerModeChanged;
        controller.StatusChanged -= OnStatusChanged;
        controller.PanelChanged -= OnPanelChanged;
        controller.LifecycleChanged -= OnLifecycleChanged;
        tray.Dispose();
        web.Dispose();
        lifetime.Dispose();
        if (profileDirectory is not null)
        {
            try { Directory.Delete(profileDirectory, true); } catch { /* WebView may release its files after process exit. */ }
        }
    }
    private void TryLoadBrandIcon()
    {
        var file = Path.Combine(AppContext.BaseDirectory, "Assets", "wb-pmm.png");
        if (!File.Exists(file)) return;
        try
        {
            using var bitmap = new Bitmap(file);
            var handle = bitmap.GetHicon();
            try { Icon = (Icon)System.Drawing.Icon.FromHandle(handle).Clone(); }
            finally { DestroyIcon(handle); }
        }
        catch { Icon = SystemIcons.Application; }
    }
    [DllImport("user32.dll", SetLastError = true)] private static extern bool DestroyIcon(IntPtr handle);
    private delegate bool WindowEnumerator(IntPtr window, IntPtr data);
    [DllImport("kernel32.dll")] private static extern uint GetCurrentThreadId();
    [DllImport("user32.dll")] private static extern bool EnumThreadWindows(uint threadId, WindowEnumerator callback, IntPtr data);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] private static extern int GetWindowText(IntPtr window, StringBuilder text, int maxCount);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] private static extern int GetClassName(IntPtr window, StringBuilder text, int maxCount);
    [DllImport("user32.dll", SetLastError = true)] private static extern bool PostMessage(IntPtr window, uint message, IntPtr wparam, IntPtr lparam);
    [DllImport("user32.dll")] private static extern IntPtr GetWindow(IntPtr window, uint command);
    [DllImport("user32.dll")] private static extern uint GetWindowThreadProcessId(IntPtr window, out uint processId);
}
