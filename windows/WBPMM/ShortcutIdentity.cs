using System.Runtime.InteropServices;
using System.Runtime.InteropServices.ComTypes;

namespace WBPMM;

internal static class ShortcutIdentity
{
    internal const string AppId = "WBPMM.Desktop";
    private static readonly Guid ShellLinkClass = new("00021401-0000-0000-C000-000000000046");
    private static PropertyKey Key => new() { FormatId = new("9F4C2855-9F79-4B39-A8D0-E1D42DE1D5F3"), Id = 5 };

    internal static int Register()
    {
        try
        {
            Set(Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.Programs), "WB-PMM", "WB-PMM.lnk"));
            Set(Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.DesktopDirectory), "WB-PMM.lnk"));
            return 0;
        }
        catch (Exception error)
        {
            // Installer diagnostics only: never emit the user profile path or exception message.
            try { Console.WriteLine($"shortcut_identity_hresult=0x{error.HResult:X8}"); } catch (IOException) { }
            return error.HResult != 0 ? error.HResult : 21;
        }
    }

    internal static void Set(string path)
    {
        if (!File.Exists(path)) throw new FileNotFoundException();
        var link = Open(path, 2); // STGM_READWRITE: update an existing shortcut.
        try
        {
            var key = Key;
            var value = new PropVariant { Type = 31, Pointer = Marshal.StringToCoTaskMemUni(AppId) };
            try
            {
                var store = (IPropertyStore)link;
                Marshal.ThrowExceptionForHR(store.SetValue(ref key, ref value));
                Marshal.ThrowExceptionForHR(store.Commit());
                ((IPersistFile)link).Save(path, true);
            }
            finally { PropVariantClear(ref value); }
        }
        finally { Marshal.FinalReleaseComObject(link); }
        if (Read(path) != AppId) throw new IOException("Shortcut identity was not persisted");
    }

    internal static string? Read(string path)
    {
        var link = Open(path, 0);
        try
        {
            var key = Key;
            Marshal.ThrowExceptionForHR(((IPropertyStore)link).GetValue(ref key, out var value));
            try { return value.Type == 31 ? Marshal.PtrToStringUni(value.Pointer) : null; }
            finally { PropVariantClear(ref value); }
        }
        finally { Marshal.FinalReleaseComObject(link); }
    }

    private static object Open(string path, int mode)
    {
        var link = Activator.CreateInstance(Type.GetTypeFromCLSID(ShellLinkClass, throwOnError: true)!)!;
        try { ((IPersistFile)link).Load(path, mode); return link; }
        catch { Marshal.FinalReleaseComObject(link); throw; }
    }

    [StructLayout(LayoutKind.Sequential, Pack = 4)]
    private struct PropertyKey { public Guid FormatId; public uint Id; }
    [StructLayout(LayoutKind.Explicit, Size = 24)]
    private struct PropVariant { [FieldOffset(0)] public ushort Type; [FieldOffset(8)] public IntPtr Pointer; }
    [ComImport, Guid("886D8EEB-8CF2-4446-8D02-CDBA1DBDCF99"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    private interface IPropertyStore
    {
        [PreserveSig] int GetCount(out uint count);
        [PreserveSig] int GetAt(uint index, out PropertyKey key);
        [PreserveSig] int GetValue(ref PropertyKey key, out PropVariant value);
        [PreserveSig] int SetValue(ref PropertyKey key, ref PropVariant value);
        [PreserveSig] int Commit();
    }
    [DllImport("ole32.dll")]
    private static extern int PropVariantClear(ref PropVariant value);
}
