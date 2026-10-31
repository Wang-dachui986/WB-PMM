using System.Text;
using System.Text.Json.Nodes;
using System.Diagnostics;
using WBPMM;

static class Check
{
    private static int passed;
    private static void True(bool value, string name)
    {
        if (!value) throw new Exception("FAIL: " + name);
        passed++;
    }
    private static void Reject(Action work, string name)
    {
        try { work(); } catch (Exception ex) when (ex is BridgeFailure or PanelConfigFailure) { passed++; return; }
        throw new Exception("FAIL: expected rejection: " + name);
    }

    public static async Task Main(string[] args)
    {
        var root = Path.Combine(Path.GetTempPath(), "wb-core-contracts-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        try
        {
            foreach (var domain in new[] { "", "copilot.tencent.com", "codebuddy.cn", "www.codebuddy.cn" })
                True(PanelClient.Region(domain) == "cn", "CN import domain alias");
            True(PanelClient.Region("www.workbuddy.ai") == "global", "global import domain");
            Reject(() => PanelClient.Region("www.codebuddy.cn.evil.example"), "unknown import domain rejected");
            var bomJson = new byte[] { 0xEF, 0xBB, 0xBF }.Concat(Encoding.UTF8.GetBytes("{\"uid\":\"fake001\"}")).ToArray();
            True(JsonNode.Parse(PanelClient.CredentialText(bomJson))!["uid"]!.GetValue<string>() == "fake001", "UTF-8 BOM normalized");
            try { PanelClient.CredentialText([0xFF, 0xFE]); throw new Exception("Malformed UTF-8 accepted"); }
            catch (DecoderFallbackException) { True(true, "malformed UTF-8 rejected"); }
            var paths = new AppPaths(root);
            var raw = new JsonObject {
                ["listen"] = "127.0.0.1:7863", ["api_key"] = "test",
                ["auth_dir"] = paths.Auths, ["state_file"] = Path.Combine(root, "state.json"),
                ["schedule"] = new JsonObject { ["checkin_enabled"] = true, ["checkin_hours"] = new JsonArray(),
                    ["travel_enabled"] = false, ["activity_enabled"] = false, ["keepalive_enabled"] = false,
                    ["school_enabled"] = false, ["cat_enabled"] = false },
                ["admin"] = new JsonObject { ["enabled"] = false }
            };
            File.WriteAllText(paths.GatewayConfig, raw.ToJsonString());
            var snapshot = GatewayConfiguration.Read(paths);
            True(snapshot.Port == 7863 && !snapshot.AllowLan && snapshot.Host == "127.0.0.1", "default loopback and empty legacy hours");
            raw["listen"] = "0.0.0.0:7863"; File.WriteAllText(paths.GatewayConfig, raw.ToJsonString());
            True(GatewayConfiguration.Read(paths).AllowLan, "LAN host is single persisted truth");
            Reject(() => GatewayConfiguration.ParseListen("0.0.0.0:07863"), "noncanonical port");
            File.WriteAllText(paths.GatewayConfig, raw.ToJsonString().Replace("\"listen\":", "\"listen\":\"x\",\"listen\":"));
            Reject(() => GatewayConfiguration.Read(paths), "duplicate JSON key");

            var view = JsonNode.Parse("""
              {"revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
               "key_configured":true,"safe_summary":{"listen_host":"127.0.0.1","schedules_disabled":true},
               "editable":{"port":7863,"upstream":{"timeout_seconds":120,"header_timeout_seconds":30,"idle_timeout_seconds":90},
               "pool":{"max_in_flight":0},"session_sticky":{"enabled":true,"ttl":"24h"},"desktop":{"proxy_url":""}}}
              """)!.AsObject();
            True(!PanelClient.ParseConfig(view).AllowLan, "older panel response defaults LAN off");
            view["editable"]!["allow_lan"] = true;
            Reject(() => PanelClient.ParseConfig(view), "mismatched LAN summary");
            view["safe_summary"]!["listen_host"] = "0.0.0.0";
            True(PanelClient.ParseConfig(view).AllowLan, "LAN projection");
            view["editable"]!["allow_lan"] = null;
            Reject(() => PanelClient.ParseConfig(view), "explicit null LAN is invalid");
            view["editable"]!.AsObject().Remove("allow_lan");
            view["safe_summary"]!["listen_host"] = "127.0.0.1";
            view["editable"]!["desktop"]!["proxy_url"] = null;
            Reject(() => PanelClient.ParseConfig(view), "explicit null proxy is invalid");

            var patch = new JsonObject { ["revision"] = new string('a', 64), ["patch"] = new JsonObject { ["port"] = 1024, ["allow_lan"] = true } };
            True(BridgeValidation.Patch(patch).Count == 2, "valid config patch");
            patch["patch"]!["port"] = 1023;
            Reject(() => BridgeValidation.Patch(patch), "privileged port rejected");
            patch["patch"]!["port"] = 1024; patch["patch"]!["allow_lan"] = "true";
            Reject(() => BridgeValidation.Patch(patch), "string Boolean rejected");
            True(GatewayConfiguration.TryProxyEndpoint("http://127.0.0.1:7890", out _, out var port) && port == 7890, "TCP proxy endpoint");
            True(!GatewayConfiguration.TryProxyEndpoint("http://user@127.0.0.1:7890", out _, out _), "proxy credentials rejected");
            if (args is ["--panel", var executable]) await CheckPanelInterop(executable, root);
            Console.WriteLine($"Windows core contract checks: PASS ({passed})");
        }
        finally { Directory.Delete(root, true); }
    }

    private static async Task CheckPanelInterop(string executable, string root)
    {
        // Real C# client / Go panel boundary, synthetic data only; no upstream requests.
        const string instance = "interop-test";
        var controlKey = new string('a', 64);
        using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(30));
        var ct = timeout.Token;
        using var child = new Process { StartInfo = new ProcessStartInfo(executable) {
            UseShellExecute = false, RedirectStandardInput = true, RedirectStandardOutput = true,
            RedirectStandardError = true, StandardInputEncoding = new UTF8Encoding(false)
        } };
        child.StartInfo.ArgumentList.Add("--desktop");
        child.Start();
        var stderr = child.StandardError.ReadToEndAsync();
        try
        {
            await child.StandardInput.WriteLineAsync(new JsonObject {
                ["protocol"] = 1, ["instance_id"] = instance, ["username"] = "desktop", ["password"] = "synthetic-password",
                ["panel_control_key"] = controlKey, ["gateway_url"] = "http://127.0.0.1:1",
                ["auth_dir"] = Path.Combine(root, "auths"), ["backup_dir"] = Path.Combine(root, "backups"),
                ["dangerous_ops"] = false
            }.ToJsonString());
            await child.StandardInput.FlushAsync(ct);
            var ready = await child.StandardOutput.ReadLineAsync(ct) ?? throw new Exception("Panel did not start");
            const string prefix = "@@WBGUI-READY@@ ";
            if (!ready.StartsWith(prefix)) throw new Exception("Invalid panel handshake");
            var port = JsonNode.Parse(ready[prefix.Length..])!["port"]!.GetValue<int>();
            using var client = new PanelClient(port);
            var cookie = await client.LoginAsync("desktop", "synthetic-password", ct);
            True(cookie.Length > 16, "real panel session established");
            True(await client.MaintenanceAsync("begin", instance, 1, controlKey, "login", [], DateTimeOffset.UtcNow.AddMinutes(10), ct),
                "C# login maintenance admitted by Go panel");
            True(await client.MaintenanceAsync("end", instance, 1, controlKey, null, null, null, ct), "maintenance released");
            True(await client.MaintenanceAsync("begin", instance, 2, controlKey, "credits", ["synthetic-account"], DateTimeOffset.UtcNow.AddMinutes(10), ct),
                "subsequent account maintenance admitted");
            True(await client.MaintenanceAsync("end", instance, 2, controlKey, null, null, null, ct), "subsequent maintenance released");
            True(await client.MaintenanceAsync("begin", instance, 3, controlKey, "import", ["fake-import"], DateTimeOffset.UtcNow.AddMinutes(10), ct), "import permit admitted");
            var credential = """{"account":{"uid":"fake-import","nickname":"Synthetic"},"auth":{"accessToken":"synthetic-token","domain":"www.codebuddy.cn"}}""";
            var rawImport = new byte[] { 0xEF, 0xBB, 0xBF }.Concat(Encoding.UTF8.GetBytes(credential)).ToArray();
            await client.ImportCredentialAsync(rawImport, false, cookie, instance, 3, ct);
            True(await client.ExistingRegionAsync("fake-import", cookie, ct) == "cn", "BOM import and existing CN alias preview agree");
            var savedFile = Path.Combine(root, "auths", "workbuddy-fake-import.json");
            var savedText = File.ReadAllText(savedFile);
            True(JsonNode.Parse(savedText)!["auth"]!["accessToken"]!.GetValue<string>() == "synthetic-token", "Go panel persisted synthetic imported credential");
            try { await client.ImportCredentialAsync(rawImport, false, cookie, instance, 3, ct); throw new Exception("Unconfirmed overwrite accepted"); }
            catch (BridgeFailure) { True(File.ReadAllText(savedFile) == savedText, "overwrite remains explicit"); }
            var cross = Encoding.UTF8.GetBytes(credential.Replace("www.codebuddy.cn", "www.workbuddy.ai"));
            try { await client.ImportCredentialAsync(cross, true, cookie, instance, 3, ct); throw new Exception("Cross-region overwrite accepted"); }
            catch (BridgeFailure) { True(File.ReadAllText(savedFile) == savedText, "cross-region overwrite preserves original"); }
            True(await client.MaintenanceAsync("end", instance, 3, controlKey, null, null, null, ct), "import maintenance released");
            True(!child.HasExited, "panel stays alive after maintenance");
        }
        finally
        {
            child.StandardInput.Close();
            try { await child.WaitForExitAsync().WaitAsync(TimeSpan.FromSeconds(8)); }
            catch (TimeoutException) { child.Kill(); await child.WaitForExitAsync(); }
            await stderr;
        }
    }
}

namespace WBPMM
{
    internal sealed class BridgeFailure(string code, string message) : Exception(message)
    {
        public string Code { get; } = code;
    }
    internal sealed class AppPaths(string root)
    {
        public string Root { get; } = root;
        public string GatewayConfig => Path.Combine(Root, "gateway-config.json");
        public string Auths => Path.Combine(Root, "auths");
        public static byte[] ReadPrivateFile(string path, long limit)
        {
            var value = File.ReadAllBytes(path);
            if (value.LongLength > limit) throw new IOException();
            return value;
        }
        public static void CheckPrivateFile(string path, long limit) { if (new FileInfo(path).Length > limit) throw new IOException(); }
        public static void RejectReparseParents(string path) { }
        public static void AtomicWrite(string path, byte[] bytes, bool overwrite = true) => File.WriteAllBytes(path, bytes);
    }
}
