//go:build darwin

package servicelaunch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/lifecycle"
)

// launchdManager maintains one launchd definition: a LaunchAgent in the
// selected account's GUI domain for login startup, or a system
// LaunchDaemon running as that account for boot startup.
type launchdManager struct {
	// runLaunchctl runs the launchctl command; a test substitutes it.
	runLaunchctl func(ctx context.Context, args []string, escalated bool) ([]byte, error)
	// readFile and writeFile stand between the manager and the
	// definition files; a test substitutes them.
	readFile  func(path string) ([]byte, error)
	writeFile func(context.Context, LaunchSpec, string, []byte) error
	remove    func(context.Context, LaunchSpec, string) error
}

// newLaunchdManager builds the production manager.
func newLaunchdManager() *launchdManager {
	return &launchdManager{
		runLaunchctl: runLaunchctlCommand,
		readFile:     os.ReadFile,
		writeFile:    publishNativeDefinition,
		remove:       removeNativeDefinition,
	}
}

// runLaunchctlCommand executes launchctl directly. No shell string is
// built anywhere: arguments run as one argv.
func runLaunchctlCommand(ctx context.Context, args []string, escalated bool) ([]byte, error) {
	return runLaunchctlCommandWith(ctx, args, escalated, runNativeLaunchctl)
}

func runLaunchctlCommandWith(ctx context.Context, args []string, escalated bool, run func(context.Context, string, []string) ([]byte, error)) ([]byte, error) {
	operation := ""
	if len(args) > 0 {
		operation = args[0]
	}
	path := "launchctl"
	if escalated {
		path = "/usr/bin/sudo"
		args = append([]string{"/bin/launchctl"}, args...)
	}
	output, err := run(ctx, path, args)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			missing := exit.ExitCode() == 113 && (operation == "print" || operation == "bootout")
			// bootout reports ESRCH for a definition that was never loaded.
			missing = missing || exit.ExitCode() == 3 && operation == "bootout"
			if missing {
				return output, fmt.Errorf("launchctl: %w", errors.Join(os.ErrNotExist, err))
			}
		}
		return output, fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, output)
	}
	return output, nil
}

func runNativeLaunchctl(ctx context.Context, path string, args []string) ([]byte, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.Bytes(), err
}

// definitionPath identifies the plist: ~/Library/LaunchAgents for login
// startup, /Library/LaunchDaemons for boot startup.
func (m *launchdManager) definitionPath(spec LaunchSpec) (string, error) {
	label := launchdLabel(spec)
	switch spec.Startup {
	case lifecycle.StartupLogin:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("servicelaunch: the account's home directory: %w", err)
		}
		return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
	case lifecycle.StartupBoot:
		return filepath.Join("/Library", "LaunchDaemons", label+".plist"), nil
	default:
		return "", fmt.Errorf("servicelaunch: unknown startup kind %s", spec.Startup)
	}
}

// launchdLabel derives the job label from the instance identity.
func launchdLabel(spec LaunchSpec) string {
	return "com.leapmux." + string(spec.Mode) + "." + spec.InstanceID
}

// definitionPlist renders the property list:
//   - The encoder XML-escapes every string value.
//   - Executable paths stay absolute.
//   - The job runs the mode's run command, never start.
func (m *launchdManager) definitionPlist(spec LaunchSpec) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	writeKey := func(key string) {
		fmt.Fprintf(&b, "\t<key>%s</key>\n", xmlEscape(key))
	}
	writeString := func(value string) {
		fmt.Fprintf(&b, "\t<string>%s</string>\n", xmlEscape(value))
	}

	writeKey("Label")
	writeString(launchdLabel(spec))

	args := append([]string{spec.Executable}, spec.RunArguments()...)
	writeKey("ProgramArguments")
	b.WriteString("\t<array>\n")
	for _, arg := range args {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", xmlEscape(arg))
	}
	b.WriteString("\t</array>\n")

	writeKey("WorkingDirectory")
	writeString(spec.WorkingDirectory)

	if spec.Startup == lifecycle.StartupBoot {
		// The LaunchDaemon runs as the SELECTED account, not as root.
		writeKey("UserName")
		writeString(spec.AccountName)
	}
	writeKey("RunAtLoad")
	b.WriteString("\t<true/>\n")
	writeKey("KeepAlive")
	b.WriteString("\t<false/>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}

// xmlEscape escapes the characters a property-list string cannot hold.
func xmlEscape(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(s)
}

func (m *launchdManager) Inspect(ctx context.Context, spec LaunchSpec) (NativeStatus, error) {
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
		DefinitionMatches: readErr == nil && bytes.Equal(data, m.definitionPlist(spec)),
		State:             "installed",
	}
	if !status.Installed {
		status.State = "not installed"
	}
	domain := launchdDomain(spec)
	disabled, err := m.runLaunchctl(ctx, []string{"print-disabled", domain}, false)
	if err != nil {
		return NativeStatus{}, err
	}
	if strings.Contains(string(disabled), `"`+launchdLabel(spec)+`" => true`) {
		status.DefinitionMatches = false
		status.State = "disabled"
	}
	out, listErr := m.runLaunchctl(ctx, []string{"print", domain + "/" + launchdLabel(spec)}, false)
	if listErr != nil && !errors.Is(listErr, os.ErrNotExist) {
		return NativeStatus{}, listErr
	}
	if listErr == nil {
		for _, line := range strings.Split(string(out), "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), " = ")
			if !ok {
				continue
			}
			switch key {
			case "state":
				status.State = value
				status.Running = value == "running" || value == "spawn scheduled" || value == "spawning"
			case "pid":
				status.PID, err = strconv.Atoi(value)
				if err != nil || status.PID <= 0 {
					return NativeStatus{}, fmt.Errorf("launchd returned an invalid process identifier")
				}
			case "last exit code", "last terminating signal":
				if value != "0" && value != "(never exited)" {
					status.Failure = key + ": " + value
				}
			}
		}
	}
	return status, nil
}

