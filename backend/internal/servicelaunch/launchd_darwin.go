//go:build darwin

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

// launchdManager maintains one launchd definition: a LaunchAgent in the
// selected account's GUI domain for login startup, or a system
// LaunchDaemon running as that account for boot startup.
type launchdManager struct {
	// runLaunchctl runs the launchctl command; a test substitutes it.
	runLaunchctl func(ctx context.Context, args []string, escalated bool) ([]byte, error)
	// readFile and writeFile stand between the manager and the
	// definition files; a test substitutes them.
	readFile  func(path string) ([]byte, error)
	writeFile func(path string, data []byte, mode os.FileMode) error
	remove    func(path string) error
}

// newLaunchdManager builds the production manager.
func newLaunchdManager() *launchdManager {
	return &launchdManager{
		runLaunchctl: runLaunchctlCommand,
		readFile:     os.ReadFile,
		writeFile:    os.WriteFile,
		remove:       os.Remove,
	}
}

// runLaunchctlCommand executes launchctl directly. No shell string is
// built anywhere: arguments run as one argv.
func runLaunchctlCommand(ctx context.Context, args []string, escalated bool) ([]byte, error) {
	path := "launchctl"
	if escalated {
		path = "/usr/bin/sudo"
		args = append([]string{"/bin/launchctl"}, args...)
	}
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, out.String())
	}
	return out.Bytes(), nil
}

// definitionPath names the plist: ~/Library/LaunchAgents for login
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

// definitionPlist renders the property list. Every string value is
// XML-escaped by the encoder; executable paths stay absolute; the job
// runs the mode's run command, never start.
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

	if len(spec.Environment()) > 0 || spec.EnvironmentFile != nil {
		writeKey("EnvironmentVariables")
		b.WriteString("\t<dict>\n")
		for _, entry := range spec.Environment() {
			name, value, _ := strings.Cut(entry, "=")
			fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", xmlEscape(name), xmlEscape(value))
		}
		b.WriteString("\t</dict>\n")
	}

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
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return NativeStatus{State: "not installed"}, nil
		}
		return NativeStatus{}, readErr
	}
	status := NativeStatus{
		Installed:         true,
		DefinitionMatches: bytes.Equal(data, m.definitionPlist(spec)),
		State:             "installed",
	}
	// A login job's state lives in the account's GUI domain; a daemon's
	// in the system domain.
	out, listErr := m.runLaunchctl(ctx, []string{"print", "gui/" + spec.AccountID + "/" + launchdLabel(spec)}, false)
	if listErr != nil {
		out, listErr = m.runLaunchctl(ctx, []string{"print", "system/" + launchdLabel(spec)}, spec.Startup == lifecycle.StartupBoot)
	}
	if listErr == nil {
		text := string(out)
		if strings.Contains(text, "state = running") {
			status.Running = true
			status.State = "running"
		}
	}
	return status, nil
}

func (m *launchdManager) Install(ctx context.Context, spec LaunchSpec) error {
	path, err := m.definitionPath(spec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("servicelaunch: create the definition directory: %w", err)
	}
	// The definition is written WITHOUT loading it: `enable` configures
	// without starting, and launchd loads nothing until `start` says so.
	// RunAtLoad in the file is inert until the job is loaded.
	if err := m.writeFile(path, m.definitionPlist(spec), 0o644); err != nil {
		return fmt.Errorf("servicelaunch: write the definition: %w", err)
	}
	// Explicitly disable any previously-loaded job with this label, so
	// enable never leaves an earlier definition armed.
	_, _ = m.runLaunchctl(ctx, []string{"disable", launchdDomain(spec) + "/" + launchdLabel(spec)}, spec.Startup == lifecycle.StartupBoot)
	return nil
}

// launchdDomain names the target domain of the job.
func launchdDomain(spec LaunchSpec) string {
	if spec.Startup == lifecycle.StartupBoot {
		return "system"
	}
	return "gui/" + spec.AccountID
}

func (m *launchdManager) Start(ctx context.Context, spec LaunchSpec) error {
	if _, err := m.runLaunchctl(ctx, []string{"bootstrap", launchdDomain(spec), mustDefinitionPath(m, spec)}, spec.Startup == lifecycle.StartupBoot); err != nil {
		// An already-bootstrapped job takes the kickstart path.
		if _, kickErr := m.runLaunchctl(ctx, []string{"kickstart", "-k", launchdDomain(spec) + "/" + launchdLabel(spec)}, spec.Startup == lifecycle.StartupBoot); kickErr != nil {
			return err
		}
	}
	return nil
}

func (m *launchdManager) Stop(ctx context.Context, spec LaunchSpec) error {
	// bootout ends the running job while the DEFINITION file stays for
	// the next login or boot.
	_, _ = m.runLaunchctl(ctx, []string{"bootout", launchdDomain(spec) + "/" + launchdLabel(spec)}, spec.Startup == lifecycle.StartupBoot)
	return nil
}

func (m *launchdManager) Remove(ctx context.Context, spec LaunchSpec) error {
	if err := m.Stop(ctx, spec); err != nil {
		return err
	}
	path, err := m.definitionPath(spec)
	if err != nil {
		return err
	}
	if err := m.remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("servicelaunch: remove the definition: %w", err)
	}
	return nil
}

func (m *launchdManager) Diagnose(ctx context.Context, spec LaunchSpec) ([]string, error) {
	notes := []string{
		fmt.Sprintf("the definition is %s", mustDefinitionPath(m, spec)),
	}
	if out, err := m.runLaunchctl(ctx, []string{"print", launchdDomain(spec) + "/" + launchdLabel(spec)}, spec.Startup == lifecycle.StartupBoot); err == nil {
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

// firstLines bounds a diagnostic excerpt.
func firstLines(text string, n int) []string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return lines
}
