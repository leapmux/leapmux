package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func writeHelperSpec(t *testing.T, spec agent.HelperSpec) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "helper.json")
	_, err := agent.WriteHelperSpec(path, spec)
	require.NoError(t, err)
	return path
}

// The dispatch reaches the provider that the spec states. Amp's permission
// helper refuses a configuration it cannot read, which proves the run reached
// it without a bridge to talk to.
func TestRunAgentHelperReachesTheProvidersHelper(t *testing.T) {
	t.Parallel()
	path := writeHelperSpec(t, agent.HelperSpec{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP.String(),
		Helper:   "permission",
		Config:   json.RawMessage(`{}`),
	})
	var stderr bytes.Buffer
	code := RunAgentHelper(context.Background(), path, agent.HelperInvocation{
		Stdin:  strings.NewReader(`{}`),
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
		Getenv: func(string) string { return "" },
	})
	assert.Equal(t, agent.HelperExitUnusable, code)
	assert.Contains(t, stderr.String(), "could not read the configuration of its permission helper")
}

func TestRunAgentHelperRefusesAHelperNoProviderRegisters(t *testing.T) {
	t.Parallel()
	path := writeHelperSpec(t, agent.HelperSpec{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE.String(),
		Helper:   "permission",
	})
	var stderr bytes.Buffer
	code := RunAgentHelper(context.Background(), path, agent.HelperInvocation{
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
	})
	assert.Equal(t, agent.HelperExitUnusable, code)
	assert.Contains(t, stderr.String(), `AGENT_PROVIDER_CLAUDE_CODE registers no helper "permission"`)
}
