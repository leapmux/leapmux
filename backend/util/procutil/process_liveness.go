package procutil

import (
	"bytes"
	"strconv"
)

// LiveProcessInfo reports the liveness and opaque start token of a local
// process from one OS inspection.
//
// StartToken identifies one process instance for PID-reuse detection on the
// same host. Its unit and epoch depend on the operating system (Darwin
// microseconds, Linux /proc/<pid>/stat field 22 ticks, Windows GetProcessTimes
// FILETIME ticks, or Unix fallback milliseconds). Zero means the start token
// could not be read or the process is not alive.
//
// Compare StartToken only against another StartToken from InspectLiveProcess on
// the same host. Never store StartToken in ProcessIdentity.StartTime, which
// holds Unix milliseconds.
type LiveProcessInfo struct {
	// Alive is true when the process exists and is not a zombie or dead task.
	// When OS permissions prevent reading the process record (for example,
	// EPERM or ERROR_ACCESS_DENIED), Alive is true conservatively and
	// StartToken is zero.
	Alive bool
	// StartToken is a positive, opaque, platform-specific token for the
	// process start time, or zero when unavailable.
	StartToken int64
}

// SameProcess reports whether info describes a live process that matches
// expectedStartToken.
//
// When expectedStartToken is positive, info must be alive and carry the exact
// same StartToken; a missing token returns false so PID reuse by another owner
// does not look like the original process. When expectedStartToken is zero or
// negative, any live process matches.
func (info LiveProcessInfo) SameProcess(expectedStartToken int64) bool {
	if !info.Alive {
		return false
	}
	if expectedStartToken <= 0 {
		return true
	}
	return info.StartToken == expectedStartToken
}

// parseLinuxLiveProcessStat parses process state (field 3) and starttime ticks
// (field 22) from a Linux /proc/<pid>/stat payload in a single pass.
// Unrecognized or truncated payloads return Alive: true conservatively without
// a start token.
func parseLinuxLiveProcessStat(data []byte) LiveProcessInfo {
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return LiveProcessInfo{Alive: true}
	}
	fields := bytes.Fields(data[end+1:])
	if len(fields) == 0 || len(fields[0]) == 0 {
		return LiveProcessInfo{Alive: true}
	}
	switch fields[0][0] {
	case 'Z', 'X', 'x':
		return LiveProcessInfo{}
	}
	// Field 3 (state) is fields[0], so field 22 (starttime) is fields[19].
	if len(fields) >= 20 {
		if ticks, err := strconv.ParseInt(string(fields[19]), 10, 64); err == nil && ticks > 0 {
			return LiveProcessInfo{Alive: true, StartToken: ticks}
		}
	}
	return LiveProcessInfo{Alive: true}
}
