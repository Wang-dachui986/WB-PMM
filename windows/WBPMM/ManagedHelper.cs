using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Text;
using System.Text.Json.Nodes;
using Microsoft.Win32.SafeHandles;

namespace WBPMM;

internal sealed class ManagedHelper(string name, string executable, string workingDirectory, SafeLog log) : IAsyncDisposable
{
    private readonly SemaphoreSlim gate = new(1, 1);
    private Process? process;
    private SafeFileHandle? job;
    private Task? outputTask, errorTask;
    public bool IsRunning { get { try { return process is { HasExited: false }; } catch { return false; } } }
    public int? Pid => IsRunning ? process!.Id : null;
    public event Action<int>? Exited;

    public async Task<JsonObject?> StartAsync(IReadOnlyList<string> arguments, JsonObject init,
        bool expectPanelReady, string instanceId, CancellationToken ct)
    {
        await gate.WaitAsync(ct);
        try
        {
            if (IsRunning) throw new BridgeFailure("already_running", "服务已在运行");
            ReleaseExited();
            var info = new ProcessStartInfo(executable)
            {
                WorkingDirectory = workingDirectory, UseShellExecute = false, CreateNoWindow = true,
                RedirectStandardInput = true, RedirectStandardOutput = true, RedirectStandardError = true,
                StandardInputEncoding = new UTF8Encoding(false), StandardOutputEncoding = new UTF8Encoding(false),
                StandardErrorEncoding = new UTF8Encoding(false)
            };
            foreach (var argument in arguments) info.ArgumentList.Add(argument);
            info.Environment.Clear();
            foreach (var key in new[] { "SystemRoot", "windir", "TEMP", "TMP", "USERPROFILE", "LOCALAPPDATA", "APPDATA", "USERNAME", "TZ" })
                if (Environment.GetEnvironmentVariable(key) is { } value) info.Environment[key] = value;
            info.Environment["PATH"] = Environment.GetFolderPath(Environment.SpecialFolder.System);
            var ready = new TaskCompletionSource<JsonObject>(TaskCreationOptions.RunContinuationsAsynchronously);
            var owned = new Process { StartInfo = info, EnableRaisingEvents = true };
            owned.Exited += (_, _) =>
            {
                int code;
                try { code = owned.ExitCode; } catch { return; }
                ready.TrySetException(new BridgeFailure("helper_exited", "后台服务在启动期间退出"));
                log.Record(name == "wbgui" ? "panel" : "gateway", "process_exited", code);
                try { Exited?.Invoke(code); } catch { /* An observer cannot take down process cleanup. */ }
            };
            process = owned;
            try
            {
                job = CreateJobObject(IntPtr.Zero, null);
                if (job.IsInvalid) throw new IOException("Job creation failed");
                var limits = new ExtendedLimits { Basic = new BasicLimits { LimitFlags = 0x2000 } }; // KILL_ON_JOB_CLOSE
                if (!SetInformationJobObject(job, 9, ref limits, (uint)Marshal.SizeOf<ExtendedLimits>()))
                    throw new IOException("Job configuration failed");
                ct.ThrowIfCancellationRequested();
                if (!owned.Start()) throw new IOException("Process start failed");
                // Both helpers wait for stdin initialization before binding ports or doing work.
                if (!AssignProcessToJobObject(job, owned.SafeHandle)) throw new IOException("Job assignment failed");
                outputTask = ReadOutputAsync(owned.StandardOutput, expectPanelReady, instanceId, owned.Id, ready);
                errorTask = DrainAsync(owned.StandardError);
                var input = init.ToJsonString();
                if (Encoding.UTF8.GetByteCount(input) > 16 * 1024) throw new IOException("Initialization too large");
                await owned.StandardInput.WriteLineAsync(input.AsMemory(), ct);
                await owned.StandardInput.FlushAsync(ct);
                log.Record(name == "wbgui" ? "panel" : "gateway", "process_started");
                if (expectPanelReady)
                    return await ready.Task.WaitAsync(TimeSpan.FromSeconds(20), ct);
                if (owned.HasExited) throw new BridgeFailure("helper_exited", "后台服务启动失败");
                return null;
            }
            catch
            {
                await StopOwnedAsync(CancellationToken.None);
                throw;
            }
        }
        finally { gate.Release(); }
    }

