package reasonix

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

// The fake server follows Reasonix's own rules (internal/acp/service.go and
// internal/acp/session_config_delta.go of Reasonix):
//
//   - A model write to a different model drops the effort override, so the new model
//     starts at `auto` (applyTo for the model axis).
//   - The server sends config_option_update before it replies to the write.
//   - A write of an effort that the current model lacks fails with -32602.
//   - A model with no effort capability has no effort option.
//
// The worker merges the stored effort into a model switch, so UpdateSettings receives
// {model: B, effort: X}.

func newReasonixSwitchServer(levelsOfB ...string) *acptest.ModelSwitchServer {
	server := &acptest.ModelSwitchServer{
		EffortID: agent.OptionIDEffort,
		Models:   []string{"model-a", "model-b"},
		Levels:   map[string][]string{"model-a": {"auto", "low", "medium", "high"}, "model-b": levelsOfB},
		Reset:    func(string, string) string { return "auto" },
	}
	server.Start("model-a", "high")
	return server
}

func newReasonixSwitchAgent(t *testing.T, server *acptest.ModelSwitchServer) (*Agent, *agenttest.Sink, func() []agenttest.RecordedRequest) {
	t.Helper()
	ag, requests := acptest.NewAgentForRPCWithRequestResponder(t,
		func() *Agent {
			a := &Agent{}
			// Start declares the mode channel in its hooks.
			a.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
			return a
		},
		func(a *Agent) *acp.Base { return &a.Base },
		server.Respond,
	)
	return ag, server.Seed(ag, true), requests
}

func TestReasonixModelSwitchWritesTheChosenEffortAfterTheNativeReset(t *testing.T) {
	t.Parallel()
	server := newReasonixSwitchServer("auto", "low", "medium", "high")
	ag, sink, requests := newReasonixSwitchAgent(t, server)
	before := sink.SettingsRefreshCount()

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: "model-b", agent.OptionIDEffort: "medium"})

	require.True(t, result.AppliedLive)
	assert.Equal(t, []string{"model=model-b", "effort=medium"}, acptest.ConfigWrites(requests()))
	assert.Equal(t, "medium", agent.CurrentOptions(ag.OptionGroups())[agent.OptionIDEffort])
	require.NotNil(t, result.Settlements[agent.OptionIDEffort].Value)
	assert.Equal(t, "medium", *result.Settlements[agent.OptionIDEffort].Value)
	require.Greater(t, sink.SettingsRefreshCount(), before, "the notification persists the reset effort")
	assert.Equal(t, "medium", sink.LastSettingsRefresh().Effort,
		"the last persisted refresh states the effort that the session runs")
}

func TestReasonixModelSwitchToAModelWithoutEffortWritesNoEffort(t *testing.T) {
	t.Parallel()
	server := newReasonixSwitchServer()
	ag, _, requests := newReasonixSwitchAgent(t, server)

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: "model-b", agent.OptionIDEffort: "high"})

	require.True(t, result.AppliedLive, "the server refuses an effort write for a model without the capability")
	assert.Equal(t, []string{"model=model-b"}, acptest.ConfigWrites(requests()))
	assert.Nil(t, optionids.GroupByID(ag.OptionGroups(), agent.OptionIDEffort))
}
