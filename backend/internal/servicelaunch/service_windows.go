//go:build windows

package servicelaunch

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/leapmux/leapmux/lifecycle"
)

// windowsServiceManager maintains the boot-mode definition: a Windows
// service under the Service Control Manager, running as the selected
// account. Login mode lives in the scheduled-task adapter.
type windowsServiceManager struct {
	runSc func(ctx context.Context, args []string, escalated bool) ([]byte, error)
}

func newWindowsServiceManager() *windowsServiceManager {
	return &windowsServiceManager{runSc: runScCommand}
}

// runScCommand executes sc.exe directly; boot-mode configuration needs
// the elevation the controller has already explained.
func runScCommand(ctx context.Context, args []string, escalated bool) ([]byte, error) {
	path := "sc.exe"
	if escalated {
		path = "powershell"
		wrapped := append([]string{"-NoProfile", "-Command", "Start-Process", "-Verb", "RunAs", "-Wait", "-FilePath", "sc.exe"}, argsForStartProcess(args)...)
		args = wrapped
	}
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("sc %s: %w: %s", strings.Join(args, " "), err, out.String())
	}
	return out.Bytes(), nil
}

// argsForStartProcess renders sc args for the elevated wrapper.
func argsForStartProcess(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		out = append(out, "-ArgumentList", a)
	}
	return out
}

// serviceName derives the service name from the instance identity.
func serviceName(spec LaunchSpec) string {
	return "leapmux-" + string(spec.Mode) + "-" + spec.InstanceID
}

func (m *windowsServiceManager) Inspect(ctx context.Context, spec LaunchSpec) (NativeStatus, error) {
	out, err := m.runSc(ctx, []string{"query", serviceName(spec)}, false)
	if err != nil {
		return NativeStatus{State: "not installed"}, nil
	}
	status := NativeStatus{Installed: true, State: "installed"}
	text := string(out)
	if strings.Contains(text, "RUNNING") {
		status.Running, status.State = true, "running"
	}
	if strings.Contains(text, "START_PENDING") {
		status.State = "start-pending"
	}
	if strings.Contains(text, "STOP_PENDING") {
		status.State = "stop-pending"
	}
	// The definition's shape comes from the qc read.
	qc, qcErr := m.runSc(ctx, []string{"qc", serviceName(spec)}, false)
	if qcErr == nil {
		status.DefinitionMatches = definitionMatchesQc(string(qc), spec)
	}
	return status, nil
}

// definitionMatchesQc compares the configured binary path with the
// spec's run command, the observable half of the definition.
func definitionMatchesQc(qc string, spec LaunchSpec) bool {
	want := strings.Join(append([]string{spec.Executable}, spec.RunArguments()...), " ")
	return strings.Contains(strings.ToLower(qc), strings.ToLower(want))
}

func (m *windowsServiceManager) Install(ctx context.Context, spec LaunchSpec) error {
	binPath := strings.Join(append([]string{spec.Executable}, spec.RunArguments()...), " ")
	escalated := true
	args := []string{"create", serviceName(spec), "binPath=", binPath, "start=", "auto", "obj=", accountSpelling(spec)}
	if _, err := m.runSc(ctx, args, escalated); err != nil {
		return err
	}
	return nil
}

// accountSpelling states the account the service runs as.
func accountSpelling(spec LaunchSpec) string {
	if strings.HasPrefix(spec.AccountID, "S-") {
		return spec.AccountID
	}
	return spec.AccountName
}

func (m *windowsServiceManager) Start(ctx context.Context, spec LaunchSpec) error {
	_, err := m.runSc(ctx, []string{"start", serviceName(spec)}, true)
	return err
}

func (m *windowsServiceManager) Stop(ctx context.Context, spec LaunchSpec) error {
	_, err := m.runSc(ctx, []string{"stop", serviceName(spec)}, true)
	return err
}

func (m *windowsServiceManager) Remove(ctx context.Context, spec LaunchSpec) error {
	_, _ = m.runSc(ctx, []string{"stop", serviceName(spec)}, true)
	_, err := m.runSc(ctx, []string{"delete", serviceName(spec)}, true)
	return err
}

func (m *windowsServiceManager) Diagnose(ctx context.Context, spec LaunchSpec) ([]string, error) {
	notes := []string{serviceName(spec)}
	if out, err := m.runSc(ctx, []string{"qfailure", serviceName(spec)}, false); err == nil {
		notes = append(notes, firstWindowsLines(string(out), 12)...)
	} else {
		notes = append(notes, "sc qfailure failed: "+err.Error())
	}
	return notes, nil
}

// firstWindowsLines bounds a diagnostic excerpt.
func firstWindowsLines(text string, n int) []string {
	lines := strings.Split(strings.TrimSpace(text), "\r\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return lines
}

// startupMatches reports whether a spec's startup kind uses this
// adapter.
func startupMatches(spec LaunchSpec) bool {
	return spec.Startup == lifecycle.StartupBoot
}
