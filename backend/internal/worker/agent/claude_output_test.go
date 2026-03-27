package agent

import (
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spanRecord captures an OpenSpan call.
type spanRecord struct {
	SpanID       string
	ParentSpanID string
}

// outputTestSink extends testSink to record spans and permission mode updates.
type outputTestSink struct {
	testSink

	spanMu      sync.Mutex
	openedSpans []spanRecord
	closedSpans []string

	modeMu          sync.Mutex
	permissionModes []string
}

func (s *outputTestSink) OpenSpan(spanID, parentSpanID string) {
	s.spanMu.Lock()
	defer s.spanMu.Unlock()
	s.openedSpans = append(s.openedSpans, spanRecord{SpanID: spanID, ParentSpanID: parentSpanID})
}

func (s *outputTestSink) CloseSpan(spanID string) {
	s.spanMu.Lock()
	defer s.spanMu.Unlock()
	s.closedSpans = append(s.closedSpans, spanID)
}

func (s *outputTestSink) UpdatePermissionMode(mode string) {
	s.modeMu.Lock()
	defer s.modeMu.Unlock()
	s.permissionModes = append(s.permissionModes, mode)
}

func (s *outputTestSink) OpenedSpans() []spanRecord {
	s.spanMu.Lock()
	defer s.spanMu.Unlock()
	return append([]spanRecord(nil), s.openedSpans...)
}

func (s *outputTestSink) ClosedSpans() []string {
	s.spanMu.Lock()
	defer s.spanMu.Unlock()
	return append([]string(nil), s.closedSpans...)
}

func (s *outputTestSink) PermissionModes() []string {
	s.modeMu.Lock()
	defer s.modeMu.Unlock()
	return append([]string(nil), s.permissionModes...)
}

// newTestAgent creates a minimal ClaudeCodeAgent for unit-testing HandleOutput.
func newTestAgent(sink OutputSink) *ClaudeCodeAgent {
	return &ClaudeCodeAgent{
		processBase: processBase{
			agentID: "test-agent",
		},
		sink: sink,
	}
}

func TestHandleOutput_AssistantToolUse(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	content := []byte(`{
		"type": "assistant",
		"parent_tool_use_id": "parent-123",
		"message": {
			"role": "assistant",
			"content": [
				{"type": "text", "text": "Let me read that file."},
				{"type": "tool_use", "id": "tu-001", "name": "Read", "input": {"file_path": "/tmp/foo.txt"}}
			]
		}
	}`)

	agent.HandleOutput(content)

	msgs := sink.Messages()
	require.Len(t, msgs, 1)

	msg := msgs[0]
	assert.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, msg.Role)
	assert.Equal(t, "parent-123", msg.ParentSpanID)
	assert.Equal(t, "tu-001", msg.SpanID)
	assert.Equal(t, "Read", msg.SpanType)

	// processAssistantBlocks should have opened a span.
	spans := sink.OpenedSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "tu-001", spans[0].SpanID)
	assert.Equal(t, "parent-123", spans[0].ParentSpanID)

	// Tool use counter should be incremented.
	assert.Equal(t, 1, agent.turnToolUses)
}

func TestHandleOutput_AssistantToolUse_FallbackParentSpanID(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	// No parent_tool_use_id, but has tool_use_id at top level (system-injected).
	content := []byte(`{
		"type": "assistant",
		"tool_use_id": "sys-tu-999",
		"message": {
			"role": "assistant",
			"content": [
				{"type": "tool_use", "id": "tu-002", "name": "Bash", "input": {"command": "ls"}}
			]
		}
	}`)

	agent.HandleOutput(content)

	msgs := sink.Messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "sys-tu-999", msgs[0].ParentSpanID)
	assert.Equal(t, "tu-002", msgs[0].SpanID)
	assert.Equal(t, "Bash", msgs[0].SpanType)
}

