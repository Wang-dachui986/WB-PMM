using System.Reflection;
using System.Security.AccessControl;
using System.Security.Principal;
using System.Runtime.InteropServices;
using System.Text;
using System.Text.Json.Nodes;
using WBPMM;

if (args.FirstOrDefault() == "--child")
{
    _ = Console.ReadLine();
    Console.WriteLine("@@WBGUI-READY@@ " + new JsonObject { ["service"] = "wbgui", ["protocol"] = 1,
        ["instance_id"] = "fixture", ["pid"] = Environment.ProcessId, ["port"] = 17863 }.ToJsonString());
    while (Console.ReadLine() is not null) { }
    return;
}
var checks = 0;
void Check(bool condition, string name) { if (!condition) throw new Exception(name); checks++; }
Check(Marshal.SizeOf(typeof(ShortcutIdentity).GetNestedType("PropertyKey", BindingFlags.NonPublic)!) == 20, "PROPERTYKEY ABI");
Check(Marshal.SizeOf(typeof(ShortcutIdentity).GetNestedType("PropVariant", BindingFlags.NonPublic)!) == 24, "PROPVARIANT x64 ABI");
var method = typeof(ManagedHelper).GetMethod("ReadOutputAsync", BindingFlags.NonPublic | BindingFlags.Static)!;
var good = new JsonObject { ["service"] = "wbgui", ["protocol"] = 1, ["instance_id"] = "fixture", ["pid"] = 123, ["port"] = 17863 };
async Task Parse(string text, bool accepted)
{
    using var reader = new StreamReader(new MemoryStream(Encoding.UTF8.GetBytes(text)));
    var ready = new TaskCompletionSource<JsonObject>();
    await (Task)method.Invoke(null, new object[] { reader, true, "fixture", 123, ready })!;
    Check(ready.Task.IsCompletedSuccessfully == accepted, "handshake verdict");
    if (accepted) Check((await ready.Task)["port"]!.GetValue<int>() == 17863, "handshake port");
    else Check(ready.Task.Exception?.GetBaseException().Message.Contains("SYNTHETIC_SECRET") == false, "no raw error output");
}
await Parse("ignored\n@@WBGUI-READY@@ " + good.ToJsonString() + "\n", true);
foreach (var pair in new[] { ("pid", (JsonNode)JsonValue.Create(999)!), ("port", (JsonNode)JsonValue.Create(1)!),
    ("instance_id", (JsonNode)JsonValue.Create("wrong")!) })
{
    var bad = (JsonObject)good.DeepClone(); bad[pair.Item1] = pair.Item2;
    await Parse("@@WBGUI-READY@@ " + bad.ToJsonString() + "\n", false);
}
await Parse("@@WBGUI-READY@@ SYNTHETIC_SECRET\n", false);
await Parse(new string('x', 17000) + "\n", false);
var root = Path.Combine(Path.GetTempPath(), "WBPMM-check-" + Guid.NewGuid().ToString("N"));
var paths = new AppPaths(root);
var log = new SafeLog(paths);
// Complete a maintenance lease with the API originally stopped, without launching Windows processes.
var controller = new DesktopController(paths, log);
var controllerType = typeof(DesktopController);
var attemptType = controllerType.GetNestedType("PendingAttempt", BindingFlags.NonPublic)!;
var flowType = controllerType.GetNestedType("MaintenanceFlow", BindingFlags.NonPublic)!;
var attempt = Activator.CreateInstance(attemptType, "desktop-1-1000", "fixture", false)!;
var helper = new ManagedHelper("wbgui", "unused", root, log);
var flow = Activator.CreateInstance(flowType, "fixture", attempt, "login", Array.Empty<string>(), "fixture",
    helper, 17863, new string('a', 64), null, null, null, false, 1UL)!;
flowType.GetField("GatewayStopped", BindingFlags.NonPublic | BindingFlags.Instance)!.SetValue(flow, true);
controllerType.GetField("_maintenance", BindingFlags.NonPublic | BindingFlags.Instance)!.SetValue(controller, flow);
controllerType.GetField("_gatewayState", BindingFlags.NonPublic | BindingFlags.Instance)!.SetValue(controller, "maintenance");
controllerType.GetField("_busy", BindingFlags.NonPublic | BindingFlags.Instance)!.SetValue(controller, "account.maintenance");
await controller.ExecuteAsync("account.maintenance", new JsonObject {
    ["operation"] = "end", ["instance_id"] = "fixture", ["generation"] = "1"
}, "desktop-1-1001", "fixture", CancellationToken.None);
var restored = controller.GetStatus();
Check(restored["gateway_state"]!.GetValue<string>() == "stopped" && !restored["busy"]!.GetValue<bool>() &&
    !restored.ContainsKey("maintenance_phase"), "maintenance restores stopped API and releases busy state");
