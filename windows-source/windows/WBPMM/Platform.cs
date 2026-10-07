using System.Runtime.InteropServices;
using System.Security.AccessControl;
using System.Security.Principal;
using Microsoft.Win32.SafeHandles;

namespace WBPMM;

internal sealed class BridgeFailure(string code, string message) : Exception(message)
{
    public string Code { get; } = code;
}

internal sealed class AppPaths
{
    public string Root { get; }
    public string Auths => Path.Combine(Root, "auths");
    public string Backups => Path.Combine(Root, "backups");
    public string Run => Path.Combine(Root, "run");
    public string Logs => Path.Combine(Root, "logs");
    public string GatewayConfig => Path.Combine(Root, "gateway-config.json");
    public string PanelConfig => Path.Combine(Root, "panel-config.json");
    public string SettingsFile => Path.Combine(Root, "settings.json");
    public string Helpers => Path.Combine(AppContext.BaseDirectory, "Helpers");
    private static SecurityIdentifier User
    {
        get
        {
            using var identity = WindowsIdentity.GetCurrent();
            return identity.User ?? throw new UnauthorizedAccessException("Windows identity unavailable");
        }
    }
    private static readonly SecurityIdentifier SystemSid = new(WellKnownSidType.LocalSystemSid, null);
    private static readonly SecurityIdentifier AdminSid = new(WellKnownSidType.BuiltinAdministratorsSid, null);

    public AppPaths(string? root = null) => Root = Path.GetFullPath(root ??
        Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "WB-PMM"));

    public void Prepare()
    {
        foreach (var directory in new[] { Root, Auths, Backups, Run, Logs }) PrepareDirectory(directory);
    }

    private static void PrepareDirectory(string path)
    {
        RejectReparseParents(path);
        if (!Directory.Exists(path))
        {
            var security = new DirectorySecurity();
            security.SetOwner(User);
            security.SetAccessRuleProtection(true, false);
            foreach (var sid in new[] { User, SystemSid, AdminSid })
                security.AddAccessRule(new FileSystemAccessRule(sid, FileSystemRights.FullControl,
                    InheritanceFlags.ContainerInherit | InheritanceFlags.ObjectInherit,
                    PropagationFlags.None, AccessControlType.Allow));
            new DirectoryInfo(path).Create(security);
        }
        RejectReparseParents(path);
        ValidateAcl(new DirectoryInfo(path).GetAccessControl());
    }

    internal static void RejectReparseParents(string path)
    {
        for (var current = Path.GetFullPath(path); !string.IsNullOrEmpty(current); current = Path.GetDirectoryName(current))
        {
            try
            {
                if ((File.GetAttributes(current) & FileAttributes.ReparsePoint) != 0)
                    throw new UnauthorizedAccessException("Linked data paths are not supported");
            }
            catch (FileNotFoundException) { }
            catch (DirectoryNotFoundException) { }
        }
    }

    private static void ValidateAcl(FileSystemSecurity security)
    {
        // Match both Go helpers: elevated Windows tokens may create Administrators-owned files.
        // Ownership alone never grants access: the restricted DACL and current-user ACE remain required.
        var owner = security.GetOwner(typeof(SecurityIdentifier));
        if (!User.Equals(owner) && !SystemSid.Equals(owner) && !AdminSid.Equals(owner))
            throw new UnauthorizedAccessException("Data has an unexpected owner");
        bool userCanRead = false;
        var rules = security.GetAccessRules(true, true, typeof(SecurityIdentifier));
        foreach (FileSystemAccessRule rule in rules)
        {
            var sid = (SecurityIdentifier)rule.IdentityReference;
            if (rule.AccessControlType != AccessControlType.Allow)
                throw new UnauthorizedAccessException("Unexpected data access rule");
            if (!sid.Equals(User) && !sid.Equals(SystemSid) && !sid.Equals(AdminSid))
                throw new UnauthorizedAccessException("Data is accessible by other users");
            if (sid.Equals(User) && (rule.FileSystemRights & FileSystemRights.ReadData) != 0) userCanRead = true;
        }
        if (!userCanRead) throw new UnauthorizedAccessException("Private access rule missing");
    }

    public static void CheckPrivateFile(string path, long limit)
    {
        using var stream = OpenPrivateFile(path, limit);
    }

    public static byte[] ReadPrivateFile(string path, long limit)
    {
        using var stream = OpenPrivateFile(path, limit);
        var bytes = new byte[checked((int)stream.Length)];
        stream.ReadExactly(bytes);
        if (stream.ReadByte() != -1) throw new IOException("Private file changed while reading");
        return bytes;
    }

    private static FileStream OpenPrivateFile(string path, long limit)
    {
        RejectReparseParents(path);
        var handle = CreateFile(path, 0x80000000, 1, IntPtr.Zero, 3, 0x00200000 | 0x08000000, IntPtr.Zero);
        if (handle.IsInvalid) { handle.Dispose(); throw new IOException("Cannot open private file"); }
        FileStream? stream = null;
        try
        {
            if (!GetFileInformationByHandle(handle, out var metadata) || metadata.NumberOfLinks != 1 ||
                (metadata.Attributes & (uint)(FileAttributes.Directory | FileAttributes.ReparsePoint)) != 0)
                throw new IOException("Private file must be a regular file with one link");
            stream = new FileStream(handle, FileAccess.Read);
            ValidateAcl(stream.GetAccessControl());
            if (stream.Length > limit) throw new IOException("Private file exceeds size limit");
            return stream;
        }
        catch { stream?.Dispose(); handle.Dispose(); throw; }
    }

    public static void AtomicWrite(string path, byte[] bytes, bool overwrite = true)
    {
        path = Path.GetFullPath(path);
        var parent = Path.GetDirectoryName(path) ?? throw new IOException("Invalid data directory");
        RejectReparseParents(path);
        ValidateAcl(new DirectoryInfo(parent).GetAccessControl());
        if (File.Exists(path)) CheckPrivateFile(path, long.MaxValue);
        var temporary = Path.Combine(parent, ".write-" + Guid.NewGuid().ToString("N") + ".tmp");
        try
        {
            using (var stream = new FileStream(temporary, FileMode.CreateNew, FileAccess.Write, FileShare.None))
            {
                stream.Write(bytes);
                stream.Flush(true);
            }
            CheckPrivateFile(temporary, bytes.LongLength);
            RejectReparseParents(path);
            File.Move(temporary, path, overwrite);
        }
        finally { if (File.Exists(temporary)) File.Delete(temporary); }
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct FileMetadata
    {
        public uint Attributes;
        public System.Runtime.InteropServices.ComTypes.FILETIME Creation, Access, Write;
        public uint Volume, SizeHigh, SizeLow, NumberOfLinks, IndexHigh, IndexLow;
    }
    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool GetFileInformationByHandle(SafeFileHandle handle, out FileMetadata metadata);
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern SafeFileHandle CreateFile(string name, uint access, uint share, IntPtr security,
        uint creation, uint flags, IntPtr template);
}
