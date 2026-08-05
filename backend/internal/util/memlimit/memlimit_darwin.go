package memlimit

import "golang.org/x/sys/unix"

func physicalMemory() (int64, error) {
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, err
	}
	return sanitizePhysicalMemory(total)
}
