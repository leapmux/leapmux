package servicelaunch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/lifecycle"
)

func TestParseNativeServiceEntryRoundTripsLaunchSpec(t *testing.T) {
	t.Parallel()
	for _, mode := range []lifecycle.Role{lifecycle.RoleHub, lifecycle.RoleWorker, lifecycle.RoleSolo, lifecycle.RoleDev} {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			root, err := lifecycle.CanonicalDir(t.TempDir())
			require.NoError(t, err)
			instanceID := lifecycle.NewInstanceID("S-1-5-21-test", mode, root).String()
			expectedName := WindowsServiceName(mode, instanceID)
			launch := LaunchSpec{
				InstanceID:    instanceID,
				Mode:          mode,
				Startup:       lifecycle.StartupBoot,
				DataDirectory: root,
			}
			expectedRunArgs := launch.RunArguments()
			entryArgs := append([]string{WindowsServiceEntryFlag, expectedName}, expectedRunArgs...)
			originalEntryArgs := append([]string(nil), entryArgs...)

			serviceName, role, runArgs, err := ParseNativeServiceEntry(entryArgs)
			require.NoError(t, err)
			assert.Equal(t, expectedName, serviceName)
			assert.Equal(t, mode, role)
			assert.Equal(t, expectedRunArgs, runArgs)

			// Returned runArgs must be a copy so caller mutations do not alter input args.
			runArgs[0] = "mutated"
			assert.Equal(t, originalEntryArgs, entryArgs)
		})
	}
}

func TestParseNativeServiceEntryMinimalValidEntry(t *testing.T) {
	t.Parallel()
	validName := "leapmux-hub-0123456789abcdef"
	serviceName, role, runArgs, err := ParseNativeServiceEntry([]string{
		WindowsServiceEntryFlag, validName, "daemon", "hub", "run",
	})
	require.NoError(t, err)
	assert.Equal(t, validName, serviceName)
	assert.Equal(t, lifecycle.RoleHub, role)
	assert.Equal(t, []string{"daemon", "hub", "run"}, runArgs)
}

func TestParseNativeServiceEntryPreservesExtraTrailingFlags(t *testing.T) {
	t.Parallel()
	validName := "leapmux-worker-0123456789abcdef"
	entryArgs := []string{
		WindowsServiceEntryFlag, validName, "daemon", "worker", "run",
		"--service-spec", "/srv/leapmux/service.yaml",
		"--data-dir", "/srv/leapmux/data",
		"--log-level", "debug",
		"--",
		"",
	}
	originalEntryArgs := append([]string(nil), entryArgs...)

	serviceName, role, runArgs, err := ParseNativeServiceEntry(entryArgs)
	require.NoError(t, err)
	assert.Equal(t, validName, serviceName)
	assert.Equal(t, lifecycle.RoleWorker, role)
	assert.Equal(t, []string{
		"daemon", "worker", "run",
		"--service-spec", "/srv/leapmux/service.yaml",
		"--data-dir", "/srv/leapmux/data",
		"--log-level", "debug",
		"--",
		"",
	}, runArgs)

	// Mutating trailing elements of runArgs must not alter the input slice.
	runArgs[len(runArgs)-1] = "mutated"
	assert.Equal(t, originalEntryArgs, entryArgs)
}

func TestParseNativeServiceEntryIgnoresNonServiceArgs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
	}{
		{
			name: "nil args",
			args: nil,
		},
		{
			name: "empty args",
			args: []string{},
		},
		{
			name: "foreground daemon command",
			args: []string{"daemon", "hub", "run"},
		},
		{
			name: "inline flag syntax is not matched",
			args: []string{WindowsServiceEntryFlag + "=leapmux-hub-0123456789abcdef", "daemon", "hub", "run"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			serviceName, role, runArgs, err := ParseNativeServiceEntry(tc.args)
			require.NoError(t, err)
			assert.Empty(t, serviceName)
			assert.Empty(t, role)
			assert.Nil(t, runArgs)
		})
	}
}

