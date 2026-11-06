package servicelaunch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/lifecycle"
)

func testSpec() lifecycle.ServiceSpec {
	return lifecycle.ServiceSpec{
		InstanceID:       "5fe1000000000000",
		Mode:             lifecycle.RoleWorker,
		Startup:          lifecycle.StartupLogin,
		AccountID:        "501",
		AccountName:      "trustin",
		Executable:       "/opt/homebrew/bin/leapmux",
		WorkingDirectory: "/Users/trustin",
		ConfigFile:       "/Users/trustin/.config/leapmux/worker/worker.yaml",
		DataDirectory:    "/Users/trustin/.config/leapmux/worker",
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
		assert.Contains(t, err.Error(), name, "the refusal names the variable")
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
	assert.Contains(t, args, spec.ConfigFile)
	assert.Contains(t, args, "--log-level")
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
