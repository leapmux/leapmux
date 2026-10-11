package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeControlPublicationFailureReturnsProtocolError(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	sink := &agenttest.ControlSink{PublicationError: errors.New("storage unavailable")}
	a := &Agent{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{Stdin: agenttest.NopStdin(&output)}), sink: agent.NewProviderServices(sink)}
	a.claudeCodeHandleControlRequest([]byte(`{"type":"control_request","request_id":"permission-1","request":{"tool_name":"Bash"}}`))
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"error","request_id":"permission-1","error":"LeapMux could not store this control request."}}`, output.String())
	assert.Empty(t, sink.PublishedControls())
}

// outputTestSink adds permission-mode and plan updates to Sink.
// It intentionally preserves Sink's OpenSpan and CloseSpan methods.
// Sink records both operations and mirrors the real SpanTracker's active set and span types.
// An override would leave both mirrors stale.
// The preceding override did that and made assertions through this fake pass without checking actual span state.
type outputTestSink struct {
	agenttest.Sink

	modeMu          sync.Mutex
	permissionModes []string

	planMu    sync.Mutex
	planCalls []planUpdateCall
}

type planUpdateCall struct {
	Title string
}

func (s *outputTestSink) UpdatePermissionMode(mode string) {
	s.modeMu.Lock()
	defer s.modeMu.Unlock()
	s.permissionModes = append(s.permissionModes, mode)
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

// outputTestSink must retain the behavior of its underlying fake.
// Its preceding span-method overrides left the active set empty.
// Assertions through that fake then passed regardless of the provider's actual span behavior.
func TestOutputTestSink_MirrorsTheSpanBookkeepingItExtends(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	sink.SetSpanType("span-close", "Read")
	sink.SetSpanType("span-kept", "Agent")
	sink.OpenSpan("span-close", "parent-1")
	sink.OpenSpan("span-kept", "")

	assert.Equal(t, []agenttest.SpanOpen{
		{SpanID: "span-close", ParentSpanID: "parent-1"},
		{SpanID: "span-kept", ParentSpanID: ""},
	}, sink.OpenSpans(), "an open reaches the embedded double, parentage included")

	sink.CloseSpan("span-close")
	sink.CloseSpan("span-kept")

	assert.Equal(t, []string{"span-close", "span-kept"}, sink.ClosedSpans())
	assert.Equal(t, "Read", sink.GetSpanType("span-close"),
		"a closed span KEEPS its type, exactly as the real tracker keeps it")
	assert.Equal(t, "Agent", sink.GetSpanType("span-kept"))

	sink.ResetSpans()
	assert.Empty(t, sink.GetSpanType("span-close"), "only the turn boundary clears types")

	// Both left the active set, so a row persisted now draws no rail.
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"type":"assistant"}`)}, agent.SpanInfo{SpanID: "span-next"}))
	msgs := sink.Messages()
	require.Len(t, msgs, 1)
	assert.Empty(t, msgs[0].SpansOpenAtPersist)
}

// newTestAgent creates a minimal Agent for unit-testing HandleOutput.
func newTestAgent(sink agent.ProviderServices) *Agent {
	return &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "test-agent",
		}),
		sink: sink,
	}
}

func TestHandleOutput_AssistantToolUse(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

	// A parent_tool_use_id identifies a forwarded subagent envelope from --forward-subagent-text.
	// Route it into the child transcript and preserve the parent transcript.
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
	child := sink.Child("child-of-parent-123")
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
	assert.Equal(t, 0, agent.TurnToolUses)
}

// Persist a subagent's tool_use row before opening its own span, as the parent transcript does.
// handlePersistableMessage persists first, and processAssistantBlocks then opens the span.
// The sink derives span_lines from the spans open during persistence.
// Opening first would give the announcing row its own active line, an extra depth column, and a rail with no preceding segment.
// Persist the matching tool_result while the span remains open, so connector_end closes that rail.
func TestRouteSubagentMessage_ToolUsePersistsBeforeItsSpanOpens(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

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

	child := sink.Child("child-of-spawn-1")
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
	assert.Equal(t, []agenttest.SpanOpen{{SpanID: "tu-bash", ParentSpanID: "spawn-1"}}, child.OpenSpans())
	assert.Equal(t, []string{"tu-bash"}, child.ClosedSpans())
}

// A foreground Task forwards its spawn prompt as a user envelope after PersistChildPrompt writes that prompt at task_started.
// Persisting the forwarded copy would open the transcript with two identical prompts.
//
// The synchronous CLI path emits that copy as a progress event before the run loop for its own UI.
// The asynchronous background Task path emits no such event.
// Only a foreground subagent therefore displayed the duplicate prompt.
func TestRouteSubagentMessage_DropsTheEchoedSpawnPrompt(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

	// The echo: a user envelope carrying prose, not a tool_result.
	agent.HandleOutput([]byte(`{
		"type": "user",
		"parent_tool_use_id": "spawn-1",
		"message": {
			"role": "user",
			"content": [{"type": "text", "text": "Review the diff."}]
		}
	}`))

	child := sink.Child("child-of-spawn-1")
	assert.Empty(t, child.Messages(),
		"the spawn prompt is written once, at task_started, not again from the echo")
}

