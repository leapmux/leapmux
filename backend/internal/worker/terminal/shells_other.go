//go:build !darwin && !linux

package terminal

// detectDefaultShell returns /bin/sh on unsupported platforms.
func detectDefaultShell() string {
	return "/bin/sh"
}
