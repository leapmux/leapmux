package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

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

	planMu    sync.Mutex
	planCalls []planUpdateCall
}

type planUpdateCall struct {
	Title string
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

func (s *outputTestSink) UpdatePlan(_ []byte, _ leapmuxv1.ContentCompression, title string) {
	s.planMu.Lock()
	defer s.planMu.Unlock()
	s.planCalls = append(s.planCalls, planUpdateCall{Title: title})
}

func (s *outputTestSink) PlanCalls() []planUpdateCall {
	s.planMu.Lock()
	defer s.planMu.Unlock()
	return append([]planUpdateCall(nil), s.planCalls...)
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

// testHomeDir returns a platform-appropriate home directory path for tests
// that exercise OS-native path handling.
func testHomeDir() string {
	if filepath.Separator == '/' {
		return "/home/user"
	}
	return filepath.Join("C:", "Users", "user")
}

func TestHandleOutput_PlanFileDetected_UsesPlatformPathSeparators(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	agent.homeDir = testHomeDir()
	planPath := filepath.Join(agent.homeDir, ".claude", "plans", "my-plan.md")

	input, err := json.Marshal(map[string]string{
		"file_path": planPath,
		"content":   "# My Plan\n\nHere is the body.",
	})
	require.NoError(t, err)

	content := []byte(fmt.Sprintf(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [
				{"type": "tool_use", "id": "tu-plan", "name": "Write", "input": %s}
			]
		}
	}`, input))

	agent.HandleOutput(content)

	calls := sink.PlanCalls()
	require.Len(t, calls, 1, "UpdatePlan should fire for a Write to ~/.claude/plans/")
	assert.Equal(t, "My Plan", calls[0].Title)
}

func TestHandleOutput_PlanFileIgnored_WhenPathIsOutsidePlansDir(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	agent.homeDir = testHomeDir()
	otherPath := filepath.Join(agent.homeDir, ".claude", "other", "note.md")

	input, err := json.Marshal(map[string]string{
		"file_path": otherPath,
		"content":   "not a plan",
	})
	require.NoError(t, err)

	content := []byte(fmt.Sprintf(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [
				{"type": "tool_use", "id": "tu-other", "name": "Write", "input": %s}
			]
		}
	}`, input))

	agent.HandleOutput(content)

	assert.Empty(t, sink.PlanCalls())
}

func TestClaudeRateLimitEvent_SchedulesResumeWhenBlocked(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	rawEvent := `{"type":"rate_limit_event","rate_limit_info":{"rateLimitType":"five_hour","status":"exceeded","resetsAt":1893456000}}`
	agent.HandleOutput([]byte(rawEvent))

	require.Equal(t, 1, sink.AutoScheduleCount())
	schedule := sink.LastAutoSchedule()
	assert.Equal(t, AutoContinueReasonRateLimit, schedule.Reason)
	assert.Equal(t, time.Unix(1893456000, 0).UTC(), schedule.DueAt)
	assert.JSONEq(t, `{"rateLimitType":"five_hour","status":"exceeded","resetsAt":1893456000}`, string(schedule.SourcePayload))

	// Persists raw rate_limit_event verbatim as SYSTEM (no longer
	// synthesizes a stripped-down {type:"rate_limit",rate_limit_info}).
	require.Equal(t, 1, sink.NotificationCount())
	last := sink.LastNotification()
	assert.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM, last.Role,
		"Claude-emitted rate_limit_event must persist as SYSTEM")
	assert.JSONEq(t, rawEvent, string(last.Content),
		"raw envelope must be preserved verbatim so future fields flow through")
}

// TestClaudeRateLimitEvent_BroadcastsSnakeCaseWire locks in the wire
// translation: Claude's SDK emits camelCase rate_limit_info, but the
// broadcast `rate_limits` map exposes a snake_case tier shape so all
// providers (Claude / Codex) deliver the same field names to the frontend.
func TestClaudeRateLimitEvent_BroadcastsSnakeCaseWire(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	rawEvent := `{"type":"rate_limit_event","rate_limit_info":{"rateLimitType":"five_hour","status":"exceeded","resetsAt":1893456000,"utilization":1.0}}`
	agent.HandleOutput([]byte(rawEvent))

	require.Equal(t, 1, sink.SessionInfoCount())
	info := sink.LastSessionInfo()
	rateLimits, ok := info["rate_limits"].(map[string]any)
	require.True(t, ok, "broadcast must carry rate_limits in snake_case, got %#v", info)

	tier, ok := rateLimits["five_hour"].(map[string]any)
	require.True(t, ok, "tier should be keyed by rate_limit_type")
	assert.Equal(t, "five_hour", tier["rate_limit_type"])
	assert.Equal(t, "exceeded", tier["status"])
	assert.Equal(t, int64(1893456000), tier["resets_at"])
	assert.Equal(t, 1.0, tier["utilization"])
}

