package agent

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/envutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every provider publishes "a turn is in flight" through OutputSink.SetTurnActive,
// and the Worker derives the agent's busy state from it. That state drives the
// thinking indicator, the Interrupt button and both close guards, and it has NO
// fallback: the browser's transcript-scanning heuristic was deleted when this
// became authoritative.
//
// So a provider that never clears its flag leaves an agent busy forever -- a
// spinner nothing stops, and a close guard refusing a tab the user can no longer
// close by any route. These tests pin the clear, per provider, on every path
// that ends a turn.

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// --- Claude Code -------------------------------------------------------------

func newClaudeAgentWithStdin(sink OutputSink) (*ClaudeCodeAgent, *bytes.Buffer) {
	var buf bytes.Buffer
	a := &ClaudeCodeAgent{
		processBase: processBase{
			agentID: "test-agent",
			stdin:   nopWriteCloser{&buf},
		},
		sink: sink,
	}
	return a, &buf
}

func TestClaudeTurnActive_SendInputOpensAndResultCloses(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a, _ := newClaudeAgentWithStdin(sink)

	require.NoError(t, a.SendInput("hello", nil))
	assert.Equal(t, []bool{true}, sink.TurnActives(), "the turn opens once the message is on stdin")

	a.HandleOutput([]byte(`{"type":"result","subtype":"success"}`))

	assert.Equal(t, []bool{true, false}, sink.TurnActives())
}

func TestClaudeTurnActive_ErroredAndInterruptedTurnsAlsoClose(t *testing.T) {
	t.Parallel()

	// Claude ends an interrupted or failed turn with a `result` envelope too, so
	// the one clear site covers those paths. Were that not true, cancelling a
	// runaway agent would leave it looking busy forever -- with the Interrupt
	// button still showing and nothing left to press it for.
	for _, envelope := range []string{
		`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom"}`,
		`{"type":"result","subtype":"success","result":"Interrupted by user"}`,
	} {
		sink := &testSink{}
		a, _ := newClaudeAgentWithStdin(sink)
		require.NoError(t, a.SendInput("hello", nil))

		a.HandleOutput([]byte(envelope))

		last, published := sink.LastTurnActive()
		require.True(t, published)
		assert.False(t, last, "envelope %s must end the turn", envelope)
	}
}

func TestClaudeTurnActive_FailedStdinWriteStartsNoTurn(t *testing.T) {
	t.Parallel()

	// A write that failed delivered nothing, so no turn began and no envelope is
	// coming to end one. Marking it busy here would latch the agent forever.
	sink := &testSink{}
	a := &ClaudeCodeAgent{
		processBase: processBase{agentID: "test-agent", stdin: failingWriteCloser{}},
		sink:        sink,
	}

	require.Error(t, a.SendInput("hello", nil))

	assert.Equal(t, []bool{false}, sink.TurnActives(),
		"the publish re-reads the flag, so a failed send republishes idle rather than opening a turn")
}

func TestClaudeTurnActive_SubagentResultLeavesTheRootTurnOpen(t *testing.T) {
	t.Parallel()

	// A forwarded subagent envelope carries parent_tool_use_id and routes into
	// the child's transcript. The root is still working, and a child finishing
	// must not clear the root's turn.
	sink := &testSink{}
	a, _ := newClaudeAgentWithStdin(sink)
	require.NoError(t, a.SendInput("hello", nil))

	a.HandleOutput([]byte(`{"type":"result","parent_tool_use_id":"parent-1","subtype":"success"}`))

	assert.Equal(t, []bool{true}, sink.TurnActives(), "only the ROOT's own result ends the root's turn")
}

type failingWriteCloser struct{}

func (failingWriteCloser) Write([]byte) (int, error) { return 0, assert.AnError }
func (failingWriteCloser) Close() error              { return nil }

// --- Codex -------------------------------------------------------------------

func TestCodexTurnActive_StartedOpensAndCompletedCloses(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newCodexAgentWithSink(sink)

	handleCodexOutput(a, parseLine([]byte(`{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-42"}}}`)))
	assert.Equal(t, []bool{true}, sink.TurnActives())
	assert.Equal(t, []leapmuxv1.AgentInputKind{leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE}, sink.TurnKinds(),
		"Codex accepts turn/steer for a provider-started turn, so the queue must classify it as steerable")

	handleCodexOutput(a, parseLine([]byte(`{"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-42","status":"completed"}}}`)))

	assert.Equal(t, []bool{true, false}, sink.TurnActives())
	assert.Equal(t, []leapmuxv1.AgentInputKind{
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_UNSPECIFIED,
	}, sink.TurnKinds(), "the turn end must clear the steering classification")
}

