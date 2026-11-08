//go:build darwin

package servicelaunch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/lifecycle"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLaunchctlWrapperClassifiesOriginalOperation(t *testing.T) {
	for _, escalated := range []bool{false, true} {
		for _, operation := range []string{"print", "bootout", "bootstrap"} {
			for _, code := range []int{113, 3, 5} {
				t.Run(fmt.Sprintf("%s/elevated=%t/exit=%d", operation, escalated, code), func(t *testing.T) {
					var nativeError error
					output, err := runLaunchctlCommandWith(t.Context(), []string{operation, "system/test-job"}, escalated, func(ctx context.Context, path string, args []string) ([]byte, error) {
						if escalated {
							assert.Equal(t, "/usr/bin/sudo", path)
							assert.Equal(t, []string{"/bin/launchctl", operation, "system/test-job"}, args)
						} else {
							assert.Equal(t, "launchctl", path)
							assert.Equal(t, []string{operation, "system/test-job"}, args)
						}
						nativeError = exec.CommandContext(ctx, "/bin/sh", "-c", fmt.Sprintf("exit %d", code)).Run()
						return []byte("native diagnostic"), nativeError
					})
					require.Error(t, err)
					assert.ErrorIs(t, err, nativeError)
					assert.Equal(t, "native diagnostic", string(output))
					if code == 113 && operation != "bootstrap" || code == 3 && operation == "bootout" {
						assert.ErrorIs(t, err, os.ErrNotExist)
					} else {
						assert.NotErrorIs(t, err, os.ErrNotExist)
						assert.Contains(t, err.Error(), "native diagnostic")
					}
				})
			}
		}
	}
	output, err := runLaunchctlCommandWith(t.Context(), nil, false, func(context.Context, string, []string) ([]byte, error) { return []byte("success"), nil })
	require.NoError(t, err)
	assert.Equal(t, "success", string(output))
}

// stubbedLaunchd swaps the manager's filesystem and launchctl halves
// for recorders, so the tests observe the exact argv and file bytes.
type stubbedLaunchd struct {
	*launchdManager

	mu               sync.Mutex
	commands         [][]string
	files            map[string][]byte
	loaded           bool
	running          bool
	disabled         bool
	label            string
	loadedDefinition []byte
}

func newStubbedLaunchd() *stubbedLaunchd {
	s := &stubbedLaunchd{files: map[string][]byte{}}
	s.launchdManager = &launchdManager{
		runLaunchctl: func(_ context.Context, args []string, escalated bool) ([]byte, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			recorded := append([]string(nil), args...)
			if escalated {
				recorded = append([]string{"SUDO"}, recorded...)
			}
			s.commands = append(s.commands, recorded)
			switch args[0] {
			case "enable":
				s.disabled = false
				s.label = args[1][strings.LastIndex(args[1], "/")+1:]
			case "disable":
				s.disabled = true
				s.label = args[1][strings.LastIndex(args[1], "/")+1:]
			case "print-disabled":
				if s.disabled {
					return []byte(`"` + s.label + `" => true`), nil
				}
			case "print":
				if !s.loaded {
					return nil, os.ErrNotExist
				}
				if s.running {
					return []byte("state = running\n"), nil
				}
				return []byte("state = exited\n"), nil
			case "bootstrap":
				if s.disabled || s.loaded {
					return nil, assert.AnError
				}
				s.loaded, s.running = true, true
				s.loadedDefinition = append([]byte(nil), s.files[args[2]]...)
			case "bootout":
				if !s.loaded {
					return nil, os.ErrNotExist
				}
				s.loaded, s.running = false, false
				s.loadedDefinition = nil
			}
			return nil, nil
		},
		readFile: func(path string) ([]byte, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			data, ok := s.files[path]
			if !ok {
				return nil, os.ErrNotExist
			}
			return data, nil
		},
		writeFile: func(_ context.Context, _ LaunchSpec, path string, data []byte) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.files[path] = data
			return nil
		},
		remove: func(_ context.Context, _ LaunchSpec, path string) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if _, ok := s.files[path]; !ok {
				return os.ErrNotExist
			}
			delete(s.files, path)
			return nil
		},
	}
	return s
}

func (s *stubbedLaunchd) allCommands() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]string(nil), s.commands...)
}

func launchdTestSpec() LaunchSpec {
	spec, err := LaunchSpecFromService(testSpec())
	if err != nil {
		panic(err)
	}
	return spec
}

