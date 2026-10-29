//go:build unix

package zcode

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInterrupt_ZCodeWireFormatMatchesProviderClassifier(t *testing.T) {
	t.Parallel()

	legacy, err := json.Marshal(map[string]any{
		"method": MethodSessionStop,
		"params": map[string]any{"sessionId": "sess-1"},
	})
	require.NoError(t, err)
	assert.True(t, zcodeProvider{}.IsInterrupt(string(legacy)),
		"zcodeProvider.IsInterrupt must recognise the legacy frame Agent.Interrupt emits")

	guarded, err := json.Marshal(map[string]any{
		"id":     7,
		"method": MethodCommand,
		"params": map[string]any{
			"commandId": "leapmux-stop-1",
			"clientId":  zcodeCommandClientID,
			"sessionId": "sess-1",
			"type":      zcodeCommandStop,
			"payload":   map[string]any{"expectedForegroundExecutionId": "exec-1"},
			"issuedAt":  0,
		},
	})
	require.NoError(t, err)
	assert.True(t, zcodeProvider{}.IsInterrupt(string(guarded)),
		"zcodeProvider.IsInterrupt must recognise the guarded v4 stop frame Agent.Interrupt emits")

	steer, err := json.Marshal(map[string]any{
		"id":     8,
		"method": MethodCommand,
		"params": map[string]any{"commandId": "leapmux-steer-1", "type": zcodeCommandSendText},
	})
	require.NoError(t, err)
	assert.False(t, zcodeProvider{}.IsInterrupt(string(steer)),
		"any other v4 command is not an interrupt")
}
