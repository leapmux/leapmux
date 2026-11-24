package acp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A stop cuts the command, and the row has to say which happened.
//
// A live round stopped `sleep 45` on ten providers. Every turn divider read
// `Turn interrupted`, and the TOOL row read five different ways: `Interrupted` from
// Codex and Pi, `Error` from Cursor and Reasonix, `(no output)` from OpenCode and
// Kilo, and nothing at all from ZCode.
//
// The split is what each provider sends when its command is cancelled. Codex and Pi
// leave the tool incomplete, so `finishACPTurn` persists it with the interrupted
// completion. The others send a FINAL result -- `failed` or an empty `completed` --
// which arrives as an ordinary result and carries no completion, so the row reports
// a failure the user caused by stopping, or reports nothing.
//
// The reader pressed Stop. `Error` names the wrong cause, and Pi already carries a
// flag for exactly this (see noteInterruptRequested); Base.Interrupt is one
// implementation covering Cursor, Kilo, OpenCode, Goose and Reasonix.
func TestACPToolResultAfterAStopReportsTheStop(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"failed", "completed"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()

			sink := &agenttest.Sink{}
			b := &Base{sink: agent.NewProviderServices(sink)}
			b.Mu.Lock()
			b.promptActive = true
			b.Mu.Unlock()
			b.noteACPInterruptRequested()

			b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-1","kind":"execute","title":"Sleep"}`))
			b.main().handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-1","status":"` + status + `"}`))

			msgs := sink.Messages()
			require.NotEmpty(t, msgs)
			last := msgs[len(msgs)-1]
			assert.Equal(t, agent.MessageCompletionInterrupted, last.Completion,
				"the user stopped this command, so the row must not report a failure or a plain result")
		})
	}
}

// Without a stop the row keeps whatever the provider reported, so an ordinary
// failure is still a failure.
func TestACPToolResultWithoutAStopKeepsItsOwnOutcome(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink)}
	b.Mu.Lock()
	b.promptActive = true
	b.Mu.Unlock()

	b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-1","kind":"execute","title":"Sleep"}`))
	b.main().handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-1","status":"failed"}`))

	msgs := sink.Messages()
	require.NotEmpty(t, msgs)
	assert.NotEqual(t, agent.MessageCompletionInterrupted, msgs[len(msgs)-1].Completion,
		"nobody stopped this, so the row states the provider's own outcome")
}

// The note belongs to ONE turn. A result that arrives in a later turn is that
// turn's, and a stale flag would relabel work nobody stopped.
func TestACPInterruptNoteDoesNotOutliveItsTurn(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink)}
	b.Mu.Lock()
	b.promptActive = true
	b.Mu.Unlock()
	b.noteACPInterruptRequested()

	// The turn ends, which is where the note is dropped.
	b.clearActivePrompt()

	b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-2","kind":"execute","title":"Later"}`))
	b.main().handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-2","status":"failed"}`))

	msgs := sink.Messages()
	require.NotEmpty(t, msgs)
	assert.NotEqual(t, agent.MessageCompletionInterrupted, msgs[len(msgs)-1].Completion,
		"the stop belonged to the turn before this one")
}

// A stop with no turn running notes nothing, so the next turn's first result is
// not relabelled by a stop that reached an idle agent.
func TestACPInterruptNoteNeedsARunningTurn(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink)}
	b.noteACPInterruptRequested()

	b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-3","kind":"execute","title":"Idle"}`))
	b.main().handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-3","status":"failed"}`))

	msgs := sink.Messages()
	require.NotEmpty(t, msgs)
	assert.NotEqual(t, agent.MessageCompletionInterrupted, msgs[len(msgs)-1].Completion,
		"no turn was running, so there was nothing to stop")
}

