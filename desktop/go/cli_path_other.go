//go:build !darwin

package main

import (
	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
)

// CLI PATH integration is macOS-only. On Windows the installer writes
// HKCU\Environment\Path directly; on Linux the .deb drops /usr/bin/leapmux
// during package install. Both happen outside the running app, so the
// sidecar has nothing to do for those platforms.

func cliPathStatusFromSidecar() *desktoppb.CliPathStatusResponse {
	return &desktoppb.CliPathStatusResponse{
		State: desktoppb.CliPathStatusResponse_STATE_UNAVAILABLE,
	}
}

func cliInstallSymlinkFromSidecar(_ bool) *desktoppb.CliInstallSymlinkResponse {
	return &desktoppb.CliInstallSymlinkResponse{
		Result:  desktoppb.CliInstallSymlinkResponse_RESULT_IO_ERROR,
		Message: "CLI symlink install is supported only on macOS",
	}
}