func TestCodexTurnActive_ReadsTurnIDNotTheInheritedPromptActive(t *testing.T) {
	t.Parallel()

	// CodexAgent embeds jsonrpcBase for its JSON-RPC plumbing and inherits
	// promptActive with it, but only acpBase.SendInput ever writes that field --
	// so for Codex it is permanently false. Reading it here would report every
	// Codex turn idle.
	sink := &recordingControlSink{}
	a := newCodexAgentWithSink(sink)

	handleCodexOutput(a, parseLine([]byte(`{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-7"}}}`)))

	a.mu.Lock()
	promptActive := a.promptActive
	a.mu.Unlock()
	require.False(t, promptActive, "the inherited flag stays false for Codex")

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
	sink := &recordingControlSink{}
	a := newCodexAgentWithSink(sink)

	handleCodexOutput(a, parseLine([]byte(`{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-42"}}}`)))
	require.Equal(t, []bool{true}, sink.TurnActives())

	// A REGISTERED child: the spawn puts child-1 in the child index, so its
	// completion routes into the child transcript.
	spawn := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-42","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	handleCodexOutput(a, parseLine([]byte(spawn)))
	handleCodexOutput(a, parseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"turn-c1"}}}`)))
	handleCodexOutput(a, parseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"turn-c1","status":"completed"}}}`)))
	assert.Equal(t, []bool{true}, sink.TurnActives(), "a registered child's turn end is not the root's")

	// An UNREGISTERED thread takes the other branch of the same test -- a late
	// receiver the spawn never named -- and must not clear the root either.
	handleCodexOutput(a, parseLine([]byte(`{"method":"turn/completed","params":{"threadId":"stranger","turn":{"id":"turn-x","status":"completed"}}}`)))
	assert.Equal(t, []bool{true}, sink.TurnActives(), "an unknown thread is still not the main thread")

	// The main thread's own completion is what ends the root's turn.
	handleCodexOutput(a, parseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-42","status":"completed"}}}`)))
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
}

// --- Pi ----------------------------------------------------------------------

func TestPiTurnActive_StaysOpenAcrossAnAutoRetry(t *testing.T) {
	t.Parallel()

	// Pi restarts a failed run itself. The turn stays open for the whole
	// backoff, where nothing streams and no envelope arrives -- and a client
	// that inferred idleness there would drop the spinner and hide the Interrupt
	// button on a run that is not finished.
	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	handlePiOutput(a, parseLine([]byte(`{"type":"agent_start"}`)))
	require.Equal(t, []bool{true}, sink.TurnActives())

	handlePiOutput(a, parseLine([]byte(`{"type":"agent_end","willRetry":true}`)))

	last, _ := sink.LastTurnActive()
	assert.True(t, last, "an agent_end that will retry does not end the turn")

	handlePiOutput(a, parseLine([]byte(`{"type":"agent_end","willRetry":false}`)))

	last, _ = sink.LastTurnActive()
	assert.False(t, last, "the retry budget is spent; now the turn ends")
}

// --- ZCode -------------------------------------------------------------------

func TestZCodeTurnActive_StartedOpensAndCompletedCloses(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a := newZCodeTestAgentWithStdin(t, sink, &zcodeRecordedStdin{})

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{"turnNumber":1,"input":"hi"}`))
	assert.Equal(t, []bool{true}, sink.TurnActives())

	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventTurnCompleted, `{"toolCallCount":1}`))

	assert.Equal(t, []bool{true, false}, sink.TurnActives())
}

func TestZCodeTurnActive_BackgroundTurnStillClears(t *testing.T) {
	t.Parallel()

	// A background turn (inputSource set) persists no divider and closes none of
	// the user's spans, so finishZCodeTurn returns early. The clear has to happen
	// BEFORE that early return, or a background turn latches the agent busy for
	// the life of the process.
	sink := &testSink{}
	a := newZCodeTestAgentWithStdin(t, sink, &zcodeRecordedStdin{})

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{"inputSource":"background"}`))
	require.Equal(t, []bool{true}, sink.TurnActives())

	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventTurnCompleted, `{}`))

	last, published := sink.LastTurnActive()
	require.True(t, published)
	assert.False(t, last, "a background turn must publish its clear despite the early return")
}

// --- ACP family (OpenCode, Cursor, Copilot, Kilo, Goose, Reasonix) ------------

