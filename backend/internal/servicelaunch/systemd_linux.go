//go:build linux

package servicelaunch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/leapmux/leapmux/lifecycle"
)

// systemdManager maintains one systemd definition: a USER service
// integrated with the graphical-session lifecycle for login startup, or
// a system service with User= and Group= for boot startup.
type systemdManager struct {
	runSystemctl func(ctx context.Context, args []string, user bool, escalated bool) ([]byte, error)
	readFile     func(path string) ([]byte, error)
	writeFile    func(path string, data []byte, mode os.FileMode) error
	remove       func(path string) error
	groupOf      func(accountName string) string
}

func newSystemdManager() *systemdManager {
	return &systemdManager{
		runSystemctl: runSystemctlCommand,
		readFile:     os.ReadFile,
		writeFile:    os.WriteFile,
		remove:       os.Remove,
		groupOf:      primaryGroupOf,
	}
}

// runSystemctlCommand executes systemctl directly -- user-scoped for a
// login service, escalated for a system one. No shell string is built.
func runSystemctlCommand(ctx context.Context, args []string, user, escalated bool) ([]byte, error) {
	path := "/usr/bin/systemctl"
	if escalated {
		path = "/usr/bin/sudo"
		args = append([]string{"/usr/bin/systemctl"}, args...)
	}
	full := append(args, "--no-ask-password")
	if user {
		full = append(full, "--user")
	}
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, path, full...)
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, out.String())
	}
	return out.Bytes(), nil
}

// primaryGroupOf states the account's primary group for the system
// unit's Group=; the default mirrors the account name, the common
// single-user workstation shape.
func primaryGroupOf(accountName string) string {
	if accountName == "" {
		return ""
	}
	return accountName
}

// unitName derives the unit's name from the instance identity.
func systemdUnitName(spec LaunchSpec) string {
	return fmt.Sprintf("leapmux-%s-%s", spec.Mode, spec.InstanceID)
}

// definitionPath names the unit file: ~/.config/systemd/user for a
// login service, /etc/systemd/system for a boot one.
func (m *systemdManager) definitionPath(spec LaunchSpec) (string, error) {
	name := systemdUnitName(spec) + ".service"
	switch spec.Startup {
	case lifecycle.StartupLogin:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("servicelaunch: the account's home directory: %w", err)
		}
		return filepath.Join(home, ".config", "systemd", "user", name), nil
	case lifecycle.StartupBoot:
		return filepath.Join("/etc", "systemd", "system", name), nil
	default:
		return "", fmt.Errorf("servicelaunch: unknown startup kind %s", spec.Startup)
	}
}

// definitionUnit renders the unit. Enable uses `systemctl enable`
// WITHOUT --now, so nothing starts during configuration; the unit
// itself integrates with graphical-session.target for login mode.
func (m *systemdManager) definitionUnit(spec LaunchSpec) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "[Unit]\n")
	fmt.Fprintf(&b, "Description=LeapMux %s (%s startup)\n", spec.Mode, spec.Startup)
	if spec.Startup == lifecycle.StartupLogin {
		// The login service's lifetime follows the graphical session:
		// it starts when the desktop does and ends with the last one.
		fmt.Fprintf(&b, "PartOf=graphical-session.target\n")
		fmt.Fprintf(&b, "After=graphical-session.target\n")
	}
	fmt.Fprintf(&b, "\n[Service]\n")
	fmt.Fprintf(&b, "Type=simple\n")
	fmt.Fprintf(&b, "WorkingDirectory=%s\n", spec.WorkingDirectory)
	args := append([]string{spec.Executable}, spec.RunArguments()...)
	fmt.Fprintf(&b, "ExecStart=%s\n", quoteSystemdCommand(args))
	if len(spec.Environment()) > 0 {
		for _, entry := range spec.Environment() {
			fmt.Fprintf(&b, "Environment=%s\n", quoteSystemdValue(entry))
		}
	}
	if spec.EnvironmentFile != nil {
		fmt.Fprintf(&b, "EnvironmentFile=%s\n", *spec.EnvironmentFile)
	}
	// No Restart: a local process failure stays visible through status
	// until the user restarts it, and nothing fights `stop`.
	fmt.Fprintf(&b, "Restart=no\n")
	fmt.Fprintf(&b, "\n[Install]\n")
	if spec.Startup == lifecycle.StartupLogin {
		fmt.Fprintf(&b, "WantedBy=graphical-session.target\n")
	} else {
		fmt.Fprintf(&b, "WantedBy=multi-user.target\n")
		fmt.Fprintf(&b, "User=%s\n", spec.AccountName)
		fmt.Fprintf(&b, "Group=%s\n", m.groupOf(spec.AccountName))
	}
	return b.Bytes()
}

