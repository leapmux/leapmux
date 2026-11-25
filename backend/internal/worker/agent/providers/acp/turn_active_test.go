package acp

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newACPTurnBase builds the base every ACP provider embeds, wired the way
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

	// Start connects the hook before startACPHandshake replaces b.sink through NewModelProgressResetSink.
	// A hook that captured the original sink would bypass every later decorator for the process's lifetime.
	// The turn flag controls input-queue dispatch alongside other queue conditions.
	// A decorator that changes SetTurnState must receive these calls, or later input can stay queued without a report.
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

	require.NoError(t, agenttest.SendInput(b, "hi", nil))
	assert.Equal(t, []bool{true}, sink.TurnActives(), "the turn opens once the prompt is on stdin")

	// The provider answers. SendDetachedRequest correlates on the request id, so
	// feeding the reply drives the real completion path: the response is handled
	// -- which persists the turn end -- and only then is the flag cleared.
	b.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}`)))

	testutil.RequireEventually(t, func() bool { return len(sink.TurnActives()) == 2 })
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
}

func TestACPTurnActive_AFailedSendOpensNoTurn(t *testing.T) {
	t.Parallel()

	// A prompt that never reached the provider started nothing, and no response
	// is coming to end it. Leaving the flag set would latch the agent busy for
	// the life of the process.
	b, sink := newACPTurnBase(t, agenttest.FailingStdin{})

	require.Error(t, agenttest.SendInput(b, "hi", nil))

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

	// Normal end_turn persists the turn end and clears promptActive without resetting open spans.
	var out bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&out))

	require.NoError(t, agenttest.SendInput(b, "hi", nil))
	b.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}`)))

	testutil.RequireEventually(t, func() bool { return len(sink.TurnLifecycle()) == 3 })
	assert.Equal(t, []string{"turn_active:true", "turn_end", "turn_active:false"}, sink.TurnLifecycle())

	// An interrupted turn still resets spans between turn_end and turn_active:false.
	var out2 bytes.Buffer
	b2, sink2 := newACPTurnBase(t, agenttest.NopStdin(&out2))
	require.NoError(t, agenttest.SendInput(b2, "hi", nil))
	b2.noteACPInterruptRequested()
	b2.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"cancelled"}}`)))

	testutil.RequireEventually(t, func() bool { return len(sink2.TurnLifecycle()) == 4 })
	assert.Equal(t, []string{"turn_active:true", "turn_end", "reset_spans", "turn_active:false"}, sink2.TurnLifecycle())
}

func TestPromptResponseSeparatesLaterSessionText(t *testing.T) {
	t.Parallel()

	b, sink := newACPTurnBase(t, &agenttest.Stdin{})
	waitForCompletion, resumeCompletion := pausePromptCompletion(t, b)
	require.NoError(t, agenttest.SendInput(b, "hi", nil))
	sessionID := b.CurrentSessionID()
	b.HandleOutput(acptest.Chunk(sessionID, "agent_message_chunk", "prompt reply"))
	require.True(t, b.HandleJSONRPCResponseForTest(promptResponse(1)))
	waitForCompletion()
	b.HandleOutput(acptest.Chunk(sessionID, "agent_message_chunk", "later session text"))
	resumeCompletion()

	testutil.RequireEventually(t, func() bool { return len(sink.TurnActives()) == 2 })
	const scope = "acp:agent_message_chunk"
	assert.Equal(t, []agent.ProgressUpdate{
		agent.ModelTextProgress(scope, "prompt reply"),
		agent.CompleteModelProgress(scope),
		agent.ModelTextProgress(scope, "later session text"),
	}, sink.ProgressUpdates())
	messages := sink.Messages()
	require.NotEmpty(t, messages)
	assert.JSONEq(t, `{"type":"assembled_message","kind":"text","text":"prompt reply","completion":"complete"}`, string(messages[0].Content))
	b.FlushBufferedOutputOffReader(agent.MessageCompletionComplete)
	messages = sink.Messages()
	require.Len(t, messages, 3)
	assert.JSONEq(t, `{"type":"assembled_message","kind":"text","text":"later session text","completion":"complete"}`, string(messages[2].Content))
}

func TestPromptResponseKeepsItsTextBeforeQueuedAgentWork(t *testing.T) {
	t.Parallel()

	b, sink := newACPTurnBase(t, &agenttest.Stdin{})
	b.sink = agent.NewModelProgressResetSink(b.sink)
	waitForCompletion, resumeCompletion := pausePromptCompletion(t, b)
	require.NoError(t, agenttest.SendInput(b, "hi", nil))
	b.HandleOutput(acptest.Chunk(b.CurrentSessionID(), "agent_message_chunk", "prompt reply"))
	require.True(t, b.HandleJSONRPCResponseForTest(promptResponse(1)))
	waitForCompletion()

	require.True(t, b.BeginAgentTurn())
	b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call","toolCallId":"work-tool","title":"Read","kind":"read","status":"completed"}`))
	b.EndAgentTurn([]byte(agentTurnEndFrame))
	resumeCompletion()

	testutil.RequireEventually(t, func() bool { return len(sink.TurnActives()) == 2 })
	messages := sink.Messages()
	require.Len(t, messages, 4)
	assert.JSONEq(t, `{"type":"assembled_message","kind":"text","text":"prompt reply","completion":"complete"}`, string(messages[0].Content))
	assert.Equal(t, "work-tool", messages[1].SpanID)
	assert.Len(t, turnEndContents(sink), 2)
}

