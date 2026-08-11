package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
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
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	// A message carrying parent_tool_use_id is a FORWARDED subagent envelope
	// (--forward-subagent-text) and routes into the child transcript, not the
	// parent's. The parent transcript is left untouched.
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

	// The parent transcript receives nothing.
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.OpenSpans())

	// The message routed into the child transcript keyed by the spawn span.
	child, ok := sink.ChildSink("child-of-parent-123").(*testSink)
	require.True(t, ok)
	msgs := child.Messages()
	require.Len(t, msgs, 1)
	msg := msgs[0]
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, msg.Source)
	assert.Equal(t, "parent-123", msg.ParentSpanID)
	assert.Equal(t, "tu-001", msg.SpanID)
	assert.Equal(t, "Read", msg.SpanType)

	// The child span tracker (not the parent's) opened the tool_use span.
	spans := child.OpenSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "tu-001", spans[0].SpanID)
	assert.Equal(t, "parent-123", spans[0].ParentSpanID)

	// The forwarded envelope is NOT counted against the parent's turn.
	assert.Equal(t, 0, agent.turnToolUses)
}

// A subagent's tool_use row must persist BEFORE its own span opens, exactly as
// the parent transcript does (handlePersistableMessage persists, then
// processAssistantBlocks opens). The sink derives span_lines from the spans open
// at persist time, so opening first stamps the tool_use row with an "active"
// line for the span it is itself announcing -- the row renders one column too
// deep and draws a rail segment with nothing above it. The paired tool_result
// must persist while the span IS open, so it closes the rail with connector_end.
func TestRouteSubagentMessage_ToolUsePersistsBeforeItsSpanOpens(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "spawn-1",
		"message": {
			"role": "assistant",
			"content": [
				{"type": "tool_use", "id": "tu-bash", "name": "Bash", "input": {"command": "git show HEAD --stat"}}
			]
		}
	}`))
	agent.HandleOutput([]byte(`{
		"type": "user",
		"parent_tool_use_id": "spawn-1",
		"message": {
			"role": "user",
			"content": [
				{"type": "tool_result", "tool_use_id": "tu-bash", "content": "1 file changed"}
			]
		}
	}`))

	child, ok := sink.ChildSink("child-of-spawn-1").(*testSink)
	require.True(t, ok)
	msgs := child.Messages()
	require.Len(t, msgs, 2)

	// The tool_use row: no span open yet, so the persisted row carries no span
	// lines at all.
	assert.Equal(t, "tu-bash", msgs[0].SpanID)
	assert.Empty(t, msgs[0].SpansOpenAtPersist,
		"a subagent tool_use must persist before its own span opens")

	// The tool_result row: the span it closes is open, so the row renders the
	// connector that ends the rail.
	assert.Equal(t, "tu-bash", msgs[1].SpanID)
	assert.True(t, msgs[1].Closing)
	require.Len(t, msgs[1].SpansOpenAtPersist, 1)
	assert.Equal(t, "tu-bash", msgs[1].SpansOpenAtPersist[0].SpanID)

	// The span still opens on the CHILD tracker (with the spawn span as parent)
	// and closes when the result lands.
	assert.Equal(t, []testSinkSpanOpen{{SpanID: "tu-bash", ParentSpanID: "spawn-1"}}, child.OpenSpans())
	assert.Equal(t, []string{"tu-bash"}, child.ClosedSpans())
}

// A subagent's plain assistant text carries no span of its own, so it must
// persist with the child's currently-open spans intact: an in-flight tool span
// keeps drawing its rail past the text row.
func TestRouteSubagentMessage_TextPersistsUnderAnOpenSpan(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "spawn-1",
		"message": {
			"role": "assistant",
			"content": [{"type": "tool_use", "id": "tu-1", "name": "Read", "input": {}}]
		}
	}`))
	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "spawn-1",
		"message": {
			"role": "assistant",
			"content": [{"type": "text", "text": "Reading the file."}]
		}
	}`))

	child, ok := sink.ChildSink("child-of-spawn-1").(*testSink)
	require.True(t, ok)
	msgs := child.Messages()
	require.Len(t, msgs, 2)

	assert.Empty(t, msgs[0].SpansOpenAtPersist)
	require.Len(t, msgs[1].SpansOpenAtPersist, 1)
	assert.Equal(t, "tu-1", msgs[1].SpansOpenAtPersist[0].SpanID)
}

// One forwarded assistant message can carry parallel tool calls. Every block
// must open a span on the child tracker, because the user envelope that follows
// closes every tool_result block; opening only the first leaves the others'
// rails undrawn while their results still try to close them.
func TestRouteSubagentMessage_ParallelToolUsesAllOpenAndClose(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "spawn-1",
		"message": {
			"role": "assistant",
			"content": [
				{"type": "tool_use", "id": "tu-a", "name": "Read", "input": {}},
				{"type": "tool_use", "id": "tu-b", "name": "Grep", "input": {}}
			]
		}
	}`))

	child, ok := sink.ChildSink("child-of-spawn-1").(*testSink)
	require.True(t, ok)
	assert.Equal(t, []testSinkSpanOpen{
		{SpanID: "tu-a", ParentSpanID: "spawn-1"},
		{SpanID: "tu-b", ParentSpanID: "spawn-1"},
	}, child.OpenSpans())
	// Both tool names are recorded, so each result resolves its own span type.
	assert.Equal(t, "Read", child.GetSpanType("tu-a"))
	assert.Equal(t, "Grep", child.GetSpanType("tu-b"))

	agent.HandleOutput([]byte(`{
		"type": "user",
		"parent_tool_use_id": "spawn-1",
		"message": {
			"role": "user",
			"content": [
				{"type": "tool_result", "tool_use_id": "tu-a", "content": "ok"},
				{"type": "tool_result", "tool_use_id": "tu-b", "content": "ok"}
			]
		}
	}`))
	assert.Equal(t, []string{"tu-a", "tu-b"}, child.ClosedSpans())
}

func TestHandleOutput_AssistantToolUse_FallbackParentSpanID(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	// A user envelope carrying parent_tool_use_id is the subagent's OWN
	// tool_result (forwarded), so it routes into the child transcript and
	// closes the child span there.
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

	// Parent transcript is untouched.
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.ClosedSpanCount())

	child, ok := sink.ChildSink("child-of-parent-123").(*testSink)
	require.True(t, ok)
	msgs := child.Messages()
	require.Len(t, msgs, 1)
	msg := msgs[0]
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, msg.Source)
	assert.Equal(t, "parent-123", msg.ParentSpanID)
	assert.Equal(t, "tu-001", msg.SpanID)

	// The child span closed after persist.
	closed := child.ClosedSpanCount()
	assert.Equal(t, 1, closed)
}

