package acp

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACPCurrentModeUpdatesTheSharedSettings(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		mode ModeChannel
	}{
		{"permission mode", ModeChannelPermissionMode},
		{"primary agent", ModeChannelPrimaryAgent},
		{"unmapped mode", ModeChannelUnmapped},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			a, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
			a.hooks.ModeChannel = test.mode
			a.handleACPUpdate(json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Retained text"}}`))
			original := json.RawMessage(` {"sessionUpdate":"current_mode_update", "currentModeId":"plan", "future":9007199254740993} `)
			a.handleACPUpdate(original)
			assert.Equal(t, "plan", *a.secondaryChannel().field)
			assert.Equal(t, 1, sink.SettingsRefreshCount())
			require.Len(t, sink.Messages(), 1)
			assert.Equal(t, []byte(original), sink.Messages()[0].Content)
			a.turnMu.Lock()
			assert.Equal(t, "Retained text", a.turnAssistantText.String())
			a.turnMu.Unlock()
		})
	}
}

func TestACPCurrentModeRejectsInvalidValuesAndAvoidsDuplicateRefreshes(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	a, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
	a.permissionMode = "plan"
	for _, payload := range []string{
		`{"sessionUpdate":"current_mode_update"}`,
		`{"sessionUpdate":"current_mode_update","currentModeId":""}`,
		`{"sessionUpdate":"current_mode_update","currentModeId":null}`,
		`{"sessionUpdate":"current_mode_update","currentModeId":0}`,
		`{"sessionUpdate":"current_mode_update","currentModeId":"plan"}`,
	} {
		a.handleACPUpdate(json.RawMessage(payload))
		assert.Equal(t, "plan", a.permissionMode)
	}
	assert.Zero(t, sink.SettingsRefreshCount())
	assert.Len(t, sink.Messages(), 5)
}

func TestObserveCurrentMode_UpdatesTheAxisOnce(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink)}
	b.hooks.ModeChannel = ModeChannelPermissionMode

	b.ObserveCurrentMode("plan")
	b.ObserveCurrentMode("plan")
	b.ObserveCurrentMode("")

	assert.Equal(t, "plan", b.PermissionModeForTest())
	assert.Equal(t, 1, sink.SettingsRefreshCount(), "a repeated report changes nothing, and an empty one states nothing")
}