func TestLaunchdInspectionNeverElevatesAndReportsStartupFailure(t *testing.T) {
	manager := newStubbedLaunchd()
	spec := launchdTestSpec()
	spec.Startup = lifecycle.StartupBoot
	path, err := manager.definitionPath(spec)
	require.NoError(t, err)
	manager.files[path] = manager.definitionPlist(spec)
	manager.runLaunchctl = func(_ context.Context, args []string, escalated bool) ([]byte, error) {
		assert.False(t, escalated, "observation must not request a password")
		if args[0] == "print-disabled" {
			return []byte("disabled services = {}"), nil
		}
		return []byte("state = exited\nlast exit code = 78\n"), nil
	}
	status, err := manager.Inspect(t.Context(), spec)
	require.NoError(t, err)
	assert.False(t, status.Running)
	assert.Contains(t, status.Failure, "78")
	_, err = manager.Diagnose(t.Context(), spec)
	require.NoError(t, err)
}

func TestLaunchdMissingDefinitionStillReportsLoadedProcess(t *testing.T) {
	manager := newStubbedLaunchd()
	spec := launchdTestSpec()
	require.NoError(t, manager.Install(t.Context(), spec))
	require.NoError(t, manager.Start(t.Context(), spec))
	path, err := manager.definitionPath(spec)
	require.NoError(t, err)
	delete(manager.files, path)
	status, err := manager.Inspect(t.Context(), spec)
	require.NoError(t, err)
	assert.False(t, status.Installed)
	assert.False(t, status.DefinitionMatches)
	assert.True(t, status.Running, "launchd can retain a loaded job after its file is removed")
}

// TestLaunchdDefinitionArguments pins the generated property list:
// literal arguments survive spaces and metacharacters, the job selects
// the intended account in boot mode, and the program runs the mode's
// run command -- never a shell, never start.
func TestLaunchdDefinitionArguments(t *testing.T) {
	spec := launchdTestSpec()
	spec.Executable = "/opt/homebrew/bin/leapmux"
	spec.ConfigFile = "/Users/trustin/My Config/worker & settings.yaml"
	spec.DataDirectory = "/Users/trustin/My Data & Files"
	spec.RuntimeArguments = []string{"--name", "a worker's laptop; test"}

	plist := newStubbedLaunchd().definitionPlist(spec)
	text := string(plist)

	assert.Contains(t, text, "<string>/opt/homebrew/bin/leapmux</string>", "the executable is literal")
	assert.Contains(t, text, "My Data &amp; Files/.lifecycle/service.yaml", "the private specification path is escaped")
	assert.NotContains(t, text, "settings.yaml", "configuration paths remain in the private specification")
	assert.NotContains(t, text, "a worker", "runtime overrides remain private")
	assert.Contains(t, text, "<string>run</string>", "the job runs the run command")
	assert.NotContains(t, text, "<string>start</string>", "the job never invokes start")
	assert.NotContains(t, text, "<key>UserName</key>", "a login agent runs as the logged-in account already")

	// Boot mode selects the account.
	boot := spec
	boot.Startup = "boot"
	bootText := string(newStubbedLaunchd().definitionPlist(boot))
	assert.Contains(t, bootText, "<key>UserName</key>")
	assert.Contains(t, bootText, "<string>trustin</string>", "the daemon runs as the selected account")
}

func TestLaunchdDefinitionDoesNotExposeEnvironment(t *testing.T) {
	spec := launchdTestSpec()
	spec.RuntimeEnvironment = map[string]string{"API_TOKEN": "private-secret-value"}
	definition := string(newStubbedLaunchd().definitionPlist(spec))
	assert.NotContains(t, definition, "private-secret-value")
	assert.NotContains(t, definition, "EnvironmentVariables")
	assert.Contains(t, definition, "--service-spec")
}

func TestLaunchdInstallEnablesWithoutStarting(t *testing.T) {
	spec := launchdTestSpec()
	manager := newStubbedLaunchd()
	require.NoError(t, manager.Install(t.Context(), spec))
	commands := flatten(manager.allCommands())
	assert.Contains(t, commands, "enable "+launchdDomain(spec)+"/"+launchdLabel(spec))
	assert.NotContains(t, commands, "disable ")
	assert.NotContains(t, commands, "bootstrap ")
	assert.NotContains(t, commands, "kickstart ")
}

func TestLaunchdStopPropagatesFailure(t *testing.T) {
	manager := newStubbedLaunchd()
	manager.runLaunchctl = func(context.Context, []string, bool) ([]byte, error) { return nil, assert.AnError }
	require.ErrorIs(t, manager.Stop(t.Context(), launchdTestSpec()), assert.AnError)
}

func TestLaunchdDisabledOverrideRequiresRepair(t *testing.T) {
	manager := newStubbedLaunchd()
	spec := launchdTestSpec()
	require.NoError(t, manager.Install(t.Context(), spec))
	_, err := manager.runLaunchctl(t.Context(), []string{"disable", launchdDomain(spec) + "/" + launchdLabel(spec)}, false)
	require.NoError(t, err)
	status, err := manager.Inspect(t.Context(), spec)
	require.NoError(t, err)
	assert.False(t, status.DefinitionMatches)
	require.Error(t, manager.Start(t.Context(), spec))
	require.NoError(t, manager.Install(t.Context(), spec))
	require.NoError(t, manager.Start(t.Context(), spec))
	assert.True(t, manager.running)
}