func TestHandleOutput_UserToolResult(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	// Pre-register a span type so GetSpanType works.
	sink.SetSpanType("tu-001", "Read")

	content := []byte(`{
		"type": "user",
		"parent_tool_use_id": "parent-123",
		"message": {
			"role": "user",
			"content": [
				{"type": "tool_result", "tool_use_id": "tu-001", "content": "file contents"}
			]
		}
	}`)

	agent.HandleOutput(content)

	msgs := sink.Messages()
	require.Len(t, msgs, 1)

	msg := msgs[0]
	assert.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_USER, msg.Role)
	assert.Equal(t, "parent-123", msg.ParentSpanID)
	assert.Equal(t, "tu-001", msg.SpanID)
	assert.Equal(t, "Read", msg.SpanType)

	// Span should be closed after persist.
	closed := sink.ClosedSpans()
	require.Len(t, closed, 1)
	assert.Equal(t, "tu-001", closed[0])
}

func TestHandleOutput_AssistantNoToolUse(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	content := []byte(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [
				{"type": "text", "text": "Hello!"}
			]
		}
	}`)

	agent.HandleOutput(content)

	msgs := sink.Messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "", msgs[0].SpanID)
	assert.Equal(t, "", msgs[0].SpanType)
	assert.Equal(t, "", msgs[0].ParentSpanID)

	// No spans opened.
	assert.Empty(t, sink.OpenedSpans())
	assert.Equal(t, 0, agent.turnToolUses)
}

func TestHandleOutput_MalformedJSON(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	// Completely invalid JSON — should not panic.
	agent.HandleOutput([]byte(`not json at all`))
	assert.Empty(t, sink.Messages())

	// Valid outer type but malformed message body — early return from envelope parse.
	agent.HandleOutput([]byte(`{"type":"assistant","message":INVALID}`))
	assert.Empty(t, sink.Messages())
}

func TestHandleOutput_EmptyContentBlocks(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	content := []byte(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": []
		}
	}`)

	agent.HandleOutput(content)

	msgs := sink.Messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "", msgs[0].SpanID)
	assert.Equal(t, "", msgs[0].SpanType)
}

func TestHandleOutput_PlanModeEnterExit(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	// Assistant sends EnterPlanMode tool_use.
	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [
				{"type": "tool_use", "id": "pm-001", "name": "EnterPlanMode", "input": {}}
			]
		}
	}`))

	// User confirms with tool_result.
	agent.HandleOutput([]byte(`{
		"type": "user",
		"message": {
			"role": "user",
			"content": [
				{"type": "tool_result", "tool_use_id": "pm-001"}
			]
		},
		"tool_use_result": {"message": "You have entered plan mode."}
	}`))

	modes := sink.PermissionModes()
	require.Len(t, modes, 1)
	assert.Equal(t, PermissionModePlan, modes[0])
}

func TestHandleOutput_PlanModeEnter_StringToolUseResult(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	// Assistant sends EnterPlanMode tool_use.
	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [
				{"type": "tool_use", "id": "pm-002", "name": "EnterPlanMode", "input": {}}
			]
		}
	}`))

	// User confirms with tool_result, but tool_use_result is a plain string.
	agent.HandleOutput([]byte(`{
		"type": "user",
		"message": {
			"role": "user",
			"content": [
				{"type": "tool_result", "tool_use_id": "pm-002"}
			]
		},
		"tool_use_result": "You have entered plan mode."
	}`))

	modes := sink.PermissionModes()
	require.Len(t, modes, 1)
	assert.Equal(t, PermissionModePlan, modes[0])
}