// A prompt that FAILS because the reader stopped it is a stop, not an error.
//
// `finishPromptRequest` decided the turn's completion from `IsStopped`, which asks
// whether the agent PROCESS is shut down -- not whether the reader pressed Stop. So a
// cancelled prompt whose RPC returns an error finished the turn as an ERROR, and
// every tool still in flight inherited it.
//
// That is the row Cursor drew: its stored status is `in_progress` and its stored
// completion is `error`, for a command the reader stopped.
func TestACPStoppedPromptThatErrorsIsAStop(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "s1"}
	b.Mu.Lock()
	b.promptActive = true
	b.Mu.Unlock()
	b.noteACPInterruptRequested()

	b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-1","kind":"execute","title":"Sleep"}`))
	b.finishPromptRequest("s1", nil, errors.New("context canceled"))

	msgs := sink.Messages()
	require.NotEmpty(t, msgs)
	last := msgs[len(msgs)-1]
	assert.Equal(t, agent.MessageCompletionInterrupted, last.Completion,
		"the reader stopped this turn, so its unfinished work reports the stop")
	assert.Empty(t, sink.LeapMuxNotifications(),
		"a stop the reader asked for is not an agent error worth telling them about")
}

// A prompt that fails on its own reports the error while keeping open tools alive.
func TestACPPromptThatFailsOnItsOwnIsAnError(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "s1"}
	b.Mu.Lock()
	b.promptActive = true
	b.Mu.Unlock()

	b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-1","kind":"execute","title":"Sleep"}`))
	b.finishPromptRequest("s1", nil, errors.New("boom"))

	assert.Empty(t, closingRows(sink, "call-1"), "non-interrupted prompt failure leaves open tools running")
	assert.True(t, b.HasOpenRootTool("call-1"))
	assert.NotEmpty(t, sink.LeapMuxNotifications(), "a real prompt failure still reaches the reader")
}

// A clean cancel returns a prompt RESPONSE rather than an error, and the tools still
// in flight when it arrives were cut by the stop.
//
// `handleACPPromptResponse` persisted every incomplete tool as an ERROR, which is
// right for a turn that simply ended with work unfinished and wrong for one the
// reader stopped. Cursor takes this path: its cancel returns normally, so neither
// the error branch nor the tool-update branch applies to it.
func TestACPStoppedPromptResponseReportsTheStop(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "s1"}
	b.Mu.Lock()
	b.promptActive = true
	b.Mu.Unlock()
	b.noteACPInterruptRequested()

	b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-1","kind":"execute","title":"Sleep"}`))
	b.handleACPPromptResponse(json.RawMessage(`{"stopReason":"cancelled"}`))

	var tool agenttest.Message
	for _, message := range sink.Messages() {
		if message.SpanID == "call-1" {
			tool = message
		}
	}
	assert.Equal(t, agent.MessageCompletionInterrupted, tool.Completion,
		"the reader stopped this turn, so the work it cut reports the stop")
}

// A normal turn end keeps open tools alive so background work can finish in a later turn.
func TestACPPromptResponseWithoutAStopKeepsTheError(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "s1"}
	b.Mu.Lock()
	b.promptActive = true
	b.Mu.Unlock()

	b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-1","kind":"execute","title":"Sleep"}`))
	b.handleACPPromptResponse(json.RawMessage(`{"stopReason":"end_turn"}`))

	assert.Empty(t, closingRows(sink, "call-1"), "normal end_turn keeps open tools alive")
	assert.True(t, b.HasOpenRootTool("call-1"))
	assert.Equal(t, "execute", sink.GetSpanType("call-1"))
}

