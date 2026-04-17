//go:build !darwin && !linux && !windows

package terminal

// detectDefaultShell returns /bin/sh on unsupported platforms.
func detectDefaultShell() string {
	return "/bin/sh"
}
