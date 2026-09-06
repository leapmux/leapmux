//go:build !windows

package terminal

import "syscall"

// processGroupOf returns pid's process group, and whether the OS answered.
//
// One syscall, and only for a process the walk already found beneath the shell
// -- never for the whole process table.
func processGroupOf(pid int) (int, bool) {
	group, err := syscall.Getpgid(pid)
	if err != nil {
		// The process exited between the scan and this read, or it belongs to
		// another user. Either way there is no group to compare.
		return 0, false
	}
	return group, true
}
