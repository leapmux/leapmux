//go:build windows

package servicelaunch

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

func newWindowsSessionMonitor() *windowsSessionMonitor {
	return &windowsSessionMonitor{enumerate: enumerateSessions}
}

var querySessionInformation = windows.NewLazySystemDLL("wtsapi32.dll").NewProc("WTSQuerySessionInformationW")

func sessionAccountField(sessionID, field uint32) (string, error) {
	var buffer *uint16
	var size uint32
	success, _, nativeErr := querySessionInformation.Call(0, uintptr(sessionID), uintptr(field), uintptr(unsafe.Pointer(&buffer)), uintptr(unsafe.Pointer(&size)))
	if success == 0 {
		return "", fmt.Errorf("read session account information: %w", windowsCallFailure(nativeErr))
	}
	if buffer == nil {
		return "", fmt.Errorf("Windows returned no session account information")
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(buffer)))
	if size < 2 || size%2 != 0 {
		return "", fmt.Errorf("Windows returned invalid session account information")
	}
	return windows.UTF16ToString(unsafe.Slice(buffer, int(size/2))), nil
}

// enumerateSessions reads the machine's sessions through WTS.
func enumerateSessions() ([]windowsSessionEntry, error) {
	return enumerateSessionsWith(windowsSessionCalls{enumerate: windows.WTSEnumerateSessions, free: windows.WTSFreeMemory})
}

// windowsSessionCalls keeps native enumeration and its buffer release together.
type windowsSessionCalls struct {
	enumerate func(windows.Handle, uint32, uint32, **windows.WTS_SESSION_INFO, *uint32) error
	free      func(uintptr)
}

func enumerateSessionsWith(calls windowsSessionCalls) ([]windowsSessionEntry, error) {
	var info *windows.WTS_SESSION_INFO
	var count uint32
	// WTS_CURRENT_SERVER_HANDLE is NULL, not INVALID_HANDLE_VALUE.
	if err := calls.enumerate(0, 0, 1, &info, &count); err != nil {
		return nil, fmt.Errorf("enumerate sessions: %w", err)
	}
	if info != nil {
		defer calls.free(uintptr(unsafe.Pointer(info)))
	}
	if count == 0 {
		return nil, nil
	}
	if info == nil || count > 1<<20 {
		return nil, fmt.Errorf("Windows returned an unreadable session list")
	}
	sessions := unsafe.Slice(info, int(count))
	snapshots := make([]windowsSessionSnapshot, 0, len(sessions))
	for _, session := range sessions {
		snapshots = append(snapshots, windowsSessionSnapshot{ID: session.SessionID, State: wtsConnectState(session.State)})
	}
	return translateWindowsSessions(snapshots, nativeWindowsSessionAccounts{})
}

type nativeWindowsSessionAccounts struct{}

func (nativeWindowsSessionAccounts) Username(id uint32) (string, error) {
	return sessionAccountField(id, 5)
}
func (nativeWindowsSessionAccounts) Domain(id uint32) (string, error) {
	return sessionAccountField(id, 7)
}
func (nativeWindowsSessionAccounts) SID(account string) (string, error) {
	sid, _, _, err := windows.LookupSID("", account)
	if err != nil {
		return "", err
	}
	return sid.String(), nil
}
