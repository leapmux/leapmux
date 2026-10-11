package opencode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

// The tests below drive one model switch the way the worker sends it. The user changes
// only the model, and the worker merges the stored effort into the update, so
// UpdateSettings receives {model: B, effort: X}.
//
// The fake server follows OpenCode's own rules (packages/opencode/src/acp/service.ts):
//
//   - A model write to a different model resets the effort to the first variant of the
//     new model (selectModelVariant, selectVariant).
//   - The server sends config_option_update before it replies to the model write.
//   - A write of an effort that the current model lacks fails with -32602.
//   - A model without variants has no effort option.

const (
	openCodeModelA = "leapmux/model-a"
	openCodeModelB = "leapmux/model-b"
)

// openCodeSwitchServer is OpenCode with two models. model-a offers low, medium and
// high. The variants of model-b are the caller's.
func openCodeSwitchServer(variantsOfB ...string) *acptest.ModelSwitchServer {
	server := &acptest.ModelSwitchServer{
		EffortID: agent.OptionIDEffort,
		Models:   []string{openCodeModelA, openCodeModelB},
		Levels:   map[string][]string{openCodeModelA: {"low", "medium", "high"}, openCodeModelB: variantsOfB},
	}
	server.Reset = func(model, _ string) string { return server.Levels[model][0] }
	server.Start(openCodeModelA, "high")
	return server
}

func newOpenCodeSwitchAgent(t *testing.T, server *acptest.ModelSwitchServer) (*Agent, *agenttest.Sink, func() []agenttest.RecordedRequest) {
	t.Helper()
	ag, requests := acptest.NewAgentForRPCWithRequestResponder(t,
		func() *Agent {
			a := &Agent{}
			*a.HooksForTest() = FamilyHooks()
			return a
		},
		func(a *Agent) *acp.Base { return &a.Base },
		server.Respond,
	)
	return ag, server.Seed(ag, true), requests
}

func TestOpenCodeModelSwitchKeepsTheChosenEffortAfterTheNativeReset(t *testing.T) {
	t.Parallel()
	server := openCodeSwitchServer("none", "low", "medium", "high")
	ag, sink, requests := newOpenCodeSwitchAgent(t, server)
	before := sink.SettingsRefreshCount()

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: openCodeModelB, agent.OptionIDEffort: "medium"})

	require.True(t, result.AppliedLive)
	writes := openCodeEffortWrites(requests())
	require.NotEmpty(t, writes)
	assert.Equal(t, "medium", writes[len(writes)-1].Params["value"],
		"the stored effort is the last write, after the reset to the first variant")
	assert.Equal(t, "medium", agent.CurrentOptions(ag.OptionGroups())[agent.OptionIDEffort])
	require.NotNil(t, result.Settlements[agent.OptionIDEffort].Value)
	assert.Equal(t, "medium", *result.Settlements[agent.OptionIDEffort].Value)
	require.Greater(t, sink.SettingsRefreshCount(), before, "the notification persists the reset effort")
	assert.Equal(t, "medium", sink.LastSettingsRefresh().Effort,
		"the last persisted refresh states the effort that the session runs")
}

// A user who turned the effort off keeps it off. The base raises the effort of a new
// model that starts at none, and then writes the stored level again.
func TestOpenCodeModelSwitchKeepsAnExplicitNoneEffort(t *testing.T) {
	t.Parallel()
	server := openCodeSwitchServer("none", "low", "medium", "high")
	server.Levels[openCodeModelA] = []string{"none", "low", "medium", "high"}
	server.Start(openCodeModelA, "none")
	ag, sink, _ := newOpenCodeSwitchAgent(t, server)

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: openCodeModelB, agent.OptionIDEffort: "none"})

	require.True(t, result.AppliedLive)
	assert.Equal(t, "none", agent.CurrentOptions(ag.OptionGroups())[agent.OptionIDEffort])
	assert.Equal(t, "none", sink.LastSettingsRefresh().Effort, "the persisted refresh states none")
}

// A model without variants has no effort option, and the server refuses a write of
// one. The switch must not write the stored effort, because the refusal would restart
// the agent.
func TestOpenCodeModelSwitchToAModelWithoutVariantsWritesNoEffort(t *testing.T) {
	t.Parallel()
	server := openCodeSwitchServer()
	ag, _, requests := newOpenCodeSwitchAgent(t, server)

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: openCodeModelB, agent.OptionIDEffort: "high"})

	require.True(t, result.AppliedLive, "the server refuses an effort write for a model without variants")
	assert.Empty(t, openCodeEffortWrites(requests()))
	assert.Nil(t, optionids.GroupByID(ag.OptionGroups(), agent.OptionIDEffort))
	_, surfaced := result.SurfacedOptions[agent.OptionIDEffort]
	assert.False(t, surfaced)
}
