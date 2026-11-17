using System.Diagnostics;
using System.Text;
using System.Text.Json.Nodes;

namespace WBPMM;

/// <summary>
/// Windows Defender Firewall check for LAN sharing.
///
/// The app is installed per user and runs without elevation, so the gateway helper
/// gets no inbound rule unless the user approved the one-time system prompt. A
/// dismissed prompt even leaves block rules behind. When LAN sharing is on, the
/// desktop controller reads the rules for wb2api.exe (no elevation needed) and, with
/// the user's consent, replaces them by one allow rule through a UAC prompt:
/// inbound TCP, this program only, local subnet only, Private and Domain profiles.
/// Public networks stay blocked; the user is told to mark a trusted network Private.
/// </summary>
internal static class LanFirewall
{
    internal const string RuleName = "WB-PMM API (LAN)";

    /// <summary>Counts only enabled inbound rules whose profile covers a currently connected network.</summary>
    internal sealed record CheckResult(int Allow, int Block, bool PublicNetwork, bool Connected)
    {
        internal bool NeedsRule => Connected && (Allow == 0 || Block > 0);
    }

    private static string Quote(string value) => "'" + value.Replace("'", "''") + "'";

    /// <summary>
    /// Collects this program's inbound rules into $rules and the path into $p. Stored
    /// program paths may differ in case or use environment variables, so filters are
    /// compared after expansion instead of relying on -Program matching.
    /// </summary>
    private static string FindRules(string program) => $$"""
        $p = {{Quote(program)}}
        $target = [IO.Path]::GetFullPath($p).ToLowerInvariant()
        $filters = @(Get-NetFirewallApplicationFilter -ErrorAction SilentlyContinue | Where-Object { $_.Program -and ([Environment]::ExpandEnvironmentVariables([string]$_.Program)).ToLowerInvariant() -eq $target })
        $rules = @($filters | Get-NetFirewallRule -ErrorAction SilentlyContinue | Where-Object { [string]$_.Direction -eq 'Inbound' })
        """;

    /// <summary>Read-only query; output is one compact JSON line.</summary>
    internal static string CheckScript(string program) => $$"""
        $ErrorActionPreference = 'SilentlyContinue'
        {{FindRules(program)}}
        $cats = @(Get-NetConnectionProfile | ForEach-Object { switch ([string]$_.NetworkCategory) { 'Public' { 'Public' } 'Private' { 'Private' } 'DomainAuthenticated' { 'Domain' } } })
        function Covers($rule) { $pr = [string]$rule.Profile; if ($pr -eq 'Any') { return $true }; foreach ($c in $cats) { if ($pr -match $c) { return $true } }; return $false }
        $rules = @($rules | Where-Object { $_.Enabled -eq 'True' -and (Covers $_) })
        $allow = @($rules | Where-Object { $_.Action -eq 'Allow' }).Count
        $block = @($rules | Where-Object { $_.Action -eq 'Block' }).Count
        [Console]::Out.WriteLine((@{ allow = $allow; block = $block; public = ($cats -contains 'Public'); connected = ($cats.Count -gt 0) } | ConvertTo-Json -Compress))
        """;

    /// <summary>Elevated fix: drop this program's inbound rules, add one scoped allow rule.</summary>
    internal static string AllowScript(string program) => $$"""
        $ErrorActionPreference = 'Stop'
        {{FindRules(program)}}
        if ($rules.Count -gt 0) { $rules | Remove-NetFirewallRule }
        New-NetFirewallRule -DisplayName {{Quote(RuleName)}} -Description 'WB-PMM LAN API sharing' -Direction Inbound -Action Allow -Program $p -Protocol TCP -RemoteAddress LocalSubnet -Profile Private,Domain | Out-Null
        exit 0
        """;

    internal static CheckResult? ParseCheck(string output)
    {
        foreach (var line in Enumerable.Reverse(output.Split('\n', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries)))
        {
            if (!line.StartsWith('{')) continue;
            try
            {
                if (JsonNode.Parse(line) is not JsonObject value) continue;
                var allow = value["allow"]?.GetValue<int>();
                var block = value["block"]?.GetValue<int>();
                var isPublic = value["public"]?.GetValue<bool>();
                var connected = value["connected"]?.GetValue<bool>();
                if (allow is >= 0 && block is >= 0 && isPublic is not null && connected is not null)
                    return new CheckResult(allow.Value, block.Value, isPublic.Value, connected.Value);
            }
            catch (Exception ex) when (ex is System.Text.Json.JsonException or InvalidOperationException or FormatException) { }
        }
        return null;
    }

    internal static string EncodedCommand(string script) => Convert.ToBase64String(Encoding.Unicode.GetBytes(script));

    private static string PowerShell =>
        Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.System), "WindowsPowerShell", "v1.0", "powershell.exe");

    /// <summary>Returns null when the state cannot be read (no PowerShell, policy, timeout).</summary>
    internal static async Task<CheckResult?> CheckAsync(string program, CancellationToken ct)
    {
        if (!OperatingSystem.IsWindows() || !File.Exists(PowerShell)) return null;
        using var process = new Process
        {
            StartInfo = new ProcessStartInfo(PowerShell)
            {
                ArgumentList = { "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", EncodedCommand(CheckScript(program)) },
                UseShellExecute = false, CreateNoWindow = true, RedirectStandardOutput = true, RedirectStandardError = true,
                StandardOutputEncoding = Encoding.UTF8
            }
        };
        try
        {
            process.Start();
            using var timeout = CancellationTokenSource.CreateLinkedTokenSource(ct);
            timeout.CancelAfter(TimeSpan.FromSeconds(20));
            var output = process.StandardOutput.ReadToEndAsync(timeout.Token);
            _ = process.StandardError.ReadToEndAsync(timeout.Token);
            await process.WaitForExitAsync(timeout.Token);
            return ParseCheck(await output);
        }
        catch
        {
            try { if (!process.HasExited) process.Kill(true); } catch { }
            return null;
        }
    }

    /// <summary>Runs the fix through UAC. False when declined or failed.</summary>
    internal static async Task<bool> AllowAsync(string program, CancellationToken ct)
    {
        if (!OperatingSystem.IsWindows() || !File.Exists(PowerShell)) return false;
        try
        {
            using var process = Process.Start(new ProcessStartInfo(PowerShell)
            {
                Arguments = "-NoProfile -NonInteractive -ExecutionPolicy Bypass -WindowStyle Hidden -EncodedCommand " + EncodedCommand(AllowScript(program)),
                UseShellExecute = true, Verb = "runas", WindowStyle = ProcessWindowStyle.Hidden
            });
            if (process is null) return false;
            using var timeout = CancellationTokenSource.CreateLinkedTokenSource(ct);
            timeout.CancelAfter(TimeSpan.FromSeconds(60));
            await process.WaitForExitAsync(timeout.Token);
            return process.ExitCode == 0;
        }
        catch { return false; } // Includes the user cancelling the UAC prompt (ERROR_CANCELLED).
    }
}

internal static class LanNames
{
    /// <summary>First DNS label of the computer name when usable as "&lt;name&gt;.local"; null otherwise.</summary>
    internal static string? HostLabel(string? raw)
    {
        if (string.IsNullOrEmpty(raw)) return null;
        var name = raw.Split('.')[0];
        if (name.Length is < 1 or > 63 || name.StartsWith('-') || name.EndsWith('-') ||
            !name.All(c => c is >= 'a' and <= 'z' or >= 'A' and <= 'Z' or >= '0' and <= '9' or '-')) return null;
        return name;
    }
}