// The filter must retain a genuine forwarded user envelope with the child's tool_result.
// Within its run loop, the CLI forwards only messages with a tool_use or tool_result block.
func TestRouteSubagentMessage_KeepsAUserEnvelopeCarryingAToolResult(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"parent_tool_use_id": "spawn-1",
		"message": {
			"role": "assistant",
			"content": [{"type": "tool_use", "id": "tu-1", "name": "Read", "input": {}}]
		}
	}`))
	agent.HandleOutput([]byte(`{
		"type": "user",
		"parent_tool_use_id": "spawn-1",
		"message": {
			"role": "user",
			"content": [{"type": "tool_result", "tool_use_id": "tu-1", "content": "ok"}]
		}
	}`))

	child := sink.Child("child-of-spawn-1")
	msgs := child.Messages()
	require.Len(t, msgs, 2)
	assert.Equal(t, "tu-1", msgs[1].SpanID)
	assert.True(t, msgs[1].Closing, "the tool_result still closes its span")
}

// Plain child assistant text owns no span and must preserve every currently open child span when persisted.
// A running tool's rail therefore continues past that text row.
func TestRouteSubagentMessage_TextPersistsUnderAnOpenSpan(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

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

	child := sink.Child("child-of-spawn-1")
	msgs := child.Messages()
	require.Len(t, msgs, 2)

	assert.Empty(t, msgs[0].SpansOpenAtPersist)
	require.Len(t, msgs[1].SpansOpenAtPersist, 1)
	assert.Equal(t, "tu-1", msgs[1].SpansOpenAtPersist[0].SpanID)
}

// One forwarded assistant message can contain parallel tool calls.
// Open a child span for every block because the next user envelope closes every tool_result block.
// Opening only the first span would omit the other rails while their results still attempt to close them.
func TestRouteSubagentMessage_ParallelToolUsesAllOpenAndClose(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

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

	child := sink.Child("child-of-spawn-1")
	assert.Equal(t, []agenttest.SpanOpen{
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
	agent := newTestAgent(agent.NewProviderServices(sink))

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
	agent := newTestAgent(agent.NewProviderServices(sink))

	// A forwarded user envelope with parent_tool_use_id carries the child's own tool_result.
	// Route it to the child transcript and close its span there.
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

	child := sink.Child("child-of-parent-123")
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
	agent := newTestAgent(agent.NewProviderServices(sink))

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
	assert.Empty(t, sink.OpenSpans())
	assert.Equal(t, 0, agent.TurnToolUses)
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
	agent := newTestAgent(agent.NewProviderServices(sink))

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
	agent := newTestAgent(agent.NewProviderServices(sink))

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
	ag := newTestAgent(agent.NewProviderServices(sink))

	rawEvent := `{"type":"rate_limit_event","rate_limit_info":{"rateLimitType":"five_hour","status":"rejected","resetsAt":1893456000}}`
	ag.HandleOutput([]byte(rawEvent))

	require.Equal(t, 1, sink.AutoScheduleCount())
	schedule := sink.LastAutoSchedule()
	assert.Equal(t, agent.AutoContinueReasonRateLimit, schedule.Reason)
	assert.Equal(t, time.Unix(1893456000, 0).UTC(), schedule.DueAt)
	assert.JSONEq(t, `{"rateLimitType":"five_hour","status":"rejected","resetsAt":1893456000}`, string(schedule.SourcePayload))

	// Persist the exact raw rate_limit_event as AGENT.
	// Do not replace it with a reduced {type:"rate_limit",rate_limit_info} payload.
	require.Equal(t, 1, sink.NotificationCount())
	last := sink.LastNotification()
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source,
		"Claude-emitted rate_limit_event must persist as AGENT")
	assert.JSONEq(t, rawEvent, string(last.Content),
		"raw envelope must be preserved verbatim so future fields flow through")
}

// TestClaudeRateLimitEvent_BroadcastsSnakeCaseWire verifies the rate-limit field conversion.
// Claude's SDK supplies camelCase rate_limit_info, while the broadcast rate_limits map uses snake_case tier fields.
// Claude and Codex therefore supply the same frontend field names.
func TestClaudeRateLimitEvent_BroadcastsSnakeCaseWire(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

	rawEvent := `{"type":"rate_limit_event","rate_limit_info":{"rateLimitType":"five_hour","status":"rejected","resetsAt":1893456000,"utilization":1.0}}`
	agent.HandleOutput([]byte(rawEvent))

	require.Equal(t, 1, sink.SessionInfoCount())
	info := sink.LastSessionInfo()
	rateLimits, ok := info["rate_limits"].(map[string]any)
	require.True(t, ok, "broadcast must carry rate_limits in snake_case, got %#v", info)
	assert.Equal(t, "merge", rateLimits["mode"])
	rateLimits, ok = rateLimits["values"].(map[string]any)
	require.True(t, ok, "rate_limits must carry a values map")

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
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{
		"type":"rate_limit_event",
		"rate_limit_info":{
			"rateLimitType":"five_hour",
			"status":"allowed",
			"resetsAt":1893456000
		}
	}`))

	require.Equal(t, 1, sink.AutoCancelCount())
	assert.Equal(t, agent.AutoContinueReasonRateLimit, sink.LastAutoCancel())
	assert.Equal(t, 0, sink.AutoScheduleCount())
}