// newACPTurnBase builds the base all six ACP providers embed, wired the way
// acpStart wires it. Going through wireTurnActive rather than a hand-built
// closure is deliberate: a test that built its own hook would pass even if the
// constructor stopped wiring one, which is the failure these exist to catch.
func newACPTurnBase(t *testing.T, stdin io.WriteCloser) (*acpBase, *testSink) {
	t.Helper()
	sink := &testSink{}
	b := &acpBase{}
	b.agentID = "test-agent"
	b.stdin = stdin
	b.sessionID = "session-1"
	b.sink = sink
	// awaitResponse selects on both, so a bare base panics on the nil ctx the
	// moment a detached request waits for its reply.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	b.ctx = ctx
	b.processDone = make(chan struct{})
	b.wireTurnActive()
	return b, sink
}

// swallowingSink stands in for a decorator that forgets to forward the turn
// flag. thinkingResetSink promotes SetTurnActive from the embedded interface
// today, so only a type like this one can tell a hook that re-reads b.sink from
// one that captured the sink it was wired with.
type swallowingSink struct {
	OutputSink
}

func (s *swallowingSink) SetTurnActive(bool, leapmuxv1.AgentInputKind, uint64) {}

func TestACPTurnActive_ThePublishFollowsALaterSinkWrap(t *testing.T) {
	t.Parallel()

	// acpStart wires the hook and startACPHandshake THEN replaces b.sink with
	// thinkingResetSink. A hook that captured the raw sink would publish past
	// every decorator for the life of the process -- and this flag is the input
	// queue's only dispatch guard, so a decorator that ever overrode
	// SetTurnActive would silently hold every later message of all six ACP
	// providers.
	var out bytes.Buffer
	b, sink := newACPTurnBase(t, nopWriteCloser{&out})
	b.sink = &swallowingSink{OutputSink: sink}

	b.publishTurnActive(true, 1)

	assert.Empty(t, sink.TurnActives(),
		"the hook re-reads b.sink, so the wrap installed after wireTurnActive is honored")
}

