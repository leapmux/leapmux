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

// The solo worker of the sidecar can start this executable again as a provider
// helper: no argument, and the helper variable set. main then runs the helper
// and never the sidecar. Its exit code is the helper's, and its stderr holds the
// helper's message and nothing more, because the provider's CLI shows that
// stderr as the reason for a refusal.
//
// The spec states a provider that registers no helper, so the helper refuses at
// once. The refusal proves that the run reached the worker's helper dispatch:
// 2 is agent.HelperExitUnusable, which this module cannot import.
func TestMainRunsTheAgentHelperForARunWithNoArgument(t *testing.T) {
	t.Parallel()
	spec := filepath.Join(t.TempDir(), "helper.json")
	require.NoError(t, os.WriteFile(spec, []byte(`{"provider":"AGENT_PROVIDER_CLAUDE_CODE","helper":"permission"}`), 0o600))

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), runMainEnv+"=1", contracts.EnvAgentHelper+"="+spec)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()

	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr), "the helper exits with its own code: %v", err)
	assert.Equal(t, 2, exitErr.ExitCode())
	assert.Equal(t, "LeapMux helper: AGENT_PROVIDER_CLAUDE_CODE registers no helper \"permission\"\n", stderr.String(),
		"the helper's message is the whole stderr")
	assert.Empty(t, stdout.String(), "the sidecar never started")
}
