package worker

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunAgentHelperRunsOnlyWithNoArgumentAndTheVariable(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		args    []string
		spec    string
		handled bool
	}{
		"no argument and the variable":        {nil, "/spec.json", true},
		"an empty argument list":              {[]string{}, "/spec.json", true},
		"an argument, even with the variable": {[]string{"solo"}, "/spec.json", false},
		"no variable":                         {nil, "", false},
		"an argument and no variable":         {[]string{"worker"}, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			ran := false
			getenv := func(key string) string {
				if key == contracts.EnvAgentHelper {
					return tc.spec
				}
				return ""
			}
			code, handled := runAgentHelper(tc.args, getenv, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{},
				func(_ context.Context, specPath string, _ agent.HelperInvocation) int {
					ran = true
					assert.Equal(t, tc.spec, specPath)
					return 3
				})
			assert.Equal(t, tc.handled, handled)
			assert.Equal(t, tc.handled, ran, "the helper runs exactly when the process is one")
			if tc.handled {
				assert.Equal(t, 3, code)
			} else {
				assert.Zero(t, code)
			}
		})
	}
}

func TestRunAgentHelperPassesTheStreamsAndTheEnvironment(t *testing.T) {
	t.Parallel()

	stdin := strings.NewReader("input")
	var stdout, stderr bytes.Buffer
	getenv := func(key string) string {
		if key == contracts.EnvAgentHelper {
			return "/spec.json"
		}
		return "value of " + key
	}
	var got agent.HelperInvocation
	_, handled := runAgentHelper(nil, getenv, stdin, &stdout, &stderr,
		func(ctx context.Context, _ string, invocation agent.HelperInvocation) int {
			require.NoError(t, ctx.Err(), "the context stays live until a signal arrives")
			got = invocation
			return 0
		})
	require.True(t, handled)
	assert.Same(t, stdin, got.Stdin)
	assert.Same(t, &stdout, got.Stdout)
	assert.Same(t, &stderr, got.Stderr)
	assert.Equal(t, "value of AGENT_TOOL_NAME", got.Getenv("AGENT_TOOL_NAME"))
}

// A run that is not a helper removes the variable from its own environment, so
// no child of the worker -- a terminal, a nested worker, any tool -- inherits a
// spec path that belongs to another agent. Not parallel: it changes the
// process environment.
func TestRunAgentHelperRemovesTheVariableFromANormalRun(t *testing.T) {
	t.Setenv(contracts.EnvAgentHelper, "/spec.json")
	code, handled := RunAgentHelper([]string{"solo"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	assert.False(t, handled)
	assert.Zero(t, code)
	_, present := os.LookupEnv(contracts.EnvAgentHelper)
	assert.False(t, present, "the variable is gone from the worker's environment")
}
