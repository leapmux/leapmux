//go:build linux

package servicelaunch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/lifecycle"
)

// systemdManager maintains one systemd definition: a USER service
// integrated with the graphical-session lifecycle for login startup, or
// a system service with User= and Group= for boot startup.
type systemdManager struct {
	runSystemctl func(ctx context.Context, args []string, user bool, escalated bool) ([]byte, error)
	readFile     func(path string) ([]byte, error)
	writeFile    func(context.Context, LaunchSpec, string, []byte) error
	remove       func(context.Context, LaunchSpec, string) error
	groupOf      func(accountName string) string
}

func newSystemdManager() *systemdManager {
	return &systemdManager{
		runSystemctl: runSystemctlCommand,
		readFile:     os.ReadFile,
		writeFile:    publishNativeDefinition,
		remove:       removeNativeDefinition,
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
// unit's Group=, using the operating system account database.
func primaryGroupOf(accountName string) string {
	account, err := user.Lookup(accountName)
	if err != nil {
		return ""
	}
	return account.Gid
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
		fmt.Fprintf(&b, "After=graphical-session.target\n")
	}
	fmt.Fprintf(&b, "\n[Service]\n")
	fmt.Fprintf(&b, "Type=simple\n")
	fmt.Fprintf(&b, "WorkingDirectory=%s\n", quoteSystemdValue(spec.WorkingDirectory))
	if spec.Startup == lifecycle.StartupBoot {
		fmt.Fprintf(&b, "User=%s\n", quoteSystemdValue(spec.AccountName))
		fmt.Fprintf(&b, "Group=%s\n", quoteSystemdValue(m.groupOf(spec.AccountName)))
	}
	args := append([]string{spec.Executable}, spec.RunArguments()...)
	fmt.Fprintf(&b, "ExecStart=%s\n", quoteSystemdCommand(args))
	// No Restart: a local process failure stays visible through status
	// until the user restarts it, and nothing fights `stop`.
	fmt.Fprintf(&b, "Restart=no\n")
	fmt.Fprintf(&b, "\n[Install]\n")
	if spec.Startup == lifecycle.StartupLogin {
		fmt.Fprintf(&b, "WantedBy=graphical-session.target\n")
	} else {
		fmt.Fprintf(&b, "WantedBy=multi-user.target\n")
	}
	return b.Bytes()
}

// quoteSystemdCommand renders the ExecStart argv: the executable plus
// each argument, quoted when it holds spaces or metacharacters.
func quoteSystemdCommand(args []string) string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		out = append(out, quoteSystemdValue(strings.ReplaceAll(arg, "$", "$$")))
	}
	return strings.Join(out, " ")
}

// quoteSystemdValue quotes one value the unit file cannot hold bare.
func quoteSystemdValue(value string) string {
	value = strings.NewReplacer("%", "%%", "\n", `\n`, "\r", `\r`, "\t", `\t`, `\`, `\\`, `"`, `\"`).Replace(value)
	if value == "" || strings.ContainsAny(value, " \t\"'\\$%") {
		return `"` + value + `"`
	}
	return value
}

func (m *systemdManager) Inspect(ctx context.Context, spec LaunchSpec) (NativeStatus, error) {
	path, err := m.definitionPath(spec)
	if err != nil {
		return NativeStatus{}, err
	}
	data, readErr := m.readFile(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		return NativeStatus{}, readErr
	}
	status := NativeStatus{
		Installed:         readErr == nil,
		DefinitionMatches: readErr == nil && bytes.Equal(data, m.definitionUnit(spec)),
		State:             "installed",
	}
	user := spec.Startup == lifecycle.StartupLogin
	out, runErr := m.runSystemctl(ctx, []string{"show", systemdUnitName(spec) + ".service", "--no-pager", "--property=LoadState", "--property=ActiveState", "--property=UnitFileState", "--property=MainPID", "--property=FragmentPath", "--property=NeedDaemonReload", "--property=Result", "--property=ExecMainCode", "--property=ExecMainStatus"}, user, false)
	if runErr != nil {
		return NativeStatus{}, runErr
	}
	properties := nativeProperties(out)
	if properties["LoadState"] == "not-found" && properties["ActiveState"] == "inactive" {
		status.State, status.DefinitionMatches = "not installed", false
		return status, nil
	}
	if properties["LoadState"] == "" || properties["ActiveState"] == "" || properties["UnitFileState"] == "" {
		return NativeStatus{}, fmt.Errorf("systemd did not return the unit's complete state")
	}
	status.State = properties["ActiveState"]
	status.Running = status.State == "active" || status.State == "activating" || status.State == "deactivating"
	status.DefinitionMatches = status.DefinitionMatches && properties["LoadState"] == "loaded" && properties["UnitFileState"] == "enabled" && properties["NeedDaemonReload"] == "no" && properties["FragmentPath"] == path
	if result := properties["Result"]; result != "" && result != "success" {
		status.Failure = fmt.Sprintf("systemd result: %s; process code: %s; process status: %s", result, properties["ExecMainCode"], properties["ExecMainStatus"])
	}
	if pid := properties["MainPID"]; pid != "" {
		status.PID, err = strconv.Atoi(pid)
		if err != nil {
			return NativeStatus{}, fmt.Errorf("systemd returned an invalid process identifier")
		}
	}
	return status, nil
}

func (*systemdManager) AuthorizeStart(ctx context.Context, spec LaunchSpec) error {
	return authorizeNativeStart(ctx, spec)
}

func (m *systemdManager) Install(ctx context.Context, spec LaunchSpec) error {
	if spec.Startup == lifecycle.StartupLogin {
		out, err := m.runSystemctl(ctx, []string{"show", "graphical-session.target", "--property=LoadState", "--property=ActiveState"}, true, false)
		if err != nil {
			return fmt.Errorf("cannot verify graphical-session support; choose boot startup for headless use: %w", err)
		}
		properties := nativeProperties(out)
		if properties["LoadState"] != "loaded" || properties["ActiveState"] != "active" {
			return fmt.Errorf("this desktop does not expose an active graphical-session.target; choose boot startup for headless use")
		}
	}
	path, err := m.definitionPath(spec)
	if err != nil {
		return err
	}
	if spec.Startup == lifecycle.StartupBoot && m.groupOf(spec.AccountName) == "" {
		return fmt.Errorf("cannot resolve the selected account's primary group")
	}
	if err := m.writeFile(ctx, spec, path, m.definitionUnit(spec)); err != nil {
		return fmt.Errorf("servicelaunch: write the definition: %w", err)
	}
	// enable WITHOUT --now: configuration never starts the service.
	// Reload after publication so the manager sees the complete definition.
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
	if _, err := m.runSystemctl(ctx, []string{"disable", systemdUnitName(spec)}, user, !user); err != nil {
		return err
	}
	path, err := m.definitionPath(spec)
	if err != nil {
		return err
	}
	if err := m.remove(ctx, spec, path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("servicelaunch: remove the definition: %w", err)
	}
	_, err = m.runSystemctl(ctx, []string{"daemon-reload"}, user, !user)
	return err
}

func (m *systemdManager) Diagnose(ctx context.Context, spec LaunchSpec) ([]string, error) {
	user := spec.Startup == lifecycle.StartupLogin
	notes := []string{systemdUnitName(spec) + ".service"}
	if out, err := m.runSystemctl(ctx, []string{"status", systemdUnitName(spec), "--no-pager", "-l"}, user, false); err == nil {
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
