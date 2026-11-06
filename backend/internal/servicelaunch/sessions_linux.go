//go:build linux

package servicelaunch

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// linuxSessionMonitor lists the account's login sessions from `who`:
// every logged-in console, display, and remote session. A locked screen
// changes nothing here; a logged-out session leaves the list. Desktops
// that expose systemd's graphical-session lifecycle are what the login
// unit integrates with; where that lifecycle is unavailable, the
// controller explains and recommends boot mode for headless use.
type linuxSessionMonitor struct {
	runWho func() ([]byte, error)
}

func newLinuxSessionMonitor() *linuxSessionMonitor {
	return &linuxSessionMonitor{runWho: func() ([]byte, error) {
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
func (m *linuxSessionMonitor) Sessions(account AccountRef) ([]Session, error) {
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
		if account.Name != "" && fields[0] != account.Name {
			continue
		}
		sessions = append(sessions, Session{ID: fields[1], Kind: SessionInteractive})
	}
	return sessions, nil
}