func TestACPTurnActive_PromptOpensTheTurnAndTheResponseCloses(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	b, sink := newACPTurnBase(t, nopWriteCloser{&out})

	require.NoError(t, b.SendInput("hi", nil))
	assert.Equal(t, []bool{true}, sink.TurnActives(), "the turn opens once the prompt is on stdin")

	// The provider answers. sendDetachedRequest correlates on the request id, so
	// feeding the reply drives the real completion path: the response is handled
	// -- which persists the turn end -- and only then is the flag cleared.
	b.handleJSONRPCResponse(parseLine([]byte(`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}`)))

	assert.Eventually(t, func() bool { return len(sink.TurnActives()) == 2 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
}

func TestACPTurnActive_ProviderErrorPersistsBufferedText(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	b, sink := newACPTurnBase(t, nopWriteCloser{&out})
	require.NoError(t, b.SendInput("hi", nil))
	b.HandleOutput(acpMessageChunk("partial answer"))

	b.handleJSONRPCResponse(parseLine([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"provider failed"}}`)))

	require.Eventually(t, func() bool { return sink.MessageCount() == 1 }, time.Second, 5*time.Millisecond)
	assert.JSONEq(t, `{
		"type":"assembled_message",
		"kind":"text",
		"text":"partial answer",
		"completion":"error"
	}`, string(sink.Messages()[0].Content))
}

func TestACPTurnActive_AFailedSendOpensNoTurn(t *testing.T) {
	t.Parallel()

	// A prompt that never reached the provider started nothing, and no response
	// is coming to end it. Leaving the flag set would latch the agent busy for
	// the life of the process.
	b, sink := newACPTurnBase(t, failingWriteCloser{})

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
	b, sink := newACPTurnBase(t, nopWriteCloser{&out})
	b.mu.Lock()
	b.promptActive = true
	b.mu.Unlock()

	b.clearActivePrompt()

	assert.Equal(t, []bool{false}, sink.TurnActives())
}

// --- The turn-end / turn-clear order, per provider ---------------------------

// A turn end and the clear it produces are ONE event split across two sink
// calls, and their order is a requirement rather than an implementation
// detail. PersistTurnEnd hands the finished turn's tool-call count to the
// Worker's activity latch; the clear that follows is the settle edge that
// spends it. Clear first and the agent settles with no count, so the client
// rings the completion sound for a turn that used no tool -- the exact case the
// "skip a zero-tool turn" rule exists to keep quiet.
//
// One test per provider, because each one funnels its turn end through a
// different handler and the order is easy to invert while every other assertion
// in the suite stays green.

// reset_spans is in the sequence for the same reason turn_end is: the clear
// releases the Worker's input queue, so the next message dispatches on it and
// must find the finished turn's spans already reset. A provider that publishes
// the clear before ResetSpans draws the dead turn's bars beside that message.
func TestTurnEndPrecedesTheClear_Claude(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a, _ := newClaudeAgentWithStdin(sink)
	require.NoError(t, a.SendInput("hello", nil))

	a.HandleOutput([]byte(`{"type":"result","subtype":"success","num_tool_uses":2}`))

	assert.Equal(t, []string{"turn_active:true", "turn_end", "reset_spans", "turn_active:false"}, sink.TurnLifecycle())
}

func TestTurnEndPrecedesTheClear_Codex(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newCodexAgentWithSink(sink)

	handleCodexOutput(a, parseLine([]byte(`{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-42"}}}`)))
	handleCodexOutput(a, parseLine([]byte(`{"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-42","status":"completed"}}}`)))

	assert.Equal(t, []string{"turn_active:true", "turn_end", "reset_spans", "turn_active:false"}, sink.TurnLifecycle())
}

func TestTurnEndPrecedesTheClear_ZCode(t *testing.T) {
	t.Parallel()

	// ZCode is the one that had it backwards: finishZCodeTurn published the
	// clear early so it would run before the two early returns below it. The
	// clear is deferred instead, which covers those returns AND lands after the
	// divider.
	sink := &testSink{}
	a := newZCodeTestAgentWithStdin(t, sink, &zcodeRecordedStdin{})

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{"turnNumber":1,"input":"hi"}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventTurnCompleted, `{"toolCallCount":1}`))

	assert.Equal(t, []string{"turn_active:true", "turn_end", "reset_spans", "turn_active:false"}, sink.TurnLifecycle())
}

func TestTurnEndPrecedesTheClear_Pi(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	handlePiOutput(a, parseLine([]byte(`{"type":"agent_start"}`)))
	handlePiOutput(a, parseLine([]byte(`{"type":"agent_end","willRetry":false}`)))

	assert.Equal(t, []string{"turn_active:true", "turn_end", "reset_spans", "turn_active:false"}, sink.TurnLifecycle())
}

func TestTurnEndPrecedesTheClear_ACP(t *testing.T) {
	t.Parallel()

	// The ACP base handles the prompt response -- which persists the turn end --
	// and only then clears promptActive, both inside one callback. This pins that
	// order, because a callback that cleared first would invert it.
	var out bytes.Buffer
	b, sink := newACPTurnBase(t, nopWriteCloser{&out})

	require.NoError(t, b.SendInput("hi", nil))
	b.handleJSONRPCResponse(parseLine([]byte(`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}`)))

	assert.Eventually(t, func() bool { return len(sink.TurnLifecycle()) == 4 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"turn_active:true", "turn_end", "reset_spans", "turn_active:false"}, sink.TurnLifecycle())
}

// Pi keeps the turn open across a retry it drives itself, so the retried
// attempt persists a plain message rather than a turn end -- and publishes no
// clear for the settle to spend a count on.
func TestTurnEndPrecedesTheClear_PiRetryEndsNoTurn(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	handlePiOutput(a, parseLine([]byte(`{"type":"agent_start"}`)))
	handlePiOutput(a, parseLine([]byte(`{"type":"agent_end","willRetry":true}`)))

	// The republished `true` is a re-read of an unchanged flag, not a second
	// turn. It is harmless because the Worker's latch is edge-triggered, and it
	// is the price of publishing from the field rather than from an argument --
	// which is what makes a MISSING publish the only way the two can drift.
	assert.Equal(t, []string{"turn_active:true", "reset_spans", "turn_active:true"}, sink.TurnLifecycle(),
		"a retried attempt neither ends the turn nor clears the flag")

	handlePiOutput(a, parseLine([]byte(`{"type":"agent_end","willRetry":false}`)))

	assert.Equal(t, []string{
		"turn_active:true", "reset_spans", "turn_active:true", "turn_end", "reset_spans", "turn_active:false",
	}, sink.TurnLifecycle())
}

// --- a turn the Worker did not start -----------------------------------------

func TestClaudeTurnActive_RootAssistantOutputArmsATurnTheWorkerDidNotStart(t *testing.T) {
	t.Parallel()

	// Claude Code emits no turn-start frame, so the Worker used to learn of a
	// turn only from its own SendInput. A turn the CLI runs by itself -- one it
	// continues after the `result` the Worker already consumed -- then left the
	// flag clear, and the next queued message went straight into that running
	// turn.
	sink := &testSink{}
	a, _ := newClaudeAgentWithStdin(sink)

	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"still working"}]}}`))

	assert.Equal(t, []bool{true}, sink.TurnActives(), "root assistant output means a turn is in flight")
	assert.ErrorIs(t, a.SendInput("second", nil), ErrAgentBusy)

	a.HandleOutput([]byte(`{"type":"result","subtype":"success"}`))
	// The refused send republishes the unchanged flag, which is what reconciles
	// a queue that dispatched into it. The clear that follows is the result's.
	assert.Equal(t, []bool{true, true, false}, sink.TurnActives(),
		"the CLI's own result still ends the turn")
}

func TestClaudeTurnActive_RootAssistantOutputPublishesOncePerTurn(t *testing.T) {
	t.Parallel()

	// Every assistant message of one turn reaches this path. Only the first
	// arms anything, so a streaming turn does not republish -- and does not
	// reconcile the Worker's input queue -- once per message block.
	sink := &testSink{}
	a, _ := newClaudeAgentWithStdin(sink)

	for range 3 {
		a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"chunk"}]}}`))
	}

	assert.Equal(t, []bool{true}, sink.TurnActives())
}