// TestClaudeRateLimitEvent_AllowedWarningCancelsResume prevents an incorrect automatic continuation after allowed_warning.
// That event warns about a served request without blocking it.
// Only rejected blocks the request.
// A warning must cancel a pending resume, as allowed does.
func TestClaudeRateLimitEvent_AllowedWarningCancelsResume(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{
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
	assert.Equal(t, agent.AutoContinueReasonRateLimit, sink.LastAutoCancel())
}

// TestClaudeRateLimitEvent_OverageAbsorbsRejected verifies that an active overage allowance accepts a request despite a rejected base window.
// overageStatus still permits the request, so no block requires waiting.
// Cancel any pending resume.
func TestClaudeRateLimitEvent_OverageAbsorbsRejected(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

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

// TestClaudeRateLimitEvent_OverageRejectedSchedulesAtOverageReset verifies a block that rejects the overage allowance itself.
// Schedule the resume at overageResetsAt instead of the base resetsAt.
func TestClaudeRateLimitEvent_OverageRejectedSchedulesAtOverageReset(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

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

// TestClaudeRateLimitEvent_RejectedWithoutResetLeavesScheduleIntact verifies rejected without resetsAt.
// The event confirms a block but supplies no reset time, so it schedules no new resume.
// It also preserves a legitimate pending resume from an earlier event with a valid reset time.
func TestClaudeRateLimitEvent_RejectedWithoutResetLeavesScheduleIntact(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

	agent.HandleOutput([]byte(`{
		"type":"rate_limit_event",
		"rate_limit_info":{"rateLimitType":"seven_day","status":"rejected"}
	}`))

	assert.Equal(t, 0, sink.AutoScheduleCount())
	assert.Equal(t, 0, sink.AutoCancelCount())
}

// The CLI reports an interrupted turn through subtype: error_during_execution with is_error: true and its own errors diagnostics.
// The reader selected Stop, but the row formerly displayed "Error during execution (12s) [ede_diagnostic] result_type=user ...".
// Other providers reported that turn as interrupted.
// LeapMux requested the stop and therefore records the known interruption in its own completion column.
func TestClaudeResult_AnInterruptedTurnStatesTheInterruption(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Chapter 1"}]}}`))
	a.noteInterruptRequested()
	a.HandleOutput([]byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"duration_ms":12000}`))

	messages := sink.Messages()
	require.NotEmpty(t, messages)
	assert.Equal(t, agent.MessageCompletionInterrupted, messages[len(messages)-1].Completion,
		"the turn end states what LeapMux asked for")
}

// A turn nobody interrupted keeps its own outcome, so a real failure still reads as
// one. The flag must not survive the turn it belongs to.
func TestClaudeResult_AFailureAfterAnInterruptedTurnStaysAFailure(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

	agent.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Chapter 1"}]}}`))
	agent.noteInterruptRequested()
	agent.HandleOutput([]byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"duration_ms":12000}`))

	agent.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Second"}]}}`))
	agent.HandleOutput([]byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"duration_ms":900}`))

	messages := sink.Messages()
	require.NotEmpty(t, messages)
	assert.Empty(t, messages[len(messages)-1].Completion, "the second turn was not interrupted")
}

// Claude Code 2.1.289 wrote these actual result frames against a local mock model.
// Retain the outcome fields and omit the usage objects:
//   - subtype.
//   - is_error.
//   - terminal_reason.
//   - errors.
//
// terminal_reason identifies how the turn ends.
// subtype and is_error alone cannot distinguish an abort from a failure.
// An abort and a failed tool both use error_during_execution.
// An API failure uses subtype: success with is_error: true.
const (
	// The stop took effect while the model streamed.
	claudeResultAbortedStreaming = `{"type":"result","subtype":"error_during_execution","is_error":true,"duration_ms":5850,"num_turns":2,"stop_reason":null,"terminal_reason":"aborted_streaming","errors":["[ede_diagnostic] result_type=user last_content_type=n/a stop_reason=null"]}`
	// The stop took effect while a tool ran.
	claudeResultAbortedTools = `{"type":"result","subtype":"error_during_execution","is_error":true,"duration_ms":3065,"num_turns":3,"stop_reason":"tool_use","terminal_reason":"aborted_tools","errors":["[ede_diagnostic] result_type=user last_content_type=n/a stop_reason=tool_use"]}`
	// The turn finished before the CLI read the stop request.
	// The probe sent Stop immediately after the answer frame.
	// The CLI acknowledged it but still ended the turn with this frame.
	claudeResultFinished = `{"type":"result","subtype":"success","is_error":false,"duration_ms":1197,"num_turns":1,"stop_reason":"end_turn","terminal_reason":"completed","api_error_status":null,"result":"QUICK ANSWER"}`
	// The turn failed on its own before the CLI read the stop.
	claudeResultAPIError = `{"type":"result","subtype":"success","is_error":true,"duration_ms":52,"num_turns":1,"stop_reason":"stop_sequence","terminal_reason":"api_error","api_error_status":400,"result":"API Error: 400 NATIVE ERROR MARKER"}`
)

// Report an interrupted turn only when result confirms that the stop affected it.
// A turn that ends before the CLI reads Stop keeps its original outcome because that stop arrives too late.
// The preceding implementation marked every result after a stop request.
// It therefore displayed "Turn interrupted" for a turn that independently finished or failed.
func TestClaudeResult_ATurnThatEndedBeforeTheStopKeepsItsOutcome(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		frame string
	}{
		{"finished, with its reason", claudeResultFinished},
		{"failed on its own, with its reason", claudeResultAPIError},
		// A CLI that states no reason: `is_error` decides.
		{"finished, without a reason", `{"type":"result","subtype":"success","is_error":false,"duration_ms":900,"result":"ok"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &outputTestSink{}
			a := newTestAgent(agent.NewProviderServices(sink))

			a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"QUICK ANSWER"}]}}`))
			a.noteInterruptRequested()
			a.HandleOutput([]byte(tc.frame))

			messages := sink.Messages()
			require.NotEmpty(t, messages)
			assert.Empty(t, messages[len(messages)-1].Completion,
				"the stop lost the race, so the turn end keeps the outcome that the frame states")
		})
	}
}

// A stop that took effect marks the turn end as interrupted. The frame states it in
// `terminal_reason`, and that statement outranks `is_error` and `subtype`.
func TestClaudeResult_AStopThatTookEffectMarksTheTurnEndInterrupted(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		frame string
	}{
		{"aborted while the model streamed", claudeResultAbortedStreaming},
		{"aborted while a tool ran", claudeResultAbortedTools},
		// The reason outranks the flag. The CLI writes this shape for an abort that
		// keeps the last complete answer (the Remote Control bridge does).
		{"aborted, with a success shape", `{"type":"result","subtype":"success","is_error":false,"result":"","terminal_reason":"aborted_streaming"}`},
		// A CLI that states no reason: `is_error` decides.
		{"aborted, without a reason", `{"type":"result","subtype":"error_during_execution","is_error":true,"duration_ms":12000}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &outputTestSink{}
			a := newTestAgent(agent.NewProviderServices(sink))

			a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"PARTIAL"}]}}`))
			a.noteInterruptRequested()
			a.HandleOutput([]byte(tc.frame))

			messages := sink.Messages()
			require.NotEmpty(t, messages)
			assert.Equal(t, agent.MessageCompletionInterrupted, messages[len(messages)-1].Completion)
		})
	}
}

// A `result` that states a finished turn spends the note, as any other `result`
// does. The next turn must not inherit it.
func TestClaudeResult_AFinishedTurnSpendsTheStopNote(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"QUICK ANSWER"}]}}`))
	a.noteInterruptRequested()
	a.HandleOutput([]byte(claudeResultFinished))

	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"NEXT"}]}}`))
	a.HandleOutput([]byte(claudeResultAbortedStreaming))

	messages := sink.Messages()
	require.NotEmpty(t, messages)
	assert.Empty(t, messages[len(messages)-1].Completion,
		"no stop was asked for during the second turn, so its abort frame alone does not mark it")
}

func TestClaudeResult_APIErrorUsesAPIErrorReason(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{
		"type":"result",
		"is_error":true,
		"result":"API Error: 500 Internal Server Error"
	}`))

	require.Equal(t, 1, sink.AutoScheduleCount())
	assert.Equal(t, agent.AutoContinueReasonAPIError, sink.LastAutoSchedule().Reason)

	a.HandleOutput([]byte(`{
		"type":"result",
		"is_error":false,
		"result":"ok"
	}`))

	require.Equal(t, 1, sink.AutoCancelCount())
	assert.Equal(t, agent.AutoContinueReasonAPIError, sink.LastAutoCancel())
}

