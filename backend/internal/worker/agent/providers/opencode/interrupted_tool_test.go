package opencode

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ClearContext replaces the session, and it reaches that swap WITHOUT passing
// clearActivePrompt. A note left behind stamped Interrupted on every completed tool row
// of the NEXT turn.
func TestACPClearContextDropsTheInterruptNote(t *testing.T) {
	a, _ := newOpenCodeAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == acp.MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.Mu.Lock()
	a.SetPromptActiveForTest(true)
	a.Mu.Unlock()
	a.NoteInterruptRequestedForTest()
	require.True(t, a.InterruptRequestedForTest())

	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	require.Equal(t, "session-2", sessionID)
	require.False(t, a.InterruptRequestedForTest(), "the note belonged to the turn the swap ended")

	a.Mu.Lock()
	a.SetPromptActiveForTest(true)
	a.Mu.Unlock()
	a.HandleToolCallForTest(json.RawMessage(`{"toolCallId":"call-1","kind":"execute","title":"Later"}`))
	a.HandleToolCallUpdateForTest(json.RawMessage(`{"toolCallId":"call-1","status":"completed"}`))
	msgs := sink.Messages()
	require.NotEmpty(t, msgs)
	assert.NotEqual(t, agent.MessageCompletionInterrupted, msgs[len(msgs)-1].Completion,
		"nobody stopped the turn after the swap")
}
