# Windows port contracts — Mac 1.0.3 parity

Target: Windows 10 22H2 / Windows 11, x64. C# .NET 10 WinForms + WebView2, existing React and two Go helpers. Source Mac tree is frozen; only this separate project changes.

Namespace: `WBPMM`. JSON is `System.Text.Json.Nodes.JsonObject` and all returned nodes must be cloned when reused. UI and controller run on the WinForms synchronization context, asynchronous methods do not block the message loop. Known errors use `BridgeFailure(string Code, string message)`; never expose raw exceptions or credentials to web content/logs.

## Root-owned foundation (Platform.cs / ManagedHelper.cs / Program.cs / csproj / packaging)
- `AppPaths` instance: string `Root`, `Auths`, `Backups`, `Run`, `Logs`, `GatewayConfig`, `PanelConfig`, `SettingsFile`, `Helpers`; constructor optional root for tests; `void Prepare()` creates/validates private owned ACL dirs; `static void CheckPrivateFile(string path, long limit)`; `static void AtomicWrite(string path, byte[] bytes, bool overwrite = true)`; helpers in AppContext.BaseDirectory/Helpers.
- `ManagedHelper(string name, string executable, string workingDirectory, SafeLog log)`: `bool IsRunning`, `int? Pid`, `event Action<int>? Exited`; `Task<JsonObject?> StartAsync(IReadOnlyList<string> arguments, JsonObject init, bool expectPanelReady, string instanceId, CancellationToken ct)` starts with clean env, bounded stdout READY parser matching instance and actual PID; keeps stdin alive. `Task StopAsync(CancellationToken ct)` closes stdin, waits bounded (15 s), then terminates only owned job/tree if needed; `DisposeAsync()` cleans owned handles. No stdout/raw stderr saved.
- `SafeLog(AppPaths paths)`: `void Record(string source,string eventName,int? exitCode=null)` for fixed safe event tokens only; `JsonObject Tail(JsonObject payload)` preserving Mac return schema; `int ClearArchives()`; `string DiagnosticText()` max 1MiB, no raw process output/paths/keys. `string RunId`.
- `BridgeFailure : Exception` exposes string `Code`.

## manual_fact_check-owned core (DesktopController.cs / PanelClient.cs / GatewayConfiguration.cs)
- `DesktopController(AppPaths paths, SafeLog log)`.
- Properties: `string? PanelOrigin`, `string? InstanceId`, `string? PanelCookie` (value only, name `wbgui_session` must verify server); `bool IsShuttingDown`; `event Action? StatusChanged`; `event Action? PanelChanged`.
- UI hooks assigned before start: `Func<string,string,CancellationToken,Task<bool>> ConfirmAsync` for normal confirm; `Func<CancellationToken,Task<string?>> ChooseAuthFileAsync`; `Action<string> CopyText`; `Action<string> OpenExternal` (controller validates OAuth host); optional `Func<string,string,CancellationToken,Task<string>> ChooseStopModeAsync` returns `wait` / `abort` / `cancel` for in-flight requests.
- `Task InitializeAsync(bool autoStartApi, CancellationToken ct)` starts panel/session and optionally API.
- `JsonObject GetStatus()` returns the Mac native status white-list (not wrapper).
- `Task<JsonObject> ExecuteAsync(string action, JsonObject payload, string requestId, string documentId, CancellationToken ct)` for all non-utility commands; return `data`, shell wraps envelope.
- `Task RetireDocumentAsync(string documentId)` cleans/cancels operations/leases for this document and preserves cleanup lock when uncertain.
- `Task HandleLifecycleAsync(bool sleeping)`; `Task ShutdownAsync()`; `ValueTask DisposeAsync()`.
- Utilities delegated by shell: preferences.*, logs.*, diagnostics.export, desktop.openDirectory; core must implement all other existing 23 bridge commands including config transactions, auth import previews, maintenance cleanup and cancellations.

## guide_verification-owned UI (MainForm.cs / NativeUtilities.cs / shell assets, frontend Windows strings)
- `MainForm(DesktopController controller, NativeUtilities utilities)` wires hooks, native status/start/stop/restart header, WebView2 host, failure/retry, tray, close-to-tray, explicit exit.
- `NativeUtilities(AppPaths paths, SafeLog log)`: `bool AutoStartApi`, `bool LaunchAtLogin`, `string Theme`; `Func<string,string,CancellationToken,Task<bool>> ConfirmAsync`, `Func<string,CancellationToken,Task<string?>> ChooseSaveFileAsync`; `Task<JsonObject> ExecuteAsync(string action,JsonObject payload,CancellationToken ct)`; preferences.persist and HKCU Run startup, notes/onboarding/logs/diagnostics/open dirs; core current status optionally `Func<JsonObject> StatusProvider`.
- Program owns single-instance activation, calls MainForm; MainForm InitializeAsync on Shown using AutoStartApi or --background plus LaunchAtLogin. Expose `ShowMainWindow()` for second instance and tray activation.
- WebView exact local panel main-origin only, no permissive navigation/popup/download/frame native IPC; nonce injected per committed document, preserve webkit bridge API shape. Retire document on navigation or process failure; pending limits and input-size limits. Private/ephemeral browser profile under Run, cleanup on exit. Cookies only via native CookieManager (no JS secrets). Browser location never contains tokens.
- Status/lifecycle events include current document_id and strictly increasing epoch strings. Reload or reconnect updates session cookie. External browser opening only by core-approved OAuth action; do not accept arbitrary web URL.
- Keep UI business pages from Mac unchanged except Windows terminology and platform bridge injection compatibility. Native data context remains secure.

## Verification
No real account/model calls. Cross-compile and isolated fixtures, fake helpers and protocol/security tests first. Windows process, ACL, WebView2, tray, installer/update/uninstall and Win10/Win11 network must be explicitly marked NOT_RUN until exercised on Windows. No Git writes.
