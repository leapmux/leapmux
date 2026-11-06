//go:build windows

package servicelaunch

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// windowsTaskManager maintains the login-mode definition: a
// NONINTERACTIVE scheduled task triggered at the account's logon. The
// task's logon uses the account's credentials -- the password-free S4U
// alternative was rejected because it restricts network and encrypted
// file access; the batch-logon right the task needs is part of what
// `enable` explains.
type windowsTaskManager struct {
	runSchtasks func(ctx context.Context, args []string) ([]byte, error)
	// credential supplies the account password at the native call
	// alone, so Install keeps the Manager interface and the value
	// never sits in a saved specification or a log.
	credential func() string
}

func newWindowsTaskManager(credential func() string) *windowsTaskManager {
	return &windowsTaskManager{runSchtasks: runSchtasksCommand, credential: credential}
}

// runSchtasksCommand executes schtasks directly.
func runSchtasksCommand(ctx context.Context, args []string) ([]byte, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "schtasks.exe", args...)
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("schtasks %s: %w: %s", strings.Join(args, " "), err, out.String())
	}
	return out.Bytes(), nil
}

// taskName derives the task's name from the instance identity.
func taskName(spec LaunchSpec) string {
	return `leapmux-` + string(spec.Mode) + `-` + spec.InstanceID
}

func (m *windowsTaskManager) Inspect(ctx context.Context, spec LaunchSpec) (NativeStatus, error) {
	out, err := m.runSchtasks(ctx, []string{"/query", "/tn", taskName(spec), "/v", "/fo", "list"})
	if err != nil {
		return NativeStatus{State: "not installed"}, nil
	}
	status := NativeStatus{Installed: true, State: "installed"}
	text := string(out)
	if strings.Contains(text, "Ready") || strings.Contains(text, "Running") {
		status.State = "ready"
	}
	if strings.Contains(text, "Running") {
		status.Running = true
	}
	status.DefinitionMatches = strings.Contains(strings.ToLower(text), strings.ToLower(spec.Executable))
	return status, nil
}

// Install creates the task WITHOUT running it. The password travels to
// the native API through the parameters of schtasks -- never in the
// saved specification, never in a log.
func (m *windowsTaskManager) Install(ctx context.Context, spec LaunchSpec) error {
	args := []string{
		"/create", "/tn", taskName(spec),
		"/tr", taskCommand(spec),
		"/sc", "onlogon",
		"/ru", accountSpelling(spec),
		"/it",
	}
	if m.credential != nil {
		if password := m.credential(); password != "" {
			args = append(args, "/rp", password)
		}
	}
	_, err := m.runSchtasks(ctx, args)
	return err
}

// taskCommand renders the run command the task executes.
func taskCommand(spec LaunchSpec) string {
	return strings.Join(append([]string{spec.Executable}, spec.RunArguments()...), " ")
}

func (m *windowsTaskManager) Start(ctx context.Context, spec LaunchSpec) error {
	_, err := m.runSchtasks(ctx, []string{"/run", "/tn", taskName(spec)})
	return err
}

func (m *windowsTaskManager) Stop(ctx context.Context, spec LaunchSpec) error {
	_, err := m.runSchtasks(ctx, []string{"/end", "/tn", taskName(spec)})
	return err
}

func (m *windowsTaskManager) Remove(ctx context.Context, spec LaunchSpec) error {
	_, err := m.runSchtasks(ctx, []string{"/delete", "/tn", taskName(spec), "/f"})
	return err
}

func (m *windowsTaskManager) Diagnose(ctx context.Context, spec LaunchSpec) ([]string, error) {
	notes := []string{taskName(spec)}
	if out, err := m.runSchtasks(ctx, []string{"/query", "/tn", taskName(spec), "/v", "/fo", "list"}); err == nil {
		notes = append(notes, firstWindowsLines(string(out), 12)...)
	} else {
		notes = append(notes, "schtasks query failed: "+err.Error())
	}
	return notes, nil
}
