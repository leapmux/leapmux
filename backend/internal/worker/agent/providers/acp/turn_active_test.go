package acp

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newACPTurnBase builds the base all six ACP providers embed, wired the way
// Start wires it. Going through wireTurnActive rather than a hand-built
// closure is deliberate: a test that built its own hook would pass even if the
// constructor stopped wiring one, which is the failure these exist to catch.
func newACPTurnBase(t *testing.T, stdin io.WriteCloser) (*Base, *agenttest.Sink) {
	t.Helper()
	sink := &agenttest.Sink{}
	// AwaitResponse selects on the context and the exit channel, so a bare base
	// panics on the nil ctx the moment a detached request waits for its reply.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	b := &Base{}
	b.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID:     "test-agent",
		Stdin:       stdin,
		Ctx:         ctx,
		ProcessDone: make(chan struct{}),
	})
	b.sessionID = "session-1"
	b.sink = agent.NewProviderServices(sink)
	b.wireTurnActive()
	return b, sink
}

func TestACPTurnActive_ThePublishFollowsALaterSinkWrap(t *testing.T) {
	t.Parallel()

	// Start wires the hook and startACPHandshake THEN replaces b.sink with
	// thinkingResetSink. A hook that captured the raw sink would publish past
	// every decorator for the life of the process -- and this flag is the input
	// queue's only dispatch guard, so a decorator that ever overrode
	// SetTurnState would silently hold every later message of all six ACP
	// providers.
	var out bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&out))
	b.sink = &swallowingSink{ProviderServices: agent.NewProviderServices(sink)}

	b.publishTurnActive(true, 1)

	assert.Empty(t, sink.TurnActives(),
		"the hook re-reads b.sink, so the wrap installed after wireTurnActive is honored")
}

func TestACPTurnActive_PromptOpensTheTurnAndTheResponseCloses(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&out))

	require.NoError(t, b.SendInput("hi", nil))
	assert.Equal(t, []bool{true}, sink.TurnActives(), "the turn opens once the prompt is on stdin")

	// The provider answers. SendDetachedRequest correlates on the request id, so
	// feeding the reply drives the real completion path: the response is handled
	// -- which persists the turn end -- and only then is the flag cleared.
	b.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}`)))

	assert.Eventually(t, func() bool { return len(sink.TurnActives()) == 2 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
}

func TestACPTurnActive_AFailedSendOpensNoTurn(t *testing.T) {
	t.Parallel()

	// A prompt that never reached the provider started nothing, and no response
	// is coming to end it. Leaving the flag set would latch the agent busy for
	// the life of the process.
	b, sink := newACPTurnBase(t, agenttest.FailingStdin{})

	require.Error(t, b.SendInput("hi", nil))

	last, published := sink.LastTurnActive()
	require.True(t, published)
	assert.False(t, last)
}

func TestACPTurnActive_ClearActivePromptCloses(t *testing.T) {
	t.Parallel()

	// Stop() clears the active prompt. Without a publish here a stopped agent
	// stays busy forever, because no prompt response is coming to end the turn.
	var out bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&out))
	b.Mu.Lock()
	b.promptActive = true
	b.Mu.Unlock()

	b.clearActivePrompt()

	assert.Equal(t, []bool{false}, sink.TurnActives())
}

func TestTurnEndPrecedesTheClear_ACP(t *testing.T) {
	t.Parallel()

	// The ACP base handles the prompt response -- which persists the turn end --
	// and only then clears promptActive, both inside one callback. This pins that
	// order, because a callback that cleared first would invert it.
	var out bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&out))

	require.NoError(t, b.SendInput("hi", nil))
	b.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}`)))

	assert.Eventually(t, func() bool { return len(sink.TurnLifecycle()) == 4 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"turn_active:true", "turn_end", "reset_spans", "turn_active:false"}, sink.TurnLifecycle())
}

func TestACPTurnActive_IssuesRisingOrderingTokens(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	var out bytes.Buffer
	b, _ := newACPTurnBase(t, agenttest.NopStdin(&out))
	b.sink = agent.NewProviderServices(sink)
	b.wireTurnActive()
	agenttest.AssertRisingTurnTokens(t, sink, b)
}

// swallowingSink stands in for a decorator that forgets to forward the turn
// flag. thinkingResetSink promotes SetTurnState from the embedded interface
// today, so only a type like this one can tell a hook that re-reads b.sink from
// one that captured the sink it was wired with.
type swallowingSink struct {
	agent.ProviderServices
}

func (s *swallowingSink) SetTurnState(agent.TurnState, uint64) {}

func TestACPTurnActive_ProviderErrorPersistsBufferedText(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&out))
	require.NoError(t, b.SendInput("hi", nil))
	b.HandleOutput(acptest.Chunk(b.CurrentSessionID(), "agent_message_chunk", "partial answer"))

	b.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"provider failed"}}`)))

	require.Eventually(t, func() bool { return sink.MessageCount() == 1 }, time.Second, 5*time.Millisecond)
	assert.JSONEq(t, `{
		"type":"assembled_message",
		"kind":"text",
		"text":"partial answer",
		"completion":"error"
	}`, string(sink.Messages()[0].Content))
}
