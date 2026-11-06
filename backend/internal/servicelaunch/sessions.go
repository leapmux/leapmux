package servicelaunch

import (
	"fmt"
	"time"
)

// SessionKind states what one login session of the account is.
type SessionKind string

const (
	// SessionInteractive is an interactive desktop login.
	SessionInteractive SessionKind = "interactive"
	// SessionDisconnected is a remote desktop session that disconnected
	// without logging out: it remains logged on.
	SessionDisconnected SessionKind = "disconnected"
	// SessionService is a service context, not a login.
	SessionService SessionKind = "service"
	// SessionBatch is a batch logon, not an interactive login.
	SessionBatch SessionKind = "batch"
	// SessionListener is a listener session, not a login.
	SessionListener SessionKind = "listener"
)

// Applicable reports whether a session of this kind counts toward the
// account's login-session set: interactive sessions do, disconnected
// ones still do (they remain logged on), and service, batch, and
// listener contexts never do.
func (k SessionKind) Applicable() bool {
	return k == SessionInteractive || k == SessionDisconnected
}

// Session is one login session of the account.
type Session struct {
	// ID identifies the session to the platform.
	ID string
	// Kind states the session's class.
	Kind SessionKind
	// StartedAt is when the session began.
	StartedAt time.Time
}

// AccountRef identifies the service account on both axes a platform
// matches sessions by: the numeric or SID identifier, and the login
// name. Darwin's `who` matches by name; Windows matches by SID.
type AccountRef struct {
	ID   string
	Name string
}

// SessionMonitor lists the account's login sessions, the input of the
// last-session rule. Login-mode instances stop only after the LAST
// applicable session ends; locking the screen and disconnecting a
// remote session do not end one.
type SessionMonitor interface {
	// Sessions lists the account's sessions now. An enumeration error
	// is NEVER "zero sessions": the caller must not stop an instance on
	// a failed lookup.
	Sessions(account AccountRef) ([]Session, error)
}

// LastSessionRule decides whether a login-mode instance may stop, from
// the account's session list. One instance serves every applicable
// session; opening another creates no second instance; the instance
// stops only after the last applicable session ends.
func LastSessionRule(sessions []Session, enumerateErr error) (mayStop bool, reason string) {
	if enumerateErr != nil {
		return false, fmt.Sprintf("the session list could not be read (%v); an unreadable list is never an empty one", enumerateErr)
	}
	applicable := 0
	for _, s := range sessions {
		if s.Kind.Applicable() {
			applicable++
		}
	}
	if applicable == 0 {
		return true, "the last applicable session ended"
	}
	return false, fmt.Sprintf("%d applicable session(s) remain", applicable)
}