func TestClaudeRateLimitEvent_AllowedCancelsResume(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	agent.HandleOutput([]byte(`{
		"type":"rate_limit_event",
		"rate_limit_info":{
			"rateLimitType":"five_hour",
			"status":"allowed",
			"resetsAt":1893456000
		}
	}`))

	require.Equal(t, 1, sink.AutoCancelCount())
	assert.Equal(t, AutoContinueReasonRateLimit, sink.LastAutoCancel())
	assert.Equal(t, 0, sink.AutoScheduleCount())
}

func TestClaudeResult_APIErrorUsesAPIErrorReason(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	agent.HandleOutput([]byte(`{
		"type":"result",
		"is_error":true,
		"result":"API Error: 500 Internal Server Error"
	}`))

	require.Equal(t, 1, sink.AutoScheduleCount())
	assert.Equal(t, AutoContinueReasonAPIError, sink.LastAutoSchedule().Reason)

	agent.HandleOutput([]byte(`{
		"type":"result",
		"is_error":false,
		"result":"ok"
	}`))

	require.Equal(t, 1, sink.AutoCancelCount())
	assert.Equal(t, AutoContinueReasonAPIError, sink.LastAutoCancel())
}

func TestClaudeResult_IdleTimeoutPrefixSchedulesAPIErrorAutoContinue(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	payload := []byte(`{
		"type":"result",
		"is_error":true,
		"result":"API Error: Stream idle timeout - partial response received"
	}`)

	agent.HandleOutput(payload)

	require.Equal(t, 1, sink.AutoScheduleCount())
	schedule := sink.LastAutoSchedule()
	assert.Equal(t, AutoContinueReasonAPIError, schedule.Reason)
	var source struct {
		Type        string `json:"type"`
		IsError     bool   `json:"is_error"`
		Result      string `json:"result"`
		NumToolUses int    `json:"num_tool_uses"`
	}
	require.NoError(t, json.Unmarshal(schedule.SourcePayload, &source))
	assert.Equal(t, "result", source.Type)
	assert.True(t, source.IsError)
	assert.Equal(t, "API Error: Stream idle timeout - partial response received", source.Result)
	assert.Equal(t, 0, source.NumToolUses)
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
	usage, ok := info["context_usage"].(map[string]interface{})
	require.True(t, ok, "expected context_usage in session info")
	assert.Equal(t, int64(100), usage["input_tokens"])
	assert.Equal(t, int64(50), usage["output_tokens"])
	assert.Equal(t, int64(10), usage["cache_creation_input_tokens"])
	assert.Equal(t, int64(30), usage["cache_read_input_tokens"])
}

func TestIsRetryableClaudeResultError(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"5xx 500", "API Error: 500 Internal Server Error", true},
		{"5xx 502", "API Error: 502 Bad Gateway", true},
		{"5xx 529", "API Error: 529 Overloaded", true},
		{"5xx 599 bare", "API Error: 599", true},
		{"5xx alternate punctuation", "API Error - 502 Bad Gateway", true},
		{"5xx repeated punctuation", "API Error:: 529 Overloaded", true},
		{"idle timeout exact", "API Error: Stream idle timeout", true},
		{"idle timeout partial response", "API Error: Stream idle timeout - partial response received", true},
		{"idle timeout alternate punctuation", "API Error - Stream idle timeout - partial response received", true},
		{"idle timeout repeated punctuation", "API Error:: Stream idle timeout", true},
		{"non-retryable 4xx", "API Error: 400 Bad Request", false},
		{"5xx single digit", "API Error: 5", false},
		{"5xx two digits", "API Error: 50", false},
		{"5xx four digits", "API Error: 5000", false},
		{"5xx alphanumeric separator", "API ErrorX 500 Internal Server Error", false},
		{"5xx alphanumeric suffix", "API Error: 500X", false},
		{"idle timeout alphanumeric separator", "API ErrorX Stream idle timeout", false},
		{"idle timeout alphanumeric suffix", "API Error: Stream idle timeoutX", false},
		{"empty string", "", false},
		{"plain text", "done", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableClaudeResultError(tt.input)
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
	usage, ok := info["context_usage"].(map[string]interface{})
	require.True(t, ok, "expected context_usage in session info")
	assert.Equal(t, int64(500), usage["input_tokens"], "subagent should not overwrite top-level input_tokens")
	assert.Equal(t, int64(200), usage["output_tokens"], "subagent should not overwrite top-level output_tokens")
}

