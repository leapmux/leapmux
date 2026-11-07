//go:build linux

package servicelaunch

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// linuxSessionMonitor reads graphical sessions from systemd-logind.
// SSH, greeter and batch sessions do not extend desktop login lifetime.
type linuxSessionMonitor struct {
	runLoginctl func(context.Context, []string) ([]byte, error)
}

func newLinuxSessionMonitor() *linuxSessionMonitor {
	return &linuxSessionMonitor{runLoginctl: func(ctx context.Context, args []string) ([]byte, error) {
		output, err := exec.CommandContext(ctx, "/usr/bin/loginctl", args...).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("read desktop sessions: %w", err)
		}
		return output, nil
	}}
}

func (m *linuxSessionMonitor) Sessions(ctx context.Context, account AccountRef) ([]Session, error) {
	ctx, cancel := context.WithTimeout(ctx, sessionQueryTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	accountUID, err := strconv.ParseUint(account.ID, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("the session account requires a numeric identifier")
	}
	output, err := m.runLoginctl(ctx, []string{"list-sessions", "--no-legend", "--no-pager"})
	if err != nil {
		return nil, err
	}
	var sessions []Session
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 2 || strings.HasPrefix(fields[0], "-") {
			return nil, fmt.Errorf("the session list contains an invalid row")
		}
		uid, err := strconv.ParseUint(fields[1], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("the session list contains an invalid account identifier")
		}
		if uid != accountUID {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		details, err := m.runLoginctl(ctx, []string{"show-session", fields[0], "--no-pager", "--property=User", "--property=Type", "--property=Class", "--property=State"})
		if err != nil {
			return nil, err
		}
		properties := nativeProperties(details)
		ownerUID, ownerErr := strconv.ParseUint(properties["User"], 10, 32)
		if ownerErr != nil {
			return nil, fmt.Errorf("the session owner could not be identified")
		}
		if ownerUID != accountUID {
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