func TestClaudeTurnActive_EveryRootFrameOfALiveTurnArmsIt(t *testing.T) {
	t.Parallel()

	// The frames that prove a live turn EARLIEST are the ones that return before
	// the persist path: the thinking-token telemetry, and a root user
	// tool_result. Arming from the assistant message alone left the whole
	// extended-thinking window of a CLI-run turn invisible, and a message the
	// user sent inside it went straight into that turn.
	for _, tc := range []struct {
		name string
		line string
	}{
		{
			name: "thinking tokens",
			line: `{"type":"system","subtype":"thinking_tokens","thinking_tokens":120}`,
		},
		{
			name: "root user tool_result",
			line: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
		},
		{
			name: "root user text echo",
			line: `{"type":"user","message":{"role":"user","content":"hello"}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &testSink{}
			a, _ := newClaudeAgentWithStdin(sink)

			a.HandleOutput([]byte(tc.line))

			assert.Equal(t, []bool{true}, sink.TurnActives(),
				"a root frame only a live turn produces arms the turn")
			assert.ErrorIs(t, a.SendInput("second", nil), ErrAgentBusy)
		})
	}
}

func TestClaudeTurnActive_TheWorkersOwnTrafficArmsNothing(t *testing.T) {
	t.Parallel()

	// A result ENDS a turn, and the control frames are the Worker talking to
	// itself. Neither is evidence that the CLI is working.
	for _, tc := range []struct {
		name string
		line string
	}{
		{name: "result", line: `{"type":"result","subtype":"success"}`},
		{name: "control_response", line: `{"type":"control_response","response":{"request_id":"r1","subtype":"success"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &testSink{}
			a, _ := newClaudeAgentWithStdin(sink)

			a.HandleOutput([]byte(tc.line))

			assert.NotContains(t, sink.TurnActives(), true,
				"no turn is in flight because of this frame")
		})
	}
}

func TestClaudeTurnActive_SubagentAssistantOutputArmsNoRootTurn(t *testing.T) {
	t.Parallel()

	// A forwarded subagent envelope carries parent_tool_use_id and belongs to
	// the child's transcript. It says nothing about the root, which may well be
	// idle while a restarted subagent runs on.
	sink := &testSink{}
	a, _ := newClaudeAgentWithStdin(sink)

	a.HandleOutput([]byte(`{"type":"assistant","parent_tool_use_id":"parent-1","message":{"role":"assistant","content":[{"type":"text","text":"child"}]}}`))

	assert.Empty(t, sink.TurnActives(), "only ROOT output arms the root's turn")
}

func TestTurnActive_EveryProviderIssuesRisingOrderingTokens(t *testing.T) {
	t.Parallel()

	// The token is what lets the Worker tell a publish it overtook from a
	// current one, and it only does that if it RISES with each publish and comes
	// from the same critical section that reads the flag. A provider that reused
	// a token, or took one outside that section, would order nothing -- and a
	// stale value would latch a turn that is over.
	for _, tc := range []struct {
		name    string
		publish func(t *testing.T, sink OutputSink)
	}{
		{
			name: "claude",
			publish: func(t *testing.T, sink OutputSink) {
				a, _ := newClaudeAgentWithStdin(sink)
				a.PublishTurnActive()
				a.PublishTurnActive()
				a.PublishTurnActive()
			},
		},
		{
			name: "codex",
			publish: func(t *testing.T, sink OutputSink) {
				a := newCodexAgentWithSink(sink)
				a.sink = sink
				a.PublishTurnActive()
				a.PublishTurnActive()
				a.PublishTurnActive()
			},
		},
		{
			name: "pi",
			publish: func(t *testing.T, sink OutputSink) {
				a := newPiAgentWithSink(sink)
				a.PublishTurnActive()
				a.PublishTurnActive()
				a.PublishTurnActive()
			},
		},
		{
			name: "zcode",
			publish: func(t *testing.T, sink OutputSink) {
				a := newZCodeTestAgentWithStdin(t, sink, &zcodeRecordedStdin{})
				a.PublishTurnActive()
				a.PublishTurnActive()
				a.PublishTurnActive()
			},
		},
		{
			name: "acpBase",
			publish: func(t *testing.T, sink OutputSink) {
				var out bytes.Buffer
				b, _ := newACPTurnBase(t, nopWriteCloser{&out})
				b.sink = sink
				b.wireTurnActive()
				b.PublishTurnActive()
				b.PublishTurnActive()
				b.PublishTurnActive()
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &testSink{}
			tc.publish(t, sink)

			seqs := sink.TurnSeqs()
			require.Len(t, seqs, 3, "each publish carries a token")
			assert.Greater(t, seqs[1], seqs[0], "the token rises with each publish")
			assert.Greater(t, seqs[2], seqs[1])
		})
	}
}

func TestClaudeTurnActive_ASystemFrameOutsideATurnArmsNothing(t *testing.T) {
	t.Parallel()

	// `system` carries four families and only the thinking-token telemetry
	// reports the root's own work. Arming from the whole type latched a turn
	// that no `result` ever ends, and BOTH consumers of the flag then broke for
	// the rest of the process: the input queue held every message the user sent
	// -- unpaused, with nothing to drain it -- and the client ran a thinking
	// indicator for an agent that does nothing.
	//
	// The startup init frame is the one every session emits, so a new agent tab
	// reached that state before the user typed anything.
	for _, tc := range []struct {
		name string
		line string
	}{
		{
			name: "startup init",
			line: `{"type":"system","subtype":"init","session_id":"s-1","slash_commands":["clear","compact"]}`,
		},
		{
			name: "background task progress",
			line: `{"type":"system","subtype":"task_progress","task_id":"t-1","description":"npm test"}`,
		},
		{
			name: "background task notification",
			line: `{"type":"system","subtype":"task_notification","task_id":"t-1","status":"completed"}`,
		},
		{
			name: "background tasks changed",
			line: `{"type":"system","subtype":"background_tasks_changed","tasks":[]}`,
		},
		{
			name: "notification-threaded status",
			line: `{"type":"system","subtype":"status","status":"compacting"}`,
		},
		{
			name: "status clear",
			line: `{"type":"system","subtype":"status","status":""}`,
		},
		{
			name: "api retry",
			line: `{"type":"system","subtype":"api_retry","attempt":2}`,
		},
		{
			name: "compaction boundary",
			line: `{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"auto"}}`,
		},
		{
			name: "unknown subtype",
			line: `{"type":"system","subtype":"some_future_event"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &testSink{}
			a, _ := newClaudeAgentWithStdin(sink)

			a.HandleOutput([]byte(tc.line))

			assert.NotContains(t, sink.TurnActives(), true, "this frame runs no turn")
			assert.NoError(t, a.SendInput("first", nil), "the next message must reach the CLI")
		})
	}
}

func TestClaudeTurnActive_TheCLIsOwnSessionStateDecides(t *testing.T) {
	t.Parallel()

	// With claudeSessionStateEnv set, Claude Code publishes its own turn state.
	// That frame is authoritative and the output heuristic never sees it:
	// `running` opens the turn before any assistant message, `requires_action`
	// reports a turn that a permission prompt blocks, and `idle` ends it after
	// the last result flushes.
	for _, tc := range []struct {
		name string
		line string
		want []bool
	}{
		{
			name: "running opens the turn",
			line: `{"type":"system","subtype":"session_state_changed","state":"running"}`,
			want: []bool{true},
		},
		{
			name: "requires_action is a turn a prompt blocks",
			line: `{"type":"system","subtype":"session_state_changed","state":"requires_action"}`,
			want: []bool{true},
		},
		{
			name: "an unknown state decides nothing",
			line: `{"type":"system","subtype":"session_state_changed","state":"quiescing"}`,
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &testSink{}
			a, _ := newClaudeAgentWithStdin(sink)

			a.HandleOutput([]byte(tc.line))

			assert.Equal(t, tc.want, sink.TurnActives())
		})
	}
}

func TestClaudeTurnActive_SessionStateIdleEndsATurnWithNoResult(t *testing.T) {
	t.Parallel()

	// The idle frame is the recovery this provider otherwise has none of. A turn
	// the CLI runs is normally ended by its `result`, and a turn armed with no
	// result behind it would hold the input queue until the process exits. The
	// CLI's own idle answers that case.
	sink := &testSink{}
	a, _ := newClaudeAgentWithStdin(sink)

	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"running"}`))
	require.ErrorIs(t, a.SendInput("second", nil), ErrAgentBusy)

	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"idle"}`))

	assert.Equal(t, []bool{true, true, false}, sink.TurnActives(),
		"the refused send republishes the flag; idle then clears it")
	assert.NoError(t, a.SendInput("second", nil), "the queue dispatches again")
}

func TestClaudeLaunchEnv_CarriesTheTurnSignalOptIn(t *testing.T) {
	t.Parallel()

	// Claude Code publishes session_state_changed for this variable alone. A
	// launch that drops it silently falls back to the output heuristic, and
	// nothing else reports the loss.
	t.Run("a plain launch", func(t *testing.T) {
		t.Parallel()

		env := claudeAgentEnv([]string{"PATH=/usr/bin"}, false)

		assert.Contains(t, env, "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1")
		assert.Contains(t, env, "CLAUDE_CODE_ENTRYPOINT=cli")
		assert.Contains(t, env, "PATH=/usr/bin", "the inherited environment survives")
		assert.False(t, envutil.HasKey(env, "CLAUDECODE"),
			"only a login shell needs the rc-file marker")
	})

	t.Run("a login shell", func(t *testing.T) {
		t.Parallel()

		env := claudeAgentEnv([]string{"PATH=/usr/bin"}, true)

		assert.Contains(t, env, "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1")
		assert.Equal(t, []string{"1"}, envutil.ValuesFor(env, "CLAUDECODE"))
	})

	// A worker that a Claude Code session itself launched inherits all three
	// markers, and each inherited value is the PARENT's. A pin that layers
	// instead of replacing still reaches the CLI with the right value, because
	// exec resolves a duplicate last-wins -- and leaves an environment that says
	// two things, which is what makes the layering invisible until something
	// else reads it.
	t.Run("an inherited value is replaced, not layered", func(t *testing.T) {
		t.Parallel()

		env := claudeAgentEnv([]string{
			"PATH=/usr/bin",
			"CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=0",
			"CLAUDE_CODE_ENTRYPOINT=sdk-ts",
			"CLAUDECODE=1",
		}, false)

		assert.Equal(t, []string{"1"}, envutil.ValuesFor(env, "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS"))
		assert.Equal(t, []string{"cli"}, envutil.ValuesFor(env, "CLAUDE_CODE_ENTRYPOINT"))
		assert.Empty(t, envutil.ValuesFor(env, "CLAUDECODE"),
			"the inherited marker is stripped, and only a login shell re-adds it")
	})

	// The inherited environment is the caller's slice. Building the launch env
	// by appending to it writes into the spare capacity that slice owns, so a
	// second launch overwrites what the first one holds.
	t.Run("two launches do not share a backing array", func(t *testing.T) {
		t.Parallel()

		inherited := make([]string, 0, 8)
		inherited = append(inherited, "PATH=/usr/bin")

		plain := claudeAgentEnv(inherited, false)
		login := claudeAgentEnv(inherited, true)

		assert.Contains(t, plain, "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1")
		assert.False(t, envutil.HasKey(plain, "CLAUDECODE"), "the second launch wrote over the first")
		assert.Equal(t, []string{"1"}, envutil.ValuesFor(login, "CLAUDECODE"))
		assert.Equal(t, []string{"PATH=/usr/bin"}, inherited, "the caller's environment is unchanged")
	})
}

func TestClaudeTurnActive_ResultStillEndsATurnTheCLIOpened(t *testing.T) {
	t.Parallel()

	// Retiring the output heuristic must not retire the falling edge. `result`
	// ends every Claude turn and arrives BEFORE the idle that reports it, so the
	// Worker's input queue opens on the result -- one frame earlier than the
	// CLI's own report, and on the frame every build sends.
	sink := &testSink{}
	a, _ := newClaudeAgentWithStdin(sink)

	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"running"}`))
	a.HandleOutput([]byte(`{"type":"result","subtype":"success"}`))

	assert.Equal(t, []bool{true, false}, sink.TurnActives())
	assert.NoError(t, a.SendInput("next", nil), "the queue dispatches on the result")
}