func TestACPIdleInterruptClosesOpenToolAndSuppressesLateUpdatesAndFinal(t *testing.T) {
	t.Parallel()

	for _, cancelBefore := range []bool{true, false} {
		name := "cancel_after_withdrawal"
		if cancelBefore {
			name = "cancel_before_withdrawal"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			out := &agenttest.Stdin{}
			b, sink := newACPTurnBase(t, out)
			b.hooks.CancelBeforeControlWithdrawal = cancelBefore
			b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-bg","kind":"execute","title":"Sleep"}`))
			b.main().handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-bg","status":"in_progress","content":[{"type":"content","content":{"type":"text","text":"running"}}]} `))
			require.True(t, b.HasOpenRootTool("call-bg"))

			require.NoError(t, b.Interrupt(agent.StopContext{}))
			assert.False(t, b.HasOpenRootTool("call-bg"))
			assert.Equal(t, []agent.MessageCompletion{agent.MessageCompletionInterrupted}, closingRows(sink, "call-bg"))
			assert.Contains(t, sink.TurnLifecycle(), "reset_spans")
			assert.Contains(t, sink.ProgressUpdates(), agent.ResetProgress())

			b.Mu.Lock()
			drained := b.promptOutputDrained
			b.Mu.Unlock()
			assert.False(t, drained, "idle Interrupt must not set promptOutputDrained")

			progressBefore := len(sink.ProgressUpdates())
			msgsBefore := sink.MessageCount()
			// Late non-final and final updates after idle interrupt must be dropped completely.
			b.main().handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-bg","status":"in_progress","content":[{"type":"content","content":{"type":"text","text":"late chunk"}}]} `))
			b.main().handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-bg","status":"failed"}`))
			assert.Equal(t, progressBefore, len(sink.ProgressUpdates()))
			assert.Equal(t, msgsBefore, sink.MessageCount())

			// A subsequent Turn 2 must still own and finish its prompt normally.
			require.NoError(t, b.SendInput("turn 2", nil))
			b.HandleOutput(acptest.Chunk("session-1", contracts.ACPUpdateAgentMessageChunk, "turn 2 reply"))
			b.HandleJSONRPCResponseForTest(promptResponse(1))
			testutil.RequireEventually(t, func() bool { return !b.PromptActive() })
			assert.Equal(t, []string{"text:turn 2 reply"}, assembledTexts(t, sink.Messages()))
			assert.Len(t, turnEndContents(sink), 1)
		})

		t.Run(name+"_concurrent_turn_2_race", func(t *testing.T) {
			t.Parallel()

			out := &agenttest.Stdin{}
			b, sink := newACPTurnBase(t, out)
			b.hooks.CancelBeforeControlWithdrawal = cancelBefore
			b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-bg","kind":"execute","title":"Sleep"}`))
			require.True(t, b.HasOpenRootTool("call-bg"))

			// Hold sessionUpdates.mu so idle Interrupt completes cancelSession
			// (releasing sessionMu.RLock) and blocks at detachIdleRootTools().
			b.sessionUpdates.mu.Lock()
			interruptErr := make(chan error, 1)
			go func() {
				interruptErr <- b.Interrupt(agent.StopContext{})
			}()
			testutil.RequireEventually(t, func() bool {
				return strings.Contains(out.String(), MethodSessionCancel)
			})

			// Start Turn 2 while idle Interrupt is paused before detachIdleRootTools().
			require.NoError(t, b.SendInput("turn 2", nil))
			b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-turn2","kind":"execute","title":"Turn2"}`))
			lifecycleBefore := len(sink.TurnLifecycle())
			progressBefore := len(sink.ProgressUpdates())
			b.sessionUpdates.mu.Unlock()

			require.NoError(t, <-interruptErr)
			assert.True(t, b.PromptActive(), "racing idle Interrupt must not disturb concurrent Turn 2")
			assert.True(t, b.HasOpenRootTool("call-turn2"), "racing idle Interrupt must not close Turn 2 tools")
			assert.Equal(t, lifecycleBefore, len(sink.TurnLifecycle()), "racing idle Interrupt must not reset spans after Turn 2 started")
			assert.Equal(t, progressBefore, len(sink.ProgressUpdates()), "racing idle Interrupt must not reset progress after Turn 2 started")
			b.Mu.Lock()
			drained := b.promptOutputDrained
			b.Mu.Unlock()
			assert.False(t, drained, "racing idle Interrupt must not set promptOutputDrained")

			b.HandleJSONRPCResponseForTest(promptResponse(1))
			testutil.RequireEventually(t, func() bool { return !b.PromptActive() })
			assert.Len(t, turnEndContents(sink), 1)
		})
	}
}

