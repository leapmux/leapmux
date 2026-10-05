package kilo

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
)

// The fake server follows Kilo's own rules (packages/opencode/src/acp/service.ts of Kilo):
//
//   - A model write always resets the effort to the first variant of the model, even for
//     the model that already runs. Kilo has no keep rule.
//   - The server sends no config_option_update. The reply to the write is the only snapshot.
//   - A write of an effort that the current model lacks fails with -32602.
//   - A model without variants has no effort option.
//
// The worker merges the stored effort into a model switch, so UpdateSettings receives
// {model: B, effort: X}.

const (
	kiloModelA = "leapmux/model-a"
	kiloModelB = "leapmux/model-b"
)

func newKiloSwitchServer(variantsOfB ...string) *acptest.ModelSwitchServer {
	server := &acptest.ModelSwitchServer{
		EffortID: agent.OptionIDEffort,
		Models:   []string{kiloModelA, kiloModelB},
		Levels:   map[string][]string{kiloModelA: {"low", "medium", "high"}, kiloModelB: variantsOfB},
	}
	server.Reset = func(model, _ string) string { return server.Levels[model][0] }
	server.Start(kiloModelA, "high")
	return server
}

func newKiloSwitchAgent(t *testing.T, server *acptest.ModelSwitchServer) (*Agent, *agenttest.Sink, func() []agenttest.RecordedRequest) {
	t.Helper()
	ag, requests := acptest.NewAgentForRPCWithRequestResponder(t,
		func() *Agent {
			a := &Agent{}
			*a.HooksForTest() = opencode.FamilyHooks()
			return a
		},
		func(a *Agent) *acp.Base { return &a.Base },
		server.Respond,
	)
	return ag, server.Seed(ag, false), requests
}

func TestKiloModelSwitchWritesTheChosenEffortAfterTheNativeReset(t *testing.T) {
	t.Parallel()
	server := newKiloSwitchServer("low", "medium", "high")
	ag, _, requests := newKiloSwitchAgent(t, server)

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: kiloModelB, agent.OptionIDEffort: "high"})

	require.True(t, result.AppliedLive)
	assert.Equal(t, []string{"model=" + kiloModelB, "effort=high"}, acptest.ConfigWrites(requests()))
	assert.Equal(t, "high", agent.CurrentOptions(ag.OptionGroups())[agent.OptionIDEffort])
	require.NotNil(t, result.Settlements[agent.OptionIDEffort].Value)
	assert.Equal(t, "high", *result.Settlements[agent.OptionIDEffort].Value)
}

func TestKiloModelSwitchToAModelWithoutVariantsWritesNoEffort(t *testing.T) {
	t.Parallel()
	server := newKiloSwitchServer()
	ag, _, requests := newKiloSwitchAgent(t, server)

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: kiloModelB, agent.OptionIDEffort: "high"})

	require.True(t, result.AppliedLive, "the server refuses an effort write for a model without variants")
	assert.Equal(t, []string{"model=" + kiloModelB}, acptest.ConfigWrites(requests()))
	assert.Nil(t, optionids.GroupByID(ag.OptionGroups(), agent.OptionIDEffort))
}