func TestHandleOutput_AssistantNoToolUse(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	rawEvent := `{"type":"rate_limit_event","rate_limit_info":{"rateLimitType":"five_hour","status":"rejected","resetsAt":1893456000}}`
	agent.HandleOutput([]byte(rawEvent))

	require.Equal(t, 1, sink.AutoScheduleCount())
	schedule := sink.LastAutoSchedule()
	assert.Equal(t, AutoContinueReasonRateLimit, schedule.Reason)
	assert.Equal(t, time.Unix(1893456000, 0).UTC(), schedule.DueAt)
	assert.JSONEq(t, `{"rateLimitType":"five_hour","status":"rejected","resetsAt":1893456000}`, string(schedule.SourcePayload))

	// Persists raw rate_limit_event verbatim as AGENT (no longer
	// synthesizes a stripped-down {type:"rate_limit",rate_limit_info}).
	require.Equal(t, 1, sink.NotificationCount())
	last := sink.LastNotification()
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source,
		"Claude-emitted rate_limit_event must persist as AGENT")
	assert.JSONEq(t, rawEvent, string(last.Content),
		"raw envelope must be preserved verbatim so future fields flow through")
}

// TestClaudeRateLimitEvent_BroadcastsSnakeCaseWire locks in the wire
// translation: Claude's SDK emits camelCase rate_limit_info, but the
// broadcast `rate_limits` map exposes a snake_case tier shape so all
// providers (Claude / Codex) deliver the same field names to the frontend.
func TestClaudeRateLimitEvent_BroadcastsSnakeCaseWire(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	rawEvent := `{"type":"rate_limit_event","rate_limit_info":{"rateLimitType":"five_hour","status":"rejected","resetsAt":1893456000,"utilization":1.0}}`
	agent.HandleOutput([]byte(rawEvent))

	require.Equal(t, 1, sink.SessionInfoCount())
	info := sink.LastSessionInfo()
	rateLimits, ok := info["rate_limits"].(map[string]any)
	require.True(t, ok, "broadcast must carry rate_limits in snake_case, got %#v", info)

	tier, ok := rateLimits["five_hour"].(map[string]any)
	require.True(t, ok, "tier should be keyed by rate_limit_type")
	assert.Equal(t, "five_hour", tier["rate_limit_type"])
	assert.Equal(t, "rejected", tier["status"])
	assert.Equal(t, int64(1893456000), tier["resets_at"])
	assert.Equal(t, 1.0, tier["utilization"])
}

func TestClaudeRateLimitEvent_AllowedCancelsResume(t *testing.T) {
	t.Parallel()

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

// TestClaudeRateLimitEvent_AllowedWarningCancelsResume guards the bug where an
// "allowed_warning" status -- a served, heads-up event in which the request was
// NOT blocked -- scheduled a spurious auto-continue. Only a hard "rejected"
// status is an actual block; a warning must cancel any pending resume, exactly
// like "allowed".
func TestClaudeRateLimitEvent_AllowedWarningCancelsResume(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	agent.HandleOutput([]byte(`{
		"type":"rate_limit_event",
		"rate_limit_info":{
			"rateLimitType":"seven_day",
			"status":"allowed_warning",
			"resetsAt":1893456000,
			"utilization":0.75
		}
	}`))

	assert.Equal(t, 0, sink.AutoScheduleCount(), "an allowed_warning event must not schedule an auto-continue")
	require.Equal(t, 1, sink.AutoCancelCount())
	assert.Equal(t, AutoContinueReasonRateLimit, sink.LastAutoCancel())
}

// TestClaudeRateLimitEvent_OverageAbsorbsRejected verifies the overage carve-out:
// while on overage, a "rejected" base window is absorbed by the overage
// allowance (overageStatus still served), so there is no block to wait out and
// any pending resume is cancelled.
func TestClaudeRateLimitEvent_OverageAbsorbsRejected(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	agent.HandleOutput([]byte(`{
		"type":"rate_limit_event",
		"rate_limit_info":{
			"rateLimitType":"seven_day",
			"status":"rejected",
			"resetsAt":1893456000,
			"isUsingOverage":true,
			"overageStatus":"allowed",
			"overageResetsAt":1893460000
		}
	}`))

	assert.Equal(t, 0, sink.AutoScheduleCount(), "overage absorbs the rejected base window; no resume")
	require.Equal(t, 1, sink.AutoCancelCount())
}

// TestClaudeRateLimitEvent_OverageRejectedSchedulesAtOverageReset verifies that a
// hard block while on overage (the overage itself is "rejected") schedules a
// resume at overageResetsAt -- not the base resetsAt.
func TestClaudeRateLimitEvent_OverageRejectedSchedulesAtOverageReset(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	agent.HandleOutput([]byte(`{
		"type":"rate_limit_event",
		"rate_limit_info":{
			"rateLimitType":"seven_day",
			"status":"rejected",
			"resetsAt":1893456000,
			"isUsingOverage":true,
			"overageStatus":"rejected",
			"overageResetsAt":1893460000
		}
	}`))

	require.Equal(t, 1, sink.AutoScheduleCount())
	assert.Equal(t, time.Unix(1893460000, 0).UTC(), sink.LastAutoSchedule().DueAt)
}

// TestClaudeRateLimitEvent_RejectedWithoutResetLeavesScheduleIntact verifies that
// a "rejected" event carrying no resetsAt is a block we can't date: it neither
// schedules (no time to wait until) nor cancels (must not drop a legitimate
// pending resume from a prior well-formed event).
func TestClaudeRateLimitEvent_RejectedWithoutResetLeavesScheduleIntact(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	agent.HandleOutput([]byte(`{
		"type":"rate_limit_event",
		"rate_limit_info":{"rateLimitType":"seven_day","status":"rejected"}
	}`))

	assert.Equal(t, 0, sink.AutoScheduleCount())
	assert.Equal(t, 0, sink.AutoCancelCount())
}

func TestClaudeResult_APIErrorUsesAPIErrorReason(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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

func TestClaudeResult_BareOverloadedSchedulesAPIErrorAutoContinue(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	// Claude Code emits overloads (Anthropic HTTP 529) both as
	// "API Error: 529 Overloaded" and, as seen in the wild, the bare
	// "API Error: Overloaded" with no numeric code. The bare form must still
	// auto-continue even though the 5xx code matcher cannot see a code.
	payload := []byte(`{
		"type":"result",
		"is_error":true,
		"result":"API Error: Overloaded"
	}`)

	agent.HandleOutput(payload)

	require.Equal(t, 1, sink.AutoScheduleCount())
	schedule := sink.LastAutoSchedule()
	assert.Equal(t, AutoContinueReasonAPIError, schedule.Reason)
	var source struct {
		Type    string `json:"type"`
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
	}
	require.NoError(t, json.Unmarshal(schedule.SourcePayload, &source))
	assert.Equal(t, "result", source.Type)
	assert.True(t, source.IsError)
	assert.Equal(t, "API Error: Overloaded", source.Result)
}

func TestHandleOutput_MalformedJSON(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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

func TestHandleOutput_ThinkingTokensBroadcastNotPersisted(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	// A `system`/`thinking_tokens` line is live telemetry: it must be
	// broadcast over the ephemeral agent_session_info channel and never
	// persisted to the timeline. Its per-delta session_id must NOT re-fire
	// session-init side effects.
	agent.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "thinking_tokens",
		"estimated_tokens": 230,
		"estimated_tokens_delta": 163,
		"session_id": "a40e65f9-f1f2-4e8b-b089-abc9692345b2"
	}`))

	assert.Equal(t, 0, sink.MessageCount(), "thinking_tokens must not be persisted")
	assert.Equal(t, 0, sink.NotificationCount(), "thinking_tokens must not be notification-threaded")
	assert.Equal(t, 0, sink.SessionIDCount(), "thinking_tokens must not re-fire session init")
	assert.Empty(t, sink.statusActives, "thinking_tokens must not re-broadcast status active")

	require.Equal(t, 1, sink.SessionInfoCount(), "thinking_tokens must broadcast session info")
	assert.Equal(t, int64(230), sink.LastSessionInfo()["thinking_tokens"])
}

func TestHandleOutput_ThinkingTokensZeroEstimateStillSwallowed(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	// A thinking_tokens line with no estimated_tokens yet (the first delta of a
	// turn can report 0) must still be intercepted: it is broadcast-only and
	// must never reach the timeline, even though the count is zero. The
	// frontend's own `> 0` gate decides whether to render it.
	agent.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "thinking_tokens",
		"session_id": "a40e65f9-f1f2-4e8b-b089-abc9692345b2"
	}`))

	assert.Equal(t, 0, sink.MessageCount(), "zero-estimate thinking_tokens must not be persisted")
	assert.Equal(t, 0, sink.SessionIDCount(), "zero-estimate thinking_tokens must not re-fire session init")
	require.Equal(t, 1, sink.SessionInfoCount(), "zero-estimate thinking_tokens must still broadcast")
	assert.Equal(t, int64(0), sink.LastSessionInfo()["thinking_tokens"])
}

