package agent

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACPCurrentModeUpdatesTheSharedSettings(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		mode acpModeChannel
	}{
		{"permission mode", modeChannelPermissionMode},
		{"primary agent", modeChannelPrimaryAgent},
		{"unmapped mode", modeChannelUnmapped},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			a, sink := newACPTurnBase(t, nopWriteCloser{&output})
			a.modeChannel = test.mode
			a.handleACPUpdate(json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Retained text"}}`), nil)
			original := json.RawMessage(` {"sessionUpdate":"current_mode_update", "currentModeId":"plan", "future":9007199254740993} `)
			a.handleACPUpdate(original, nil)
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
	a, sink := newACPTurnBase(t, nopWriteCloser{&output})
	a.permissionMode = "plan"
	for _, payload := range []string{
		`{"sessionUpdate":"current_mode_update"}`,
		`{"sessionUpdate":"current_mode_update","currentModeId":""}`,
		`{"sessionUpdate":"current_mode_update","currentModeId":null}`,
		`{"sessionUpdate":"current_mode_update","currentModeId":0}`,
		`{"sessionUpdate":"current_mode_update","currentModeId":"plan"}`,
	} {
		a.handleACPUpdate(json.RawMessage(payload), nil)
		assert.Equal(t, "plan", a.permissionMode)
	}
	assert.Zero(t, sink.SettingsRefreshCount())
	assert.Len(t, sink.Messages(), 5)
}
