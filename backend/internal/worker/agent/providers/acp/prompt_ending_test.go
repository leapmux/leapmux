package acp

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type promptEndingSink struct {
	*agenttest.Sink
	events []string
}

func (s *promptEndingSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	s.events = append(s.events, "message")
	return s.Sink.PersistMessage(source, content, span)
}

func (s *promptEndingSink) PersistLeapMuxNotification(content map[string]interface{}) {
	s.events = append(s.events, "note")
	s.Sink.PersistLeapMuxNotification(content)
}

func (s *promptEndingSink) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	s.events = append(s.events, "end")
	return s.Sink.PersistTurnEnd(content, span)
}

func TestFailedPromptWithEndFrameWritesTheDivider(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	b, recording := newACPTurnBase(t, agenttest.NopStdin(&output))
	sink := &promptEndingSink{Sink: recording}
	b.sink = agent.NewProviderServices(sink)
	b.promptActive = true
	b.handleACPUpdate(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tool-1","title":"Run","kind":"execute","status":"pending"}`))
	b.handleACPUpdate(json.RawMessage(`{"sessionUpdate":"agent_message_chunk","messageId":"answer","content":{"type":"text","text":"partial answer"}}`))
	sink.events = nil
	frame := json.RawMessage(`{"sessionUpdate":"session_info_update","_meta":{"native":{"kind":"turn_end","error":"failed"}}}`)
	b.hooks.PromptEnded = func(error, bool) PromptEnding { return PromptEnding{EndFrame: frame} }

	b.finishPromptRequest("session-1", nil, errors.New("failed"))

	assert.Equal(t, []string{"message", "message", "note", "end"}, sink.events)
	rows := sink.Messages()
	require.Len(t, rows, 4)
	assert.Contains(t, string(rows[1].Content), "partial answer")
	assert.Contains(t, string(rows[1].Content), `"completion":"error"`)
	assert.Equal(t, agent.MessageCompletionError, rows[2].Completion)
	assert.True(t, rows[2].Closing)
	assert.Equal(t, []byte(frame), rows[3].Content)
	assert.True(t, rows[3].TurnEnd)
	active, _ := sink.LastTurnActive()
	assert.False(t, active)
}

func TestFailedPromptNoteWrittenSkipsTheNote(t *testing.T) {
	t.Parallel()
	for _, endFrame := range []json.RawMessage{nil, json.RawMessage(`{"stopReason":"error"}`)} {
		t.Run(string(endFrame), func(t *testing.T) {
			var output bytes.Buffer
			b, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
			b.hooks.PromptEnded = func(error, bool) PromptEnding {
				return PromptEnding{NoteWritten: true, EndFrame: endFrame}
			}
			b.finishPromptRequest("session-1", nil, errors.New("failed"))
			assert.Empty(t, sink.LeapMuxNotifications())
			if len(endFrame) == 0 {
				assert.Empty(t, sink.Messages())
			} else {
				require.Len(t, sink.Messages(), 1)
				assert.True(t, sink.Messages()[0].TurnEnd)
			}
		})
	}
}

func TestFailedPromptWithoutHookIsUnchanged(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
	b.finishPromptRequest("session-1", nil, errors.New("failed"))
	assert.Empty(t, sink.Messages())
	require.Len(t, sink.LeapMuxNotifications(), 1)
	assert.Contains(t, sink.LeapMuxNotifications()[0], "error")
}

func TestFailedPromptWithEndFrameKeepsAQueuedAgentTurn(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	b, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
	b.promptActive = true
	b.handleACPUpdate(json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"prompt text"}}`))
	require.True(t, b.BeginAgentTurn())
	b.handleACPUpdate(json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"agent text"}}`))
	b.hooks.PromptEnded = func(error, bool) PromptEnding {
		return PromptEnding{EndFrame: json.RawMessage(`{"stopReason":"error"}`)}
	}

	b.finishPromptRequest("session-1", nil, errors.New("failed"))

	assert.True(t, b.AgentTurnActive())
	assert.Equal(t, []string{"text:prompt text"}, assembledTexts(t, sink.Messages()))
	assert.Len(t, turnEndContents(sink), 1)
	assert.Equal(t, "agent text", b.TurnAssistantTextForTest().String())
	b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))
	assert.Equal(t, []string{"text:prompt text", "text:agent text"}, assembledTexts(t, sink.Messages()))
	assert.Len(t, turnEndContents(sink), 2)
}