func TestHandleOutput_ThinkingTokensFractionalEstimateStillSwallowed(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	// A thinking_tokens line whose estimated_tokens arrives in a fractional or
	// exponent form must still be intercepted. estimated_tokens is decoded as
	// float64, so `230.0`/`1.5e4` parse cleanly; an int64 field would make the
	// unmarshal error and let the line fall through to persistence -- the exact
	// timeline bloat the interception prevents. The count is truncated to int64
	// for the broadcast.
	agent.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "thinking_tokens",
		"estimated_tokens": 15000.0,
		"session_id": "a40e65f9-f1f2-4e8b-b089-abc9692345b2"
	}`))

	assert.Equal(t, 0, sink.MessageCount(), "fractional thinking_tokens must not be persisted")
	assert.Equal(t, 0, sink.SessionIDCount(), "fractional thinking_tokens must not re-fire session init")
	require.Equal(t, 1, sink.SessionInfoCount(), "fractional thinking_tokens must still broadcast")
	assert.Equal(t, int64(15000), sink.LastSessionInfo()["thinking_tokens"])
}

func TestHandleOutput_ThinkingTokensMalformedEstimateStillSwallowed(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	// A thinking_tokens line whose estimated_tokens arrives in an unexpected
	// wire form -- here a JSON string instead of a number -- must STILL be
	// intercepted. estimated_tokens is captured as RawMessage, so the subtype
	// match does not depend on the count parsing; the count is parsed leniently
	// and a failure broadcasts 0. A typed-number field would error on the
	// outer unmarshal, return false, and let the line fall through to
	// session-init + persistence -- the exact timeline bloat this prevents.
	agent.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "thinking_tokens",
		"estimated_tokens": "230",
		"session_id": "a40e65f9-f1f2-4e8b-b089-abc9692345b2"
	}`))

	assert.Equal(t, 0, sink.MessageCount(), "malformed thinking_tokens must not be persisted")
	assert.Equal(t, 0, sink.SessionIDCount(), "malformed thinking_tokens must not re-fire session init")
	require.Equal(t, 1, sink.SessionInfoCount(), "malformed thinking_tokens must still broadcast")
	assert.Equal(t, int64(0), sink.LastSessionInfo()["thinking_tokens"], "an unparseable count broadcasts 0")
}

func TestHandleOutput_ThinkingTokensOverflowEstimateStillSwallowed(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	// An estimated_tokens that overflows float64 (1e400) makes the lenient
	// inner parse error; it must still be swallowed (broadcast 0, not
	// persisted) and must not produce a NaN/Inf -> int64 conversion.
	agent.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "thinking_tokens",
		"estimated_tokens": 1e400,
		"session_id": "a40e65f9-f1f2-4e8b-b089-abc9692345b2"
	}`))

	assert.Equal(t, 0, sink.MessageCount(), "overflowing thinking_tokens must not be persisted")
	assert.Equal(t, 0, sink.SessionIDCount(), "overflowing thinking_tokens must not re-fire session init")
	require.Equal(t, 1, sink.SessionInfoCount(), "overflowing thinking_tokens must still broadcast")
	assert.Equal(t, int64(0), sink.LastSessionInfo()["thinking_tokens"], "an out-of-range count broadcasts 0")
}

func TestHandleOutput_ThinkingTokensNegativeEstimateClampedToZero(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	// A running token estimate is non-negative by definition. A negative wire
	// value must be clamped to 0 at the source (still swallowed, never
	// persisted) so no consumer ever sees a negative count.
	agent.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "thinking_tokens",
		"estimated_tokens": -42.9,
		"session_id": "a40e65f9-f1f2-4e8b-b089-abc9692345b2"
	}`))

	assert.Equal(t, 0, sink.MessageCount(), "negative thinking_tokens must not be persisted")
	assert.Equal(t, 0, sink.SessionIDCount(), "negative thinking_tokens must not re-fire session init")
	require.Equal(t, 1, sink.SessionInfoCount(), "negative thinking_tokens must still broadcast")
	assert.Equal(t, int64(0), sink.LastSessionInfo()["thinking_tokens"], "a negative count clamps to 0")
}

