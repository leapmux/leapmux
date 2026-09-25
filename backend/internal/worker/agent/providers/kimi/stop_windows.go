//go:build windows

package kimi

// kimiDescendantGroups lists nothing on Windows: the server starts its commands
// without detaching them there, so they stay in the job object the worker
// assigned the server to, and the job's teardown ends them.
func kimiDescendantGroups(int) []int { return nil }

// killKimiGroups has nothing to kill on Windows. See kimiDescendantGroups.
func killKimiGroups([]int) {}