log.Record("app", "webview_startup_failed", hresult: unchecked((int)0x80070005));
Check(log.DiagnosticText().Contains("webview_startup_failed") && log.DiagnosticText().Contains("0x80070005"), "startup HRESULT retained without raw exceptions");
log.Record("app", "SYNTHETIC_SECRET");
log.Record("gateway", "process_started");
Check(!log.DiagnosticText().Contains("SYNTHETIC_SECRET"), "log whitelist");
// This path is intentionally absent: a failed disk snapshot must retain in-memory diagnostics.
var missingLog = new SafeLog(new AppPaths(Path.Combine(root, "absent")));
missingLog.Record("app", "webview_startup_failed", hresult: unchecked((int)0x80070005));
missingLog.SaveSnapshot();
Check(missingLog.DiagnosticText().Contains("0x80070005"), "diagnostics survive unavailable log directory");
var page = log.Tail(new JsonObject { ["source"] = "gateway", ["limit"] = 1 });
Check(page["entries"]!.AsArray().Count == 1 && page["reset"]!.GetValue<bool>(), "source and reset");
for (var n = 0; n < 2100; n++) log.Record("app", "app_info");
Check(JsonNode.Parse(log.DiagnosticText())!["entries"]!.AsArray().Count == 2000, "bounded log");
if (OperatingSystem.IsWindows())
{
    paths.Prepare();
    try
    {
        // Exercise the real ACL validator without changing ownership on the test machine.
        var validate = typeof(AppPaths).GetMethod("ValidateAcl", BindingFlags.NonPublic | BindingFlags.Static)!;
        using var identity = WindowsIdentity.GetCurrent();
        var user = identity.User!;
        var admin = new SecurityIdentifier(WellKnownSidType.BuiltinAdministratorsSid, null);
        var system = new SecurityIdentifier(WellKnownSidType.LocalSystemSid, null);
        var everyone = new SecurityIdentifier(WellKnownSidType.WorldSid, null);
        void AclCase(SecurityIdentifier owner, bool publicRead, bool userAccess, bool accepted)
        {
            var acl = new FileSecurity();
            acl.SetOwner(owner);
            acl.SetAccessRuleProtection(true, false);
            acl.AddAccessRule(new FileSystemAccessRule(admin, FileSystemRights.FullControl, AccessControlType.Allow));
            if (userAccess) acl.AddAccessRule(new FileSystemAccessRule(user, FileSystemRights.FullControl, AccessControlType.Allow));
            if (publicRead) acl.AddAccessRule(new FileSystemAccessRule(everyone, FileSystemRights.Read, AccessControlType.Allow));
            var actual = true;
            try { validate.Invoke(null, [acl]); }
            catch (TargetInvocationException e) when (e.InnerException is UnauthorizedAccessException) { actual = false; }
            Check(actual == accepted, "private ACL owner/access contract");
        }
        foreach (var owner in new[] { user, admin, system })
        {
            AclCase(owner, false, true, true);
            AclCase(owner, true, true, false);
            AclCase(owner, false, false, false);
        }
        AclCase(everyone, false, true, false);
        Exception? shortcutError = null;
        var shortcutThread = new Thread(() =>
        {
            object? shell = null, shortcut = null;
            try
            {
                var shortcutPath = Path.Combine(paths.Root, "synthetic.lnk");
                shell = Activator.CreateInstance(Type.GetTypeFromProgID("WScript.Shell")!);
                shortcut = ((dynamic)shell!).CreateShortcut(shortcutPath);
                ((dynamic)shortcut).TargetPath = Environment.ProcessPath!;
                ((dynamic)shortcut).Save();
                Marshal.FinalReleaseComObject(shortcut); shortcut = null;
                ShortcutIdentity.Set(shortcutPath);
                if (ShortcutIdentity.Read(shortcutPath) != ShortcutIdentity.AppId) throw new Exception("shortcut not persisted");
                ShortcutIdentity.Set(shortcutPath); // The upgrade/repair operation is repeatable.
            }
            catch (Exception error) { shortcutError = error; }
            finally
            {
                if (shortcut is not null) Marshal.FinalReleaseComObject(shortcut);
                if (shell is not null) Marshal.FinalReleaseComObject(shell);
            }
        });
        shortcutThread.SetApartmentState(ApartmentState.STA);
        shortcutThread.Start(); shortcutThread.Join();
        Check(shortcutError is null, "shortcut write/read/repair: " + shortcutError?.GetType().Name);
        var filename = Path.Combine(paths.Root, "synthetic.json");
        AppPaths.AtomicWrite(filename, Encoding.UTF8.GetBytes("first"), false);
        Check(Encoding.UTF8.GetString(AppPaths.ReadPrivateFile(filename, 100)) == "first", "private read/write");
        try { AppPaths.AtomicWrite(filename, Encoding.UTF8.GetBytes("wrong"), false); throw new Exception("clobbered"); }
        catch (IOException) { checks++; }
        Check(Encoding.UTF8.GetString(AppPaths.ReadPrivateFile(filename, 100)) == "first", "original retained");
        var security = new FileInfo(filename).GetAccessControl();
        security.AddAccessRule(new FileSystemAccessRule(new SecurityIdentifier(WellKnownSidType.WorldSid, null), FileSystemRights.Read, AccessControlType.Allow));
        new FileInfo(filename).SetAccessControl(security);
        try { AppPaths.ReadPrivateFile(filename, 100); throw new Exception("public ACL accepted"); }
        catch (UnauthorizedAccessException) { checks++; }
        await using var child = new ManagedHelper("wbgui", Environment.ProcessPath!, paths.Root, log);
        var arguments = string.Equals(Path.GetFileNameWithoutExtension(Environment.ProcessPath), "dotnet", StringComparison.OrdinalIgnoreCase)
            ? new[] { Assembly.GetExecutingAssembly().Location, "--child" } : new[] { "--child" };
        var ready = await child.StartAsync(arguments, new JsonObject(), true, "fixture", CancellationToken.None);
        Check(child.IsRunning && ready is not null, "owned helper ready");
        await child.StopAsync(CancellationToken.None);
        Check(!child.IsRunning && child.Pid is null, "EOF shutdown");
    }
    finally { Directory.Delete(root, true); }
}
else Console.WriteLine("Windows shortcut COM, ACL and Job Object checks: NOT_RUN (requires Windows)");
Console.WriteLine($"Foundation checks PASS: {checks}");