func TestHandleOutput_ThinkingTokensFiniteHugeEstimateClampedToZero(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	// A finite but absurd estimate (1e300) parses cleanly into float64 -- unlike
	// 1e400, it does NOT overflow the inner unmarshal -- yet it is far above
	// math.MaxInt64, so int64(1e300) would saturate to a garbage ~9.2-quintillion
	// count. It must be treated as out of range like 1e400 and broadcast 0, not a
	// nonsense count, so the two huge-input paths agree.
	agent.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "thinking_tokens",
		"estimated_tokens": 1e300,
		"session_id": "a40e65f9-f1f2-4e8b-b089-abc9692345b2"
	}`))

	assert.Equal(t, 0, sink.MessageCount(), "finite-huge thinking_tokens must not be persisted")
	require.Equal(t, 1, sink.SessionInfoCount(), "finite-huge thinking_tokens must still broadcast")
	assert.Equal(t, int64(0), sink.LastSessionInfo()["thinking_tokens"], "a finite-huge count broadcasts 0")
}

func TestParseThinkingTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		content      string
		wantEstimate int64
		wantOK       bool
	}{
		{"plain integer", `{"subtype":"thinking_tokens","estimated_tokens":230}`, 230, true},
		{"fractional", `{"subtype":"thinking_tokens","estimated_tokens":15000.0}`, 15000, true},
		{"exponent", `{"subtype":"thinking_tokens","estimated_tokens":1.5e4}`, 15000, true},
		{"truncates toward zero", `{"subtype":"thinking_tokens","estimated_tokens":230.9}`, 230, true},
		{"absent count broadcasts 0", `{"subtype":"thinking_tokens"}`, 0, true},
		{"string count broadcasts 0", `{"subtype":"thinking_tokens","estimated_tokens":"230"}`, 0, true},
		{"null count broadcasts 0", `{"subtype":"thinking_tokens","estimated_tokens":null}`, 0, true},
		{"float64-overflow (1e400) broadcasts 0", `{"subtype":"thinking_tokens","estimated_tokens":1e400}`, 0, true},
		{"finite-huge (1e300) broadcasts 0", `{"subtype":"thinking_tokens","estimated_tokens":1e300}`, 0, true},
		{"negative clamps to 0", `{"subtype":"thinking_tokens","estimated_tokens":-42.9}`, 0, true},
		{"non-thinking subtype is not a match", `{"subtype":"init","estimated_tokens":230}`, 0, false},
		{"missing subtype is not a match", `{"estimated_tokens":230}`, 0, false},
		{"invalid JSON is not a match", `{"subtype":`, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			estimate, ok := parseThinkingTokens([]byte(tt.content))
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantEstimate, estimate)
		})
	}
}

func TestHandleOutput_NonThinkingSystemMessageStillPersists(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	// A plain `system` message that is neither a thinking_tokens line nor a
	// notification-threaded subtype falls through to persistence unchanged —
	// the thinking_tokens interception must not swallow other system lines.
	agent.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "init",
		"session_id": "a40e65f9-f1f2-4e8b-b089-abc9692345b2"
	}`))

	assert.Equal(t, 1, sink.MessageCount(), "non-thinking system message must persist")
	assert.Equal(t, 0, sink.SessionInfoCount(), "non-thinking system message must not broadcast thinking session info")
	assert.Equal(t, 1, sink.SessionIDCount(), "init message should update session id")
}

func TestIsRetryableClaudeResultError(t *testing.T) {
	t.Parallel()

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
		{"5xx lowercase prefix", "api error: 500 internal server error", true},
		{"idle timeout exact", "API Error: Stream idle timeout", true},
		{"idle timeout partial response", "API Error: Stream idle timeout - partial response received", true},
		{"idle timeout alternate punctuation", "API Error - Stream idle timeout - partial response received", true},
		{"idle timeout repeated punctuation", "API Error:: Stream idle timeout", true},
		{"idle timeout lowercase", "api error: stream idle timeout", true},
		{"overloaded bare", "API Error: Overloaded", true},
		{"overloaded alternate punctuation", "API Error - Overloaded", true},
		{"overloaded repeated punctuation", "API Error:: Overloaded", true},
		{"overloaded trailing text", "API Error: Overloaded - please retry", true},
		{"overloaded lowercase", "API Error: overloaded", true},
		{"overloaded uppercase", "API ERROR: OVERLOADED", true},
		{"non-retryable 4xx", "API Error: 400 Bad Request", false},
		{"5xx single digit", "API Error: 5", false},
		{"5xx two digits", "API Error: 50", false},
		{"5xx four digits", "API Error: 5000", false},
		{"5xx alphanumeric separator", "API ErrorX 500 Internal Server Error", false},
		{"5xx alphanumeric suffix", "API Error: 500X", false},
		{"idle timeout alphanumeric separator", "API ErrorX Stream idle timeout", false},
		{"idle timeout alphanumeric suffix", "API Error: Stream idle timeoutX", false},
		{"overloaded alphanumeric separator", "API ErrorX Overloaded", false},
		{"overloaded alphanumeric suffix", "API Error: OverloadedX", false},
		{"overloaded without prefix", "Server Overloaded", false},
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
	t.Parallel()

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
	t.Parallel()

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

func TestHandleOutput_ResultModelUsageLegacyOpusResolvesTo1M(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	// A legacy bare "opus" now collapses to "opus[1m]" (Opus is 1M-only), so it must
	// resolve to the 1M window. Even if modelUsage carries BOTH a standard-context
	// "claude-opus-4-6" and a 1M "claude-opus-4-6[1m]" key -- a shape the current CLI
	// does not emit -- both normalize to "opus[1m]", and findPrimaryContextWindow's
	// max-among-matches tie-break deterministically picks the 1M window regardless of
	// map iteration order.
	agent.model = "opus"

	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [{"type": "text", "text": "hello"}],
			"usage": {"input_tokens": 100, "output_tokens": 50}
		}
	}`))

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
	assert.Equal(t, int64(1000000), usage["context_window"], "legacy opus resolves to the 1M window")
}

func TestHandleOutput_SubagentResultDoesNotOverwriteContextWindow(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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

// TestGetOrCreateUsageSnapshot_SeedsFromDynamicCatalog verifies the context-usage
// snapshot seeds its window from the per-agent dynamic catalog (effortCatalog),
// not the static catalog alone. A model discovered only from the live CLI -- the
// whole point of dynamic discovery -- is absent from claudeCodeAvailableModels, so
// a static-only seed would report no window until the first result message; the
// dynamic seed reports the [1m]-inferred window immediately.
func TestGetOrCreateUsageSnapshot_SeedsFromDynamicCatalog(t *testing.T) {
	t.Parallel()

	// Precondition: the model is unknown to the static catalog, so a static seed
	// would be 0.
	require.Equal(t, int64(0), modelContextWindow(claudeCodeAvailableModels, "mythos"),
		"precondition: mythos is not in the static catalog")

	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	agent.model = "mythos"
	agent.availableModels = convertClaudeModels([]claudeCodeModelInfo{
		{Value: "mythos[1m]", DisplayName: "Mythos (1M)", SupportsEffort: true, SupportedEffortLevels: []string{"high", "xhigh"}},
		{Value: "mythos", DisplayName: "Mythos", SupportsEffort: true, SupportedEffortLevels: []string{"high", "xhigh"}},
	}, nil)

	// An assistant usage message seeds the snapshot (getOrCreateUsageSnapshot); the
	// result message (no modelUsage) broadcasts the seeded window without overwriting it.
	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [{"type": "text", "text": "hi"}],
			"usage": {"input_tokens": 100, "output_tokens": 50}
		}
	}`))
	agent.HandleOutput([]byte(`{"type": "result", "subtype": "success"}`))

	require.GreaterOrEqual(t, sink.SessionInfoCount(), 1)
	usage, ok := sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	require.True(t, ok, "expected context_usage in session info")
	assert.Equal(t, int64(200_000), usage["context_window"],
		"window seeded from the dynamic catalog entry (mythos, no [1m] suffix -> 200K)")
}