    private static async Task ReadOutputAsync(StreamReader reader, bool expectReady, string instanceId, int pid,
        TaskCompletionSource<JsonObject> ready)
    {
        const string prefix = "@@WBGUI-READY@@ ";
        var buffer = new char[4096];
        var line = new StringBuilder();
        var collecting = expectReady;
        try
        {
            int count;
            while ((count = await reader.ReadAsync(buffer)) > 0)
            {
                if (!collecting) continue;
                for (var index = 0; index < count; index++)
                {
                    var character = buffer[index];
                    if (character == '\n')
                    {
                        var text = line.ToString().TrimEnd('\r');
                        line.Clear();
                        if (!text.StartsWith(prefix, StringComparison.Ordinal)) continue;
                        var result = JsonNode.Parse(text[prefix.Length..]) as JsonObject;
                        if (result is null || result["service"]?.GetValue<string>() != "wbgui" ||
                            result["protocol"]?.GetValue<int>() != 1 || result["instance_id"]?.GetValue<string>() != instanceId ||
                            result["pid"]?.GetValue<int>() != pid || result["port"]?.GetValue<int>() is not (>= 1024 and <= 65535))
                            throw new IOException("Invalid helper identity");
                        ready.TrySetResult(result);
                        collecting = false;
                        break;
                    }
                    line.Append(character);
                    if (line.Length > 16 * 1024) throw new IOException("Oversized helper handshake");
                }
            }
            if (collecting) throw new IOException("Missing helper handshake");
        }
        catch { ready.TrySetException(new BridgeFailure("handshake_rejected", "后台服务握手校验失败")); }
        finally
        {
            line.Clear();
            // Invalid output must still be consumed to avoid a blocked child during shutdown.
            await DrainAsync(reader);
        }
    }

    private static async Task DrainAsync(StreamReader reader)
    {
        try
        {
            var buffer = new char[4096];
            while (await reader.ReadAsync(buffer) > 0) { }
            Array.Clear(buffer);
        }
        catch (Exception ex) when (ex is IOException or ObjectDisposedException) { }
    }

    public async Task StopAsync(CancellationToken ct, TimeSpan? grace = null)
    {
        await gate.WaitAsync(ct);
        try { await StopOwnedAsync(ct, grace); }
        finally { gate.Release(); }
    }

    private async Task StopOwnedAsync(CancellationToken ct, TimeSpan? grace = null)
    {
        var owned = process;
        if (owned is null) { ReleaseExited(); return; }
        bool started;
        try { _ = owned.Id; started = true; } catch (InvalidOperationException) { started = false; }
        if (!started) { ReleaseExited(); return; }
        if (!owned.HasExited)
        {
            try { owned.StandardInput.Close(); } catch (IOException) { }
            try { await owned.WaitForExitAsync(ct).WaitAsync(grace ?? TimeSpan.FromSeconds(15), ct); }
            catch (Exception ex) when (ex is TimeoutException or OperationCanceledException)
            {
                // Only this retained process handle/job is ours, never a port-based PID lookup.
                if (!owned.HasExited)
                {
                    if (job is { IsInvalid: false, IsClosed: false }) TerminateJobObject(job, 1);
                    if (!owned.HasExited) owned.Kill(entireProcessTree: true);
                    try { await owned.WaitForExitAsync().WaitAsync(TimeSpan.FromSeconds(5)); }
                    catch { throw new BridgeFailure("stop_failed", "服务仍在退出，请稍后重试"); }
                }
            }
        }
        if (!owned.HasExited) throw new BridgeFailure("stop_failed", "尚未确认服务退出");
        if (outputTask is not null && errorTask is not null)
            try { await Task.WhenAll(outputTask, errorTask).WaitAsync(TimeSpan.FromSeconds(2)); } catch { }
        ReleaseExited();
    }

    private void ReleaseExited()
    {
        if (IsRunning) return;
        job?.Dispose(); job = null;
        process?.Dispose(); process = null;
        outputTask = errorTask = null;
    }

    public async ValueTask DisposeAsync()
    {
        await StopAsync(CancellationToken.None);
        gate.Dispose();
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct BasicLimits
    {
        public long ProcessTime, JobTime;
        public uint LimitFlags;
        public UIntPtr MinimumWorkingSet, MaximumWorkingSet;
        public uint ActiveProcessLimit;
        public UIntPtr Affinity;
        public uint PriorityClass, SchedulingClass;
    }
    [StructLayout(LayoutKind.Sequential)]
    private struct IoCounters { public ulong ReadOperations, WriteOperations, OtherOperations, ReadBytes, WriteBytes, OtherBytes; }
    [StructLayout(LayoutKind.Sequential)]
    private struct ExtendedLimits
    {
        public BasicLimits Basic;
        public IoCounters Io;
        public UIntPtr ProcessMemory, JobMemory, PeakProcessMemory, PeakJobMemory;
    }
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern SafeFileHandle CreateJobObject(IntPtr attributes, string? name);
    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool SetInformationJobObject(SafeFileHandle job, int infoClass, ref ExtendedLimits limits, uint length);
    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool AssignProcessToJobObject(SafeFileHandle job, SafeProcessHandle process);
    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool TerminateJobObject(SafeFileHandle job, uint code);
}
