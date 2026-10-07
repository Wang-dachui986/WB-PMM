using System.IO.Pipes;
using System.Runtime.InteropServices;
using System.Security.Principal;
using System.Text;

namespace WBPMM;

internal static class Program
{
    [STAThread]
    private static int Main(string[] args)
    {
        if (args is ["--register-shortcut"]) return ShortcutIdentity.Register();
        Application.SetHighDpiMode(HighDpiMode.PerMonitorV2);
        Application.EnableVisualStyles();
        Application.SetCompatibleTextRenderingDefault(false);
        SetCurrentProcessExplicitAppUserModelID("WBPMM.Desktop");
        if (args.Any(value => value is not ("--background" or "--shutdown"))) return 2;
        var sid = WindowsIdentity.GetCurrent().User?.Value;
        if (sid is null) return 1;
        var identity = "WBPMM." + sid;
        using var mutex = new Mutex(false, @"Global\" + identity);
        var owner = false;
        try { owner = mutex.WaitOne(0); } catch (AbandonedMutexException) { owner = true; }
        var shutdown = args.Contains("--shutdown");
        if (!owner)
        {
            if (!SendAsync(identity, shutdown ? "shutdown" : "show").GetAwaiter().GetResult()) return 1;
            if (!shutdown) return 0;
            // The owner releases its mutex only after Form exit and helper cleanup.
            try { owner = mutex.WaitOne(TimeSpan.FromSeconds(45)); } catch (AbandonedMutexException) { owner = true; }
            if (owner) mutex.ReleaseMutex();
            return owner ? 0 : 1;
        }
        if (shutdown) { mutex.ReleaseMutex(); return 0; }
        SafeLog? log = null;
        try
        {
            var paths = new AppPaths(); paths.Prepare();
            log = new SafeLog(paths); log.Record("app", "instance_started");
            SynchronizationContext.SetSynchronizationContext(new WindowsFormsSynchronizationContext());
            var controller = new DesktopController(paths, log);
            var utilities = new NativeUtilities(paths, log);
            using var form = new MainForm(controller, utilities) { StartInBackground = args.Contains("--background") };
            using var cancellation = new CancellationTokenSource();
            Task? server = null;
            form.Shown += (_, _) => server = ListenAsync(identity, form, cancellation.Token);
            Application.Run(form);
            cancellation.Cancel();
            if (server is not null) try { server.GetAwaiter().GetResult(); } catch (OperationCanceledException) { }
            log.Record("app", "shutdown_completed");
            return 0;
        }
        catch
        {
            MessageBox.Show("WB-PMM 无法启动。请检查当前用户数据目录权限和安装文件完整性；现有账号数据已保留。",
                "WB-PMM", MessageBoxButtons.OK, MessageBoxIcon.Error);
            return 1;
        }
        finally { log?.SaveSnapshot(); mutex.ReleaseMutex(); }
    }

    private static async Task<bool> SendAsync(string name, string command)
    {
        using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(15));
        try
        {
            using var pipe = new NamedPipeClientStream(".", name, PipeDirection.Out, PipeOptions.Asynchronous | PipeOptions.CurrentUserOnly);
            await pipe.ConnectAsync(timeout.Token);
            await pipe.WriteAsync(Encoding.ASCII.GetBytes(command + "\n"), timeout.Token);
            await pipe.FlushAsync(timeout.Token);
            return true;
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException or OperationCanceledException) { return false; }
    }

    private static async Task ListenAsync(string name, MainForm form, CancellationToken ct)
    {
        while (!ct.IsCancellationRequested)
        {
            using var pipe = new NamedPipeServerStream(name, PipeDirection.In, 1, PipeTransmissionMode.Byte,
                PipeOptions.Asynchronous | PipeOptions.CurrentUserOnly);
            await pipe.WaitForConnectionAsync(ct).ConfigureAwait(false);
            using var timeout = CancellationTokenSource.CreateLinkedTokenSource(ct);
            timeout.CancelAfter(TimeSpan.FromSeconds(2));
            var buffer = new byte[16]; var used = 0;
            try
            {
                while (used < buffer.Length)
                {
                    var read = await pipe.ReadAsync(buffer.AsMemory(used, 1), timeout.Token).ConfigureAwait(false);
                    if (read == 0 || buffer[used++] == '\n') break;
                }
                var command = Encoding.ASCII.GetString(buffer, 0, used);
                if (command == "show\n") form.BeginInvoke(form.ShowMainWindow);
                else if (command == "shutdown\n") form.BeginInvoke(async () =>
                {
                    try { await form.RequestExitAsync(); }
                    catch { /* The form retains ownership; installer times out instead of forcing exit. */ }
                });
            }
            catch (Exception ex) when (ex is IOException or OperationCanceledException or InvalidOperationException) { }
        }
    }

    [DllImport("shell32.dll", CharSet = CharSet.Unicode)]
    private static extern int SetCurrentProcessExplicitAppUserModelID(string appId);
}