func TestHandleOutput_MultipleToolUses(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	content := []byte(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [
				{"type": "tool_use", "id": "tu-a", "name": "Read", "input": {}},
				{"type": "tool_use", "id": "tu-b", "name": "Grep", "input": {}}
			]
		}
	}`)

	agent.HandleOutput(content)

	// Only the first tool_use block determines spanID/spanType.
	msgs := sink.Messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "tu-a", msgs[0].SpanID)
	assert.Equal(t, "Read", msgs[0].SpanType)

	// Both tool_use blocks should open spans.
	spans := sink.OpenedSpans()
	require.Len(t, spans, 2)
	assert.Equal(t, "tu-a", spans[0].SpanID)
	assert.Equal(t, "tu-b", spans[1].SpanID)

	// Tool use counter should reflect both.
	assert.Equal(t, 2, agent.turnToolUses)
}

func TestHandleOutput_TopLevelAssistantBroadcastsContextUsage(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	// A top-level assistant message (no parent_tool_use_id) with usage should
	// broadcast context usage via session info.
	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [{"type": "text", "text": "hello"}],
			"usage": {"input_tokens": 100, "output_tokens": 50, "cache_creation_input_tokens": 10, "cache_read_input_tokens": 30}
		}
	}`))

	// Force a result message to trigger the broadcast (assistant messages are
	// debounced, but result messages always broadcast).
	agent.HandleOutput([]byte(`{
		"type": "result",
		"subtype": "success"
	}`))

	require.GreaterOrEqual(t, sink.SessionInfoCount(), 1)
	info := sink.LastSessionInfo()
	usage, ok := info["contextUsage"].(map[string]interface{})
	require.True(t, ok, "expected contextUsage in session info")
	assert.Equal(t, int64(100), usage["inputTokens"])
	assert.Equal(t, int64(50), usage["outputTokens"])
	assert.Equal(t, int64(10), usage["cacheCreationInputTokens"])
	assert.Equal(t, int64(30), usage["cacheReadInputTokens"])
}

func TestIsSyntheticAPIError(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"500 error", `{"type":"result","is_error":true,"result":"API Error: 500 Internal Server Error"}`, true},
		{"502 error", `{"type":"result","is_error":true,"result":"API Error: 502 Bad Gateway"}`, true},
		{"529 overloaded", `{"type":"result","is_error":true,"result":"API Error: 529 Overloaded"}`, true},
		{"599 bare", `{"type":"result","is_error":true,"result":"API Error: 599"}`, true},
		{"400 not matched", `{"type":"result","is_error":true,"result":"API Error: 400 Bad Request"}`, false},
		{"single digit 5", `{"type":"result","is_error":true,"result":"API Error: 5"}`, false},
		{"two digit 50", `{"type":"result","is_error":true,"result":"API Error: 50"}`, false},
		{"four digit 5000", `{"type":"result","is_error":true,"result":"API Error: 5000"}`, false},
		{"not error", `{"type":"result","is_error":false,"result":"done"}`, false},
		{"not result type", `{"type":"assistant","is_error":true,"result":"API Error: 500"}`, false},
		{"normal result", `{"type":"result","subtype":"success","result":"All done","duration_ms":1234}`, false},
		{"invalid json", `{invalid`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSyntheticAPIError([]byte(tt.input))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHandleOutput_SubagentAssistantDoesNotOverwriteContextUsage(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	// First, a top-level assistant message sets the usage baseline.
	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [{"type": "text", "text": "top level"}],
			"usage": {"input_tokens": 500, "output_tokens": 200, "cache_creation_input_tokens": 40, "cache_read_input_tokens": 100}
		}
	}`))

	// Then a subagent assistant message (with parent_tool_use_id) has smaller
	// usage — it must NOT overwrite the top-level snapshot.
	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "agent-tu-1",
		"message": {
			"role": "assistant",
			"content": [{"type": "text", "text": "subagent"}],
			"usage": {"input_tokens": 50, "output_tokens": 10, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 5}
		}
	}`))

	// Force broadcast via result.
	agent.HandleOutput([]byte(`{
		"type": "result",
		"subtype": "success"
	}`))

	require.GreaterOrEqual(t, sink.SessionInfoCount(), 1)
	info := sink.LastSessionInfo()
	usage, ok := info["contextUsage"].(map[string]interface{})
	require.True(t, ok, "expected contextUsage in session info")
	assert.Equal(t, int64(500), usage["inputTokens"], "subagent should not overwrite top-level inputTokens")
	assert.Equal(t, int64(200), usage["outputTokens"], "subagent should not overwrite top-level outputTokens")
}
