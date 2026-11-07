package servicelaunch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/lifecycle"
)

func testSpec() lifecycle.ServiceSpec {
	return lifecycle.ServiceSpec{
		InstanceID:       "5fe1000000000000",
		Mode:             lifecycle.RoleWorker,
		Startup:          lifecycle.StartupLogin,
		AccountID:        "501",
		AccountName:      "trustin",
		Executable:       testutil.NativeAbsPath("/opt/homebrew/bin/leapmux"),
		WorkingDirectory: testutil.NativeAbsPath("/Users/trustin"),
		ConfigFile:       testutil.NativeAbsPath("/Users/trustin/.config/leapmux/worker/worker.yaml"),
		DataDirectory:    testutil.NativeAbsPath("/Users/trustin/.config/leapmux/worker"),
	}
}

// TestEnvironmentCannotOverrideIdentity pins the protected identity
// variables: every supported name supplied through the runtime
// environment is refused, so an environment file cannot change the
// service account's identity or home directory.
func TestEnvironmentCannotOverrideIdentity(t *testing.T) {
	for _, name := range ProtectedIdentityVariables() {
		spec := testSpec()
		spec.RuntimeEnvironment = map[string]string{name: "/elsewhere"}
		launch, err := LaunchSpecFromService(spec)
		require.Error(t, err, "%s must not be overridable", name)
		assert.Contains(t, err.Error(), name, "the refusal identifies the variable")
		assert.Contains(t, err.Error(), "identity", "the refusal states why")
		_ = launch
	}
	// Ordinary variables pass.
	spec := testSpec()
	spec.RuntimeEnvironment = map[string]string{"PATH": "/usr/bin", "LEAPMUX_WORKER_LOG_LEVEL": "debug"}
	_, err := LaunchSpecFromService(spec)
	require.NoError(t, err)
}

// The native definition runs the mode's `run` command under its config
// file -- never `start`, which could recurse into the controller.
func TestRunArgumentsInvokeRunNotStart(t *testing.T) {
	spec := testSpec()
	spec.RuntimeArguments = []string{"--log-level", "debug"}
	launch, err := LaunchSpecFromService(spec)
	require.NoError(t, err)

	args := launch.RunArguments()
	require.NotEmpty(t, args)
	assert.Equal(t, "worker", args[0])
	assert.Equal(t, "run", args[1], "native definitions invoke run, never start")
	assert.NotContains(t, args, spec.ConfigFile)
	assert.NotContains(t, args, "--log-level")
	assert.Contains(t, args, "--service-spec")
	assert.Contains(t, args, "--data-dir")
	assert.Contains(t, args, spec.DataDirectory, "native launch must pin the separately selected data directory")
}

// A relative path in the specification is refused: a service must not
// depend on the directory the user ran enable from.
func TestRelativePathsAreRefused(t *testing.T) {
	for name, mutate := range map[string]func(*lifecycle.ServiceSpec){
		"executable":        func(s *lifecycle.ServiceSpec) { s.Executable = "leapmux" },
		"working directory": func(s *lifecycle.ServiceSpec) { s.WorkingDirectory = "relative/dir" },
		"config file":       func(s *lifecycle.ServiceSpec) { s.ConfigFile = "./worker.yaml" },
		"data directory":    func(s *lifecycle.ServiceSpec) { s.DataDirectory = "data" },
	} {
		spec := testSpec()
		mutate(&spec)
		_, err := LaunchSpecFromService(spec)
		require.Error(t, err, "a relative %s is refused", name)
		assert.Contains(t, err.Error(), "absolute")
	}
}

func TestEnvironmentFileIsValidatedBeforeConfiguration(t *testing.T) {
	for _, test := range []struct{ name, data string }{
		{"malformed", "[unterminated"},
		{"sequence", "- value\n"},
		{"number", "TOKEN: 123\n"},
		{"boolean", "TOKEN: true\n"},
		{"nested", "TOKEN:\n  nested: value\n"},
		{"duplicate", "TOKEN: first\nTOKEN: second\n"},
		{"identity", "HOME: /another/account\n"},
		{"windows identity", "userprofile: /another/account\n"},
		{"empty key", "'': value\n"},
		{"invalid key", "BAD=KEY: value\n"},
		{"null value", "TOKEN: \"a\\0b\"\n"},
		{"multiple documents", "TOKEN: first\n---\nTOKEN: second\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.yaml")
			require.NoError(t, os.WriteFile(path, []byte(test.data), 0o600))
			spec := testSpec()
			spec.EnvironmentFile = &path
			controller, _ := newTestController(t, &recordingManager{})
			_, err := controller.Preview(t.Context(), spec)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "another/account", "validation must not print environment values")
		})
	}
	for _, path := range []string{"relative.yaml", filepath.Join(t.TempDir(), "missing.yaml")} {
		spec := testSpec()
		spec.EnvironmentFile = &path
		controller, _ := newTestController(t, &recordingManager{})
		_, err := controller.Preview(t.Context(), spec)
		require.Error(t, err)
	}
}

func TestLoadEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	require.NoError(t, os.WriteFile(path, []byte("TOKEN: '123'\nEMPTY: ''\nPATH: /provider/bin\n"), 0o600))
	spec, err := LaunchSpecFromService(testSpec())
	require.NoError(t, err)
	spec.EnvironmentFile = &path
	spec.RuntimeEnvironment = map[string]string{"PATH": "/explicit/bin", "OTHER": "true"}
	values, err := spec.LoadEnvironment()
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"TOKEN": "123", "EMPTY": "", "PATH": "/explicit/bin", "OTHER": "true"}, values)
	values["OTHER"] = "changed"
	assert.Equal(t, "true", spec.RuntimeEnvironment["OTHER"])
}

func TestSavedRuntimeRejectsNullArgument(t *testing.T) {
	spec := testSpec()
	spec.RuntimeArguments = []string{"--name", "a\x00b"}
	_, err := LaunchSpecFromService(spec)
	require.Error(t, err)
}

func TestSavedRuntimeAllowsFlagSpellingAsValue(t *testing.T) {
	spec := testSpec()
	spec.RuntimeArguments = []string{"--name", "--config"}
	_, err := LaunchSpecFromService(spec)
	require.NoError(t, err)
}

func TestNativeArgumentsKeepRuntimeOverridesPrivate(t *testing.T) {
	spec := testSpec()
	spec.Mode = lifecycle.RoleHub
	spec.RuntimeArguments = []string{"--storage-postgres-dsn", "postgres://user:private-password@host/database"}
	launch, err := LaunchSpecFromService(spec)
	require.NoError(t, err)
	arguments := launch.RunArguments()
	assert.NotContains(t, strings.Join(arguments, " "), "private-password")
	assert.NotContains(t, arguments, "--storage-postgres-dsn")
	assert.Contains(t, arguments, "--service-spec")
	assert.Contains(t, arguments, lifecycle.PathsOf(spec.DataDirectory).ServiceSpec)
	assert.Contains(t, arguments, spec.DataDirectory)
}

func TestWindowsEnvironmentNamesAreCaseInsensitive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "environment.yaml")
	require.NoError(t, os.WriteFile(path, []byte("Path: /file/bin\nTOKEN: value\n"), 0o600))
	spec := LaunchSpec{EnvironmentFile: &path, RuntimeEnvironment: map[string]string{"PATH": "/explicit/bin"}}
	values, err := spec.loadEnvironment(true)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"PATH": "/explicit/bin", "TOKEN": "value"}, values)
	require.NoError(t, os.WriteFile(path, []byte("Path: first\nPATH: second\n"), 0o600))
	_, err = spec.loadEnvironment(true)
	require.ErrorContains(t, err, "duplicate")
	spec.EnvironmentFile = nil
	spec.RuntimeEnvironment = map[string]string{"PATH": "first", "Path": "second"}
	_, err = spec.loadEnvironment(true)
	require.ErrorContains(t, err, "duplicate")
}

func TestEnvironmentFileSizeLimit(t *testing.T) {
	for _, size := range []int{0, 16<<20 - 1, 16 << 20, 16<<20 + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "environment.yaml")
			var data []byte
			if size > 0 {
				data = []byte("TOKEN: value\n#" + strings.Repeat("x", size-len("TOKEN: value\n#")))
			}
			require.NoError(t, os.WriteFile(path, data, 0o600))
			values, err := (LaunchSpec{EnvironmentFile: &path}).LoadEnvironment()
			if size == 0 || size > 16<<20 {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, map[string]string{"TOKEN": "value"}, values)
			}
		})
	}
}

func TestNativeStructurePreservesRuntimeSelectorForExplicitLoading(t *testing.T) {
	for kind, path := range invalidEnvironmentFiles(t) {
		t.Run(kind, func(t *testing.T) {
			spec := testSpec()
			spec.EnvironmentFile = &path
			launch, err := LaunchSpecFromService(spec)
			require.NoError(t, err)
			require.NotNil(t, launch.EnvironmentFile)
			assert.Equal(t, path, *launch.EnvironmentFile)
			_, err = launch.LoadEnvironment()
			require.Error(t, err)
		})
	}
}
