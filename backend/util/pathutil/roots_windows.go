//go:build windows

package pathutil

import (
	"os"

	"golang.org/x/sys/windows"
)

// FilesystemRoots returns the drive roots that currently carry a volume.
//
// GetLogicalDrives, not GetLogicalDriveStringsW: the bitmask needs no
// buffer-sizing round trip, no UTF-16 decode and no double-NUL split, and its
// result is one uint32 -- which is what lets drivesFromBitmask, and therefore
// the whole filter rule, be unit-tested on a machine that has no drives.
//
// x/sys reports a zero mask as an error. No Windows host has no drives, so
// this answers with the system drive rather than an empty list, which a
// directory picker would render as "nowhere to browse".
func FilesystemRoots() []string {
	mask, err := windows.GetLogicalDrives()
	if err != nil || mask == 0 {
		return []string{systemDriveRoot()}
	}
	roots := drivesFromBitmask(mask, driveTypeOf)
	if len(roots) == 0 {
		return []string{systemDriveRoot()}
	}
	return roots
}

// systemDriveRoot is the last-resort answer: the drive Windows booted from.
//
// Read from the environment rather than written as "C:\", because a
// Windows-To-Go install or one relettered with bcdedit does not boot from C.
func systemDriveRoot() string {
	if drive := os.Getenv("SystemDrive"); drive != "" {
		return drive + `\`
	}
	return `C:\`
}

// driveTypeOf is the real GetDriveTypeW.
//
// It is safe to call in a loop, which GetVolumeInformationW would not be.
// GetDriveTypeW resolves a REMOTE letter through the local redirector's
// mapping table rather than through the server, and it reports a REMOVABLE or
// CDROM device class without touching the media -- so an empty optical drive
// and a disconnected mapped drive both answer promptly. The calls that wait
// out the SMB timeout are GetVolumeInformationW and GetDiskFreeSpaceExW, and
// this file makes neither. The loop also makes at most 26 calls.
//
// If a volume label is ever added to the response, that changes: the label
// needs GetVolumeInformationW, and a timeout wrapper then becomes mandatory.
func driveTypeOf(root string) uint32 {
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		// Kept by the filter, which drops DRIVE_NO_ROOT_DIR alone.
		return driveUnknown
	}
	return windows.GetDriveType(p)
}
