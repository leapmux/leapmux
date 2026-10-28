package qwen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

// The fake server follows Qwen Code's own rules (packages/cli/src/acp-integration of
// Qwen Code):
//
//   - A model write carries the reasoning tier over. Config.handleModelChange copies
//     it and Session.reconcileReasoningSelection drops a tier that the new model
//     does not support.
//   - The server never sends config_option_update. The reply to the write holds the
//     only snapshot.
//   - A write of a tier that the current model lacks fails with -32602.
//   - A model without a reasoning capability has no reasoning_effort option.
//
// The worker merges the stored tier into a model switch, so UpdateSettings receives
// {model: B, reasoning_effort: X}.

func newQwenSwitchServer(levelsOfB ...string) *acptest.ModelSwitchServer {
	server := &acptest.ModelSwitchServer{
		EffortID: contracts.QwenConfigReasoningEffort,
		Models:   []string{"model-a", "model-b"},
		Levels:   map[string][]string{"model-a": {"none", "default", "low", "medium", "high"}, "model-b": levelsOfB},
	}
	server.Start("model-a", "high")
	return server
}

func TestQwenModelSwitchKeepsTheTierThatTheServerKept(t *testing.T) {
	t.Parallel()
	server := newQwenSwitchServer("none", "default", "low", "medium", "high")
	a, _, requests := newQwenAgent(t, nil, server.Respond)
	server.Seed(a, false)

	result := a.UpdateSettings(map[string]string{agent.OptionIDModel: "model-b", contracts.QwenConfigReasoningEffort: "high"})

	require.True(t, result.AppliedLive)
	assert.Equal(t, []string{"model=model-b"}, acptest.ConfigWrites(requests()))
	assert.Equal(t, "high", agent.CurrentOptions(a.OptionGroups())[contracts.QwenConfigReasoningEffort])
}

// A tier that the new model does not support is dropped by the server, which shows the
// default of the model. The base does not push the stale tier.
func TestQwenModelSwitchSettlesOnTheTierOfAModelThatDroppedTheStoredOne(t *testing.T) {
	t.Parallel()
	server := newQwenSwitchServer("none", "default")
	server.Reset = func(string, string) string { return "default" }
	a, _, requests := newQwenAgent(t, nil, server.Respond)
	server.Seed(a, false)

	result := a.UpdateSettings(map[string]string{agent.OptionIDModel: "model-b", contracts.QwenConfigReasoningEffort: "high"})

	require.True(t, result.AppliedLive)
	assert.Equal(t, []string{"model=model-b"}, acptest.ConfigWrites(requests()))
	require.NotNil(t, result.Settlements[contracts.QwenConfigReasoningEffort].Value)
	assert.Equal(t, "default", *result.Settlements[contracts.QwenConfigReasoningEffort].Value)
}

// A model without a reasoning capability has no reasoning_effort option, and the
// server refuses a write of one. The base must not write the stored tier then.
func TestQwenModelSwitchToAModelWithoutReasoningWritesNoTier(t *testing.T) {
	t.Parallel()
	server := newQwenSwitchServer()
	a, _, requests := newQwenAgent(t, nil, server.Respond)
	server.Seed(a, false)

	result := a.UpdateSettings(map[string]string{agent.OptionIDModel: "model-b", contracts.QwenConfigReasoningEffort: "high"})

	require.True(t, result.AppliedLive, "the server refuses a tier write for a model without reasoning")
	assert.Equal(t, []string{"model=model-b"}, acptest.ConfigWrites(requests()))
	assert.Nil(t, optionids.GroupByID(a.OptionGroups(), contracts.QwenConfigReasoningEffort))
}

// The launch states the style only for a value other than Auto, so the style
// of Qwen's own settings applies.
func TestQwenOutputStyleArgs(t *testing.T) {
	t.Parallel()
	assert.Nil(t, qwenOutputStyleArgs(""))
	assert.Nil(t, qwenOutputStyleArgs("auto"))
	assert.Nil(t, qwenOutputStyleArgs("  "))
	assert.Equal(t, []string{"--output-style", "Concise"}, qwenOutputStyleArgs("Concise"))
}

// A changed style needs a restart; the running value and an absent value
// reach the shared writer.
func TestQwenUpdateSettingsRestartsOnAStyleChange(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	a.stateMu.Lock()
	a.outputStyle = "Concise"
	a.stateMu.Unlock()

	restart := a.UpdateSettings(map[string]string{OptionOutputStyle: "Learning"})
	assert.False(t, restart.AppliedLive, "Qwen reads the style only at start")

	same := a.UpdateSettings(map[string]string{OptionOutputStyle: "Concise"})
	assert.True(t, same.AppliedLive, "the running style needs no restart")

	absent := a.UpdateSettings(map[string]string{agent.OptionIDModel: "m"})
	assert.True(t, absent.AppliedLive)
}

// The group lists Auto first and the built-in styles, and shows the launch
// value as the current one.
func TestQwenOutputStyleGroup(t *testing.T) {
	t.Parallel()
	group := qwenOutputStyleGroup("", qwenBuiltinOutputStyles)
	assert.Equal(t, OptionOutputStyle, group.GetId())
	assert.Equal(t, qwenOutputStyleAuto, group.GetCurrentValue())
	ids := make([]string, 0, len(group.GetOptions()))
	for _, option := range group.GetOptions() {
		ids = append(ids, option.GetId())
	}
	assert.Equal(t, []string{qwenOutputStyleAuto, "Concise", "Proactive", "Explanatory", "Learning"}, ids)

	a, _, _ := newQwenAgent(t, nil, nil)
	a.stateMu.Lock()
	a.outputStyle = "Proactive"
	a.stateMu.Unlock()
	groups := a.outputStyleGroups()
	require.Len(t, groups, 1)
	assert.Equal(t, "Proactive", groups[0].GetCurrentValue())
}
