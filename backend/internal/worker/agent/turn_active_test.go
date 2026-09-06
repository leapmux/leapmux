package agent

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"

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

	handleCodexOutput(a, parseLine([]byte(`{"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-42","status":"completed"}}}`)))

	assert.Equal(t, []bool{true, false}, sink.TurnActives())
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

// --- Pi ----------------------------------------------------------------------

func TestPiTurnActive_StaysOpenAcrossAnAutoRetry(t *testing.T) {
	t.Parallel()

	// Pi restarts a failed run itself. The turn stays open for the whole
	// backoff, where nothing streams and no envelope arrives -- and a client
	// that inferred idleness there would drop the spinner and hide the Interrupt
	// button on a run that is still going.
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
	b.wireTurnActive(sink)
	return b, sink
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

func TestTurnEndPrecedesTheClear_Claude(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	a, _ := newClaudeAgentWithStdin(sink)
	require.NoError(t, a.SendInput("hello", nil))

	a.HandleOutput([]byte(`{"type":"result","subtype":"success","num_tool_uses":2}`))

	assert.Equal(t, []string{"turn_active:true", "turn_end", "turn_active:false"}, sink.TurnLifecycle())
}

func TestTurnEndPrecedesTheClear_Codex(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newCodexAgentWithSink(sink)

	handleCodexOutput(a, parseLine([]byte(`{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-42"}}}`)))
	handleCodexOutput(a, parseLine([]byte(`{"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-42","status":"completed"}}}`)))

	assert.Equal(t, []string{"turn_active:true", "turn_end", "turn_active:false"}, sink.TurnLifecycle())
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

	assert.Equal(t, []string{"turn_active:true", "turn_end", "turn_active:false"}, sink.TurnLifecycle())
}

func TestTurnEndPrecedesTheClear_Pi(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := newPiAgentWithSink(sink)

	handlePiOutput(a, parseLine([]byte(`{"type":"agent_start"}`)))
	handlePiOutput(a, parseLine([]byte(`{"type":"agent_end","willRetry":false}`)))

	assert.Equal(t, []string{"turn_active:true", "turn_end", "turn_active:false"}, sink.TurnLifecycle())
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

	assert.Eventually(t, func() bool { return len(sink.TurnLifecycle()) == 3 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"turn_active:true", "turn_end", "turn_active:false"}, sink.TurnLifecycle())
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
	assert.Equal(t, []string{"turn_active:true", "turn_active:true"}, sink.TurnLifecycle(),
		"a retried attempt neither ends the turn nor clears the flag")

	handlePiOutput(a, parseLine([]byte(`{"type":"agent_end","willRetry":false}`)))

	assert.Equal(t, []string{"turn_active:true", "turn_active:true", "turn_end", "turn_active:false"}, sink.TurnLifecycle())
}