func TestClaudeResult_IdleTimeoutPrefixSchedulesAPIErrorAutoContinue(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	payload := []byte(`{
		"type":"result",
		"is_error":true,
		"result":"API Error: Stream idle timeout - partial response received"
	}`)

	a.HandleOutput(payload)

	require.Equal(t, 1, sink.AutoScheduleCount())
	schedule := sink.LastAutoSchedule()
	assert.Equal(t, agent.AutoContinueReasonAPIError, schedule.Reason)
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

func TestClaudeTurnCounterPreservesOriginalBytes(t *testing.T) {
	t.Parallel()
	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.TurnToolUses = 2
	raw := []byte(`{"type":"result", "subtype":"success", "result":"Done", "future":9007199254740993}`)
	a.HandleOutput(raw)
	messages := sink.Messages()
	require.NotEmpty(t, messages)
	last := messages[len(messages)-1]
	assert.Equal(t, raw, last.Content)
	assert.Contains(t, string(last.Metadata), `"num_tool_uses":2`)
}

func TestClaudeResult_BareOverloadedSchedulesAPIErrorAutoContinue(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	ag := newTestAgent(agent.NewProviderServices(sink))

	// Claude Code reports an Anthropic HTTP 529 overload through either of these messages:
	//   - "API Error: 529 Overloaded".
	//   - "API Error: Overloaded".
	// The second form must still continue automatically although the 5xx matcher finds no numeric code.
	payload := []byte(`{
		"type":"result",
		"is_error":true,
		"result":"API Error: Overloaded"
	}`)

	ag.HandleOutput(payload)

	require.Equal(t, 1, sink.AutoScheduleCount())
	schedule := sink.LastAutoSchedule()
	assert.Equal(t, agent.AutoContinueReasonAPIError, schedule.Reason)
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
	agent := newTestAgent(agent.NewProviderServices(sink))

	// Completely invalid JSON must not cause a panic.
	agent.HandleOutput([]byte(`not json at all`))
	assert.Empty(t, sink.Messages())

	// Return early when the outer type is valid but the message body fails envelope parsing.
	agent.HandleOutput([]byte(`{"type":"assistant","message":INVALID}`))
	assert.Empty(t, sink.Messages())
}

func TestHandleOutput_EmptyContentBlocks(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

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
	agent := newTestAgent(agent.NewProviderServices(sink))

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
	assert.Equal(t, contracts.ClaudeModePlan, modes[0])
}

func TestHandleOutput_PlanModeEnter_StringToolUseResult(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

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
	assert.Equal(t, contracts.ClaudeModePlan, modes[0])
}

func TestHandleOutput_MultipleToolUses(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

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

	// Both tool_use blocks open spans.
	spans := sink.OpenSpans()
	require.Len(t, spans, 2)
	assert.Equal(t, "tu-a", spans[0].SpanID)
	assert.Equal(t, "tu-b", spans[1].SpanID)

	// The tool-use count includes both calls.
	assert.Equal(t, 2, agent.TurnToolUses)
}

func TestHandleOutput_TopLevelAssistantBroadcastsContextUsage(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

	// A main-session assistant message with usage broadcasts context usage through session information.
	// It carries no parent_tool_use_id.
	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [{"type": "text", "text": "hello"}],
			"usage": {"input_tokens": 100, "output_tokens": 50, "cache_creation_input_tokens": 10, "cache_read_input_tokens": 30}
		}
	}`))

	// Send a result message to trigger the broadcast.
	// Assistant messages debounce this update, but result messages always broadcast it.
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
	agent := newTestAgent(agent.NewProviderServices(sink))

	// A system/thinking_tokens line supplies live telemetry.
	// Broadcast it through the temporary agent_session_info channel and never persist it in the timeline.
	// Its delta-specific session_id must not repeat session-initialization effects.
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
	assert.Empty(t, sink.StatusActives(), "thinking_tokens must not re-broadcast status active")

	require.Equal(t, 1, sink.SessionInfoCount(), "thinking_tokens must broadcast session info")
	assert.Equal(t, int64(230), sink.LastSessionInfo()["thinking_tokens"])
}

func TestClaudeWaitClearsNativeProgress(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.SimulateExitForTest()
	sink.ReportProgress(agent.NativeTokenProgress("claude:thinking", 42))

	require.NoError(t, a.Wait())
	assert.Equal(t, agent.ProgressSnapshot{}, sink.ProgressSnapshot())
}

func TestHandleOutput_ThinkingTokensZeroEstimateStillSwallowed(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	ag := newTestAgent(agent.NewProviderServices(sink))

	// Intercept a thinking_tokens line even when estimated_tokens is absent or zero, as the first turn delta can be.
	// Broadcast it without timeline persistence.
	// The frontend's > 0 condition decides whether to display the count.
	ag.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "thinking_tokens",
		"session_id": "a40e65f9-f1f2-4e8b-b089-abc9692345b2"
	}`))

	assert.Equal(t, 0, sink.MessageCount(), "zero-estimate thinking_tokens must not be persisted")
	assert.Equal(t, 0, sink.SessionIDCount(), "zero-estimate thinking_tokens must not re-fire session init")
	updates := sink.ProgressUpdates()
	require.Len(t, updates, 1, "zero-estimate thinking_tokens must still broadcast")
	assert.Equal(t, agent.ProgressModelComplete, updates[0].Operation)
}

func TestHandleOutput_ThinkingTokensFractionalEstimateStillSwallowed(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

	// Intercept thinking_tokens when estimated_tokens uses a fractional or exponent form.
	// The inner float64 parse accepts 230.0 and 1.5e4, and the broadcast truncates that count to int64.
	// A directly typed int64 field would fail decoding and let the telemetry line enter the timeline.
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
	ag := newTestAgent(agent.NewProviderServices(sink))

	// Intercept thinking_tokens even when estimated_tokens uses an unexpected type, such as a JSON string instead of a number.
	// Capture that value as RawMessage, so matching the subtype does not depend on parsing the count.
	// A failed count parse broadcasts zero.
	// A typed numeric field could fail the outer decode and return false, letting the line initialize a session and persist telemetry.
	ag.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "thinking_tokens",
		"estimated_tokens": "230",
		"session_id": "a40e65f9-f1f2-4e8b-b089-abc9692345b2"
	}`))

	assert.Equal(t, 0, sink.MessageCount(), "malformed thinking_tokens must not be persisted")
	assert.Equal(t, 0, sink.SessionIDCount(), "malformed thinking_tokens must not re-fire session init")
	updates := sink.ProgressUpdates()
	require.Len(t, updates, 1, "malformed thinking_tokens must still broadcast")
	assert.Equal(t, agent.ProgressModelComplete, updates[0].Operation)
}

func TestHandleOutput_ThinkingTokensOverflowEstimateStillSwallowed(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	ag := newTestAgent(agent.NewProviderServices(sink))

	// The estimated_tokens value 1e400 overflows float64 and fails the inner count parse.
	// Still consume the line and broadcast zero without persisting it.
	// Never convert NaN or infinity into int64.
	ag.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "thinking_tokens",
		"estimated_tokens": 1e400,
		"session_id": "a40e65f9-f1f2-4e8b-b089-abc9692345b2"
	}`))

	assert.Equal(t, 0, sink.MessageCount(), "overflowing thinking_tokens must not be persisted")
	assert.Equal(t, 0, sink.SessionIDCount(), "overflowing thinking_tokens must not re-fire session init")
	updates := sink.ProgressUpdates()
	require.Len(t, updates, 1, "overflowing thinking_tokens must still broadcast")
	assert.Equal(t, agent.ProgressModelComplete, updates[0].Operation)
}