func TestClaudeTurnActive_AForwardedChildStateDecidesNothing(t *testing.T) {
	t.Parallel()

	// A forwarded subagent envelope carries parent_tool_use_id, and the child's
	// turn is not the root's: a restarted subagent runs on while the root sits
	// idle. So a child's copy of the frame decides nothing -- for the idle, the
	// clear it would publish is exactly the failure the turn flag exists to
	// prevent, because the input queue would then dispatch into the root's
	// running turn.
	t.Run("a child idle leaves the root's turn running", func(t *testing.T) {
		t.Parallel()

		sink := &testSink{}
		a, _ := newClaudeAgentWithStdin(sink)

		a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"running"}`))
		a.HandleOutput([]byte(`{"type":"system","parent_tool_use_id":"p1","subtype":"session_state_changed","state":"idle"}`))

		assert.Equal(t, []bool{true}, sink.TurnActives(), "only the root's own state moves the flag")
		assert.ErrorIs(t, a.SendInput("next", nil), ErrAgentBusy)
	})

	t.Run("a child state does not retire the heuristic", func(t *testing.T) {
		t.Parallel()

		sink := &testSink{}
		a, _ := newClaudeAgentWithStdin(sink)

		a.HandleOutput([]byte(`{"type":"system","parent_tool_use_id":"p1","subtype":"session_state_changed","state":"running"}`))
		require.Empty(t, sink.TurnActives(), "a child's frame arms nothing")

		// The ROOT has still stated nothing, so its assistant message is still
		// the evidence that a turn runs. A child frame that retired the
		// heuristic would leave this build with no rising edge at all.
		a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`))

		assert.Equal(t, []bool{true}, sink.TurnActives())
	})
}