func TestLaunchdRepairReplacesStoppedLoadedDefinition(t *testing.T) {
	manager := newStubbedLaunchd()
	spec := launchdTestSpec()
	require.NoError(t, manager.Install(t.Context(), spec))
	require.NoError(t, manager.Start(t.Context(), spec))
	original := append([]byte(nil), manager.loadedDefinition...)
	manager.running = false // The application exited; launchd retains its loaded definition.
	spec.WorkingDirectory = "/Users/trustin/new working directory"
	require.NoError(t, manager.Install(t.Context(), spec))
	assert.Equal(t, original, manager.loadedDefinition, "rewriting the file alone cannot replace the loaded job")
	require.NoError(t, manager.Start(t.Context(), spec))
	assert.Equal(t, manager.definitionPlist(spec), manager.loadedDefinition)
	assert.NotEqual(t, original, manager.loadedDefinition)
	assert.NotContains(t, flatten(manager.allCommands()), "kickstart")
}

func TestLaunchdBootstrapRefusalNeverKillsOrStartsAnOldJob(t *testing.T) {
	manager := newStubbedLaunchd()
	spec := launchdTestSpec()
	require.NoError(t, manager.Install(t.Context(), spec))
	run := manager.runLaunchctl
	manager.runLaunchctl = func(ctx context.Context, args []string, elevated bool) ([]byte, error) {
		if args[0] == "bootstrap" {
			return nil, assert.AnError
		}
		return run(ctx, args, elevated)
	}
	require.ErrorIs(t, manager.Start(t.Context(), spec), assert.AnError)
	assert.False(t, manager.running)
	assert.NotContains(t, flatten(manager.allCommands()), "kickstart")
}

// The definition lands in the account's LaunchAgents for login startup
// and the system LaunchDaemons for boot startup, and Inspect reads the
// same path back.
func TestLaunchdDefinitionPaths(t *testing.T) {
	spec := launchdTestSpec()
	s := newStubbedLaunchd()
	require.NoError(t, s.Install(context.Background(), spec))

	agentPath := definitionPathOf(t, s, spec)
	assert.Contains(t, agentPath, "Library/LaunchAgents")
	assert.Contains(t, agentPath, "com.leapmux.worker.")

	status, err := s.Inspect(context.Background(), spec)
	require.NoError(t, err)
	assert.True(t, status.Installed)
	assert.True(t, status.DefinitionMatches, "the written definition matches its own spec")
	assert.False(t, status.Running, "installing never starts the job")

	boot := spec
	boot.Startup = "boot"
	bootPath := definitionPathOf(t, s, boot)
	assert.Equal(t, "/Library/LaunchDaemons/com.leapmux.worker."+spec.InstanceID+".plist", bootPath)
}

// The operations preserve these behaviors:
//   - Install writes the definition without starting it.
//   - Start bootstraps.
//   - Stop removes the active job while the definition file stays.
func TestLaunchdInstallStartStopRemove(t *testing.T) {
	spec := launchdTestSpec()
	s := newStubbedLaunchd()
	ctx := context.Background()

	require.NoError(t, s.Install(ctx, spec))
	// No bootstrap or kickstart ran: enable never starts the job.
	for _, cmd := range s.allCommands() {
		assert.NotContains(t, cmd, "bootstrap")
		assert.NotContains(t, cmd, "kickstart")
	}

	require.NoError(t, s.Start(ctx, spec))
	assert.Contains(t, flatten(s.allCommands()), "bootstrap")

	require.NoError(t, s.Stop(ctx, spec))
	assert.Contains(t, flatten(s.allCommands()), "bootout")
	_, stillThere := s.files[definitionPathOf(t, s, spec)]
	assert.True(t, stillThere, "stop preserves the definition for the next login or boot")

	require.NoError(t, s.Remove(ctx, spec))
	_, gone := s.files[definitionPathOf(t, s, spec)]
	assert.False(t, gone, "remove deletes the definition")
}

// flatten renders every recorded argv as one string per command.
func flatten(commands [][]string) string {
	var sb strings.Builder
	for _, cmd := range commands {
		sb.WriteString(strings.Join(cmd, " "))
		sb.WriteString("\n")
	}
	return sb.String()
}

// definitionPathOf resolves the definition path through the embedded
// manager of whichever stub holds it.
func definitionPathOf(t *testing.T, m interface {
	definitionPath(LaunchSpec) (string, error)
}, spec LaunchSpec) string {
	t.Helper()
	path, err := m.definitionPath(spec)
	require.NoError(t, err)
	return path
}
