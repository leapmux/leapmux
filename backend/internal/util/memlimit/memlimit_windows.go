package memlimit

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// memoryStatusEx mirrors MEMORYSTATUSEX. x/sys/windows does not declare
// GlobalMemoryStatusEx, so the struct and the call are spelled out here rather
// than pulling in a dependency for one field.
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

func physicalMemory() (int64, error) {
	status := memoryStatusEx{}
	status.length = uint32(unsafe.Sizeof(status))
	ret, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if ret == 0 {
		return 0, err
	}
	return sanitizePhysicalMemory(status.totalPhys)
}
