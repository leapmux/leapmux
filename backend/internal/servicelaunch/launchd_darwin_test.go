//go:build darwin

package servicelaunch

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubbedLaunchd swaps the manager's filesystem and launchctl halves
// for recorders, so the tests observe the exact argv and file bytes.
type stubbedLaunchd struct {
	*launchdManager

	mu       sync.Mutex
	commands [][]string
	files    map[string][]byte
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
			return []byte("state = running\n"), nil
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
		writeFile: func(path string, data []byte, mode os.FileMode) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.files[path] = data
			return nil
		},
		remove: func(path string) error {
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

// TestLaunchdDefinitionArguments pins the generated property list:
// literal arguments survive spaces and metacharacters, the job selects
// the intended account in boot mode, and the program runs the mode's
// run command -- never a shell, never start.
func TestLaunchdDefinitionArguments(t *testing.T) {
	spec := launchdTestSpec()
	spec.Executable = "/opt/homebrew/bin/leapmux"
	spec.ConfigFile = "/Users/trustin/My Config/worker & settings.yaml"
	spec.RuntimeArguments = []string{"--name", "a worker's laptop; test"}

	plist := newStubbedLaunchd().definitionPlist(spec)
	text := string(plist)

	assert.Contains(t, text, "<string>/opt/homebrew/bin/leapmux</string>", "the executable is literal")
	assert.Contains(t, text, "&amp; settings.yaml", "the metacharacter is escaped")
	assert.Contains(t, text, "a worker&apos;s laptop; test", "an argument keeps its spaces and apostrophe")
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
	assert.True(t, status.Running, "the stubbed launchctl reports running")

	boot := spec
	boot.Startup = "boot"
	bootPath := definitionPathOf(t, s, boot)
	assert.Equal(t, "/Library/LaunchDaemons/com.leapmux.worker."+spec.InstanceID+".plist", bootPath)
}

// Install writes the definition without starting it; Start bootstraps;
// Stop removes the active job while the definition file stays.
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
