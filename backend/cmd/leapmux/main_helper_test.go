package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/version"
)

// runMainEnv makes this test binary run main in place of the tests, so a test
// can start the real entry point as a child process.
const runMainEnv = "LEAPMUX_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		// main ends the process with its own exit code.
		main()
	}
	os.Exit(m.Run())
}

// runMain starts main as a child process with args and extraEnv, and returns
// its exit code, its stdout and its stderr.
func runMain(t *testing.T, args []string, extraEnv ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(append(os.Environ(), runMainEnv+"=1"), extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), stdout.String(), stderr.String()
	}
	require.NoError(t, err)
	return 0, stdout.String(), stderr.String()
}

// A provider's CLI starts the executable with no argument and with the helper
// variable set. main then runs the helper: the helper's exit code is the
// process's, and its stderr holds the helper's message and nothing more. Amp
// shows that stderr to the model as the reason for a refusal, so a log line
// there would reach the model too.
func TestMainRunsTheAgentHelperForARunWithNoArgument(t *testing.T) {
	t.Parallel()
	spec := filepath.Join(t.TempDir(), "helper.json")
	env, err := agent.WriteHelperSpec(spec, agent.HelperSpec{
		Provider: "AGENT_PROVIDER_CLAUDE_CODE",
		Helper:   "permission",
	})
	require.NoError(t, err)

	code, stdout, stderr := runMain(t, nil, env)

	assert.Equal(t, agent.HelperExitUnusable, code)
	assert.Equal(t, "LeapMux helper: AGENT_PROVIDER_CLAUDE_CODE registers no helper \"permission\"\n", stderr,
		"the helper's message is the whole stderr")
	assert.Empty(t, stdout)
}

// A run with an argument is never a helper, whatever the environment holds:
// the leapmux CLI needs an argument for everything else that it does.
func TestMainRunsTheCLIWhenItHasAnArgument(t *testing.T) {
	t.Parallel()
	spec := filepath.Join(t.TempDir(), "helper.json")
	env, err := agent.WriteHelperSpec(spec, agent.HelperSpec{
		Provider: "AGENT_PROVIDER_CLAUDE_CODE",
		Helper:   "permission",
	})
	require.NoError(t, err)
	require.Equal(t, contracts.EnvAgentHelper+"="+spec, env, "the child gets the helper variable")

	code, stdout, stderr := runMain(t, []string{"--version"}, env)

	assert.Zero(t, code)
	assert.Equal(t, version.Format()+"\n", stdout)
	assert.NotContains(t, stderr, "LeapMux helper")
}
