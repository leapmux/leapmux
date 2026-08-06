//go:build !linux && !darwin && !windows

package memlimit

// physicalMemory is not implemented for this platform, so -- with cgroupPaths
// empty here as on every non-Linux GOOS (see cgroup_unsupported.go) -- Detect
// falls back to GOMEMLIMIT or FallbackBytes. Building here is deliberate: a hub
// on an unlisted GOOS should start with a conservative default, not fail to
// compile.
func physicalMemory() (int64, error) { return 0, errNoLimit }
