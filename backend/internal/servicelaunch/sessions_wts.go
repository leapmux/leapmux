package servicelaunch

import (
	"fmt"
	"strings"
)

// windowsSessionMonitor lists the account's logon sessions from the WTS
// enumeration, matched by the account's SECURITY IDENTIFIER:
// interactive sessions count, DISCONNECTED sessions that remain logged
// on count, and service, listener, and batch contexts never do.
type windowsSessionMonitor struct {
	enumerate func() ([]windowsSessionEntry, error)
}

// windowsSessionEntry is a translated session with a verified account SID.
type windowsSessionEntry struct {
	ID   uint32
	Kind SessionKind
	SID  string
}

type wtsConnectState uint32

const (
	wtsActive wtsConnectState = iota
	wtsConnected
	wtsConnectQuery
	wtsShadow
	wtsDisconnected
	wtsIdle
	wtsListen
	wtsReset
	wtsDown
	wtsInit
)

type windowsSessionSnapshot struct {
	ID    uint32
	State wtsConnectState
}
type windowsSessionAccountReader interface {
	Username(uint32) (string, error)
	Domain(uint32) (string, error)
	SID(string) (string, error)
}

func translateWindowsSessions(snapshots []windowsSessionSnapshot, accounts windowsSessionAccountReader) ([]windowsSessionEntry, error) {
	var entries []windowsSessionEntry
	for _, s := range snapshots {
		if s.ID == 0 {
			continue
		}
		var kind SessionKind
		switch s.State {
		case wtsActive, wtsConnected, wtsIdle:
			kind = SessionInteractive
		case wtsDisconnected:
			kind = SessionDisconnected
		case wtsConnectQuery, wtsShadow, wtsListen, wtsReset, wtsDown, wtsInit:
			continue
		default:
			return nil, fmt.Errorf("the Windows state %d for session %d is unknown", s.State, s.ID)
		}
		username, err := accounts.Username(s.ID)
		if err != nil {
			return nil, fmt.Errorf("read session %d username: %w", s.ID, err)
		}
		if username == "" {
			// Connected and idle entries can have no logged-on account.
			// Active and disconnected entries still require verified identity.
			if s.State == wtsConnected || s.State == wtsIdle {
				continue
			}
			return nil, fmt.Errorf("the logged-on session's account could not be identified")
		}
		domain, err := accounts.Domain(s.ID)
		if err != nil {
			return nil, fmt.Errorf("read session %d domain: %w", s.ID, err)
		}
		account := username
		if domain != "" {
			account = domain + `\` + username
		}
		sid, err := accounts.SID(account)
		if err != nil {
			return nil, fmt.Errorf("resolve session %d account identifier: %w", s.ID, err)
		}
		if sid == "" {
			return nil, fmt.Errorf("the logged-on session's account has no security identifier")
		}
		entries = append(entries, windowsSessionEntry{ID: s.ID, Kind: kind, SID: sid})
	}
	return entries, nil
}

// Sessions lists the account's applicable sessions. A session the
// enumeration cannot attribute to an account returns an error, so the
// supervisor cannot mistake an unreadable list for final logout.
func (m *windowsSessionMonitor) Sessions(account AccountRef) ([]Session, error) {
	if !strings.HasPrefix(account.ID, "S-") {
		return nil, fmt.Errorf("the session account requires a security identifier")
	}
	all, err := m.enumerate()
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, entry := range all {
		if entry.ID == 0 || !entry.Kind.Applicable() {
			continue
		}
		if entry.SID == "" {
			return nil, fmt.Errorf("a logged-on session has no verified account identifier")
		}
		if entry.SID != account.ID {
			continue
		}
		out = append(out, Session{
			ID:   fmt.Sprintf("%d", entry.ID),
			Kind: entry.Kind,
		})
	}
	return out, nil
}