func TestACPFinishOwnedPromptRequestHandlesNativeCancelledStopReason(t *testing.T) {
	t.Parallel()

	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	var endedStopped bool
	b.hooks.PromptEnded = func(_ error, stopped bool) PromptEnding {
		endedStopped = stopped
		return PromptEnding{}
	}
	b.Mu.Lock()
	b.promptActive = true
	b.Mu.Unlock()
	b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-cancel","kind":"execute","title":"Sleep"}`))

	b.finishPromptRequest("session-1", json.RawMessage(`{"stopReason":"cancelled"}`), nil)
	assert.True(t, endedStopped, "native cancelled stopReason must pass stopped=true to PromptEnded")
	assert.Equal(t, []agent.MessageCompletion{agent.MessageCompletionInterrupted}, closingRows(sink, "call-cancel"))
	assert.False(t, b.HasOpenRootTool("call-cancel"))
	assert.Contains(t, sink.ProgressUpdates(), agent.ResetProgress())
}

func TestACPRPCErrorAfterStopResetsSpans(t *testing.T) {
	t.Parallel()

	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	b.Mu.Lock()
	b.promptActive = true
	b.Mu.Unlock()
	b.noteACPInterruptRequested()
	b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-err","kind":"execute","title":"Sleep"}`))

	b.finishPromptRequest("session-1", nil, errors.New("rpc canceled"))
	assert.Equal(t, []agent.MessageCompletion{agent.MessageCompletionInterrupted}, closingRows(sink, "call-err"))
	assert.Contains(t, sink.TurnLifecycle(), "reset_spans")
	assert.Contains(t, sink.ProgressUpdates(), agent.ResetProgress())
}

func TestACPDuplicateInlineFinalToolCallSkipsHookAndSubagentRowLeak(t *testing.T) {
	t.Parallel()

	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	var hookCalls int
	b.hooks.SubagentFromToolCall = func(tc ToolCallEnvelope) *SubagentObservation {
		hookCalls++
		return &SubagentObservation{
			RowKey: "row-" + tc.ToolCallID,
			Title:  tc.Title,
			Status: bgtask.StatusRunning,
		}
	}

	frame := json.RawMessage(`{"toolCallId":"call-dup","kind":"execute","title":"Run","status":"completed"}`)
	b.main().handleToolCall(frame)
	b.main().handleToolCall(frame)

	assert.Equal(t, 1, hookCalls, "duplicate inline-final tool_call must not re-invoke SubagentFromToolCall")
	b.turnMu.Lock()
	leakedRows := len(b.toolSubagentRows)
	b.turnMu.Unlock()
	assert.Zero(t, leakedRows, "duplicate inline-final tool_call must not leak toolSubagentRows")
	assert.Equal(t, []agent.MessageCompletion{""}, closingRows(sink, "call-dup"))
}

type failingWriteCloser struct {
	err error
}

func (f failingWriteCloser) Write([]byte) (int, error) { return 0, f.err }
func (f failingWriteCloser) Close() error              { return nil }