// TestExtractAndBroadcastUsage_WindowFallsBackToStaticCatalog verifies that a model the
// live CLI dropped from its dynamic list -- but that the session is still running and
// the static catalog still knows -- reports its real context window via the
// effortResolver's static fallback, the same per-entry fallback effort/ultracode
// resolution uses. Before the fix the window resolved over the dynamic catalog alone (a
// whole-list swap with no per-entry fallback), so such a model reported "unknown" until
// a result message supplied a window -- diverging from how its effort still resolved.
func TestExtractAndBroadcastUsage_WindowFallsBackToStaticCatalog(t *testing.T) {
	t.Parallel()

	// Precondition: opus[1m] is a static-catalog model with a 1M window.
	require.Equal(t, int64(1_000_000), modelContextWindow(claudeCodeAvailableModels, "opus[1m]"),
		"precondition: opus[1m] carries a 1M window in the static catalog")

	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	agent.model = "opus[1m]"
	// The live CLI reported a dynamic list that does NOT include opus[1m] (e.g. it
	// dropped the model the resumed session is still running). effortCatalog returns
	// this list verbatim, so a dynamic-only window lookup would miss.
	agent.availableModels = convertClaudeModels([]claudeCodeModelInfo{
		{Value: "sonnet", DisplayName: "Sonnet", SupportsEffort: true, SupportedEffortLevels: []string{"high"}},
	}, nil)
	require.Nil(t, FindAvailableModel(agent.availableModels, "opus[1m]"),
		"precondition: opus[1m] is absent from the dynamic catalog")

	agent.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50}}}`))
	agent.HandleOutput([]byte(`{"type":"result","subtype":"success"}`)) // no modelUsage -> window from the catalog seed

	require.GreaterOrEqual(t, sink.SessionInfoCount(), 1)
	usage, ok := sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	require.True(t, ok, "expected context_usage in session info")
	assert.Equal(t, int64(1_000_000), usage["context_window"],
		"window resolved from the static fallback (opus[1m] = 1M), not reported unknown")
}

// TestGetOrCreateUsageSnapshot_SentinelWindowIsUnknown verifies a session stuck on the
// unresolved account-default sentinel ("default") reports NO context window. The
// sentinel catalog entry is a placeholder with no concrete window, and we deliberately
// do not fabricate one: the broadcast omits context_window so the indicator shows
// "unknown" (matching the frontend) until the sentinel resolves to a concrete model or
// a result message supplies the real window.
func TestGetOrCreateUsageSnapshot_SentinelWindowIsUnknown(t *testing.T) {
	t.Parallel()

	// Precondition: the sentinel entry carries no context window.
	require.Equal(t, int64(0), modelContextWindow(claudeCodeAvailableModels, DefaultModelSentinel),
		"precondition: the sentinel has no concrete window")

	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	agent.model = DefaultModelSentinel // stuck: the CLI never echoed a concrete applied.model

	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [{"type": "text", "text": "hi"}],
			"usage": {"input_tokens": 100, "output_tokens": 50}
		}
	}`))
	agent.HandleOutput([]byte(`{"type": "result", "subtype": "success"}`))

	require.GreaterOrEqual(t, sink.SessionInfoCount(), 1)
	usage, ok := sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	require.True(t, ok, "expected context_usage in session info")
	_, hasWindow := usage["context_window"]
	assert.False(t, hasWindow,
		"unresolved sentinel reports no context window (unknown), not a fabricated value")
}

// TestExtractAndBroadcastUsage_ReseedsWindowOnModelChange covers the usage snapshot
// outliving a model change: a session that starts on the unresolved account-default
// sentinel reports no window (unknown), and once the sentinel resolves to a concrete
// 1M-context model the window is re-seeded from the catalog -- not left unknown until a
// result message with matching modelUsage happens to refresh it.
func TestExtractAndBroadcastUsage_ReseedsWindowOnModelChange(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	agent.model = DefaultModelSentinel // unresolved at the first turn -> window unknown

	assistant := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50}}}`
	result := `{"type":"result","subtype":"success"}` // no modelUsage -> window comes from the catalog re-seed

	agent.HandleOutput([]byte(assistant))
	agent.HandleOutput([]byte(result))
	usage, ok := sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	require.True(t, ok)
	_, hasWindow := usage["context_window"]
	require.False(t, hasWindow, "unresolved sentinel reports no window")

	// The sentinel resolves to a 1M-context model (refreshSettingsFromAgent stored it).
	agent.model = "opus[1m]"
	agent.HandleOutput([]byte(assistant))
	agent.HandleOutput([]byte(result))
	usage, ok = sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, int64(1_000_000), usage["context_window"],
		"window re-seeded from the catalog when the model resolves off the sentinel")
}

// TestExtractAndBroadcastUsage_ReseedsDownwardOnModelDowngrade locks the re-seed's
// DOWNGRADE direction. The re-seed comment claims it rescues a session that began on
// "a smaller-window model", but the only other re-seed test goes the other way
// (200K sentinel -> 1M). The more dangerous direction is a session on a 1M model that
// live-switches to a 200K model: if the re-seed regressed, the indicator would keep
// over-reporting 1M (showing far more headroom than real) until a result message with
// matching modelUsage happened to correct it.
func TestExtractAndBroadcastUsage_ReseedsDownwardOnModelDowngrade(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	agent.model = "opus[1m]" // known 1M model via the static catalog fallback

	assistant := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50}}}`
	result := `{"type":"result","subtype":"success"}` // no modelUsage -> window comes from the catalog re-seed

	agent.HandleOutput([]byte(assistant))
	agent.HandleOutput([]byte(result))
	usage, ok := sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, int64(1_000_000), usage["context_window"], "opus[1m] seeds the 1M window")

	// Live-switch to Sonnet (200K): the window must re-seed DOWN, not stay at 1M.
	agent.model = "sonnet"
	agent.HandleOutput([]byte(assistant))
	agent.HandleOutput([]byte(result))
	usage, ok = sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, int64(200_000), usage["context_window"],
		"window re-seeded down to 200K on a 1M->Sonnet downgrade")
}