func TestHandleOutput_ResultModelUsagePicksPrimaryContextWindow(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	agent.model = "opus[1m]"

	// Send an assistant message so there is usage to broadcast.
	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [{"type": "text", "text": "hello"}],
			"usage": {"input_tokens": 100, "output_tokens": 50}
		}
	}`))

	// A result message with modelUsage containing two models — haiku (200k)
	// and opus[1m] (1M). The primary model (opus[1m]) should be selected.
	agent.HandleOutput([]byte(`{
		"type": "result",
		"subtype": "success",
		"modelUsage": {
			"claude-haiku-4-5-20251001": {"contextWindow": 200000},
			"claude-opus-4-6[1m]": {"contextWindow": 1000000}
		}
	}`))

	require.GreaterOrEqual(t, sink.SessionInfoCount(), 1)
	info := sink.LastSessionInfo()
	usage, ok := info["context_usage"].(map[string]interface{})
	require.True(t, ok, "expected context_usage in session info")
	assert.Equal(t, int64(1000000), usage["context_window"], "should pick primary model's context_window")
}

func TestHandleOutput_ResultModelUsagePicksNon1MContextWindow(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	agent.model = "opus"

	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [{"type": "text", "text": "hello"}],
			"usage": {"input_tokens": 100, "output_tokens": 50}
		}
	}`))

	// When primary model is "opus" (no [1m]), it should match "claude-opus-4-6"
	// (no bracket suffix) and NOT "claude-opus-4-6[1m]".
	agent.HandleOutput([]byte(`{
		"type": "result",
		"subtype": "success",
		"modelUsage": {
			"claude-opus-4-6": {"contextWindow": 200000},
			"claude-opus-4-6[1m]": {"contextWindow": 1000000}
		}
	}`))

	require.GreaterOrEqual(t, sink.SessionInfoCount(), 1)
	info := sink.LastSessionInfo()
	usage, ok := info["context_usage"].(map[string]interface{})
	require.True(t, ok, "expected context_usage in session info")
	assert.Equal(t, int64(200000), usage["context_window"], "should pick non-1M opus context_window")
}

func TestHandleOutput_SubagentResultDoesNotOverwriteContextWindow(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	agent.model = "opus[1m]"

	// Top-level assistant message establishes usage baseline.
	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [{"type": "text", "text": "top level"}],
			"usage": {"input_tokens": 500, "output_tokens": 200}
		}
	}`))

	// Top-level result sets the context window to 1M.
	agent.HandleOutput([]byte(`{
		"type": "result",
		"subtype": "success",
		"modelUsage": {
			"claude-haiku-4-5-20251001": {"contextWindow": 200000},
			"claude-opus-4-6[1m]": {"contextWindow": 1000000}
		}
	}`))

	require.GreaterOrEqual(t, sink.SessionInfoCount(), 1)
	info := sink.LastSessionInfo()
	usage, ok := info["context_usage"].(map[string]interface{})
	require.True(t, ok, "expected context_usage in session info")
	assert.Equal(t, int64(1000000), usage["context_window"], "top-level result should set 1M")

	prevCount := sink.SessionInfoCount()

	// A subagent result with parent_tool_use_id should NOT overwrite the
	// context window even though its modelUsage only contains haiku (200k).
	agent.HandleOutput([]byte(`{
		"type": "result",
		"parent_tool_use_id": "agent-tu-1",
		"subtype": "success",
		"modelUsage": {
			"claude-haiku-4-5-20251001": {"contextWindow": 200000}
		}
	}`))

	// Force a broadcast via a new top-level assistant + result cycle.
	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [{"type": "text", "text": "next turn"}],
			"usage": {"input_tokens": 600, "output_tokens": 250}
		}
	}`))
	agent.HandleOutput([]byte(`{
		"type": "result",
		"subtype": "success"
	}`))

	require.Greater(t, sink.SessionInfoCount(), prevCount)
	info = sink.LastSessionInfo()
	usage, ok = info["context_usage"].(map[string]interface{})
	require.True(t, ok, "expected context_usage in session info")
	assert.Equal(t, int64(1000000), usage["context_window"],
		"subagent result must not overwrite context window")
}