func pausePromptCompletion(t *testing.T, b *Base) (wait, resume func()) {
	t.Helper()
	ctx := testutil.DeadlineContext(t)
	entered := make(chan struct{})
	allow := make(chan struct{})
	b.hooks.PromptEnded = func(error, bool) PromptEnding {
		close(entered)
		select {
		case <-allow:
		case <-ctx.Done():
		}
		return PromptEnding{}
	}
	return func() {
		t.Helper()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("the correlated response did not reach the completion callback")
		}
	}, func() { close(allow) }
}

func TestPromptResponseKeepsCapturedTextWhenTheSessionIsReplaced(t *testing.T) {
	t.Parallel()

	b, sink := newACPTurnBase(t, &agenttest.Stdin{})
	require.NoError(t, agenttest.SendInput(b, "hi", nil))
	sessionID := b.CurrentSessionID()

	// ClearContext holds this lock while the reader delivers outgoing frames.
	b.sessionMu.Lock()
	b.beginSessionUpdates()
	b.HandleOutput(acptest.Chunk(sessionID, "agent_message_chunk", "old prompt reply"))
	matched := b.HandleJSONRPCResponseForTest(promptResponse(1))
	b.HandleOutput(acptest.Chunk(sessionID, "agent_message_chunk", "idle text from the old session"))
	b.sessionUpdates.mu.Lock()
	b.flushPreviousSessionUpdates()
	outgoing := b.replaceRootSession(func() {
		b.Mu.Lock()
		b.sessionID = "session-2"
		b.resetTurnStateLocked()
		b.Mu.Unlock()
	})
	b.sessionUpdates.mu.Unlock()
	b.sessionMu.Unlock()
	require.True(t, matched)
	b.finishACPTurnFor(outgoing, agent.MessageCompletionInterrupted, RootTranscriptSessionReplaced)
	b.rootCallbacks.wait()

	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.JSONEq(t, `{"type":"assembled_message","kind":"text","text":"old prompt reply","completion":"interrupted"}`, string(messages[0].Content))
	assert.JSONEq(t, `{"type":"assembled_message","kind":"text","text":"idle text from the old session","completion":"interrupted"}`, string(messages[1].Content))
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

// swallowingSink represents a decorator that does not forward the turn state.
// NewModelProgressResetSink forwards TurnServices directly to its inner services.
// This test decorator distinguishes a hook that reads b.sink again from one that captured the original sink.
type swallowingSink struct {
	agent.ProviderServices
}

func (s *swallowingSink) SetTurnState(agent.TurnState, uint64) {}

func TestACPTurnActive_ProviderErrorPersistsBufferedText(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&out))
	require.NoError(t, agenttest.SendInput(b, "hi", nil))
	b.HandleOutput(acptest.Chunk(b.CurrentSessionID(), "agent_message_chunk", "partial answer"))

	b.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"provider failed"}}`)))

	testutil.RequireEventually(t, func() bool { return sink.MessageCount() == 1 })
	assert.JSONEq(t, `{
		"type":"assembled_message",
		"kind":"text",
		"text":"partial answer",
		"completion":"error"
	}`, string(sink.Messages()[0].Content))
}
