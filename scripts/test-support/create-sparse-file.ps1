$ErrorActionPreference = 'Stop'

# FSCTL_SET_SPARSE marks the owned file before SetLength changes its logical size.
# https://learn.microsoft.com/en-us/windows/win32/api/winioctl/ni-winioctl-fsctl_set_sparse
Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.IO;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;

public static class SparseFileFixture
{
    private const uint FsctlSetSparse = 0x000900c4;

    [DllImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool DeviceIoControl(
        SafeFileHandle file, uint controlCode,
        IntPtr input, uint inputSize, IntPtr output, uint outputSize,
        out uint returnedBytes, IntPtr overlapped);

    public static void Create(string path, long length)
    {
        using (var file = new FileStream(path, FileMode.Create, FileAccess.ReadWrite, FileShare.Read))
        {
            uint returnedBytes;
            if (!DeviceIoControl(file.SafeFileHandle, FsctlSetSparse,
                IntPtr.Zero, 0, IntPtr.Zero, 0, out returnedBytes, IntPtr.Zero))
            {
                throw new Win32Exception(Marshal.GetLastWin32Error());
            }
            file.SetLength(length);
        }
    }
}
'@

[SparseFileFixture]::Create($env:LEAPMUX_TEST_SPARSE_FILE, [long]$env:LEAPMUX_TEST_SPARSE_SIZE)