func TestACPIdleInterruptCancelErrorDoesNotMutateState(t *testing.T) {
	t.Parallel()

	for _, cancelBefore := range []bool{true, false} {
		name := "cancel_after_withdrawal"
		if cancelBefore {
			name = "cancel_before_withdrawal"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cancelWriteErr := errors.New("stdin broken")
			b, _ := newACPTurnBase(t, failingWriteCloser{err: cancelWriteErr})
			sink := &agenttest.ControlSink{}
			b.sink = agent.NewProviderServices(sink)
			b.hooks.CancelBeforeControlWithdrawal = cancelBefore

			b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-bg","kind":"execute","title":"Sleep"}`))
			require.True(t, b.HasOpenRootTool("call-bg"))
			b.HandleOutput([]byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"sessionId":"session-1","options":[{"optionId":"allow_once","kind":"allow_once","name":"allow_once"}]}}`))
			require.Equal(t, 1, sink.PublishedControlCount())

			err := b.Interrupt(agent.StopContext{})
			require.ErrorIs(t, err, cancelWriteErr)
			assert.True(t, b.HasOpenRootTool("call-bg"), "failed cancel must keep open tools untouched")
			assert.Empty(t, closingRows(&sink.Sink, "call-bg"), "failed cancel must not write closing rows")
			assert.NotContains(t, sink.TurnLifecycle(), "reset_spans", "failed cancel must not reset spans")
			assert.NotContains(t, sink.ProgressUpdates(), agent.ResetProgress(), "failed cancel must not reset progress")
			if cancelBefore {
				assert.Empty(t, sink.CanceledControls(), "CancelBeforeControlWithdrawal must not withdraw controls when cancel fails")
			}
		})
	}
}

func TestACPNativeCloseAllowsToolNotificationAndReuseWhileForcedDropsAll(t *testing.T) {
	t.Parallel()

	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	var notifications []string
	b.hooks.ToolNotification = func(tcu ToolCallUpdateEnvelope) bool {
		notifications = append(notifications, tcu.ToolCallID)
		return true
	}

	b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-native","kind":"execute","title":"Delegate"}`))
	b.main().handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-native","status":"completed"}`))
	notifications = nil
	// Duplicate final update must not write a second closing row, CloseSpan, or ToolNotification.
	closedBefore := len(sink.ClosedSpans())
	b.main().handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-native","status":"completed"}`))
	assert.Equal(t, []agent.MessageCompletion{""}, closingRows(sink, "call-native"))
	assert.Equal(t, closedBefore, len(sink.ClosedSpans()), "duplicate final update must not emit a second CloseSpan")
	assert.Empty(t, notifications, "duplicate final update must not re-invoke ToolNotification")

	// Non-final update after a native close must still reach ToolNotification.
	b.main().handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-native","status":"in_progress"}`))
	assert.Equal(t, []string{"call-native"}, notifications, "native close must not block ToolNotification on late non-final updates")

	// Genuine toolCallId reuse via a new non-final tool_call must reopen cleanly.
	b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-native","kind":"execute","title":"Reuse"}`))
	b.main().handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-native","status":"completed"}`))
	assert.Equal(t, []agent.MessageCompletion{"", ""}, closingRows(sink, "call-native"))
	assert.Equal(t, 2, b.TurnToolUsesForTest(), "reused toolCallId must count its second completion")

	// Forced close via idle Interrupt must drop ToolNotification and late final updates.
	notifications = nil
	b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-forced","kind":"execute","title":"Sleep"}`))
	require.NoError(t, b.Interrupt(agent.StopContext{}))
	b.main().handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-forced","status":"in_progress"}`))
	b.main().handleToolCallUpdate(json.RawMessage(`{"toolCallId":"call-forced","status":"completed"}`))
	assert.Empty(t, notifications, "forced close must drop late ToolNotification updates")
	assert.Equal(t, []agent.MessageCompletion{agent.MessageCompletionInterrupted}, closingRows(sink, "call-forced"))
}

func TestACPWrappedResultEnvelopeCancelledStopReasonClosesOpenTools(t *testing.T) {
	t.Parallel()

	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	var endedStopped bool
	b.hooks.PromptEnded = func(_ error, stopped bool) PromptEnding {
		endedStopped = stopped
		return PromptEnding{}
	}
	b.Mu.Lock()
	b.promptActive = true
	b.Mu.Unlock()
	b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-wrapped","kind":"execute","title":"Sleep"}`))

	b.finishPromptRequest("session-1", json.RawMessage(`{"role":"result","content":{"stopReason":"cancelled"}}`), nil)
	assert.True(t, endedStopped, "wrapped cancelled envelope must pass stopped=true to PromptEnded")
	assert.Equal(t, []agent.MessageCompletion{agent.MessageCompletionInterrupted}, closingRows(sink, "call-wrapped"))
	assert.False(t, b.HasOpenRootTool("call-wrapped"))
}
