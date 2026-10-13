package testutil

import "runtime"

// PrivateFilePerm answers the permission bits that a private (owner-only)
// file reports on this platform. POSIX answers the exact 0o600 that the
// private writes request; Windows keeps no owner-only bits -- chmod there
// clears only the read-only flag, so a writable file reports 0o666.
func PrivateFilePerm() uint32 {
	if runtime.GOOS == "windows" {
		return 0o666
	}
	return 0o600
}

// PrivateDirPerm answers the permission bits that a private (owner-only)
// directory reports on this platform, for the same reason as
// PrivateFilePerm: Windows reports 0o777 for a writable directory.
func PrivateDirPerm() uint32 {
	if runtime.GOOS == "windows" {
		return 0o777
	}
	return 0o700
}