func TestParseNativeServiceEntryRejectsInvalidEntries(t *testing.T) {
	t.Parallel()
	validName := "leapmux-hub-0123456789abcdef"

	for _, tc := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "flag only",
			args:    []string{WindowsServiceEntryFlag},
			wantErr: "invalid Windows service entry",
		},
		{
			name:    "two args only",
			args:    []string{WindowsServiceEntryFlag, validName},
			wantErr: "invalid Windows service entry",
		},
		{
			name:    "three args only",
			args:    []string{WindowsServiceEntryFlag, validName, "daemon"},
			wantErr: "invalid Windows service entry",
		},
		{
			name:    "truncated arguments",
			args:    []string{WindowsServiceEntryFlag, validName, "daemon", "hub"},
			wantErr: "invalid Windows service entry",
		},
		{
			name:    "missing daemon token",
			args:    []string{WindowsServiceEntryFlag, validName, "hub", "run"},
			wantErr: "invalid Windows service entry",
		},
		{
			name:    "wrong root command",
			args:    []string{WindowsServiceEntryFlag, validName, "service", "hub", "run"},
			wantErr: "invalid Windows service entry",
		},
		{
			name:    "uppercase daemon token",
			args:    []string{WindowsServiceEntryFlag, validName, "Daemon", "hub", "run"},
			wantErr: "invalid Windows service entry",
		},
		{
			name:    "wrong verb",
			args:    []string{WindowsServiceEntryFlag, validName, "daemon", "hub", "start"},
			wantErr: "invalid Windows service entry",
		},
		{
			name:    "uppercase run verb",
			args:    []string{WindowsServiceEntryFlag, validName, "daemon", "hub", "RUN"},
			wantErr: "invalid Windows service entry",
		},
		{
			name:    "invalid role",
			args:    []string{WindowsServiceEntryFlag, validName, "daemon", "invalid", "run"},
			wantErr: "invalid",
		},
		{
			name:    "empty service name",
			args:    []string{WindowsServiceEntryFlag, "", "daemon", "hub", "run"},
			wantErr: "invalid Windows service entry",
		},
		{
			name:    "invalid service name",
			args:    []string{WindowsServiceEntryFlag, "not-a-service", "daemon", "hub", "run"},
			wantErr: "invalid Windows service entry",
		},
		{
			name:    "unknown role in service name",
			args:    []string{WindowsServiceEntryFlag, "leapmux-admin-0123456789abcdef", "daemon", "hub", "run"},
			wantErr: "invalid Windows service entry",
		},
		{
			name:    "trailing newline in service name",
			args:    []string{WindowsServiceEntryFlag, validName + "\n", "daemon", "hub", "run"},
			wantErr: "invalid Windows service entry",
		},
		{
			name:    "uppercase hex in service name",
			args:    []string{WindowsServiceEntryFlag, "leapmux-hub-0123456789ABCDEF", "daemon", "hub", "run"},
			wantErr: "invalid Windows service entry",
		},
		{
			name:    "short hex in service name",
			args:    []string{WindowsServiceEntryFlag, "leapmux-hub-0123456789abcde", "daemon", "hub", "run"},
			wantErr: "invalid Windows service entry",
		},
		{
			name:    "long hex in service name",
			args:    []string{WindowsServiceEntryFlag, "leapmux-hub-0123456789abcdef0", "daemon", "hub", "run"},
			wantErr: "invalid Windows service entry",
		},
		{
			name:    "service name role does not match command role",
			args:    []string{WindowsServiceEntryFlag, validName, "daemon", "worker", "run"},
			wantErr: `service name role "hub" does not match command role "worker"`,
		},
		{
			name:    "worker service name does not match solo command role",
			args:    []string{WindowsServiceEntryFlag, "leapmux-worker-0123456789abcdef", "daemon", "solo", "run"},
			wantErr: `service name role "worker" does not match command role "solo"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			serviceName, role, runArgs, err := ParseNativeServiceEntry(tc.args)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Empty(t, serviceName)
			assert.Empty(t, role)
			assert.Nil(t, runArgs)
		})
	}
}
