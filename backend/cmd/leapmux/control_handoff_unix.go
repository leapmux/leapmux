//go:build !windows

package main

import "syscall"

// execControlCLI replaces this process with target, which keeps the process
// id, the standard streams and the controlling terminal. It returns only when
// the replacement fails, and then nothing of target ran.
func execControlCLI(target string, argv, env []string) (int, error) {
	return 0, syscall.Exec(target, argv, env)
}
