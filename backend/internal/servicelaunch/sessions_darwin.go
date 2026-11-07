//go:build darwin

package servicelaunch

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// darwinSessionMonitor lists the account's login sessions from the
// system's console login records. Shell sessions do not extend a desktop
// login. A locked desktop retains its console login record.
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
		if account.Name == "" {
			return nil, fmt.Errorf("the session account name is required")
		}
		if fields[0] != account.Name || fields[1] != "console" {
			continue
		}
		sessions = append(sessions, Session{ID: fields[1], Kind: SessionInteractive})
	}
	return sessions, nil
}
