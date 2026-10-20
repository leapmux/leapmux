package acp

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
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
