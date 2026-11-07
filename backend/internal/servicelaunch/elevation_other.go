//go:build !windows

package servicelaunch

// RunNativeHelper leaves command dispatch unchanged on operating systems
// whose native configuration uses direct privileged commands.
func RunNativeHelper([]string) (bool, error) { return false, nil }