func TestHandleOutput_SubagentResultWithoutParentIDDoesNotOverwriteContextWindow(t *testing.T) {
	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	agent.model = "opus[1m]"

	// Top-level assistant message establishes usage baseline.
	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [{"type": "text", "text": "top level"}],
			"usage": {"input_tokens": 500, "output_tokens": 200}
		}
	}`))

	// Top-level result sets the context window to 1M.
	agent.HandleOutput([]byte(`{
		"type": "result",
		"subtype": "success",
		"modelUsage": {
			"claude-haiku-4-5-20251001": {"contextWindow": 200000},
			"claude-opus-4-6[1m]": {"contextWindow": 1000000}
		}
	}`))

	require.GreaterOrEqual(t, sink.SessionInfoCount(), 1)
	info := sink.LastSessionInfo()
	usage, ok := info["context_usage"].(map[string]interface{})
	require.True(t, ok, "expected context_usage in session info")
	assert.Equal(t, int64(1000000), usage["context_window"])

	prevCount := sink.SessionInfoCount()

	// A subagent result that is MISSING parent_tool_use_id (defense-in-depth
	// scenario). Its modelUsage only contains haiku — the primary model
	// (opus[1m]) is absent, so findPrimaryContextWindow returns 0 and the
	// context window is NOT overwritten.
	agent.HandleOutput([]byte(`{
		"type": "result",
		"subtype": "success",
		"modelUsage": {
			"claude-haiku-4-5-20251001": {"contextWindow": 200000}
		}
	}`))

	// Force a broadcast via a new assistant + result cycle.
	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [{"type": "text", "text": "next turn"}],
			"usage": {"input_tokens": 600, "output_tokens": 250}
		}
	}`))
	agent.HandleOutput([]byte(`{
		"type": "result",
		"subtype": "success"
	}`))

	require.Greater(t, sink.SessionInfoCount(), prevCount)
	info = sink.LastSessionInfo()
	usage, ok = info["context_usage"].(map[string]interface{})
	require.True(t, ok, "expected context_usage in session info")
	assert.Equal(t, int64(1000000), usage["context_window"],
		"subagent result without parent_tool_use_id must not overwrite context window")
}

func TestFindPrimaryContextWindow(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		usage    map[string]json.RawMessage
		expected int64
	}{
		{
			name:  "opus[1m] matches claude-opus-4-6[1m]",
			model: "opus[1m]",
			usage: map[string]json.RawMessage{
				"claude-haiku-4-5-20251001": json.RawMessage(`{"contextWindow": 200000}`),
				"claude-opus-4-6[1m]":       json.RawMessage(`{"contextWindow": 1000000}`),
			},
			expected: 1000000,
		},
		{
			name:  "opus matches claude-opus-4-6 not claude-opus-4-6[1m]",
			model: "opus",
			usage: map[string]json.RawMessage{
				"claude-opus-4-6":     json.RawMessage(`{"contextWindow": 200000}`),
				"claude-opus-4-6[1m]": json.RawMessage(`{"contextWindow": 1000000}`),
			},
			expected: 200000,
		},
		{
			name:  "sonnet[1m] matches claude-sonnet-4-6[1m]",
			model: "sonnet[1m]",
			usage: map[string]json.RawMessage{
				"claude-sonnet-4-6[1m]": json.RawMessage(`{"contextWindow": 1000000}`),
			},
			expected: 1000000,
		},
		{
			name:  "haiku matches claude-haiku-4-5-20251001",
			model: "haiku",
			usage: map[string]json.RawMessage{
				"claude-haiku-4-5-20251001": json.RawMessage(`{"contextWindow": 200000}`),
			},
			expected: 200000,
		},
		{
			name:  "primary model not in usage returns 0",
			model: "opus[1m]",
			usage: map[string]json.RawMessage{
				"claude-haiku-4-5-20251001": json.RawMessage(`{"contextWindow": 200000}`),
			},
			expected: 0,
		},
		{
			name:  "empty model falls back to max",
			model: "",
			usage: map[string]json.RawMessage{
				"claude-haiku-4-5-20251001": json.RawMessage(`{"contextWindow": 200000}`),
				"claude-opus-4-6[1m]":       json.RawMessage(`{"contextWindow": 1000000}`),
			},
			expected: 1000000,
		},
		{
			name:     "empty usage returns 0",
			model:    "opus[1m]",
			usage:    map[string]json.RawMessage{},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findPrimaryContextWindow(tt.usage, tt.model)
			assert.Equal(t, tt.expected, got)
		})
	}
}
