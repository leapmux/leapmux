//go:build windows

package terminal

// processGroupOf always declines on Windows, which has no process groups.
//
// A Windows console has a job object and a console process list, and neither
// separates "the shell's own work" from "a job the user started" the way a
// process group does. Declining means every descendant is reported, which is
// the same answer this file's target gave before the group filter existed.
func processGroupOf(int) (int, processGroupResult) {
	return 0, processGroupRefused
}