func TestHandleOutput_ThinkingTokensNegativeEstimateClampedToZero(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	ag := newTestAgent(agent.NewProviderServices(sink))

	// A running token estimate cannot be negative.
	// Clamp a negative native value to zero before broadcasting it, consume the line, and never persist it.
	// No consumer then receives a negative count.
	ag.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "thinking_tokens",
		"estimated_tokens": -42.9,
		"session_id": "a40e65f9-f1f2-4e8b-b089-abc9692345b2"
	}`))

	assert.Equal(t, 0, sink.MessageCount(), "negative thinking_tokens must not be persisted")
	assert.Equal(t, 0, sink.SessionIDCount(), "negative thinking_tokens must not re-fire session init")
	updates := sink.ProgressUpdates()
	require.Len(t, updates, 1, "negative thinking_tokens must still broadcast")
	assert.Equal(t, agent.ProgressModelComplete, updates[0].Operation)
}

func TestHandleOutput_ThinkingTokensFiniteHugeEstimateClampedToZero(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	ag := newTestAgent(agent.NewProviderServices(sink))

	// The finite value 1e300 parses as float64, while 1e400 overflows that parse.
	// Both exceed math.MaxInt64 and must produce the same zero broadcast without timeline persistence.
	// Converting 1e300 directly to int64 can produce an invalid count near the integer limit instead of the reported estimate.
	ag.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "thinking_tokens",
		"estimated_tokens": 1e300,
		"session_id": "a40e65f9-f1f2-4e8b-b089-abc9692345b2"
	}`))

	assert.Equal(t, 0, sink.MessageCount(), "finite-huge thinking_tokens must not be persisted")
	updates := sink.ProgressUpdates()
	require.Len(t, updates, 1, "finite-huge thinking_tokens must still broadcast")
	assert.Equal(t, agent.ProgressModelComplete, updates[0].Operation)
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
	agent := newTestAgent(agent.NewProviderServices(sink))

	// Persist an ordinary system message unchanged when it is neither thinking_tokens nor a notification-threaded subtype.
	// The telemetry filter must not discard other system lines.
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
	agent := newTestAgent(agent.NewProviderServices(sink))

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
	agent := newTestAgent(agent.NewProviderServices(sink))
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

	// The result's modelUsage contains haiku with a 200k context and opus[1m] with a 1M context.
	// Select the primary model, opus[1m].
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
	agent := newTestAgent(agent.NewProviderServices(sink))
	// The bare opus alias normalizes to opus[1m] because this Opus selection uses a 1M context only.
	// modelUsage can contain both claude-opus-4-6 and claude-opus-4-6[1m], although the current CLI does not emit that combination.
	// Both normalize to opus[1m].
	// findPrimaryContextWindow selects the largest matching context, so it chooses 1M regardless of map order.
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
	agent := newTestAgent(agent.NewProviderServices(sink))
	agent.model = "opus[1m]"

	// The main-session assistant message sets the usage baseline.
	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [{"type": "text", "text": "top level"}],
			"usage": {"input_tokens": 500, "output_tokens": 200}
		}
	}`))

	// The main-session result sets the context window to 1M.
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

	// A child result with parent_tool_use_id does not replace the main context window even when its modelUsage contains only haiku with 200k.
	agent.HandleOutput([]byte(`{
		"type": "result",
		"parent_tool_use_id": "agent-tu-1",
		"subtype": "success",
		"modelUsage": {
			"claude-haiku-4-5-20251001": {"contextWindow": 200000}
		}
	}`))

	// Send another main-session assistant message and result to trigger a broadcast.
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
	agent := newTestAgent(agent.NewProviderServices(sink))
	agent.model = "opus[1m]"

	// The main-session assistant message sets the usage baseline.
	agent.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [{"type": "text", "text": "top level"}],
			"usage": {"input_tokens": 500, "output_tokens": 200}
		}
	}`))

	// The main-session result sets the context window to 1M.
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

	// This child result intentionally omits parent_tool_use_id to test the additional protection.
	// Its modelUsage contains only haiku and omits the primary opus[1m] model.
	// findPrimaryContextWindow therefore returns zero and preserves the current context window.
	agent.HandleOutput([]byte(`{
		"type": "result",
		"subtype": "success",
		"modelUsage": {
			"claude-haiku-4-5-20251001": {"contextWindow": 200000}
		}
	}`))

	// Send another assistant message and result to trigger a broadcast.
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

// TestGetOrCreateUsageSnapshot_SeedsFromDynamicCatalog verifies that effortCatalog supplies the initial context window from each agent's dynamic catalog.
// A model discovered only through the live CLI is absent from claudeCodeAvailableModels.
// Using only the static catalog would report no window until a result message arrives.
// The dynamic catalog immediately supplies the window inferred from [1m].
func TestGetOrCreateUsageSnapshot_SeedsFromDynamicCatalog(t *testing.T) {
	t.Parallel()

	// Precondition: the model is unknown to the static catalog, so a static seed
	// would be 0.
	require.Equal(t, int64(0), agent.FindAvailableModel(claudeCodeAvailableModels, "mythos").GetContextWindow(),
		"precondition: mythos is not in the static catalog")

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))
	agent.model = "mythos"
	agent.availableModels = convertClaudeModels([]claudeCodeModelInfo{
		{Value: "mythos[1m]", DisplayName: "Mythos (1M)", SupportsEffort: true, SupportedEffortLevels: []string{"high", "xhigh"}},
		{Value: "mythos", DisplayName: "Mythos", SupportsEffort: true, SupportedEffortLevels: []string{"high", "xhigh"}},
	}, nil)

	// The assistant usage message initializes the snapshot through getOrCreateUsageSnapshot.
	// The result has no modelUsage and broadcasts the initialized window without replacing it.
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

