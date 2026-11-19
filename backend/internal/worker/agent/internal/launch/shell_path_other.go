//go:build !windows

package launch

import "os/exec"

// RefreshCommandPath is a no-op on platforms where a shell reads its profile
// to update PATH.
func RefreshCommandPath(*exec.Cmd) error { return nil }

func resolvePreparedShell(_ *exec.Cmd, shell string) (string, error) { return shell, nil }