// TestExtractAndBroadcastUsage_ClearsWindowOnSwitchToUnknownModel verifies that
// switching to a model unknown to BOTH catalogs (e.g. a resumed session running a model
// the live CLI dropped into unavailable_models, so it is in neither the dynamic list
// nor the static fallback) CLEARS the window to "unknown" rather than continuing to
// over-report the previous model's larger window. modelContextWindow returns 0 for such
// a model, and the re-seed fires on the model change even though the new window is 0, so
// the broadcast omits context_window. A result message's modelUsage supplies the real
// window once one arrives.
func TestExtractAndBroadcastUsage_ClearsWindowOnSwitchToUnknownModel(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	agent.model = "opus[1m]" // known 1M model via the static catalog fallback

	assistant := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50}}}`
	result := `{"type":"result","subtype":"success"}` // no modelUsage -> window comes from the catalog re-seed

	agent.HandleOutput([]byte(assistant))
	agent.HandleOutput([]byte(result))
	usage, ok := sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, int64(1_000_000), usage["context_window"], "opus[1m] seeds the 1M window")

	// Precondition: the switched-to model is in neither catalog.
	require.Equal(t, int64(0), modelContextWindow(agent.effortCatalog(), "ghost-model"),
		"precondition: ghost-model is unknown to both catalogs")

	// Switch to a model neither catalog knows (filtered/unavailable but still running).
	agent.model = "ghost-model"
	agent.HandleOutput([]byte(assistant))
	agent.HandleOutput([]byte(result))
	usage, ok = sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	require.True(t, ok)
	_, hasWindow := usage["context_window"]
	assert.False(t, hasWindow,
		"switching to a model unknown to both catalogs clears the stale 1M window to unknown")
}

// TestExtractAndBroadcastUsage_ResultWindowSurvivesReseed verifies the authoritative
// window from a result message's modelUsage is NOT clobbered by the catalog re-seed on a
// later turn for the SAME model. The re-seed runs every turn now (it must, to clear a
// stale window when the model switches to an unknown one), so the windowModel guard is
// the only thing protecting a CLI-reported window from being overwritten by the coarser
// catalog estimate.
func TestExtractAndBroadcastUsage_ResultWindowSurvivesReseed(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	agent.model = "opus[1m]" // catalog estimate for this id is 1M

	assistant := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50}}}`
	// A result whose modelUsage reports a CLI-adjusted window (500K) that differs from
	// the catalog's 1M estimate for the same model.
	resultWithUsage := `{"type":"result","subtype":"success","modelUsage":{"claude-opus-4-6[1m]":{"contextWindow":500000}}}`
	resultNoUsage := `{"type":"result","subtype":"success"}`

	agent.HandleOutput([]byte(assistant))
	agent.HandleOutput([]byte(resultWithUsage))
	usage, ok := sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, int64(500000), usage["context_window"],
		"result modelUsage is authoritative (500K, not the 1M catalog estimate)")

	// A later turn on the SAME model: the always-running catalog re-seed must not
	// overwrite the authoritative 500K with the 1M estimate.
	agent.HandleOutput([]byte(assistant))
	agent.HandleOutput([]byte(resultNoUsage))
	usage, ok = sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, int64(500000), usage["context_window"],
		"catalog re-seed must not clobber the authoritative window for the same model")
}

func TestFindPrimaryContextWindow(t *testing.T) {
	t.Parallel()

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
			// Opus is 1M-only: a legacy bare "opus" collapses to "opus[1m]", so it
			// matches BOTH a standard "claude-opus-4-6" and a 1M "claude-opus-4-6[1m]"
			// key. The max-among-matches tie-break returns the 1M window deterministically
			// (map iteration order must not change the result). The current CLI does not
			// emit both keys; this guards the collision path regardless.
			name:  "legacy opus picks the max window across colliding keys",
			model: "opus",
			usage: map[string]json.RawMessage{
				"claude-opus-4-6":     json.RawMessage(`{"contextWindow": 200000}`),
				"claude-opus-4-6[1m]": json.RawMessage(`{"contextWindow": 1000000}`),
			},
			expected: 1000000,
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
			// S6: the normalized-equality match rejects an unrelated family whose API id
			// merely contains "opus" as a substring (e.g. a hypothetical "opusplus"),
			// where the old substring scan would have false-matched it.
			name:  "opus does not match an unrelated opusplus family",
			model: "opus",
			usage: map[string]json.RawMessage{
				"claude-opusplus-1": json.RawMessage(`{"contextWindow": 500000}`),
				"claude-opus-4-8":   json.RawMessage(`{"contextWindow": 200000}`),
			},
			expected: 200000,
		},
		{
			// S6: normalization lowercases, so a "[1M]" spelling matches "opus[1m]" --
			// the old case-sensitive suffix Contains would have missed it (returned 0).
			name:  "uppercase [1M] suffix still matches opus[1m]",
			model: "opus[1m]",
			usage: map[string]json.RawMessage{
				"claude-opus-4-8[1M]": json.RawMessage(`{"contextWindow": 1000000}`),
			},
			expected: 1000000,
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

// TestContextUsageSnapshot_BuildBroadcast exercises the debounce and window-omission
// rules of buildBroadcast directly. The integration tests (HandleOutput) cover the
// result-message path, but the 10s debounce for non-result messages and the
// LastBroadcast stamping were previously only reachable through a real clock; passing
// `now` in makes them deterministic.
func TestContextUsageSnapshot_BuildBroadcast(t *testing.T) {
	t.Parallel()

	base := time.Unix(1_700_000_000, 0).UTC()

	t.Run("no usage yields no broadcast", func(t *testing.T) {
		s := &contextUsageSnapshot{ContextWindow: 200_000}
		m, ok := s.buildBroadcast(claudeMsgTypeAssistant, base)
		assert.False(t, ok)
		assert.Nil(t, m)
		assert.True(t, s.LastBroadcast.IsZero(), "a suppressed broadcast must not stamp LastBroadcast")
	})

	t.Run("result with usage broadcasts and includes a known window", func(t *testing.T) {
		s := &contextUsageSnapshot{InputTokens: 10, OutputTokens: 5, CacheReadInputTokens: 3, ContextWindow: 200_000}
		m, ok := s.buildBroadcast(claudeMsgTypeResult, base)
		require.True(t, ok)
		assert.Equal(t, int64(10), m["input_tokens"])
		assert.Equal(t, int64(5), m["output_tokens"])
		assert.Equal(t, int64(3), m["cache_read_input_tokens"])
		assert.Equal(t, int64(200_000), m["context_window"])
		assert.Equal(t, base, s.LastBroadcast, "broadcasting stamps LastBroadcast")
	})

	t.Run("unknown window is omitted", func(t *testing.T) {
		s := &contextUsageSnapshot{InputTokens: 1} // ContextWindow == 0
		m, ok := s.buildBroadcast(claudeMsgTypeResult, base)
		require.True(t, ok)
		_, has := m["context_window"]
		assert.False(t, has, "ContextWindow==0 must omit context_window so the indicator shows unknown")
	})

	t.Run("non-result is debounced within the 10s window", func(t *testing.T) {
		s := &contextUsageSnapshot{InputTokens: 1, LastBroadcast: base}
		m, ok := s.buildBroadcast(claudeMsgTypeAssistant, base.Add(9*time.Second))
		assert.False(t, ok, "9s < 10s debounce: no broadcast")
		assert.Nil(t, m)
		assert.Equal(t, base, s.LastBroadcast, "a suppressed broadcast must not move LastBroadcast")
	})

	t.Run("non-result broadcasts once the 10s window elapses", func(t *testing.T) {
		s := &contextUsageSnapshot{InputTokens: 1, LastBroadcast: base}
		at := base.Add(10 * time.Second)
		_, ok := s.buildBroadcast(claudeMsgTypeAssistant, at)
		assert.True(t, ok, ">=10s elapsed: broadcast")
		assert.Equal(t, at, s.LastBroadcast)
	})

	t.Run("result bypasses the debounce window", func(t *testing.T) {
		s := &contextUsageSnapshot{InputTokens: 1, LastBroadcast: base}
		_, ok := s.buildBroadcast(claudeMsgTypeResult, base.Add(time.Second))
		assert.True(t, ok, "a result message always broadcasts, even mid-debounce")
	})
}

// TestHandleOutput_SystemSessionStateChangedConsumed verifies a Claude
// session_state_changed system line is consumed silently (not persisted into
// the transcript and not driving the registry).
func TestHandleOutput_SystemSessionStateChangedConsumed(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	content := []byte(`{"type":"system","subtype":"session_state_changed","session_id":"s1","cwd":"/tmp"}`)
	agent.HandleOutput(content)

	assert.Equal(t, 0, sink.MessageCount(), "session_state_changed must be consumed, not persisted")
	assert.Empty(t, sink.BackgroundTasks(), "session_state_changed must not create a registry row")
}

// TestClaudeHandleTaskStarted_FallsBackToPromptFirstLine verifies that when
// task_started omits description but carries a spawn prompt, the registry row
// title falls back to the first line of the prompt (probe-verified shape).
func TestClaudeHandleTaskStarted_FallsBackToPromptFirstLine(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	content := []byte(`{"type":"system","subtype":"task_started","task_id":"t1","tool_use_id":"tu1","task_type":"local_bash","prompt":"build the feature\nand ship it"}`)
	agent.HandleOutput(content)

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "build the feature", tasks[0].Title,
		"empty description falls back to firstLine(prompt)")
}