// TestExtractAndBroadcastUsage_WindowFallsBackToStaticCatalog covers a current model missing from the live CLI's dynamic list but present in the static catalog.
// effortResolver supplies its actual context window through the same individual-model fallback used for effort and ultracode.
// The preceding window lookup replaced the whole static list with the dynamic list and missed this model.
// It reported an unknown window until a result supplied one, although effort resolution already found the model through the fallback.
func TestExtractAndBroadcastUsage_WindowFallsBackToStaticCatalog(t *testing.T) {
	t.Parallel()

	// Precondition: opus[1m] is a static-catalog model with a 1M window.
	require.Equal(t, int64(1_000_000), agent.FindAvailableModel(claudeCodeAvailableModels, "opus[1m]").GetContextWindow(),
		"precondition: opus[1m] carries a 1M window in the static catalog")

	sink := &outputTestSink{}
	ag := newTestAgent(agent.NewProviderServices(sink))
	ag.model = "opus[1m]"
	// The native dynamic list omits opus[1m], although the resumed session can still use that model.
	// effortCatalog returns the list exactly, so a dynamic-only lookup misses its context window.
	ag.availableModels = convertClaudeModels([]claudeCodeModelInfo{
		{Value: "sonnet", DisplayName: "Sonnet", SupportsEffort: true, SupportedEffortLevels: []string{"high"}},
	}, nil)
	require.Nil(t, agent.FindAvailableModel(ag.availableModels, "opus[1m]"),
		"precondition: opus[1m] is absent from the dynamic catalog")

	ag.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50}}}`))
	ag.HandleOutput([]byte(`{"type":"result","subtype":"success"}`)) // no modelUsage -> window from the catalog seed

	require.GreaterOrEqual(t, sink.SessionInfoCount(), 1)
	usage, ok := sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	require.True(t, ok, "expected context_usage in session info")
	assert.Equal(t, int64(1_000_000), usage["context_window"],
		"window resolved from the static fallback (opus[1m] = 1M), not reported unknown")
}

// TestGetOrCreateUsageSnapshot_SentinelWindowIsUnknown covers the unresolved account-default sentinel, default.
// Its catalog entry supplies no actual context window.
// Omit context_window from the broadcast instead of inventing a value, so the indicator displays unknown as the frontend does.
// The window remains unknown until the sentinel resolves to a concrete model or a result supplies the actual window.
func TestGetOrCreateUsageSnapshot_SentinelWindowIsUnknown(t *testing.T) {
	t.Parallel()

	// Precondition: the sentinel entry carries no context window.
	require.Equal(t, int64(0), agent.FindAvailableModel(claudeCodeAvailableModels, agent.DefaultModelSentinel).GetContextWindow(),
		"precondition: the sentinel has no concrete window")

	sink := &outputTestSink{}
	ag := newTestAgent(agent.NewProviderServices(sink))
	ag.model = agent.DefaultModelSentinel // stuck: the CLI never echoed a concrete applied.model

	ag.HandleOutput([]byte(`{
		"type": "assistant",
		"message": {
			"role": "assistant",
			"content": [{"type": "text", "text": "hi"}],
			"usage": {"input_tokens": 100, "output_tokens": 50}
		}
	}`))
	ag.HandleOutput([]byte(`{"type": "result", "subtype": "success"}`))

	require.GreaterOrEqual(t, sink.SessionInfoCount(), 1)
	usage, ok := sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	require.True(t, ok, "expected context_usage in session info")
	_, hasWindow := usage["context_window"]
	assert.False(t, hasWindow,
		"unresolved sentinel reports no context window (unknown), not a fabricated value")
}

// TestExtractAndBroadcastUsage_ReseedsWindowOnModelChange verifies that an existing usage snapshot follows a changed model.
// The unresolved account-default sentinel initially reports no window.
// When it resolves to a model with a 1M context, update the window immediately from the catalog.
// Do not require a later result with matching modelUsage to correct the unknown value.
func TestExtractAndBroadcastUsage_ReseedsWindowOnModelChange(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	ag := newTestAgent(agent.NewProviderServices(sink))
	ag.model = agent.DefaultModelSentinel // unresolved at the first turn -> window unknown

	assistant := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50}}}`
	result := `{"type":"result","subtype":"success"}` // no modelUsage -> window comes from the catalog re-seed

	ag.HandleOutput([]byte(assistant))
	ag.HandleOutput([]byte(result))
	usage, ok := sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	require.True(t, ok)
	_, hasWindow := usage["context_window"]
	require.False(t, hasWindow, "unresolved sentinel reports no window")

	// The sentinel resolves to a 1M-context model (refreshSettingsFromAgent stored it).
	ag.model = "opus[1m]"
	ag.HandleOutput([]byte(assistant))
	ag.HandleOutput([]byte(result))
	usage, ok = sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, int64(1_000_000), usage["context_window"],
		"window re-seeded from the catalog when the model resolves off the sentinel")
}

// TestExtractAndBroadcastUsage_ReseedsDownwardOnModelDowngrade verifies a context reduction from 1M to 200K after a live model change.
// The other initialization test covers the opposite direction, from the 200K sentinel setup to 1M.
// If the downward update fails, the indicator reports more available context than the model actually supports.
// That incorrect 1M estimate would remain until a result with matching modelUsage corrects it.
func TestExtractAndBroadcastUsage_ReseedsDownwardOnModelDowngrade(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))
	agent.model = "opus[1m]" // known 1M model via the static catalog fallback

	assistant := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50}}}`
	result := `{"type":"result","subtype":"success"}` // no modelUsage -> window comes from the catalog re-seed

	agent.HandleOutput([]byte(assistant))
	agent.HandleOutput([]byte(result))
	usage, ok := sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, int64(1_000_000), usage["context_window"], "opus[1m] seeds the 1M window")

	// Switch to Sonnet with a 200K context and require the window to decrease from 1M.
	agent.model = "sonnet"
	agent.HandleOutput([]byte(assistant))
	agent.HandleOutput([]byte(result))
	usage, ok = sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, int64(200_000), usage["context_window"],
		"window re-seeded down to 200K on a 1M->Sonnet downgrade")
}

// TestExtractAndBroadcastUsage_ClearsWindowOnSwitchToUnknownModel verifies a current model absent from both catalogs.
// A resumed model can move into unavailable_models and leave both the dynamic list and static fallback.
// The catalog then returns zero, and the model change must clear the preceding window even though the replacement window is zero.
// Omit context_window instead of continuing to report the preceding model's larger context.
// A later result's modelUsage supplies the actual window when available.
func TestExtractAndBroadcastUsage_ClearsWindowOnSwitchToUnknownModel(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	ag := newTestAgent(agent.NewProviderServices(sink))
	ag.model = "opus[1m]" // known 1M model via the static catalog fallback

	assistant := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50}}}`
	result := `{"type":"result","subtype":"success"}` // no modelUsage -> window comes from the catalog re-seed

	ag.HandleOutput([]byte(assistant))
	ag.HandleOutput([]byte(result))
	usage, ok := sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, int64(1_000_000), usage["context_window"], "opus[1m] seeds the 1M window")

	// Precondition: the switched-to model is in neither catalog.
	require.Equal(t, int64(0), agent.FindAvailableModel(ag.effortCatalog(), "ghost-model").GetContextWindow(),
		"precondition: ghost-model is unknown to both catalogs")

	// Switch to a model neither catalog knows (filtered/unavailable but still running).
	ag.model = "ghost-model"
	ag.HandleOutput([]byte(assistant))
	ag.HandleOutput([]byte(result))
	usage, ok = sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	require.True(t, ok)
	_, hasWindow := usage["context_window"]
	assert.False(t, hasWindow,
		"switching to a model unknown to both catalogs clears the stale 1M window to unknown")
}

