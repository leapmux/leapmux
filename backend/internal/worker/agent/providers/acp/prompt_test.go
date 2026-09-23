package acp

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACPPromptCanStartAfterTheSessionChanges(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	a, _ := newACPTurnBase(t, agenttest.NopStdin(&output))
	require.NoError(t, a.SendInput("Old session input", nil))
	cleared := make(chan error, 1)
	go func() {
		_, err := a.ClearContext()
		cleared <- err
	}()
	require.Eventually(t, func() bool {
		return a.IsPendingForTest(int64(2))
	}, time.Second, time.Millisecond)
	a.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":2,"result":{"sessionId":"new-session"}}`)))
	require.NoError(t, <-cleared)
	require.NoError(t, a.SendInput("New session input", nil))
}

func TestACPPromptIgnoresAResponseFromThePreviousSession(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	a, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
	require.NoError(t, a.SendInput("Current session input", nil))
	a.finishPromptRequest("previous-session", json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	assert.Empty(t, sink.Messages())
	active, _ := sink.LastTurnActive()
	assert.True(t, active)
	require.ErrorIs(t, a.SendInput("Later input", nil), agent.ErrAgentBusy)
}

func TestACPPromptPreservesTheCompleteNativeResultWrapper(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	a, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
	original := json.RawMessage(` {"id":"native-result", "role":"result", "future":9007199254740993, "content":{"stopReason":"end_turn","usage":{"totalTokens":0}}} `)
	a.handleACPPromptResponse(original)
	require.Len(t, sink.Messages(), 1)
	assert.Equal(t, []byte(original), sink.Messages()[0].Content)
	assert.True(t, sink.Messages()[0].TurnEnd)
}