func TestPromptEndedIgnoresFailureFrameAfterAStopOrSuccess(t *testing.T) {
	t.Parallel()
	for _, stopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "stopped"}[stopped], func(t *testing.T) {
			var output bytes.Buffer
			b, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
			b.hooks.PromptEnded = func(error, bool) PromptEnding {
				return PromptEnding{EndFrame: json.RawMessage(`{"stopReason":"error"}`)}
			}
			var failure error
			if stopped {
				b.promptActive = true
				b.noteACPInterruptRequested()
				failure = errors.New("cancelled")
			}
			b.finishPromptRequest("session-1", json.RawMessage(`{"stopReason":"end_turn"}`), failure)
			assert.Empty(t, sink.LeapMuxNotifications())
			if stopped {
				assert.Empty(t, sink.Messages())
			} else {
				require.Len(t, sink.Messages(), 1)
				assert.JSONEq(t, `{"stopReason":"end_turn"}`, string(sink.Messages()[0].Content))
			}
		})
	}
}

// The verdict follows the row that ends the turn: the turn-end row of a
// success, or the failure note of a failed prompt that sent no end frame.
func TestPromptEndingRetry_TheVerdictFollowsTheRowThatEndsTheTurn(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		response *providerkit.ParsedLine
		endRow   string
	}{
		"success": {promptResponse(1), "turn_end"},
		"failure": {providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"Internal error"}}`)), "leapmux_notification"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b, sink := newACPTurnBase(t, &agenttest.Stdin{})
			b.hooks.PromptEnded = func(error, bool) PromptEnding {
				retry := transientVerdict()
				return PromptEnding{Retry: &retry}
			}
			require.NoError(t, b.SendInput("hi", nil))

			b.HandleJSONRPCResponseForTest(tc.response)
			testutil.RequireEventually(t, func() bool { return !b.PromptActive() })

			timeline := sink.Timeline()
			require.GreaterOrEqual(t, len(timeline), 2)
			assert.Equal(t, "schedule_server_error", timeline[len(timeline)-1].Op, "the verdict is the last operation")
			assert.Equal(t, tc.endRow, timeline[len(timeline)-2].Op, "the row that ends the turn comes directly before it")
			ops, _ := retryOps(timeline)
			assert.Len(t, ops, 1, "the turn applies exactly one verdict")
			require.Equal(t, 1, sink.AutoScheduleCount())
			assert.JSONEq(t, `{"error":"503"}`, string(sink.LastAutoSchedule().SourcePayload))
		})
	}
}

func TestPromptEndingRetry_ANilVerdictCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	for name, hook := range map[string]func(error, bool) PromptEnding{
		"no hook":        nil,
		"a nil verdict":  func(error, bool) PromptEnding { return PromptEnding{} },
		"a zero verdict": func(error, bool) PromptEnding { return PromptEnding{Retry: &TurnVerdict{}} },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b, sink := newACPTurnBase(t, &agenttest.Stdin{})
			b.hooks.PromptEnded = hook
			require.NoError(t, b.SendInput("hi", nil))

			b.HandleJSONRPCResponseForTest(promptResponse(1))
			testutil.RequireEventually(t, func() bool { return !b.PromptActive() })

			ops, _ := retryOps(sink.Timeline())
			require.Equal(t, []string{"cancel_server_error"}, opNames(ops))
			assert.Equal(t, providerkit.TurnEndedWithoutRetryableFailure, ops[0].Reason)
		})
	}
}

func TestPromptEndingRetry_AStoppedPromptAppliesTheVerdictThatItsHookReturns(t *testing.T) {
	t.Parallel()
	b, sink := newACPTurnBase(t, &agenttest.Stdin{})
	resetAt := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	var sawStopped bool
	b.hooks.PromptEnded = func(_ error, stopped bool) PromptEnding {
		sawStopped = stopped
		retry := usageVerdict(resetAt)
		return PromptEnding{Retry: &retry}
	}
	require.NoError(t, b.SendInput("hi", nil))
	b.noteACPInterruptRequested()

	b.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"cancelled"}}`)))
	testutil.RequireEventually(t, func() bool { return !b.PromptActive() })

	assert.True(t, sawStopped)
	ops, _ := retryOps(sink.Timeline())
	require.Equal(t, []string{"schedule_usage_limit", "cancel_server_error"}, opNames(ops))
	assert.True(t, resetAt.Equal(ops[0].ResetAt))
}