func (*launchdManager) AuthorizeStart(ctx context.Context, spec LaunchSpec) error {
	return authorizeNativeStart(ctx, spec)
}

func (m *launchdManager) Install(ctx context.Context, spec LaunchSpec) error {
	if err := ValidateLaunchSpec(spec); err != nil {
		return err
	}
	path, err := m.definitionPath(spec)
	if err != nil {
		return err
	}
	if _, err := spec.LoadEnvironment(); err != nil {
		return err
	}
	// The definition is written WITHOUT loading it: `enable` configures
	// without starting, and launchd loads nothing until `start` says so.
	// RunAtLoad in the file is inert until the job is loaded.
	if err := m.writeFile(ctx, spec, path, m.definitionPlist(spec)); err != nil {
		return fmt.Errorf("servicelaunch: write the definition: %w", err)
	}
	// Enabling clears a persistent disabled override without loading the job.
	_, err = m.runLaunchctl(ctx, []string{"enable", launchdDomain(spec) + "/" + launchdLabel(spec)}, spec.Startup == lifecycle.StartupBoot)
	return err
}

// launchdDomain identifies the target domain of the job.
func launchdDomain(spec LaunchSpec) string {
	if spec.Startup == lifecycle.StartupBoot {
		return "system"
	}
	return "gui/" + spec.AccountID
}

func (m *launchdManager) Start(ctx context.Context, spec LaunchSpec) error {
	if err := ValidateLaunchSpec(spec); err != nil {
		return err
	}
	if _, err := spec.LoadEnvironment(); err != nil {
		return err
	}
	status, err := m.Inspect(ctx, spec)
	if err != nil {
		return err
	}
	if status.Running {
		return fmt.Errorf("servicelaunch: the launchd job is already running")
	}
	if !status.Installed || !status.DefinitionMatches {
		return fmt.Errorf("servicelaunch: repair the launchd definition with enable before starting")
	}
	// A stopped job can retain the old loaded definition after its file
	// changes. Unload it before bootstrapping the saved replacement.
	if err := m.Stop(ctx, spec); err != nil {
		return err
	}
	_, err = m.runLaunchctl(ctx, []string{"bootstrap", launchdDomain(spec), mustDefinitionPath(m, spec)}, spec.Startup == lifecycle.StartupBoot)
	return err
}

func (m *launchdManager) Stop(ctx context.Context, spec LaunchSpec) error {
	// bootout ends the running job while the DEFINITION file stays for
	// the next login or boot.
	_, err := m.runLaunchctl(ctx, []string{"bootout", launchdDomain(spec) + "/" + launchdLabel(spec)}, spec.Startup == lifecycle.StartupBoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (m *launchdManager) Remove(ctx context.Context, spec LaunchSpec) error {
	if err := m.Stop(ctx, spec); err != nil {
		return err
	}
	path, err := m.definitionPath(spec)
	if err != nil {
		return err
	}
	if err := m.remove(ctx, spec, path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("servicelaunch: remove the definition: %w", err)
	}
	return nil
}

func (m *launchdManager) Diagnose(ctx context.Context, spec LaunchSpec) ([]string, error) {
	notes := []string{
		fmt.Sprintf("the definition is %s", mustDefinitionPath(m, spec)),
	}
	if out, err := m.runLaunchctl(ctx, []string{"print", launchdDomain(spec) + "/" + launchdLabel(spec)}, false); err == nil {
		notes = append(notes, firstLines(string(out), 12)...)
	} else {
		notes = append(notes, "launchctl print failed: "+err.Error())
	}
	return notes, nil
}

// mustDefinitionPath resolves the definition path for the diagnostics.
func mustDefinitionPath(m *launchdManager, spec LaunchSpec) string {
	path, err := m.definitionPath(spec)
	if err != nil {
		return "<unknown>"
	}
	return path
}

// firstLines limits a diagnostic excerpt.
func firstLines(text string, n int) []string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return lines
}
