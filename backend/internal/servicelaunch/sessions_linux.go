//go:build linux

package servicelaunch

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// linuxSessionMonitor reads graphical sessions from systemd-logind.
// SSH, greeter and batch sessions do not extend desktop login lifetime.
type linuxSessionMonitor struct {
	runLoginctl func([]string) ([]byte, error)
}

func newLinuxSessionMonitor() *linuxSessionMonitor {
	return &linuxSessionMonitor{runLoginctl: func(args []string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, "/usr/bin/loginctl", args...).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("read desktop sessions: %w", err)
		}
		return output, nil
	}}
}

func (m *linuxSessionMonitor) Sessions(account AccountRef) ([]Session, error) {
	if account.ID == "" {
		return nil, fmt.Errorf("the session account identifier is required")
	}
	output, err := m.runLoginctl([]string{"list-sessions", "--no-legend", "--no-pager"})
	if err != nil {
		return nil, err
	}
	var sessions []Session
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if strings.HasPrefix(fields[0], "-") {
			return nil, fmt.Errorf("the session list contains an invalid identifier")
		}
		details, err := m.runLoginctl([]string{"show-session", fields[0], "--no-pager", "--property=User", "--property=Type", "--property=Class", "--property=State"})
		if err != nil {
			return nil, err
		}
		properties := nativeProperties(details)
		if properties["User"] == "" {
			return nil, fmt.Errorf("the session owner could not be identified")
		}
		if properties["User"] != account.ID {
			continue
		}
		if properties["Type"] == "" || properties["Class"] == "" || properties["State"] == "" {
			return nil, fmt.Errorf("the session type or state could not be identified")
		}
		switch properties["Class"] {
		case "user", "user-early", "user-light":
		default:
			continue
		}
		switch properties["Type"] {
		case "x11", "wayland", "mir":
		default:
			continue
		}
		// A disconnected remote desktop retains its logind session. Neither
		// inactivity nor screen lock removes it from the account's session set.
		sessions = append(sessions, Session{ID: fields[0], Kind: SessionInteractive})
	}
	return sessions, nil
}

func nativeProperties(data []byte) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			result[key] = value
		}
	}
	return result
}
