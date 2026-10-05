package dirac

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

// The fake server follows Dirac's own rules (cli/src/agent/sessionConfig.ts of Dirac):
//
//   - A model write never touches reasoning_effort. The value set is the same for every
//     model, and the option is always present.
//   - The server sends config_option_update before it replies to the write.
//
// The worker merges the stored level into a model switch, so UpdateSettings receives
// {model: B, reasoning_effort: X}.
func TestDiracModelSwitchKeepsTheReasoningEffort(t *testing.T) {
	t.Parallel()
	levels := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	server := &acptest.ModelSwitchServer{
		EffortID: contracts.DiracConfigReasoningEffort,
		Models:   []string{"model-a", "model-b"},
		Levels:   map[string][]string{"model-a": levels, "model-b": levels},
	}
	server.Start("model-a", "xhigh")
	a, requests := acptest.NewAgentForRPCWithRequestResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		server.Respond,
	)
	*a.HooksForTest() = a.configure(nil)
	server.Seed(a, true)

	result := a.UpdateSettings(map[string]string{agent.OptionIDModel: "model-b", contracts.DiracConfigReasoningEffort: "xhigh"})

	require.True(t, result.AppliedLive)
	assert.Equal(t, []string{"model=model-b"}, acptest.ConfigWrites(requests()), "the server kept the level, so no second write follows")
	assert.Equal(t, "xhigh", agent.CurrentOptions(a.OptionGroups())[contracts.DiracConfigReasoningEffort])
}
