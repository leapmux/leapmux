//go:build windows

package servicelaunch

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// windowsSessionMonitor lists the account's logon sessions from the WTS
// enumeration, matched by the account's SECURITY IDENTIFIER:
// interactive sessions count, DISCONNECTED sessions that remain logged
// on count, and service, listener, and batch contexts never do.
type windowsSessionMonitor struct {
	enumerate func() ([]windowsSessionEntry, error)
}

// windowsSessionEntry is one enumerated session.
type windowsSessionEntry struct {
	ID       uint32
	Kind     SessionKind
	SID      string
	Username string
}

func newWindowsSessionMonitor() *windowsSessionMonitor {
	return &windowsSessionMonitor{enumerate: enumerateSessions}
}

// The WTS connect states the plan's table maps: WTSActive is an
// interactive login, WTSDisconnected a remote session that stayed
// logged on, and every other state a context that never counts.
const (
	wtsActive       = 0
	wtsDisconnected = 4
)

// enumerateSessions reads the machine's sessions through WTS.
func enumerateSessions() ([]windowsSessionEntry, error) {
	var info *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(windows.InvalidHandle, 0, 1, &info, &count); err != nil {
		return nil, fmt.Errorf("enumerate sessions: %w", err)
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(info)))
	if info == nil || count == 0 {
		return nil, nil
	}
	sessions := unsafe.Slice(info, int(count))
	entries := make([]windowsSessionEntry, 0, len(sessions))
	for _, s := range sessions {
		kind := SessionKind("other")
		switch s.State {
		case wtsActive:
			kind = SessionInteractive
		case wtsDisconnected:
			kind = SessionDisconnected
		}
		entries = append(entries, windowsSessionEntry{
			ID:       s.SessionID,
			Kind:     kind,
			Username: windows.UTF16PtrToString(s.WindowStationName),
		})
	}
	return entries, nil
}

// Sessions lists the account's applicable sessions. A session the
// enumeration cannot attribute to the account stays out: the rule's
// safe direction keeps a possibly-live instance running.
func (m *windowsSessionMonitor) Sessions(account AccountRef) ([]Session, error) {
	all, err := m.enumerate()
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, entry := range all {
		if !entry.Kind.Applicable() {
			continue
		}
		// The SID match is the authority; the name narrows the lookup
		// where the enumeration reports one.
		if account.ID != "" && entry.SID != "" && entry.SID != account.ID {
			continue
		}
		if account.ID == "" && account.Name != "" && entry.Username != account.Name {
			continue
		}
		out = append(out, Session{
			ID:   fmt.Sprintf("%d", entry.ID),
			Kind: entry.Kind,
		})
	}
	return out, nil
}
