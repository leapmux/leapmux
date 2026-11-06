//go:build linux

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

// stubbedSystemd records every systemctl argv and unit file.
type stubbedSystemd struct {
	*systemdManager

	mu       sync.Mutex
	commands [][]string
	files    map[string][]byte
}

func newStubbedSystemd() *stubbedSystemd {
	s := &stubbedSystemd{files: map[string][]byte{}}
	s.systemdManager = &systemdManager{
		runSystemctl: func(_ context.Context, args []string, user, escalated bool) ([]byte, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			recorded := append([]string(nil), args...)
			if user {
				recorded = append(recorded, "--user")
			}
			if escalated {
				recorded = append([]string{"SUDO"}, recorded...)
			}
			s.commands = append(s.commands, recorded)
			return []byte("active\n"), nil
		},
		readFile: func(path string) ([]byte, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if data, ok := s.files[path]; ok {
				return data, nil
			}
			return nil, os.ErrNotExist
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
			delete(s.files, path)
			return nil
		},
		groupOf: func(string) string { return "staff" },
	}
	return s
}

func (s *stubbedSystemd) flat() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sb strings.Builder
	for _, cmd := range s.commands {
		sb.WriteString(strings.Join(cmd, " "))
		sb.WriteString("\n")
	}
	return sb.String()
}

// TestSystemdDefinitionArguments pins the generated unit: literal
// arguments with spaces and metacharacters, the selected account in
// boot mode, the graphical-session integration in login mode, and the
// run command -- never start.
func TestSystemdDefinitionArguments(t *testing.T) {
	spec, err := LaunchSpecFromService(testSpec())
	require.NoError(t, err)
	spec.ConfigFile = "/home/trustin/My Config/worker.yaml"
	spec.RuntimeArguments = []string{"--name", "a worker's laptop"}

	unit := string(newStubbedSystemd().definitionUnit(spec))
	assert.Contains(t, unit, `ExecStart=/opt/homebrew/bin/leapmux worker run --config "/home/trustin/My Config/worker.yaml" --name "a worker's laptop"`)
	assert.Contains(t, unit, "PartOf=graphical-session.target", "login mode follows the graphical session")
	assert.Contains(t, unit, "WantedBy=graphical-session.target")
	assert.NotContains(t, unit, "\nstart\n", "the unit never invokes start")
	assert.NotContains(t, unit, "Restart=always", "no automatic restart policy")
	assert.NotContains(t, unit, "User=", "a user service already runs as the account")

	boot := spec
	boot.Startup = "boot"
	bootUnit := string(newStubbedSystemd().definitionUnit(boot))
	assert.Contains(t, bootUnit, "User=trustin\n", "the system unit runs as the selected account")
	assert.Contains(t, bootUnit, "Group=staff\n")
	assert.Contains(t, bootUnit, "WantedBy=multi-user.target")
}

// Install enables WITHOUT --now; Start starts; Stop preserves the
// enabled definition.
func TestSystemdInstallStartStop(t *testing.T) {
	spec, err := LaunchSpecFromService(testSpec())
	require.NoError(t, err)
	s := newStubbedSystemd()
	ctx := context.Background()

	require.NoError(t, s.Install(ctx, spec))
	enableLine := firstCommandContaining(s.flat(), "enable")
	require.NotNil(t, enableLine, "enable runs")
	assert.NotContains(t, *enableLine, "--now", "enable never starts the service")

	require.NoError(t, s.Start(ctx, spec))
	assert.Contains(t, s.flat(), "start leapmux-worker-")

	require.NoError(t, s.Stop(ctx, spec))
	assert.Contains(t, s.flat(), "stop leapmux-worker-")
	// disable never ran: the definition stays armed for the next login.
	assert.NotContains(t, s.flat(), "disable")
}

func firstCommandContaining(flat, fragment string) *string {
	for _, line := range strings.Split(flat, "\n") {
		if strings.Contains(line, fragment) {
			return &line
		}
	}
	return nil
}