// TestClaudeHandleTaskStarted_PrefersDescriptionOverPrompt verifies the
// description wins when both description and prompt are present.
func TestClaudeHandleTaskStarted_PrefersDescriptionOverPrompt(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	content := []byte(`{"type":"system","subtype":"task_started","task_id":"t1","tool_use_id":"tu1","task_type":"local_bash","description":"the real title","prompt":"something else\nmultiline"}`)
	agent.HandleOutput(content)

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "the real title", tasks[0].Title,
		"description must win over prompt when both are present")
}

// TestClaudeHandleTaskProgress_PrefersDescriptionOverLastToolName verifies the
// activity line derives from description first (probe-verified order), then
// last_tool_name, then usage counts.
func TestClaudeHandleTaskProgress_PrefersDescriptionOverLastToolName(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	// Seed a running row.
	started := []byte(`{"type":"system","subtype":"task_started","task_id":"t1","tool_use_id":"tu1","task_type":"local_bash","description":"original"}`)
	agent.HandleOutput(started)

	// Progress with a description + last_tool_name: description wins.
	progress := []byte(`{"type":"system","subtype":"task_progress","task_id":"t1","description":"installing deps","last_tool_name":"Bash"}`)
	agent.HandleOutput(progress)

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "installing deps", tasks[0].ActiveForm,
		"progress activity prefers description over last_tool_name")
}

// TestHandleOutput_TaskStartedUpsertsSubagentRegistryAndChild verifies that a
// task_started for a Task subagent (local_agent with a tool_use_id) upserts a
// Running registry row keyed by task_id AND pre-creates the child transcript
// via EnsureChildAgent.
func TestHandleOutput_TaskStartedUpsertsSubagentRegistryAndChild(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	content := []byte(`{"type":"system","subtype":"task_started","task_id":"task-sub-1","tool_use_id":"tu-spawn-1","task_type":"local_agent","description":"research the codebase"}`)
	agent.HandleOutput(content)

	// One registry row, keyed by task_id, Running, titled by the description.
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	row := tasks[0]
	assert.Equal(t, "task-sub-1", row.RowKey)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "research the codebase", row.Title)
	assert.Equal(t, bgtask.KindSubagent, row.Kind, "local_agent maps to a Subagent-kind row")

	// EnsureChildAgent was invoked keyed by the spawn tool_use id, so the child
	// transcript resolves immediately. Resolve the child sink via ChildSink using
	// the synthetic id EnsureChildAgent produced (child-of-<spawnSpanID>).
	child, ok := sink.ChildSink("child-of-tu-spawn-1").(*testSink)
	require.True(t, ok, "EnsureChildAgent must have created a child keyed by the spawn span")
	require.NotNil(t, child)
	// The child registry row also carries the child agent id and title.
	assert.Equal(t, "child-of-tu-spawn-1", row.ChildAgentID)
}

// TestHandleOutput_TaskStartedLocalBashUpsertsShellNoChild verifies that a
// task_started for a background shell (local_bash) upserts a Shell-kind registry
// row and does NOT pre-create a child transcript (shells have no transcript).
func TestHandleOutput_TaskStartedLocalBashUpsertsShellNoChild(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	content := []byte(`{"type":"system","subtype":"task_started","task_id":"task-shell-1","tool_use_id":"tu-shell-1","task_type":"local_bash","description":"long build"}`)
	agent.HandleOutput(content)

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	row := tasks[0]
	assert.Equal(t, "task-shell-1", row.RowKey)
	assert.Equal(t, bgtask.KindShell, row.Kind, "local_bash maps to a Shell-kind row")
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "long build", row.Title)
	// Claude names a background shell by its COMMAND (verified against 2.1.220:
	// task_started ships description="sleep 2 && echo BG-MARKER"), so the command
	// is already the title. Copying it into Description too rendered the row's
	// secondary line as a verbatim echo of its own title.
	assert.Empty(t, row.Description, "description must not echo the title")
	// No child transcript pre-created for a shell.
	assert.Equal(t, "", row.ChildAgentID, "local_bash must not EnsureChildAgent")
}

// The same rule for a subagent row: its secondary line comes from
// task_progress activity, so task_started must not pre-fill it with the title.
func TestHandleOutput_TaskStartedLeavesTheDescriptionEmpty(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)
	agent.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-1","tool_use_id":"tu-1","task_type":"local_agent","description":"SCAN triage angle"}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "SCAN triage angle", tasks[0].Title)
	assert.Empty(t, tasks[0].Description)
}

// TestHandleOutput_TaskStartedCarriesWorkflowGroup verifies that a task_started
// carrying a workflow_name populates the registry row's GroupKey/GroupLabel so
// the sidebar groups workflow rows together.
func TestHandleOutput_TaskStartedCarriesWorkflowGroup(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	content := []byte(`{"type":"system","subtype":"task_started","task_id":"wf-1","task_type":"local_workflow","workflow_name":"release","description":"cut release"}`)
	agent.HandleOutput(content)

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	row := tasks[0]
	assert.Equal(t, "wf-1", row.RowKey)
	assert.Equal(t, "workflow:release", row.GroupKey, "GroupKey derives from workflow_name")
	assert.Equal(t, "release", row.GroupLabel, "GroupLabel is the workflow_name")
	assert.Equal(t, "cut release", row.Title)
}

