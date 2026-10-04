package acp

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
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

// A prompt that fails on its own is still an error, and still worth reporting.
func TestACPPromptThatFailsOnItsOwnIsAnError(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "s1"}
	b.Mu.Lock()
	b.promptActive = true
	b.Mu.Unlock()

	b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-1","kind":"execute","title":"Sleep"}`))
	b.finishPromptRequest("s1", nil, errors.New("boom"))

	msgs := sink.Messages()
	require.NotEmpty(t, msgs)
	assert.Equal(t, agent.MessageCompletionError, msgs[len(msgs)-1].Completion,
		"nobody stopped this, so the turn failed")
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

// A turn that ends on its own with work unfinished is still a failure.
func TestACPPromptResponseWithoutAStopKeepsTheError(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	b := &Base{sink: agent.NewProviderServices(sink), sessionID: "s1"}
	b.Mu.Lock()
	b.promptActive = true
	b.Mu.Unlock()

	b.main().handleToolCall(json.RawMessage(`{"toolCallId":"call-1","kind":"execute","title":"Sleep"}`))
	b.handleACPPromptResponse(json.RawMessage(`{"stopReason":"end_turn"}`))

	var tool agenttest.Message
	for _, message := range sink.Messages() {
		if message.SpanID == "call-1" {
			tool = message
		}
	}
	assert.Equal(t, agent.MessageCompletionError, tool.Completion,
		"nobody stopped this, so a tool the turn left unfinished failed")
}