func TestClaudeTurnActive_AStaleSessionIdleDoesNotOpenTheNextTurn(t *testing.T) {
	t.Parallel()

	// The CLI emits idle when its run loop stops, BEFORE it reads the next
	// message. The Worker dispatches that message off the turn end it already
	// saw, so its send can land between the two frames -- and an idle applied
	// after it would clear the turn that message just opened. The input queue
	// follows this flag, so the next message would then dispatch INTO the
	// running turn.
	sink := &testSink{}
	a, _ := newClaudeAgentWithStdin(sink)

	require.NoError(t, a.SendInput("first", nil))
	a.HandleOutput([]byte(`{"type":"result","subtype":"success"}`))
	// The queue drains on that turn end and hands the CLI the next message.
	require.NoError(t, a.SendInput("second", nil))

	// The previous turn's idle arrives now.
	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"idle"}`))

	assert.Equal(t, []bool{true, false, true}, sink.TurnActives(),
		"the stale idle publishes nothing")
	assert.ErrorIs(t, a.SendInput("third", nil), ErrAgentBusy,
		"the second turn is still in flight")

	// The result for the second message ends it, and a later idle is current
	// again. The refused send above republished the unchanged flag, which is
	// what reconciles a queue that dispatched into the turn.
	a.HandleOutput([]byte(`{"type":"result","subtype":"success"}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"idle"}`))
	assert.Equal(t, []bool{true, false, true, true, false, false}, sink.TurnActives())
}

func TestClaudeTurnActive_TheHeuristicRetiresOnceTheCLIStatesItsOwnTurn(t *testing.T) {
	t.Parallel()

	// The output heuristic exists for a CLI that publishes no
	// session_state_changed frame. A CLI that publishes one has stated the
	// answer, so nothing else is read as evidence for the life of the process --
	// and the arming surface shrinks to the named signals, which is what keeps a
	// frame the vendor adds later inert.
	sink := &testSink{}
	a, _ := newClaudeAgentWithStdin(sink)

	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"idle"}`))
	require.Equal(t, []bool{false}, sink.TurnActives())

	// A root assistant frame is the heuristic's strongest evidence. It arms
	// nothing here: this CLI reports `running` when a turn starts.
	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`))

	assert.Equal(t, []bool{false}, sink.TurnActives(), "the CLI's own state is the only source now")
	assert.NoError(t, a.SendInput("next", nil), "the queue still dispatches")
}