// TestExtractAndBroadcastUsage_ResultWindowSurvivesReseed verifies that the result's modelUsage window survives later turns on the same model.
// The catalog initialization runs every turn so it can clear a stale window after a change to an unknown model.
// windowModel prevents that initialization from replacing the CLI's confirmed window with a less precise catalog estimate.
func TestExtractAndBroadcastUsage_ResultWindowSurvivesReseed(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))
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

	// On a later turn with the same model, preserve the confirmed 500K window instead of replacing it with the catalog's 1M estimate.
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
			// The bare opus alias normalizes to opus[1m] because this Opus selection uses a 1M context only.
			// It matches both claude-opus-4-6 and claude-opus-4-6[1m].
			// Select the largest matching context so map order cannot change the 1M result.
			// The current CLI does not emit both keys, but this test still protects that duplicate-normalization case.
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
			// S6 requires equality after normalization.
			// An unrelated API family such as opusplus must not match merely because its ID contains opus.
			// The preceding substring scan incorrectly accepted that case.
			name:  "opus does not match an unrelated opusplus family",
			model: "opus",
			usage: map[string]json.RawMessage{
				"claude-opusplus-1": json.RawMessage(`{"contextWindow": 500000}`),
				"claude-opus-4-8":   json.RawMessage(`{"contextWindow": 200000}`),
			},
			expected: 200000,
		},
		{
			// S6 verifies lowercase normalization: [1M] matches opus[1m].
			// The preceding case-sensitive suffix search returned zero for that spelling.
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

// TestContextUsageSnapshot_BuildBroadcast directly exercises buildBroadcast's debounce and omitted-window rules.
// HandleOutput integration tests cover the result-message path.
// Supplying now makes the 10-second debounce for other messages and LastBroadcast updates deterministic without a real clock.
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

// TestHandleOutput_SystemSessionStateChangedConsumed verifies that a Claude session_state_changed system line updates neither the transcript nor the registry.
// Consume that line without persistence.
func TestHandleOutput_SystemSessionStateChangedConsumed(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

	content := []byte(`{"type":"system","subtype":"session_state_changed","session_id":"s1","cwd":"/tmp"}`)
	agent.HandleOutput(content)

	assert.Equal(t, 0, sink.MessageCount(), "session_state_changed must be consumed, not persisted")
	assert.Empty(t, sink.BackgroundTasks(), "session_state_changed must not create a registry row")
}

// TestClaudeHandleTaskStarted_FallsBackToPromptFirstLine covers task_started without a description but with a spawn prompt.
// The registry title uses the prompt's first line, matching the captured native shape.
func TestClaudeHandleTaskStarted_FallsBackToPromptFirstLine(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

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
	agent := newTestAgent(agent.NewProviderServices(sink))

	content := []byte(`{"type":"system","subtype":"task_started","task_id":"t1","tool_use_id":"tu1","task_type":"local_bash","description":"the real title","prompt":"something else\nmultiline"}`)
	agent.HandleOutput(content)

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "the real title", tasks[0].Title,
		"description must win over prompt when both are present")
}

// TestClaudeHandleTaskProgress_PrefersDescriptionOverLastToolName verifies the captured activity selection order:
//   - description.
//   - last_tool_name.
//   - Usage counts.
func TestClaudeHandleTaskProgress_PrefersDescriptionOverLastToolName(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

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

// TestHandleOutput_TaskStartedUpsertsSubagentRegistryAndChild covers task_started for a local_agent with tool_use_id.
// Upsert a Running row under task_id and create its child transcript through EnsureChildAgent.
func TestHandleOutput_TaskStartedUpsertsSubagentRegistryAndChild(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

	content := []byte(`{"type":"system","subtype":"task_started","task_id":"task-sub-1","tool_use_id":"tu-spawn-1","task_type":"local_agent","description":"research the codebase"}`)
	agent.HandleOutput(content)

	// The task_id identifies one Running row, and its description supplies the title.
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	row := tasks[0]
	assert.Equal(t, "task-sub-1", row.RowKey)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "research the codebase", row.Title)
	assert.Equal(t, bgtask.KindSubagent, row.Kind, "local_agent maps to a Subagent-kind row")

	// EnsureChildAgent uses the spawn tool_use ID and creates the child-of-<spawnSpanID> transcript identity.
	// Read the created-child list before calling Child.
	// Child can create a sink even when EnsureChildAgent never creates that child, which would hide a missing creation.
	require.Contains(t, sink.ChildAgentIDs(), "child-of-tu-spawn-1",
		"EnsureChildAgent must have created a child keyed by the spawn span")
	// The child registry row also carries the child agent id and title.
	assert.Equal(t, "child-of-tu-spawn-1", row.ChildAgentID)
}

// TestHandleOutput_TaskStartedLocalBashUpsertsShellNoChild covers task_started for a local_bash command.
// Create a shell registry row without a child transcript because shell rows own no transcript.
func TestHandleOutput_TaskStartedLocalBashUpsertsShellNoChild(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

	content := []byte(`{"type":"system","subtype":"task_started","task_id":"task-shell-1","tool_use_id":"tu-shell-1","task_type":"local_bash","description":"long build"}`)
	agent.HandleOutput(content)

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	row := tasks[0]
	assert.Equal(t, "task-shell-1", row.RowKey)
	assert.Equal(t, bgtask.KindShell, row.Kind, "local_bash maps to a Shell-kind row")
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "long build", row.Title)
	// Claude Code 2.1.220 reports a background shell command as its task_started description, such as "sleep 2 && echo BG-MARKER".
	// The command already supplies the title.
	// Copying it to Description also repeated that title in the row's secondary line.
	assert.Empty(t, row.Description, "description must not echo the title")
	// The shell kind alone does not prove that this title is a command.
	// BashTool selects description || command, so a model description takes precedence over the raw command.
	// task_started forwards only that selected string and supplies no discriminator.
	// Marking it as a command would incorrectly display model prose in monospace.
	assert.False(t, row.TitleIsCommand, "Claude cannot tell its shell title from prose")
	// No child transcript pre-created for a shell.
	assert.Equal(t, "", row.ChildAgentID, "local_bash must not EnsureChildAgent")
}

// The same rule for a subagent row: its secondary line comes from
// task_progress activity, so task_started must not pre-fill it with the title.
func TestHandleOutput_TaskStartedLeavesTheDescriptionEmpty(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))
	agent.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-1","tool_use_id":"tu-1","task_type":"local_agent","description":"SCAN triage angle"}`))

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, "SCAN triage angle", tasks[0].Title)
	assert.Empty(t, tasks[0].Description)
}

// TestHandleOutput_TaskStartedUsesWorkflowKindAndGroup verifies that a
// local_workflow event uses the workflow registry kind and carries its group.
func TestHandleOutput_TaskStartedUsesWorkflowKindAndGroup(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

	content := []byte(`{"type":"system","subtype":"task_started","task_id":"wf-1","task_type":"local_workflow","workflow_name":"release","description":"cut release"}`)
	agent.HandleOutput(content)

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	row := tasks[0]
	assert.Equal(t, "wf-1", row.RowKey)
	assert.Equal(t, bgtask.KindWorkflow, row.Kind, "local_workflow maps to a Workflow-kind row")
	assert.Equal(t, "workflow:release", row.GroupKey, "GroupKey derives from workflow_name")
	assert.Equal(t, "release", row.GroupLabel, "GroupLabel is the workflow_name")
	assert.Equal(t, "cut release", row.Title)
	assert.Empty(t, row.ChildAgentID, "a workflow has no child transcript")
}

