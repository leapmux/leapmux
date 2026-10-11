package acp

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModeListChangeRepublishesGoalCapabilities(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink)}
	b.hooks.ModeChannel = ModeChannelPermissionMode
	update := func(modes ...string) json.RawMessage {
		options := make([]map[string]string, len(modes))
		for i, mode := range modes {
			options[i] = map[string]string{"value": mode, "name": mode}
		}
		raw, err := json.Marshal(map[string]any{
			"sessionUpdate": "config_option_update",
			"configOptions": []map[string]any{{"id": "mode", "category": "mode", "type": "select", "currentValue": modes[0], "options": options}},
		})
		require.NoError(t, err)
		return raw
	}

	b.HandleConfigOptionUpdateForTest(update("normal", "plan"))
	assert.Equal(t, 1, sink.GoalCapabilityPublishes())
	b.HandleConfigOptionUpdateForTest(update("normal", "plan"))
	assert.Equal(t, 1, sink.GoalCapabilityPublishes(), "an unchanged list republishes nothing")
	// A provider can derive its goal actions from this list (Reasonix sets a
	// goal through its `goal` mode), so a new list republishes them.
	b.HandleConfigOptionUpdateForTest(update("normal", "plan", "goal"))
	assert.Equal(t, 2, sink.GoalCapabilityPublishes())
}

// A provider reports a command list of its own through ReplaceAvailableCommands.
// An empty name offers no command, the set ignores order and repeats, and only
// a change republishes the goal capabilities.
func TestReplaceAvailableCommands_RepublishesTheGoalCapabilitiesOnAChange(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink)}

	b.ReplaceAvailableCommands([]string{"goal", "", "compact"})
	assert.True(t, b.HasAvailableCommand("goal"))
	assert.True(t, b.HasAvailableCommand("compact"))
	assert.False(t, b.HasAvailableCommand(""), "an empty name offers no command")
	assert.Equal(t, 1, sink.GoalCapabilityPublishes())

	b.ReplaceAvailableCommands([]string{"compact", "goal", "goal"})
	assert.Equal(t, 1, sink.GoalCapabilityPublishes(), "the same set in another order republishes nothing")

	b.ReplaceAvailableCommands(nil)
	assert.False(t, b.HasAvailableCommand("goal"), "an empty list withdraws every command")
	assert.Equal(t, 2, sink.GoalCapabilityPublishes())

	unwired := &Base{}
	unwired.ReplaceAvailableCommands([]string{"goal"})
	assert.True(t, unwired.HasAvailableCommand("goal"), "an agent with no services yet still records the list")
}

func TestModeListChangeOnARefreshRepublishesGoalCapabilities(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink)}
	b.hooks.ModeChannel = ModeChannelPermissionMode

	b.ApplySessionRefreshForTest(json.RawMessage(`{"sessionId":"s","modes":{"currentModeId":"normal","availableModes":[{"id":"normal"},{"id":"goal"}]}}`))
	assert.Equal(t, 1, sink.GoalCapabilityPublishes())
	b.ApplySessionRefreshForTest(json.RawMessage(`{"sessionId":"s","modes":{"currentModeId":"normal","availableModes":[{"id":"normal"},{"id":"goal"}]}}`))
	assert.Equal(t, 1, sink.GoalCapabilityPublishes())
}
