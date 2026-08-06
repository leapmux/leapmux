//go:build !linux

package memlimit

// cgroupPaths is empty everywhere but Linux: no other platform has cgroups, so
// the shared walk reports "no limit configured" and Detect falls through to
// physical memory rather than treating a perfectly healthy Mac (or Windows
// server, or BSD box) as a machine it failed to probe.
//
// One file for every non-Linux GOOS rather than a copy per platform: the stub
// was byte-identical in the darwin, windows and "other" files, and a build
// constraint that leaves exactly one definition on every target is the mechanism
// that keeps it that way -- adding a platform file with its own physicalMemory
// no longer has to remember this half.
func cgroupPaths() []string { return nil }