// TestHandleOutput_TaskProgressUpdatesActivityOnly verifies that task_progress
// only refreshes the activity (ActiveForm) and never creates a new row or
// changes the status off Running.
func TestHandleOutput_TaskProgressUpdatesActivityOnly(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

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
		{"completed", "completed", bgtask.StatusSucceeded},
		{"failed", "failed", bgtask.StatusFailed},
		{"stopped", "stopped", bgtask.StatusStopped},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &outputTestSink{}
			agent := newTestAgent(agent.NewProviderServices(sink))

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

// TestHandleOutput_TaskNotificationUnknownStatusLeavesRowRunning verifies that an unknown task_notification status leaves the task open.
// Reject an unknown mapping rather than treating its zero value as a chosen outcome.
// StatusUnspecified is zero; StatusPending is a separate explicit status.
func TestHandleOutput_TaskNotificationUnknownStatusLeavesRowRunning(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

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

// TestHandleOutput_DuplicateTaskStartedIsIdempotent verifies that replaying task_started leaves exactly one registry row.
// The shared row_key makes the second upsert merge into the first.
func TestHandleOutput_DuplicateTaskStartedIsIdempotent(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

	started := []byte(`{"type":"system","subtype":"task_started","task_id":"t-dup","tool_use_id":"tu-dup","task_type":"local_bash","description":"once is enough"}`)
	agent.HandleOutput(started)
	agent.HandleOutput(started)

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1, "duplicate task_started must merge into a single registry row")
	assert.Equal(t, "t-dup", tasks[0].RowKey)
	assert.Equal(t, bgtask.StatusRunning, tasks[0].Status)
	assert.Equal(t, "once is enough", tasks[0].Title)
}

// TestHandleOutput_SubagentAssistantRoutesToChildTranscript first registers the spawning tool_use ID through task_started.
// A forwarded assistant envelope with parent_tool_use_id then enters only the child transcript and preserves the parent transcript.
func TestHandleOutput_SubagentAssistantRoutesToChildTranscript(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

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
	child := sink.Child("child-of-tu-route")
	msgs := child.Messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, msgs[0].Source)
	assert.Equal(t, "tu-route", msgs[0].ParentSpanID)
	assert.Equal(t, "tu-child-1", msgs[0].SpanID)
	assert.Equal(t, "Read", msgs[0].SpanType)
}

// TestHandleOutput_SubagentResultRoutesAsChildTurnEnd covers a forwarded child result with parent_tool_use_id.
// Persist its turn-end divider through PersistTurnEnd instead of an ordinary PersistMessage, and close its registry row.
func TestHandleOutput_SubagentResultRoutesAsChildTurnEnd(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	agent := newTestAgent(agent.NewProviderServices(sink))

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
	child := sink.Child("child-of-tu-result")
	msgs := child.Messages()
	require.Len(t, msgs, 1)
	assert.True(t, msgs[0].TurnEnd, "forwarded result routes through PersistTurnEnd into the child transcript")

	// The registry row closes as Succeeded for a result without an error.
	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.StatusSucceeded, tasks[0].Status, "subagent result closes the registry row")
}

// claudeSpanForEnvelope resolves envelope spans for both transcripts.
// An envelope without a tool block attaches to no span, including these cases:
//   - Plain assistant text.
//   - A result.
//   - An unknown type.
func TestClaudeSpanForEnvelope(t *testing.T) {
	t.Parallel()

	spanTypeFor := func(id string) string {
		if id == "tu-known" {
			return "Read"
		}
		return ""
	}
	parse := func(t *testing.T, raw string) *messageEnvelope {
		t.Helper()
		var env messageEnvelope
		require.NoError(t, json.Unmarshal([]byte(raw), &env))
		return &env
	}

	t.Run("assistant takes the first tool_use", func(t *testing.T) {
		env := parse(t, `{"message":{"content":[
			{"type":"text","text":"hi"},
			{"type":"tool_use","id":"tu-a","name":"Read"},
			{"type":"tool_use","id":"tu-b","name":"Bash"}
		]}}`)
		id, typ, closing := claudeSpanForEnvelope(claudeMsgTypeAssistant, env, spanTypeFor)
		assert.Equal(t, "tu-a", id, "the FIRST tool_use decides the row's span")
		assert.Equal(t, "Read", typ)
		assert.False(t, closing)
	})

	t.Run("user takes the first tool_result and looks its type up", func(t *testing.T) {
		env := parse(t, `{"message":{"content":[{"type":"tool_result","tool_use_id":"tu-known"}]}}`)
		id, typ, closing := claudeSpanForEnvelope(claudeMsgTypeUser, env, spanTypeFor)
		assert.Equal(t, "tu-known", id)
		assert.Equal(t, "Read", typ, "the type comes from the transcript's own tracker")
		assert.True(t, closing, "a tool_result closes its span")
	})

	t.Run("an unknown tool_use_id yields a blank type, not a guess", func(t *testing.T) {
		env := parse(t, `{"message":{"content":[{"type":"tool_result","tool_use_id":"tu-gone"}]}}`)
		id, typ, closing := claudeSpanForEnvelope(claudeMsgTypeUser, env, spanTypeFor)
		assert.Equal(t, "tu-gone", id)
		assert.Empty(t, typ)
		assert.True(t, closing)
	})

	t.Run("no tool block means no span", func(t *testing.T) {
		for name, raw := range map[string]string{
			"plain text":      `{"message":{"content":[{"type":"text","text":"hi"}]}}`,
			"empty content":   `{"message":{"content":[]}}`,
			"blank tool id":   `{"message":{"content":[{"type":"tool_use","id":"","name":"Read"}]}}`,
			"blank result id": `{"message":{"content":[{"type":"tool_result","tool_use_id":""}]}}`,
		} {
			t.Run(name, func(t *testing.T) {
				msgType := claudeMsgTypeAssistant
				if name == "blank result id" {
					msgType = claudeMsgTypeUser
				}
				id, typ, closing := claudeSpanForEnvelope(msgType, parse(t, raw), spanTypeFor)
				assert.Empty(t, id)
				assert.Empty(t, typ)
				assert.False(t, closing)
			})
		}
	})

	t.Run("a result envelope belongs to no span", func(t *testing.T) {
		env := parse(t, `{"message":{"content":[{"type":"tool_use","id":"tu-a","name":"Read"}]}}`)
		id, _, closing := claudeSpanForEnvelope(claudeMsgTypeResult, env, spanTypeFor)
		assert.Empty(t, id, "a turn-end envelope is not a tool row")
		assert.False(t, closing)
	})
}

// /clear and plan exit start a new conversation while retaining the session.
// LeapMux reports that boundary through the shared context_cleared notice, so the same transcript rule renders it for every provider.
func TestHandleOutput_ConversationResetPersistsContextCleared(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"conversation_reset","new_conversation_id":"conv-2"}`))

	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, notifications[0].Source)
	assert.JSONEq(t, `{"type":"context_cleared"}`, string(notifications[0].Content))
	// The frame is a notice, not a transcript row: nothing is persisted as a message.
	assert.Empty(t, sink.Messages())
}
