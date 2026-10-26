package goose

import (
	"testing"

	"github.com/leapmux/leapmux/internal/util/optionmap"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

// The fake server follows Goose's own rules (crates/goose/src/acp/server.rs and
// response_builder.rs of Goose):
//
//   - A model write keeps the raw thinking_effort value. The server masks the value
//     that it shows: a model that does not reason shows only `off`.
//   - The server sends config_option_update before it replies to the write.
//   - The thinking_effort option is always present.
//
// The worker merges the stored effort into a model switch, so UpdateSettings receives
// {model: B, thinking_effort: X}.

func newGooseSwitchServer(levelsOfB ...string) *acptest.ModelSwitchServer {
	server := &acptest.ModelSwitchServer{
		EffortID: contracts.GooseConfigThinkingEffort,
		Models:   []string{"model-a", "model-b"},
		Levels:   map[string][]string{"model-a": {"off", "low", "medium", "high", "max"}, "model-b": levelsOfB},
	}
	server.Start("model-a", "medium")
	return server
}

func newGooseSwitchAgent(t *testing.T, server *acptest.ModelSwitchServer) (*Agent, *agenttest.Sink, func() []agenttest.RecordedRequest) {
	t.Helper()
	ag, requests := acptest.NewAgentForRPCWithRequestResponder(t,
		func() *Agent {
			a := &Agent{}
			// Start declares both values in its hooks.
			a.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
			a.HooksForTest().EffortConfigID = contracts.GooseConfigThinkingEffort
			return a
		},
		func(a *Agent) *acp.Base { return &a.Base },
		server.Respond,
	)
	return ag, server.Seed(ag, true), requests
}

func TestGooseModelSwitchKeepsTheEffortThatTheServerKept(t *testing.T) {
	t.Parallel()
	server := newGooseSwitchServer("off", "low", "medium", "high", "max")
	ag, _, requests := newGooseSwitchAgent(t, server)

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: "model-b", contracts.GooseConfigThinkingEffort: "medium"})

	require.True(t, result.AppliedLive)
	assert.Equal(t, []string{"model=model-b"}, acptest.ConfigWrites(requests()), "the server kept the level, so no second write follows")
	assert.Equal(t, "medium", agent.CurrentOptions(ag.OptionGroups())[contracts.GooseConfigThinkingEffort])
}

// A model that does not reason shows only `off`. The base cannot write the stored
// level there, and it settles on what the session shows.
func TestGooseModelSwitchToAModelThatDoesNotReasonSettlesOnWhatTheSessionShows(t *testing.T) {
	t.Parallel()
	server := newGooseSwitchServer("off")
	// The server masks the shown value and keeps the raw one.
	server.Reset = func(string, string) string { return "off" }
	ag, _, requests := newGooseSwitchAgent(t, server)

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: "model-b", contracts.GooseConfigThinkingEffort: "medium"})

	require.True(t, result.AppliedLive)
	assert.Equal(t, []string{"model=model-b"}, acptest.ConfigWrites(requests()))
	require.NotNil(t, result.Settlements[contracts.GooseConfigThinkingEffort].Value)
	assert.Equal(t, "off", *result.Settlements[contracts.GooseConfigThinkingEffort].Value)
}

// A changed Native Commands value needs a restart; the running value and an
// absent value reach the shared writer.
func TestGooseUpdateSettingsRestartsOnANativeCommandsChange(t *testing.T) {
	t.Parallel()
	goose, _ := acptest.NewAgentForRPC(t,
		func() *Agent { return &Agent{} },
		func(agent *Agent) *acp.Base { return &agent.Base },
	)
	goose.nativeCommands = false

	restart := goose.UpdateSettings(map[string]string{OptionNativeCommands: "true"})
	assert.False(t, restart.AppliedLive, "Goose reads the terminal capability only in the handshake")

	same := goose.UpdateSettings(map[string]string{OptionNativeCommands: "false"})
	assert.True(t, same.AppliedLive)

	absent := goose.UpdateSettings(map[string]string{agent.OptionIDModel: "m"})
	assert.True(t, absent.AppliedLive)

	groups := goose.nativeCommandGroups()
	require.Len(t, groups, 1)
	assert.Equal(t, "false", groups[0].GetCurrentValue())
	goose.nativeCommands = true
	groups = goose.nativeCommandGroups()
	assert.Equal(t, "true", groups[0].GetCurrentValue())
}

func TestGooseRejectsInvalidNativeCommandsBeforeOtherSettings(t *testing.T) {
	t.Parallel()
	for _, current := range []bool{false, true} {
		for _, value := range []string{"bogus", "0", "-1", "999999999999999999", "TRUE", " false "} {
			a, requests := acptest.NewAgentForRPC(t, func() *Agent { return &Agent{} }, func(a *Agent) *acp.Base { return &a.Base })
			a.nativeCommands = current
			a.HooksForTest().LocalOptionGroups = a.nativeCommandGroups
			a.SetModelForTest("model-a")
			before := a.SettingsSnapshot().ConfirmedOptions()
			result := a.UpdateSettings(optionmap.Map{OptionNativeCommands: value, agent.OptionIDModel: "model-b"})
			assert.True(t, result.AppliedLive, "an invalid choice must not restart the process")
			assert.Equal(t, before, result.ConfirmedOptions())
			assert.Empty(t, requests())
		}
	}
}
