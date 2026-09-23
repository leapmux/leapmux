package codex

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexTurnActive_StartedOpensAndCompletedCloses(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-42"}}}`)))
	assert.Equal(t, []bool{true}, sink.TurnActives())
	assert.Equal(t, []leapmuxv1.AgentInputKind{leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE}, sink.TurnKinds(),
		"Codex accepts turn/steer for a provider-started turn, so the queue must classify it as steerable")

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-42","status":"completed"}}}`)))

	assert.Equal(t, []bool{true, false}, sink.TurnActives())
	assert.Equal(t, []leapmuxv1.AgentInputKind{
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_UNSPECIFIED,
	}, sink.TurnKinds(), "the turn end must clear the steering classification")
}

func TestCodexTurnActive_ReadsNativeTurnID(t *testing.T) {
	t.Parallel()

	// Codex publishes its activity from the native turn identity.
	sink := &agenttest.ControlSink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-7"}}}`)))

	a.Mu.Lock()
	turnID := a.turnID
	a.Mu.Unlock()
	require.Equal(t, "turn-7", turnID)

	last, published := sink.LastTurnActive()
	require.True(t, published)
	assert.True(t, last, "the published state comes from turnID")
}

func TestCodexTurnActive_ChildThreadCompletionLeavesTheRootTurnOpen(t *testing.T) {
	t.Parallel()

	// A collab child ends its own turn while the main thread keeps working.
	// The root's flag is what the MAIN tab's thinking indicator reads, so
	// clearing it here would report the agent finished while the root still runs.
	// Claude's forwarded-subagent result is pinned the same way above.
	sink := &agenttest.ControlSink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-42"}}}`)))
	require.Equal(t, []bool{true}, sink.TurnActives())

	// A REGISTERED child: the spawn puts child-1 in the child index, so its
	// completion routes into the child transcript.
	spawn := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-42","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(spawn)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"turn-c1"}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"turn-c1","status":"completed"}}}`)))
	assert.Equal(t, []bool{true}, sink.TurnActives(), "a registered child's turn end is not the root's")

	// An UNREGISTERED thread takes the other branch of the same test -- a late
	// receiver the spawn never named -- and must not clear the root either.
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"stranger","turn":{"id":"turn-x","status":"completed"}}}`)))
	assert.Equal(t, []bool{true}, sink.TurnActives(), "an unknown thread is still not the main thread")

	// The main thread's own completion is what ends the root's turn.
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-42","status":"completed"}}}`)))
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
}

func TestTurnEndPrecedesTheClear_Codex(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-42"}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-42","status":"completed"}}}`)))

	assert.Equal(t, []string{"turn_active:true", "turn_end", "reset_spans", "turn_active:false"}, sink.TurnLifecycle())
}

func TestCodexTurnActive_IssuesRisingOrderingTokens(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agenttest.AssertRisingTurnTokens(t, sink, a)
}
