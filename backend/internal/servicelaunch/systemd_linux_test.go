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

	"github.com/leapmux/leapmux/lifecycle"
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
			if args[0] == "show" {
				return []byte("LoadState=loaded\nActiveState=active\nUnitFileState=enabled\nMainPID=0\nNeedDaemonReload=no\n"), nil
			}
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
		writeFile: func(_ context.Context, _ LaunchSpec, path string, data []byte) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.files[path] = data
			return nil
		},
		remove: func(_ context.Context, _ LaunchSpec, path string) error {
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
	spec.DataDirectory = "/home/trustin/My Data/worker"
	spec.RuntimeArguments = []string{"--name", "a worker's laptop"}

	unit := string(newStubbedSystemd().definitionUnit(spec))
	assert.Contains(t, unit, `ExecStart=/opt/homebrew/bin/leapmux worker run --data-dir "/home/trustin/My Data/worker"`)
	assert.Contains(t, unit, `--service-spec "/home/trustin/My Data/worker/.lifecycle/service.yaml"`)
	assert.NotContains(t, unit, "a worker's laptop")
	assert.Contains(t, unit, "After=graphical-session.target", "login mode starts with the graphical session")
	assert.NotContains(t, unit, "PartOf=", "the account-wide supervisor decides final logout")
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

	// Neither startup kind lets systemd kill an application that is still
	// shutting down.
	for _, definition := range []string{unit, bootUnit} {
		_, service, found := strings.Cut(definition, "\n[Service]\n")
		require.True(t, found)
		service, _, found = strings.Cut(service, "\n[Install]\n")
		require.True(t, found)
		assert.Contains(t, strings.Split(service, "\n"), "TimeoutStopSec=infinity", "systemd never sends SIGKILL after SIGTERM")
	}
}

func TestSystemdDefinitionCarriesEveryLaunchField(t *testing.T) {
	manager := newStubbedSystemd()
	for _, startup := range []lifecycle.StartupKind{lifecycle.StartupLogin, lifecycle.StartupBoot} {
		t.Run(string(startup), func(t *testing.T) {
			base, err := LaunchSpecFromService(testSpec())
			require.NoError(t, err)
			base.Startup = startup
			render := func(spec LaunchSpec) []byte {
				path, err := manager.definitionPath(spec)
				require.NoError(t, err)
				return append([]byte(path+"\n"), manager.definitionUnit(spec)...)
			}
			assertDefinitionCarries(t, base, render, fieldsCarriedBy(map[string]bool{
				"InstanceID": true,
				"Mode":       true,
				"Startup":    true,
				// The instance identity derives from AccountID, so another
				// account changes the unit name. managedRun refuses a saved
				// AccountID that is not the running account.
				"AccountID": false,
				// A system service sets User= to the account. A user service
				// runs as the account whose service manager loads it, and
				// managedRun refuses a saved AccountName that is not the
				// running account.
				"AccountName":      startup == lifecycle.StartupBoot,
				"Executable":       true,
				"WorkingDirectory": true,
				"DataDirectory":    true,
			}))
		})
	}
}

func TestSystemdDefinitionDoesNotExposeEnvironment(t *testing.T) {
	spec, err := LaunchSpecFromService(testSpec())
	require.NoError(t, err)
	spec.RuntimeEnvironment = map[string]string{"API_TOKEN": "private-secret-value"}
	path := "/private/runtime.yaml"
	spec.EnvironmentFile = &path
	definition := string(newStubbedSystemd().definitionUnit(spec))
	assert.NotContains(t, definition, "private-secret-value")
	assert.NotContains(t, definition, "EnvironmentFile=")
	assert.Contains(t, definition, "--service-spec")
}

func TestSystemdInspectionReconcilesNativeProcessWithoutDefinitionFile(t *testing.T) {
	for _, scenario := range []string{"failed", "missing file", "missing everywhere"} {
		t.Run(scenario, func(t *testing.T) {
			manager := newStubbedSystemd()
			spec, err := LaunchSpecFromService(testSpec())
			require.NoError(t, err)
			definition, err := manager.definitionPath(spec)
			require.NoError(t, err)
			if scenario == "failed" {
				manager.files[definition] = manager.definitionUnit(spec)
			}
			manager.runSystemctl = func(_ context.Context, args []string, user, elevated bool) ([]byte, error) {
				assert.False(t, elevated, "native inspection must not request authorization")
				assert.Contains(t, args, "--property=Result")
				assert.Contains(t, args, "--property=ExecMainStatus")
				switch scenario {
				case "failed":
					return []byte("LoadState=loaded\nActiveState=failed\nUnitFileState=enabled\nMainPID=0\nFragmentPath=" + definition + "\nNeedDaemonReload=no\nResult=exit-code\nExecMainCode=1\nExecMainStatus=203\n"), nil
				case "missing file":
					return []byte("LoadState=loaded\nActiveState=active\nUnitFileState=enabled\nMainPID=2357\nFragmentPath=" + definition + "\nNeedDaemonReload=no\nResult=success\n"), nil
				default:
					return []byte("LoadState=not-found\nActiveState=inactive\nUnitFileState=\nMainPID=0\n"), nil
				}
			}
			status, err := manager.Inspect(t.Context(), spec)
			require.NoError(t, err)
			assert.Equal(t, scenario == "failed", status.Installed)
			assert.Equal(t, scenario == "failed", status.DefinitionMatches)
			assert.Equal(t, scenario == "missing file", status.Running)
			if scenario == "failed" {
				assert.Contains(t, status.Failure, "203")
			} else {
				assert.Empty(t, status.Failure)
			}
		})
	}
}

// The operations preserve these behaviors:
//   - Install enables without --now.
//   - Start starts.
//   - Stop preserves the enabled definition.
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