// TestHandleOutput_TaskProgressUpdatesActivityOnly verifies that task_progress
// only refreshes the activity (ActiveForm) and never creates a new row or
// changes the status off Running.
func TestHandleOutput_TaskProgressUpdatesActivityOnly(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	started := []byte(`{"type":"system","subtype":"task_started","task_id":"t-prog","tool_use_id":"tu-prog","task_type":"local_bash","description":"seed title"}`)
	agent.HandleOutput(started)

	progress := []byte(`{"type":"system","subtype":"task_progress","task_id":"t-prog","description":"running tests"}`)
	agent.HandleOutput(progress)

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1, "progress must not create a new registry row")
	row := tasks[0]
	assert.Equal(t, "t-prog", row.RowKey)
	assert.Equal(t, bgtask.StatusRunning, row.Status, "progress must not change status")
	assert.Equal(t, "running tests", row.ActiveForm, "progress updates ActiveForm")
}

// TestHandleOutput_TaskNotificationClosesRegistryEntry verifies that a
// task_notification transitions the registry row to the final status mapped
// from the wire status (completed/failed/stopped) and closes it.
func TestHandleOutput_TaskNotificationClosesRegistryEntry(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		wireStatus string
		wantStatus bgtask.Status
	}{
		{"completed", "completed", bgtask.StatusCompleted},
		{"failed", "failed", bgtask.StatusFailed},
		{"stopped", "stopped", bgtask.StatusStopped},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &outputTestSink{}
			agent := newTestAgent(sink)

			started := []byte(`{"type":"system","subtype":"task_started","task_id":"t-notif","tool_use_id":"tu-notif","task_type":"local_bash","description":"do work"}`)
			agent.HandleOutput(started)
			require.Len(t, sink.BackgroundTasks(), 1)

			notif := []byte(`{"type":"system","subtype":"task_notification","task_id":"t-notif","status":"` + tc.wireStatus + `","summary":"done and dusted"}`)
			agent.HandleOutput(notif)

			tasks := sink.BackgroundTasks()
			require.Len(t, tasks, 1, "notification must close the existing row, not add a new one")
			assert.Equal(t, tc.wantStatus, tasks[0].Status,
				"wire status %q maps to %v", tc.wireStatus, tc.wantStatus)
			assert.True(t, tasks[0].Status.IsFinished(), "notification status must be final")
		})
	}
}

// TestHandleOutput_TaskNotificationUnknownStatusLeavesRowRunning verifies that a
// task_notification carrying a status the map does not recognize does NOT
// give a final status to the row. An unrecognized status must not close a running task
// (the zero-value StatusPending would otherwise write an active+ended row).
func TestHandleOutput_TaskNotificationUnknownStatusLeavesRowRunning(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	started := []byte(`{"type":"system","subtype":"task_started","task_id":"t-unk","tool_use_id":"tu-unk","task_type":"local_bash","description":"do work"}`)
	agent.HandleOutput(started)
	require.Len(t, sink.BackgroundTasks(), 1)

	// "running" is not in claudeTaskStatusMap; the handler must ignore it.
	notif := []byte(`{"type":"system","subtype":"task_notification","task_id":"t-unk","status":"running","summary":"still going"}`)
	agent.HandleOutput(notif)

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1, "unknown status must not add a row")
	assert.Equal(t, bgtask.StatusRunning, tasks[0].Status, "unknown status must leave the row running")
	assert.True(t, tasks[0].EndedAt.IsZero(), "unknown status must not stamp ended_at")
}

// TestHandleOutput_DuplicateTaskStartedIsIdempotent verifies that replaying the
// same task_started line leaves exactly one registry row (the upsert is keyed by
// row_key, so the second call merges into the first).
func TestHandleOutput_DuplicateTaskStartedIsIdempotent(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	started := []byte(`{"type":"system","subtype":"task_started","task_id":"t-dup","tool_use_id":"tu-dup","task_type":"local_bash","description":"once is enough"}`)
	agent.HandleOutput(started)
	agent.HandleOutput(started)

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1, "duplicate task_started must merge into a single registry row")
	assert.Equal(t, "t-dup", tasks[0].RowKey)
	assert.Equal(t, bgtask.StatusRunning, tasks[0].Status)
	assert.Equal(t, "once is enough", tasks[0].Title)
}

// TestHandleOutput_SubagentAssistantRoutesToChildTranscript verifies that after
// a task_started registers the spawn tool_use id, a forwarded assistant envelope
// carrying parent_tool_use_id routes into the CHILD transcript and never touches
// the parent transcript.
func TestHandleOutput_SubagentAssistantRoutesToChildTranscript(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	// Register the subagent (local_agent) keyed by tool_use_id "tu-route".
	started := []byte(`{"type":"system","subtype":"task_started","task_id":"t-route","tool_use_id":"tu-route","task_type":"local_agent","description":"child task"}`)
	agent.HandleOutput(started)

	// Forwarded assistant message carrying parent_tool_use_id.
	assistant := []byte(`{
		"type": "assistant",
		"parent_tool_use_id": "tu-route",
		"message": {
			"role": "assistant",
			"content": [
				{"type": "text", "text": "child is working"},
				{"type": "tool_use", "id": "tu-child-1", "name": "Read", "input": {"file_path": "/tmp/x"}}
			]
		}
	}`)
	agent.HandleOutput(assistant)

	// Parent transcript untouched.
	assert.Empty(t, sink.Messages(), "forwarded assistant must not persist to the parent transcript")

	// Child transcript received the message.
	child, ok := sink.ChildSink("child-of-tu-route").(*testSink)
	require.True(t, ok)
	msgs := child.Messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, msgs[0].Source)
	assert.Equal(t, "tu-route", msgs[0].ParentSpanID)
	assert.Equal(t, "tu-child-1", msgs[0].SpanID)
	assert.Equal(t, "Read", msgs[0].SpanType)
}

// TestHandleOutput_SubagentResultRoutesAsChildTurnEnd verifies that a forwarded
// subagent result message (parent_tool_use_id set) routes into the child
// transcript as a PersistTurnEnd divider (not a regular PersistMessage) and
// closes the registry row.
func TestHandleOutput_SubagentResultRoutesAsChildTurnEnd(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(sink)

	started := []byte(`{"type":"system","subtype":"task_started","task_id":"t-result","tool_use_id":"tu-result","task_type":"local_agent","description":"child result task"}`)
	agent.HandleOutput(started)

	result := []byte(`{
		"type": "result",
		"parent_tool_use_id": "tu-result",
		"subtype": "success",
		"result": "child finished"
	}`)
	agent.HandleOutput(result)

	// Parent transcript untouched.
	assert.Empty(t, sink.Messages(), "forwarded result must not persist to the parent transcript")

	// Child transcript received a turn-end divider.
	child, ok := sink.ChildSink("child-of-tu-result").(*testSink)
	require.True(t, ok)
	msgs := child.Messages()
	require.Len(t, msgs, 1)
	assert.True(t, msgs[0].TurnEnd, "forwarded result routes through PersistTurnEnd into the child transcript")

	// The registry row is closed (Completed for a non-error result).
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.StatusCompleted, tasks[0].Status, "subagent result closes the registry row")
}
