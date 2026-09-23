package claude

import (
	"context"
	"os/exec"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Claude's registration states no resolver, so a launch resolves the name that
// its locator lists: the same list that the availability scan probes. Passing the
// names in would be a second source that could disagree with it.
func TestClaudeLaunchFallsBackToItsRegisteredBinaryName(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this machine")
	}
	reg := Registration()
	spec, err := providerkit.ResolveLaunch(context.Background(), agent.Options{Shell: shell},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, reg.Locator)

	require.NoError(t, err)
	assert.Equal(t, "claude", spec.Program, "Claude registers exactly one candidate")
	assert.Empty(t, spec.PrefixArgs)
	assert.Empty(t, spec.Env)
}
