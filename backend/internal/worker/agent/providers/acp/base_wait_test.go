package acp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

func TestACPWaitRunsProviderCleanupBeforeChildAndTurnCleanup(t *testing.T) {
	t.Parallel()
	a, _ := newTestAgentForRPC(t)
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.HandleOutput(acptest.Chunk("session-1", "agent_message_chunk", "Unfinished answer."))
	before := len(sink.Messages())
	hookCalls := 0
	a.hooks.BeforeWaitCleanup = func() {
		hookCalls++
		assert.Len(t, sink.Messages(), before, "the base must call the hook before it flushes the turn")
	}

	a.SimulateExitForTest()
	require.NoError(t, a.Wait())
	assert.Equal(t, 1, hookCalls)
	messages := sink.Messages()
	require.Greater(t, len(messages), before)
	assert.Contains(t, string(messages[len(messages)-1].Content), "Unfinished answer.")
}