// quoteSystemdCommand renders the ExecStart argv: the executable plus
// each argument, quoted when it holds spaces or metacharacters.
func quoteSystemdCommand(args []string) string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		out = append(out, quoteSystemdValue(arg))
	}
	return strings.Join(out, " ")
}

// quoteSystemdValue quotes one value the unit file cannot hold bare.
func quoteSystemdValue(value string) string {
	if strings.ContainsAny(value, " \t\"'\\") {
		return `"` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`) + `"`
	}
	return value
}

func (m *systemdManager) Inspect(ctx context.Context, spec LaunchSpec) (NativeStatus, error) {
	path, err := m.definitionPath(spec)
	if err != nil {
		return NativeStatus{}, err
	}
	data, readErr := m.readFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return NativeStatus{State: "not installed"}, nil
		}
		return NativeStatus{}, readErr
	}
	status := NativeStatus{
		Installed:         true,
		DefinitionMatches: bytes.Equal(data, m.definitionUnit(spec)),
		State:             "installed",
	}
	user := spec.Startup == lifecycle.StartupLogin
	out, runErr := m.runSystemctl(ctx, []string{"is-active", systemdUnitName(spec)}, user, !user)
	switch strings.TrimSpace(string(out)) {
	case "active":
		status.Running, status.State = true, "active"
	case "activating":
		status.State = "activating"
	}
	_ = runErr
	return status, nil
}

func (m *systemdManager) Install(ctx context.Context, spec LaunchSpec) error {
	path, err := m.definitionPath(spec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("servicelaunch: create the definition directory: %w", err)
	}
	if err := m.writeFile(path, m.definitionUnit(spec), 0o644); err != nil {
		return fmt.Errorf("servicelaunch: write the definition: %w", err)
	}
	// enable WITHOUT --now: configuration never starts the service.
	// The controller's operation lock is NOT held while systemd does
	// its own; the daemon reload is what a start needs, and `start`
	// runs it again below.
	user := spec.Startup == lifecycle.StartupLogin
	if _, err := m.runSystemctl(ctx, []string{"daemon-reload"}, user, !user); err != nil {
		return err
	}
	_, err = m.runSystemctl(ctx, []string{"enable", systemdUnitName(spec)}, user, !user)
	return err
}

func (m *systemdManager) Start(ctx context.Context, spec LaunchSpec) error {
	user := spec.Startup == lifecycle.StartupLogin
	if _, err := m.runSystemctl(ctx, []string{"daemon-reload"}, user, !user); err != nil {
		return err
	}
	_, err := m.runSystemctl(ctx, []string{"start", systemdUnitName(spec)}, user, !user)
	return err
}

func (m *systemdManager) Stop(ctx context.Context, spec LaunchSpec) error {
	user := spec.Startup == lifecycle.StartupLogin
	// The service stays ENABLED: the definition answers the next login
	// or boot, only the running instance ends.
	_, err := m.runSystemctl(ctx, []string{"stop", systemdUnitName(spec)}, user, !user)
	return err
}

func (m *systemdManager) Remove(ctx context.Context, spec LaunchSpec) error {
	user := spec.Startup == lifecycle.StartupLogin
	_, _ = m.runSystemctl(ctx, []string{"disable", systemdUnitName(spec)}, user, !user)
	path, err := m.definitionPath(spec)
	if err != nil {
		return err
	}
	if err := m.remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("servicelaunch: remove the definition: %w", err)
	}
	_, err = m.runSystemctl(ctx, []string{"daemon-reload"}, user, !user)
	return err
}

func (m *systemdManager) Diagnose(ctx context.Context, spec LaunchSpec) ([]string, error) {
	user := spec.Startup == lifecycle.StartupLogin
	notes := []string{systemdUnitName(spec) + ".service"}
	if out, err := m.runSystemctl(ctx, []string{"status", systemdUnitName(spec), "--no-pager", "-l"}, user, !user); err == nil {
		notes = append(notes, systemdFirstLines(string(out), 12)...)
	} else {
		notes = append(notes, "systemctl status failed: "+err.Error())
	}
	return notes, nil
}

// systemdFirstLines bounds a diagnostic excerpt.
func systemdFirstLines(text string, n int) []string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return lines
}
