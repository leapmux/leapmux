//go:build darwin

package servicelaunch

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// darwinSessionMonitor lists the account's login sessions from the
// system's own session records: `who` names every logged-in console
// and remote session of every user, filtered to the account. Locking
// the screen produces no change here, and a disconnected remote
// desktop session stays listed until it logs out -- exactly the
// last-session rule's inputs.
type darwinSessionMonitor struct {
	// runWho runs the who command; a test substitutes it.
	runWho func() ([]byte, error)
}

func newDarwinSessionMonitor() *darwinSessionMonitor {
	return &darwinSessionMonitor{runWho: func() ([]byte, error) {
		var out bytes.Buffer
		cmd := exec.Command("/usr/bin/who")
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			return out.Bytes(), fmt.Errorf("who: %w: %s", err, out.String())
		}
		return out.Bytes(), nil
	}}
}

// Sessions lists the account's interactive sessions.
func (m *darwinSessionMonitor) Sessions(account AccountRef) ([]Session, error) {
	out, err := m.runWho()
	if err != nil {
		return nil, err
	}
	var sessions []Session
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// `who` spells the account by name; the console and the ttys of
		// one account all list alike. Every entry `who` reports is an
		// interactive login -- a locked screen stays listed, and a
		// logged-out session leaves it.
		if account.Name != "" && fields[0] != account.Name {
			continue
		}
		sessions = append(sessions, Session{ID: fields[1], Kind: SessionInteractive})
	}
	return sessions, nil
}
