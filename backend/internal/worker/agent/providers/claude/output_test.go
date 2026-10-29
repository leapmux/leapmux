package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"

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
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, schedule.Kind)
	assert.Equal(t, time.Unix(1893456000, 0).UTC(), schedule.ResetAt)
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
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, sink.LastAutoCancel())
	assert.Equal(t, `Claude reported status "allowed" for the five_hour usage limit`, sink.LastAutoCancelReason())
	assert.Equal(t, 0, sink.AutoScheduleCount())
}

// An event without rateLimitType still cancels, and its reason states the
// "unknown" type that the session info also shows.
func TestClaudeRateLimitEvent_AllowedWithoutATypeStatesAnUnknownLimit(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`))

	require.Equal(t, 1, sink.AutoCancelCount())
	assert.Equal(t, `Claude reported status "allowed" for the unknown usage limit`, sink.LastAutoCancelReason())
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

	assert.Equal(t, 0, sink.AutoScheduleCount(), "an allowed_warning event must not schedule an automatic retry")
	require.Equal(t, 1, sink.AutoCancelCount())
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, sink.LastAutoCancel())
	assert.Equal(t, `Claude reported status "allowed_warning" for the seven_day usage limit`, sink.LastAutoCancelReason())
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
	assert.Equal(t, `Claude reported overage status "allowed" for the seven_day usage limit`, sink.LastAutoCancelReason(),
		"the reason states the overage status that lifted the block, not the rejected base status")
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
	assert.Equal(t, time.Unix(1893460000, 0).UTC(), sink.LastAutoSchedule().ResetAt)
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
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, sink.LastAutoSchedule().Kind)

	a.HandleOutput([]byte(`{
		"type":"result",
		"is_error":false,
		"result":"ok"
	}`))

	require.Equal(t, 1, sink.AutoCancelCount())
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, sink.LastAutoCancel())
	assert.Equal(t, providerkit.TurnEndedWithoutRetryableFailure, sink.LastAutoCancelReason())
}

func TestClaudeResult_IdleTimeoutPrefixSchedulesServerErrorAutoRetry(t *testing.T) {
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
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, schedule.Kind)
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

func TestClaudeTurnToolUses_ResetAtEachResult(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"running"}`))
	for turn := range 2 {
		a.HandleOutput([]byte(fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call-%d","name":"Read","input":{"file_path":"README.md"}}]}}`, turn)))
		a.HandleOutput([]byte(`{"type":"result","subtype":"success"}`))
		messages := sink.Messages()
		var metadata struct {
			Count int `json:"num_tool_uses"`
		}
		require.NoError(t, json.Unmarshal(messages[len(messages)-1].Metadata, &metadata))
		assert.Equal(t, 1, metadata.Count)
		assert.Zero(t, a.TurnToolUses)
	}
}

func TestExtractAndBroadcastUsage_DebouncesOnTheProcessClock(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	sink := &agenttest.Sink{}
	a := &Agent{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{Clock: clock}), sink: agent.NewProviderServices(sink)}
	frame := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"answer"}],"usage":{"input_tokens":100,"output_tokens":10}}}`)
	a.HandleOutput(frame)
	require.Len(t, sink.SessionInfoValues("context_usage"), 1)
	clock.Advance(9 * time.Second).MustWait(testutil.DeadlineContext(t))
	a.HandleOutput(frame)
	assert.Len(t, sink.SessionInfoValues("context_usage"), 1)
	clock.Advance(time.Second).MustWait(testutil.DeadlineContext(t))
	a.HandleOutput(frame)
	assert.Len(t, sink.SessionInfoValues("context_usage"), 2)
}

func TestClaudeResult_BareOverloadedSchedulesServerErrorAutoRetry(t *testing.T) {
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
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, schedule.Kind)
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

// --- The automatic-retry verdict of a `result` ---

// claudeTurnEndRow is the timeline entry of the `result` row.
var claudeTurnEndRow = agenttest.TimelineEntry{Op: "turn_end"}

// claudeServerErrorScheduled is the timeline entry of a scheduled server-error retry.
var claudeServerErrorScheduled = agenttest.TimelineEntry{
	Op: "schedule_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
}

// claudeServerErrorCancelled is the timeline entry of a server-error retry
// that a turn end without a retryable failure cancels.
var claudeServerErrorCancelled = agenttest.TimelineEntry{
	Op: "cancel_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
	Reason: providerkit.TurnEndedWithoutRetryableFailure,
}

// A turn that succeeded can end on text that starts like an API error: the
// model may quote one. Only a `result` that states a failure (`is_error`) can
// schedule a retry.
func TestClaudeResult_ASuccessThatQuotesAnAPIErrorCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{"type":"result","subtype":"success","is_error":false,"num_turns":1,"stop_reason":"end_turn","terminal_reason":"completed","api_error_status":null,"result":"API Error: 503 upstream connect error is what the log shows."}`))

	assert.Equal(t, []agenttest.TimelineEntry{claudeTurnEndRow, claudeServerErrorCancelled}, sink.Timeline())
}

// A turn that the user stopped is never retried, even when the turn ended on a
// retryable failure before the CLI read the stop.
func TestClaudeResult_AStoppedTurnCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		frame string
	}{
		{"the stop took effect", claudeResultAbortedStreaming},
		{"the turn failed on an overload before the CLI read the stop", `{"type":"result","subtype":"success","is_error":true,"duration_ms":52,"num_turns":1,"stop_reason":"stop_sequence","terminal_reason":"api_error","api_error_status":529,"result":"API Error: 529 Overloaded"}`},
		{"the turn failed on the network, and the frame states no reason", `{"type":"result","subtype":"success","is_error":true,"result":"API Error: Connection lost mid-response. The response above may be incomplete."}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &outputTestSink{}
			a := newTestAgent(agent.NewProviderServices(sink))

			a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"PARTIAL"}]}}`))
			a.noteInterruptRequested()
			a.HandleOutput([]byte(tc.frame))

			assert.Equal(t, []agenttest.TimelineEntry{{Op: "message"}, claudeTurnEndRow, claudeServerErrorCancelled}, sink.Timeline())
		})
	}
}

// The network texts are real (R/transcripts-claude.md, Claude Code 2.1.289).
// The session files store no `result`, so each frame wraps its text in the
// `result` shape that the probe recorded for an API failure.
func TestClaudeResult_ANetworkFailureSchedulesAServerErrorRetry(t *testing.T) {
	t.Parallel()

	for _, text := range []string{
		"API Error: Connection refused — a firewall or proxy may be blocking it (ECONNREFUSED)",
		"API Error: Can't reach the API server — check your internet or DNS (ENOTFOUND)",
		"API Error: Connection lost mid-response. The response above may be incomplete.",
	} {
		t.Run(text, func(t *testing.T) {
			t.Parallel()
			sink := &outputTestSink{}
			a := newTestAgent(agent.NewProviderServices(sink))

			frame, err := json.Marshal(map[string]any{
				"type": "result", "subtype": "success", "is_error": true, "num_turns": 1,
				"stop_reason": "stop_sequence", "terminal_reason": "api_error", "api_error_status": nil, "result": text,
			})
			require.NoError(t, err)
			a.HandleOutput(frame)

			assert.Equal(t, []agenttest.TimelineEntry{claudeTurnEndRow, claudeServerErrorScheduled}, sink.Timeline())
			require.Equal(t, 1, sink.AutoScheduleCount())
			assert.JSONEq(t, string(frame), string(sink.LastAutoSchedule().SourcePayload), "the retry carries the frame that failed")
		})
	}
}

// A 429 that the CLI could not outlast and that states no usage limit
// schedules a server-error retry (decision 3).
func TestClaudeResult_APlainRateLimitSchedulesAServerErrorRetry(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{"type":"result","subtype":"success","is_error":true,"num_turns":1,"stop_reason":"stop_sequence","terminal_reason":"api_error","api_error_status":429,"result":"API Error: 429 {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"Number of concurrent connections has exceeded your rate limit. Please try again later.\"}}"}`))

	assert.Equal(t, []agenttest.TimelineEntry{claudeTurnEndRow, claudeServerErrorScheduled}, sink.Timeline())
}

// claudeUsageLimitResult is the `result` of the LeapMux database (DB #9 of
// R/transcripts-codex-leapmuxdb.md, row 2026-10-24T19:55:05Z). The export
// replaced the objects of `usage`, `modelUsage`, `subagent_stats`, and
// `model_usage_since_last_result` with a placeholder string, so this copy
// leaves them out.
const claudeUsageLimitResult = `{"duration_api_ms": 22161158, "stop_reason": "stop_sequence", "session_id": "de93f3cf-a9f6-4b24-900b-7689ab510577", "total_cost_usd": 308.46059859999974, "permission_denials": [], "terminal_reason": "api_error", "fast_mode_state": "off", "fast_mode_disabled_reason": "sdk_opt_in_required", "origin": {"kind": "task-notification", "producer": "session-task", "runId": "0mvmsqs5t-22f76850"}, "safety_stops": 0, "is_error": true, "num_turns": 41, "subtype": "success", "api_error_status": 429, "api_error_origin": "first_party", "api_error": "usage_limit_reached", "result": "You've hit your session limit · resets 5:10am (Asia/Seoul)", "type": "result", "duration_ms": 826117, "uuid": "279138af-1537-4631-9d11-8d571727aa8f", "queued_turn_count": 0, "result_index": 40}`

// The usage limit itself stays with rate_limit_event, which states its reset.
// The `result` of that limit is a 429, and it must not also schedule a
// server-error retry that would hit the same limit again and again.
func TestClaudeResult_TheUsageLimitResultSchedulesNoServerErrorRetry(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		frame string
	}{
		{"the database result with the usage-limit code", claudeUsageLimitResult},
		// The same frame from a CLI that ended the turn at its blocking limit,
		// with no usage-limit code.
		{"the blocking limit without the usage-limit code", strings.Replace(
			strings.Replace(claudeUsageLimitResult, `"api_error": "usage_limit_reached", `, "", 1),
			`"terminal_reason": "api_error"`, `"terminal_reason": "blocking_limit"`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &outputTestSink{}
			a := newTestAgent(agent.NewProviderServices(sink))

			a.HandleOutput([]byte(tc.frame))

			assert.Equal(t, []agenttest.TimelineEntry{claudeTurnEndRow, claudeServerErrorCancelled}, sink.Timeline())
		})
	}
}

// claudeRejectedRateLimitEvent carries the rate_limit_info of DB #8 of
// R/transcripts-codex-leapmuxdb.md (row 2026-10-28T00:57:33Z).
const claudeRejectedRateLimitEvent = `{"type":"rate_limit_event","rate_limit_info":{"status": "rejected", "resetsAt": 1793160000, "rateLimitType": "five_hour", "overageStatus": "rejected", "overageDisabledReason": "out_of_credits", "isUsingOverage": false, "unifiedWindows": {"five_hour": {"utilization": 1, "resetsAt": 1793160000, "status": "rejected", "surpassedThreshold": 1}, "seven_day": {"utilization": 0.09, "resetsAt": 1793631600, "status": "allowed"}}}}`

// claudeAllowedRateLimitEvent is the `allowed` event of DB #10 of
// R/transcripts-codex-leapmuxdb.md.
const claudeAllowedRateLimitEvent = `{"type": "rate_limit_event", "rate_limit_info": {"status": "allowed", "resetsAt": 1792836600, "rateLimitType": "five_hour", "overageStatus": "rejected", "overageDisabledReason": "out_of_credits", "isUsingOverage": false, "unifiedWindows": {"seven_day": {"utilization": 0.13, "resetsAt": 1793026800, "status": "allowed"}}}, "uuid": "941b1188-5c35-4403-9d6e-29f90ceade42", "session_id": "de93f3cf-a9f6-4b24-900b-7689ab510577"}`

// claudePlainRateLimitResult is a 429 `result` with no usage-limit code, as a
// CLI before 2.1.302 writes the usage limit.
const claudePlainRateLimitResult = `{"type":"result","subtype":"success","is_error":true,"num_turns":1,"stop_reason":"stop_sequence","terminal_reason":"api_error","api_error_status":429,"result":"You've hit your session limit · resets 1pm (Asia/Seoul)"}`

var (
	claudeUsageLimitScheduled = agenttest.TimelineEntry{
		Op: "schedule_usage_limit", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT,
		ResetAt: time.Unix(1793160000, 0).UTC(),
	}
	claudeUsageLimitLifted = agenttest.TimelineEntry{
		Op: "cancel_usage_limit", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT,
		Reason: `Claude reported status "allowed" for the five_hour usage limit`,
	}
	claudeNotificationRow = agenttest.TimelineEntry{Op: "notification"}
)

// A CLI before 2.1.302 writes no usage-limit code, so the rejected event of the
// turn states that the 429 is the usage limit.
func TestClaudeResult_ARateLimitAfterARejectedEventSchedulesNoServerErrorRetry(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(claudeRejectedRateLimitEvent))
	a.HandleOutput([]byte(claudePlainRateLimitResult))

	assert.Equal(t, []agenttest.TimelineEntry{
		claudeNotificationRow, claudeUsageLimitScheduled,
		claudeTurnEndRow, claudeServerErrorCancelled,
	}, sink.Timeline())
}

// The latest rate_limit_event of the turn states the block. A later event that
// allows the request supersedes an earlier rejection, and the reverse.
func TestClaudeResult_TheLatestRateLimitEventDecides(t *testing.T) {
	t.Parallel()

	t.Run("an allowed event after a rejected one makes the 429 a rate limit", func(t *testing.T) {
		t.Parallel()
		sink := &outputTestSink{}
		a := newTestAgent(agent.NewProviderServices(sink))

		a.HandleOutput([]byte(claudeRejectedRateLimitEvent))
		a.HandleOutput([]byte(claudeAllowedRateLimitEvent))
		a.HandleOutput([]byte(claudePlainRateLimitResult))

		assert.Equal(t, []agenttest.TimelineEntry{
			claudeNotificationRow, claudeUsageLimitScheduled,
			claudeNotificationRow, claudeUsageLimitLifted,
			claudeTurnEndRow, claudeServerErrorScheduled,
		}, sink.Timeline())
	})
	t.Run("a rejected event after an allowed one makes the 429 the usage limit", func(t *testing.T) {
		t.Parallel()
		sink := &outputTestSink{}
		a := newTestAgent(agent.NewProviderServices(sink))

		a.HandleOutput([]byte(claudeAllowedRateLimitEvent))
		a.HandleOutput([]byte(claudeRejectedRateLimitEvent))
		a.HandleOutput([]byte(claudePlainRateLimitResult))

		assert.Equal(t, []agenttest.TimelineEntry{
			claudeNotificationRow, claudeUsageLimitLifted,
			claudeNotificationRow, claudeUsageLimitScheduled,
			claudeTurnEndRow, claudeServerErrorCancelled,
		}, sink.Timeline())
	})
}

// The `result` spends the mark of a rejected event, so the next turn's 429
// reads as the rate limit that it states.
func TestClaudeResult_ARejectedEventMarksOnlyItsOwnTurn(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(claudeRejectedRateLimitEvent))
	a.HandleOutput([]byte(claudePlainRateLimitResult))
	a.HandleOutput([]byte(claudePlainRateLimitResult))

	assert.Equal(t, []agenttest.TimelineEntry{
		claudeNotificationRow, claudeUsageLimitScheduled,
		claudeTurnEndRow, claudeServerErrorCancelled,
		claudeTurnEndRow, claudeServerErrorScheduled,
	}, sink.Timeline())
}

// A transient failure retries even when a rejected rate_limit_event marked
// the turn: the mark qualifies only a 429, and an overload is no usage limit.
func TestClaudeResult_ATransientFailureRetriesDespiteARejectedEvent(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(claudeRejectedRateLimitEvent))
	a.HandleOutput([]byte(`{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":529,"result":"API Error: 529 Overloaded"}`))

	assert.Equal(t, []agenttest.TimelineEntry{
		claudeNotificationRow, claudeUsageLimitScheduled,
		claudeTurnEndRow, claudeServerErrorScheduled,
	}, sink.Timeline())
}

// The mark of a rejected event cannot outlive its turn, also when no `result`
// spends it: the result edge of any turn end clears it.
func TestClaudeResult_TheResultEdgeClearsTheRejectedMark(t *testing.T) {
	t.Parallel()

	a := newTestAgent(agent.NewProviderServices(&outputTestSink{}))
	a.HandleOutput([]byte(claudeRejectedRateLimitEvent))
	a.recordClaudeResultEdge()

	assert.False(t, a.takeTurnRateLimitRejected())
}

func TestClaudeResult_A400CancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(claudeResultAPIError))

	assert.Equal(t, []agenttest.TimelineEntry{claudeTurnEndRow, claudeServerErrorCancelled}, sink.Timeline())
}

// A billing block that the vendor text states cancels both retries: waiting
// does not lift it.
func TestClaudeResult_ACreditBlockCancelsBothRetries(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":400,"result":"API Error: 400 {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits.\"}}"}`))

	reason := "the vendor reported a billing or plan block, which only a billing or plan change lifts"
	assert.Equal(t, []agenttest.TimelineEntry{
		claudeTurnEndRow,
		{Op: "cancel_usage_limit", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, Reason: reason},
		{Op: "cancel_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, Reason: reason},
	}, sink.Timeline())
}

// A third-party endpoint states its usage limit in the text. The verdict reads
// the reset on the agent's clock, so the test pins it at the capture's time.
func TestClaudeResult_AVendorUsageLimitSchedulesAtItsStatedReset(t *testing.T) {
	t.Parallel()

	clock := testutil.NewQuartzMock(t)
	clock.Set(time.Date(2026, 9, 11, 16, 19, 3, 0, time.UTC)).MustWait(testutil.DeadlineContext(t))
	sink := &outputTestSink{}
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "test-agent", Clock: clock}),
		sink:    agent.NewProviderServices(sink),
	}

	a.HandleOutput([]byte(`{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":429,"result":"API Error: 429 {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"code\":\"1308\",\"message\":\"[1308][Usage limit reached for 5 hour. Your limit will reset at 2026-09-12 00:35:41][20260912001903b4955568412247d7]\"},\"request_id\":\"20260912001903b4955568412247d7\"}"}`))

	assert.Equal(t, []agenttest.TimelineEntry{
		claudeTurnEndRow,
		{
			Op: "schedule_usage_limit", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT,
			ResetAt: time.Date(2026, 9, 11, 16, 35, 42, 0, time.UTC),
		},
		{
			Op: "cancel_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
			Reason: "the turn ended on a usage limit that lifts at a stated time",
		},
	}, sink.Timeline())
}

// A subagent's turn end is not a turn end of the session, so it applies no
// verdict: the root `result` decides.
func TestClaudeResult_AChildResultAppliesNoVerdict(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"t-fail","tool_use_id":"tu-fail","task_type":"local_agent","description":"child task"}`))
	a.HandleOutput([]byte(`{"type":"result","parent_tool_use_id":"tu-fail","subtype":"success","is_error":true,"result":"API Error: 529 Overloaded"}`))

	assert.Zero(t, sink.AutoScheduleCount(), "the parent schedules nothing")
	assert.Zero(t, sink.AutoCancelCount(), "the parent cancels nothing")
	child := sink.Child("child-of-tu-fail")
	assert.Equal(t, []agenttest.TimelineEntry{claudeTurnEndRow}, child.Timeline(), "the child records only its turn end")
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
	// The telemetry filter must not discard other system lines. An unknown subtype is the sharpest case: its stored row is the signal that a new frame exists.
	agent.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "some_future_event",
		"session_id": "a40e65f9-f1f2-4e8b-b089-abc9692345b2"
	}`))

	assert.Equal(t, 1, sink.MessageCount(), "non-thinking system message must persist")
	assert.Equal(t, 0, sink.SessionInfoCount(), "non-thinking system message must not broadcast thinking session info")
	assert.Equal(t, 0, sink.SessionIDCount(), "only init moves the session")
}

// TestClaudeTurnFailure_ClassifiesTheResultText reads the `result` text of a
// failed turn (`is_error: true`) with no other signal. Each transient form
// schedules a server-error retry, and every other text gives none.
func TestClaudeTurnFailure_ClassifiesTheResultText(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		text string
		want providerkit.TurnFailureKind
	}{
		{"a 500", "API Error: 500 Internal Server Error", providerkit.TurnFailureTransient},
		{"a 502", "API Error: 502 Bad Gateway", providerkit.TurnFailureTransient},
		{"a 529", "API Error: 529 Overloaded", providerkit.TurnFailureTransient},
		{"a bare 599", "API Error: 599", providerkit.TurnFailureTransient},
		{"a 5xx with other punctuation", "API Error - 502 Bad Gateway", providerkit.TurnFailureTransient},
		{"a 5xx with repeated punctuation", "API Error:: 529 Overloaded", providerkit.TurnFailureTransient},
		{"a 5xx in lowercase", "api error: 500 internal server error", providerkit.TurnFailureTransient},
		// Claude Code 2.1.310 appends this advice to a 5xx.
		{"a 5xx with the advice of 2.1.310", "API Error: 500 Internal server error. This is a server-side issue, usually temporary — try again in a moment.", providerkit.TurnFailureTransient},
		{"an idle timeout", "API Error: Stream idle timeout", providerkit.TurnFailureTransient},
		{"an idle timeout with a partial response", "API Error: Stream idle timeout - partial response received", providerkit.TurnFailureTransient},
		{"an idle timeout with other punctuation", "API Error - Stream idle timeout - partial response received", providerkit.TurnFailureTransient},
		{"an idle timeout with repeated punctuation", "API Error:: Stream idle timeout", providerkit.TurnFailureTransient},
		{"an idle timeout in lowercase", "api error: stream idle timeout", providerkit.TurnFailureTransient},
		{"a bare overload", "API Error: Overloaded", providerkit.TurnFailureTransient},
		{"an overload with other punctuation", "API Error - Overloaded", providerkit.TurnFailureTransient},
		{"an overload with repeated punctuation", "API Error:: Overloaded", providerkit.TurnFailureTransient},
		{"an overload with trailing text", "API Error: Overloaded - please retry", providerkit.TurnFailureTransient},
		{"an overload in lowercase", "API Error: overloaded", providerkit.TurnFailureTransient},
		{"an overload in uppercase", "API ERROR: OVERLOADED", providerkit.TurnFailureTransient},
		{"repeated overloads", "API Error: Repeated 529 Overloaded errors. The API is at capacity — this is usually temporary. Try again in a moment.", providerkit.TurnFailureTransient},
		// The real texts of R/transcripts-claude.md (Claude Code 2.1.289).
		{"a refused connection", "API Error: Connection refused — a firewall or proxy may be blocking it (ECONNREFUSED)", providerkit.TurnFailureTransient},
		{"an unreachable server", "API Error: Can't reach the API server — check your internet or DNS (ENOTFOUND)", providerkit.TurnFailureTransient},
		{"a connection lost mid-response", "API Error: Connection lost mid-response. The response above may be incomplete.", providerkit.TurnFailureTransient},
		// The wordings of Claude Code 2.1.310, from its binary.
		{"an unreachable server with a typographic apostrophe", "API Error: Can’t reach the API server — check your internet or DNS (EAI_AGAIN)", providerkit.TurnFailureTransient},
		{"a dropped connection", "API Error: Connection dropped (ECONNRESET)", providerkit.TurnFailureTransient},
		{"a lost connection", "API Error: Connection to the API was lost (ECONNRESET). This is usually temporary — try again.", providerkit.TurnFailureTransient},
		{"no internet route", "API Error: No internet route — check your connection or VPN (ENETUNREACH)", providerkit.TurnFailureTransient},
		{"a connection that failed with a code", "API Error: Unable to connect to API (EPROTO)", providerkit.TurnFailureTransient},
		{"a connection that failed with no code", "API Error: Unable to connect to API. Check your internet connection", providerkit.TurnFailureTransient},
		{"a connection that failed in a terse form", "API Error: Unable to connect to API", providerkit.TurnFailureTransient},
		{"a request that timed out", "API Error: Request timed out.", providerkit.TurnFailureTransient},
		{"a connection lost before a response", "API Error: Connection lost before a response was produced. Try again.", providerkit.TurnFailureTransient},
		{"a server error mid-response", "API Error: Server error mid-response. The response above may be incomplete.", providerkit.TurnFailureTransient},
		{"a response that stopped arriving", "API Error: The response stopped arriving. The response above may be incomplete.", providerkit.TurnFailureTransient},
		{"a response that stalled", "API Error: The response stalled before a response was produced. Try again.", providerkit.TurnFailureTransient},
		{"a response that never fully arrived", "API Error: Part of the response never arrived and no response was produced. Try again.", providerkit.TurnFailureTransient},
		{"a malformed stream", "API Error: The response stream was malformed. The response above may be incomplete.", providerkit.TurnFailureTransient},
		{"a computer that went to sleep", "API Error: Your computer went to sleep mid-response. The response above may be incomplete.", providerkit.TurnFailureTransient},
		{"a computer that went to sleep before a response", "API Error: Your computer went to sleep before a response was produced. Try again.", providerkit.TurnFailureTransient},
		// A certificate or proxy problem lasts until somebody changes the setup.
		{"a certificate problem", "API Error: Unable to connect to API: SSL certificate has expired", providerkit.TurnFailureNotRetryable},
		{"a proxy that refused the tunnel", "API Error: Couldn't connect through your proxy (ERR_PROXY_TUNNEL) — the proxy refused the tunnel: check its credentials and that it allows this host", providerkit.TurnFailureNotRetryable},
		{"a 400", "API Error: 400 Bad Request", providerkit.TurnFailureNotRetryable},
		{"a 400 of the probe", "API Error: 400 NATIVE ERROR MARKER", providerkit.TurnFailureNotRetryable},
		{"an expired session", "Failed to authenticate: OAuth session expired and could not be refreshed", providerkit.TurnFailureNotRetryable},
		{"a failed compaction", "Prompt is too long · automatic compaction failed: API Error: 400 probe: invalid request (compact-error400)", providerkit.TurnFailureNotRetryable},
		{"a single digit after the prefix", "API Error: 5", providerkit.TurnFailureNotRetryable},
		{"two digits after the prefix", "API Error: 50", providerkit.TurnFailureNotRetryable},
		{"four digits after the prefix", "API Error: 5000", providerkit.TurnFailureNotRetryable},
		{"a 5xx after an alphanumeric separator", "API ErrorX 502 Bad Gateway", providerkit.TurnFailureNotRetryable},
		{"a 5xx with an alphanumeric suffix", "API Error: 500X", providerkit.TurnFailureNotRetryable},
		{"an idle timeout after an alphanumeric separator", "API ErrorX Stream idle timeout", providerkit.TurnFailureNotRetryable},
		{"an idle timeout with an alphanumeric suffix", "API Error: Stream idle timeoutX", providerkit.TurnFailureNotRetryable},
		{"an overload after an alphanumeric separator", "API ErrorX Overloaded", providerkit.TurnFailureNotRetryable},
		{"an overload with an alphanumeric suffix", "API Error: OverloadedX", providerkit.TurnFailureNotRetryable},
		{"an overload without the prefix", "Upstream Overloaded", providerkit.TurnFailureNotRetryable},
		// The vendor parser reads only the body that follows "API Error": any
		// other text is Claude Code's own.
		{"the DeepSeek overload after the prefix", "API Error: Server Overloaded", providerkit.TurnFailureTransient},
		{"the DeepSeek overload without the prefix", "Server Overloaded", providerkit.TurnFailureNotRetryable},
		{"Claude Code's own extra-usage text is no billing block", "You're out of extra usage · resets 3pm (Asia/Seoul)", providerkit.TurnFailureNotRetryable},
		{"a lost connection without the prefix", "Connection lost mid-response. The response above may be incomplete.", providerkit.TurnFailureNotRetryable},
		{"an empty text", "", providerkit.TurnFailureNotRetryable},
		{"plain text", "done", providerkit.TurnFailureNotRetryable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := messageEnvelope{IsError: true, Result: tc.text, TerminalReason: "api_error"}
			assert.Equal(t, tc.want, claudeTurnFailure(&env, claudeTurnSignals{}, observedAt).Kind)
		})
	}
}

// TestClaudeTurnFailure_ReadsTheFrameAndTheTurn decodes each frame through the
// envelope, as the handler does, so a renamed field fails here.
func TestClaudeTurnFailure_ReadsTheFrameAndTheTurn(t *testing.T) {
	t.Parallel()

	// The Z.AI capture of R/vendors.md: the Anthropic-compatible endpoint
	// answered Claude Code with 1308. observedAt is the time of the capture.
	zaiObservedAt := time.Date(2026, 9, 11, 16, 19, 3, 0, time.UTC)
	// The stamp reads 2026-09-12 00:35:41 in UTC+8, and the retry waits one
	// second past it.
	zaiResetAt := time.Date(2026, 9, 11, 16, 35, 42, 0, time.UTC)
	observedAt := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		frame      string
		signals    claudeTurnSignals
		observedAt time.Time
		want       providerkit.TurnFailure
	}{
		{
			name:  "a plain 429 is a rate limit",
			frame: `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":429,"result":"API Error: 429 {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"Number of concurrent connections has exceeded your rate limit. Please try again later.\"}}"}`,
			want:  providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Claude Code reported HTTP 429 after its own retries"},
		},
		{
			name:    "a 429 after a rejected event is the usage limit that the event schedules",
			frame:   `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":429,"result":"You've hit your session limit · resets 1pm (Asia/Seoul)"}`,
			signals: claudeTurnSignals{rateLimitRejected: true},
			want:    providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable},
		},
		{
			name:  "a 429 with the usage-limit code is the usage limit",
			frame: `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":429,"api_error":"usage_limit_reached","result":"You've hit your session limit · resets 5:10am (Asia/Seoul)"}`,
			want:  providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable},
		},
		{
			name:  "a 429 at the blocking limit is not a rate limit",
			frame: `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"blocking_limit","api_error_status":429,"result":"You've hit your session limit · resets 5:10am (Asia/Seoul)"}`,
			want:  providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable},
		},
		{
			name:  "a 400 is not retried",
			frame: claudeResultAPIError,
			want:  providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable},
		},
		{
			name:  "a status of null is no status",
			frame: `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":null,"result":"API Error: 400 probe"}`,
			want:  providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable},
		},
		{
			name:  "a 429 of a turn that did not fail is not retried",
			frame: `{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed","api_error_status":429,"result":"done"}`,
			want:  providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable},
		},
		{
			name:  "a 5xx text of a turn that did not fail is not retried",
			frame: `{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed","result":"API Error: 503 upstream connect error"}`,
			want:  providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable},
		},
		{
			name:    "a stopped turn is not retried, even on a transient failure",
			frame:   `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":529,"result":"API Error: 529 Overloaded"}`,
			signals: claudeTurnSignals{stopRequested: true},
			want:    providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable},
		},
		{
			name:    "a stopped turn is not retried, even on a rate limit",
			frame:   `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":429,"result":"API Error: 429 rate limited"}`,
			signals: claudeTurnSignals{stopRequested: true},
			want:    providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable},
		},
		{
			name:  "a turn that the CLI aborted while it streamed is not retried",
			frame: `{"type":"result","subtype":"error_during_execution","is_error":true,"terminal_reason":"aborted_streaming","result":"API Error: 529 Overloaded"}`,
			want:  providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable},
		},
		{
			name:  "a turn that the CLI aborted while a tool ran is not retried",
			frame: `{"type":"result","subtype":"error_during_execution","is_error":true,"terminal_reason":"aborted_tools","result":"API Error: 529 Overloaded"}`,
			want:  providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable},
		},
		{
			name:       "a Z.AI usage limit with a stamp schedules at its reset",
			frame:      `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":429,"result":"API Error: 429 {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"code\":\"1308\",\"message\":\"[1308][Usage limit reached for 5 hour. Your limit will reset at 2026-09-12 00:35:41][20260912001903b4955568412247d7]\"},\"request_id\":\"20260912001903b4955568412247d7\"}"}`,
			observedAt: zaiObservedAt,
			want: providerkit.TurnFailure{
				Kind: providerkit.TurnFailureUsageLimited, ResetAt: zaiResetAt,
				Cause: "Z.AI reported a usage limit that lifts at a stated time",
			},
		},
		{
			name:       "a Z.AI usage limit outranks a rejected event",
			frame:      `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":429,"result":"API Error: 429 {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"code\":\"1308\",\"message\":\"[1308][Usage limit reached for 5 hour. Your limit will reset at 2026-09-12 00:35:41][20260912001903b4955568412247d7]\"},\"request_id\":\"20260912001903b4955568412247d7\"}"}`,
			signals:    claudeTurnSignals{rateLimitRejected: true},
			observedAt: zaiObservedAt,
			want: providerkit.TurnFailure{
				Kind: providerkit.TurnFailureUsageLimited, ResetAt: zaiResetAt,
				Cause: "Z.AI reported a usage limit that lifts at a stated time",
			},
		},
		{
			name:  "an Anthropic credit block cancels both retries",
			frame: `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":400,"result":"API Error: 400 {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits.\"}}"}`,
			want:  providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the vendor reported a billing or plan block"},
		},
		{
			name:  "an Anthropic spend limit schedules at the stated access time",
			frame: `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":429,"result":"API Error: 429 {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"You have reached your API usage limits: Your workspace has reached its spend limit. You will regain access on 2026-11-01 at 00:00 UTC.\"}}"}`,
			want: providerkit.TurnFailure{
				Kind: providerkit.TurnFailureUsageLimited, ResetAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
				Cause: "Anthropic reported a spend limit that lifts at a stated time",
			},
		},
		{
			name:  "an Anthropic account limit without a reset outranks the 429",
			frame: `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":429,"result":"API Error: 429 {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"This request would exceed your account's rate limit. Please try again later.\"}}"}`,
			want: providerkit.TurnFailure{
				Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowUnstated,
				Cause: "Anthropic reported an account rate limit",
			},
		},
		{
			name:    "a compaction turn is not retried, even on a server error",
			frame:   `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":500,"result":"API Error: 500 Internal Server Error"}`,
			signals: claudeTurnSignals{compaction: true},
			want:    providerkit.CompactionEnd(),
		},
		{
			name:    "a compaction turn is not retried, even on a usage limit that states its reset",
			frame:   `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":429,"result":"API Error: 429 {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"You have reached your API usage limits: Your workspace has reached its spend limit. You will regain access on 2026-11-01 at 00:00 UTC.\"}}"}`,
			signals: claudeTurnSignals{compaction: true},
			want:    providerkit.CompactionEnd(),
		},
		{
			name:    "a compaction turn that the user stopped ends on the compaction verdict",
			frame:   `{"type":"result","subtype":"error_during_execution","is_error":true,"terminal_reason":"aborted_streaming"}`,
			signals: claudeTurnSignals{compaction: true, stopRequested: true},
			want:    providerkit.CompactionEnd(),
		},
		{
			name:    "a successful compaction turn ends on the compaction verdict",
			frame:   `{"type":"result","subtype":"success","is_error":false,"result":""}`,
			signals: claudeTurnSignals{compaction: true},
			want:    providerkit.CompactionEnd(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var env messageEnvelope
			require.NoError(t, json.Unmarshal([]byte(tc.frame), &env))
			at := tc.observedAt
			if at.IsZero() {
				at = observedAt
			}
			assert.Equal(t, tc.want, claudeTurnFailure(&env, tc.signals, at))
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

// --- The system-subtype verdicts: the hidden table and the rendered frames. ---

func TestHandleOutput_HiddenSystemSubtypesAreNeverPersisted(t *testing.T) {
	t.Parallel()

	for _, subtype := range contracts.ClaudeHiddenSystemSubtypeKeys {
		t.Run(subtype, func(t *testing.T) {
			sink := &outputTestSink{}
			a := newTestAgent(agent.NewProviderServices(sink))
			a.HandleOutput([]byte(fmt.Sprintf(`{"type":"system","subtype":%q,"uuid":"u-1","session_id":"s-1"}`, subtype)))
			assert.Empty(t, sink.Messages(), "%s must not persist a message", subtype)
			assert.Empty(t, sink.PersistedNotifications(), "%s must not persist a notification", subtype)
		})
	}
}

func TestHandleOutput_RenderedSystemSubtypesPersist(t *testing.T) {
	t.Parallel()

	fixtures := []struct{ name, line string }{
		{name: "permission_denied", line: `{"type":"system","subtype":"permission_denied","tool_name":"Bash","tool_use_id":"toolu_1","decision_reason_type":"classifier","decision_reason":"the command removes a directory","uuid":"u-1","session_id":"s-1"}`},
		{name: "model_fallback", line: `{"type":"system","subtype":"model_fallback","trigger":"overloaded","original_model":"claude-opus-5-5","fallback_model":"claude-sonnet-5-5","content":"Switched to claude-sonnet-5-5 due to high demand for claude-opus-5-5","uuid":"u-1","session_id":"s-1"}`},
		{name: "model_refusal_fallback", line: `{"type":"system","subtype":"model_refusal_fallback","trigger":"refusal","direction":"retry","scope":"local","original_model":"claude-opus-5-5","fallback_model":"claude-sonnet-5-5","request_id":null,"content":"Switched to claude-sonnet-5-5","uuid":"u-1","session_id":"s-1"}`},
		{name: "model_consent_fallback", line: `{"type":"system","subtype":"model_consent_fallback","choice":"switch_default","original_model":"claude-opus-5-5","fallback_model":"claude-sonnet-5-5","content":"Switched to claude-sonnet-5-5","uuid":"u-1","session_id":"s-1"}`},
		{name: "notification", line: `{"type":"system","subtype":"notification","key":"compact-error","text":"Error compacting conversation","priority":"high","color":"error","uuid":"u-1","session_id":"s-1"}`},
		{name: "informational", line: `{"type":"system","subtype":"informational","content":"Stop hook failed: boom","level":"warning","uuid":"u-1","session_id":"s-1"}`},
		{name: "memory_recall", line: `{"type":"system","subtype":"memory_recall","mode":"select","memories":[{"path":"/m/a.md","scope":"personal"}],"uuid":"u-1","session_id":"s-1"}`},
		{name: "hook_response", line: `{"type":"system","subtype":"hook_response","hook_id":"h1","hook_name":"setup.sh","hook_event":"SessionStart","output":"","stdout":"","stderr":"boom","exit_code":2,"outcome":"error","uuid":"u-1","session_id":"s-1"}`},
		{name: "ui_log", line: `{"type":"system","subtype":"ui_log","plugin":"my-plugin","text":"hello","uuid":"u-1","session_id":"s-1"}`},
		{name: "ui_toast", line: `{"type":"system","subtype":"ui_toast","plugin":"my-plugin","text":"hello","timeout_ms":3000,"uuid":"u-1","session_id":"s-1"}`},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			sink := &outputTestSink{}
			a := newTestAgent(agent.NewProviderServices(sink))
			a.HandleOutput([]byte(fixture.line))
			rows := append(sink.Messages(), sink.PersistedNotifications()...)
			require.Len(t, rows, 1, "%s must persist its row", fixture.name)
			assert.JSONEq(t, fixture.line, string(rows[0].Content))
		})
	}
}

func TestHandleOutput_InitIsConsumedAndNotPersisted(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["goal"],"session_id":"s-1"}`))

	assert.Empty(t, sink.Messages(), "init must not persist a message")
	assert.Empty(t, sink.PersistedNotifications(), "init must not persist a notification")
	assert.Equal(t, 1, sink.SessionIDCount(), "init states the session")
	assert.Equal(t, "s-1", sink.LastSessionID())
}

func TestHandleOutput_ANonInitSystemFrameDoesNotReFireSessionInit(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"s-1"}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"notification","key":"k","text":"hi","priority":"low","uuid":"u-1","session_id":"s-2"}`))

	assert.Equal(t, 1, sink.SessionIDCount(), "only init moves the session")
	assert.Equal(t, "s-1", sink.LastSessionID())
	assert.Equal(t, []string{"s-1"}, sink.StatusActives(), "only init re-broadcasts status active")
}

func TestHandleOutput_InformationalDropsInfoAndPersistsTheRest(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"informational","content":"verbose only","level":"info","uuid":"u-1","session_id":"s-1"}`))
	assert.Empty(t, sink.Messages(), "the interface shows level info in its own transcript mode only")

	a.HandleOutput([]byte(`{"type":"system","subtype":"informational","content":"Stop hook failed: boom","level":"warning","uuid":"u-2","session_id":"s-1"}`))
	assert.Len(t, sink.Messages(), 1)
}

func TestHandleOutput_InformationalWithoutALevelPersists(t *testing.T) {
	t.Parallel()

	// Only the stated level info is dropped. A frame that states no level, or a
	// level of the wrong type, is not known to be verbose-only, so it persists
	// as every level other than info does.
	for name, line := range map[string]string{
		"absent level":   `{"type":"system","subtype":"informational","content":"Stop hook failed: boom","uuid":"u-1","session_id":"s-1"}`,
		"null level":     `{"type":"system","subtype":"informational","content":"Stop hook failed: boom","level":null,"uuid":"u-1","session_id":"s-1"}`,
		"mistyped level": `{"type":"system","subtype":"informational","content":"Stop hook failed: boom","level":3,"uuid":"u-1","session_id":"s-1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sink := &outputTestSink{}
			a := newTestAgent(agent.NewProviderServices(sink))
			a.HandleOutput([]byte(line))
			assert.Len(t, sink.Messages(), 1, "%s must persist its row", name)
		})
	}
}

func TestHandleOutput_HookResponsePersistsOnlyFailures(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"hook_response","hook_id":"h1","hook_name":"setup.sh","hook_event":"SessionStart","output":"","stdout":"","stderr":"","exit_code":0,"outcome":"success","uuid":"u-1","session_id":"s-1"}`))
	assert.Empty(t, sink.Messages(), "a passing hook states nothing to show")

	a.HandleOutput([]byte(`{"type":"system","subtype":"hook_response","hook_id":"h1","hook_name":"setup.sh","hook_event":"SessionStart","output":"","stdout":"","stderr":"","exit_code":null,"outcome":"cancelled","uuid":"u-2","session_id":"s-1"}`))
	assert.Empty(t, sink.Messages(), "a cancelled hook states nothing to show")

	a.HandleOutput([]byte(`{"type":"system","subtype":"hook_response","hook_id":"h1","hook_name":"setup.sh","hook_event":"SessionStart","output":"","stdout":"","stderr":"boom","exit_code":2,"outcome":"error","uuid":"u-3","session_id":"s-1"}`))
	assert.Len(t, sink.Messages(), 1, "a failing hook reports itself")
}

func TestHandleOutput_ModelFallbacksRefreshTheSessionModel(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{"type":"system","subtype":"model_refusal_fallback","trigger":"refusal","direction":"retry","scope":"local","original_model":"claude-opus-5-5","fallback_model":"claude-sonnet-5-5","request_id":null,"content":"Switched to claude-sonnet-5-5","uuid":"u-1","session_id":"s-1"}`))
	assert.Equal(t, 0, sink.SettingsRefreshCount(), "a local fallback serves one response only")

	// The refusal_fable_noretry probe supplies this session fallback shape.
	a.HandleOutput([]byte(`{"type":"system","subtype":"model_refusal_fallback","trigger":"refusal","direction":"sticky","scope":"session","original_model":"claude-fable-5","fallback_model":"claude-opus-4-8","request_id":null,"content":"Switched to claude-opus-4-8","uuid":"u-2","session_id":"s-1"}`))
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "opus[1m]", sink.LastSettingsRefresh().Model)
	assert.Equal(t, "opus[1m]", a.model, "the session model follows the fallback")

	a.HandleOutput([]byte(`{"type":"system","subtype":"model_consent_fallback","choice":"switch_default","original_model":"claude-opus-5-5","fallback_model":"claude-opus-5-5","content":"Switched to claude-opus-5-5","uuid":"u-3","session_id":"s-1"}`))
	require.Equal(t, 2, sink.SettingsRefreshCount())
	assert.Equal(t, "opus[1m]", sink.LastSettingsRefresh().Model)
	assert.Equal(t, "opus[1m]", a.model)

	assert.Len(t, sink.Messages(), 3, "every fallback row still persists")
}

func TestHandleOutput_ModelRefusalFallbackWithoutScopeRefreshesTheSessionModel(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	// An absent scope means a session fallback. Only local leaves the session model unchanged.
	a.HandleOutput([]byte(`{"type":"system","subtype":"model_refusal_fallback","trigger":"refusal","direction":"retry","original_model":"claude-fable-5","fallback_model":"claude-opus-4-8","request_id":null,"content":"Switched to claude-opus-4-8","uuid":"u-1","session_id":"s-1"}`))

	require.Equal(t, 1, sink.SettingsRefreshCount(), "an absent scope is the session-scoped swap")
	assert.Equal(t, "opus[1m]", sink.LastSettingsRefresh().Model)
	assert.Equal(t, "opus[1m]", a.model, "the session model follows the fallback")
	assert.Len(t, sink.Messages(), 1, "the row itself still persists")
}

func TestHandleOutput_ModelFallbackKeepsTheContextWindow(t *testing.T) {
	t.Parallel()
	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"model_refusal_fallback","scope":"session","fallback_model":"claude-opus-4-8"}`))
	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"answer"}],"usage":{"input_tokens":100,"output_tokens":10}}}`))
	usage, ok := sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, int64(1_000_000), usage["context_window"])
}

func TestClaudeUnknownFramesPersist(t *testing.T) {
	t.Parallel()
	agenttest.AssertUnknownFramePersists(t, []agenttest.UnknownFrameCase{
		{Name: "current session", Frame: []byte(`{"type":"leapmux_probe_new_frame","session_id":"s-1"}`)},
		{Name: "other session", Frame: []byte(`{"type":"leapmux_probe_new_frame","session_id":"s-other"}`), OtherSession: true},
		{Name: "no session", Frame: []byte(`{"type":"leapmux_probe_new_frame"}`), OtherSession: true},
	}, func(t *testing.T, tc agenttest.UnknownFrameCase) *agenttest.Sink {
		t.Helper()
		sink := &agenttest.Sink{}
		a := newTestAgent(agent.NewProviderServices(sink))
		a.sessionID = "s-1"
		a.HandleOutput(tc.Frame)
		return sink
	})
}

func TestHandleOutput_SyntheticUserFrameStoresNoRow(t *testing.T) {
	t.Parallel()
	// These texts are trimmed from the named Claude probe runs.
	for _, tc := range []struct{ run, text string }{
		{"skill_allowed", "# Keybindings Skill"},
		{"autocompact_ok", "This session is being continued from a previous conversation that ran out of context."},
		{"goal_met", "Stop hook feedback:\n[Reply with the word OK once]: not yet (evaluation 1)"},
		{"refusal_fable", "Your response above was stopped by a safety classifier"},
	} {
		t.Run(tc.run, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newTestAgent(agent.NewProviderServices(sink))
			a.HandleOutput([]byte(fmt.Sprintf(`{"type":"user","isSynthetic":true,"parent_tool_use_id":null,"message":{"role":"user","content":[{"type":"text","text":%q}]}}`, tc.text)))
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.PersistedNotifications())
		})
	}
}

func TestHandleOutput_SyntheticToolResultKeepsItsRow(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	raw := []byte(`{"type":"user","isSynthetic":true,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call","content":"done"}]}}`)
	a.HandleOutput(raw)
	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, string(raw), string(sink.Messages()[0].Content))
}

func claudeAPIErrorForTest(t *testing.T, text, parent string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type": "assistant", "is_api_error_message": true, "parent_tool_use_id": parent,
		"message": map[string]any{"role": "assistant", "model": "<synthetic>", "content": []map[string]string{{"type": "text", "text": text}}},
	})
	require.NoError(t, err)
	return raw
}

func TestHandleOutput_TheFinalAPIErrorShowsOnlyOnTheDivider(t *testing.T) {
	t.Parallel()
	// Each case trims the error text from the corresponding recorded probe run.
	for _, tc := range []struct{ run, text string }{
		{"apierror", "API Error: 400 probe: invalid request (PROBE_ERR400)"},
		{"retry500", "API Error: 500 probe: internal error (PROBE_ERR500)."},
		{"refusal_fable_noretry", "API Error: Opus 4.8's safeguards flagged this message."},
		{"autocompact_fail400", "Prompt is too long · automatic compaction failed: API Error: 400 probe: invalid request (compact-error400)"},
		{"r2_goal_error", "Prompt is too long · automatic compaction failed: API Error: 400 probe: invalid request (compact-error400)"},
	} {
		t.Run(tc.run, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newTestAgent(agent.NewProviderServices(sink))
			a.HandleOutput(claudeAPIErrorForTest(t, tc.text, ""))
			assert.Empty(t, sink.Messages())
			if tc.run == "r2_goal_error" {
				a.HandleOutput([]byte(`{"type":"system","subtype":"informational","level":"info","content":"Goal evaluation continues"}`))
			}
			end := []byte(fmt.Sprintf(`{"type":"result","subtype":"success","is_error":true,"result":%q}`, tc.text))
			a.HandleOutput(end)
			require.Len(t, sink.Messages(), 1)
			assert.JSONEq(t, string(end), string(sink.Messages()[0].Content))
			assert.Contains(t, sink.TurnLifecycle(), "turn_end")
		})
	}
}

func TestHandleOutput_AnAPIErrorFollowedByADifferentResultPersists(t *testing.T) {
	t.Parallel()
	for _, end := range []string{
		`{"type":"result","is_error":true,"result":"another error"}`,
		`{"type":"result","is_error":false,"result":"API Error: failure"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"next answer"}]}}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"next prompt"}]}}`,
	} {
		t.Run(end, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newTestAgent(agent.NewProviderServices(sink))
			first := claudeAPIErrorForTest(t, "API Error: failure", "")
			a.HandleOutput(first)
			a.HandleOutput([]byte(end))
			require.Len(t, sink.Messages(), 2)
			assert.JSONEq(t, string(first), string(sink.Messages()[0].Content))
			assert.JSONEq(t, end, string(sink.Messages()[1].Content))
		})
	}
}

func TestHandleOutput_AHeldAPIErrorPersistsAtProcessExit(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	first := claudeAPIErrorForTest(t, "API Error: failure", "")
	a.HandleOutput(first)
	assert.Empty(t, sink.Messages())
	a.SimulateExitForTest()
	require.NoError(t, a.Wait())
	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, string(first), string(sink.Messages()[0].Content))
}

func TestHandleOutput_AHeldAPIErrorIgnoresSyntheticContextAndChildOutput(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput(claudeAPIErrorForTest(t, "API Error: root failure", ""))
	a.HandleOutput([]byte(`{"type":"user","isSynthetic":true,"message":{"content":[{"type":"text","text":"Stop hook feedback"}]}}`))
	a.HandleOutput([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"unrelated","response":{}}}`))
	a.HandleOutput([]byte(`{"type":"assistant","parent_tool_use_id":"child-spawn","message":{"content":[{"type":"text","text":"child answer"}]}}`))
	assert.Empty(t, sink.Messages())
	a.HandleOutput([]byte(`{"type":"result","is_error":true,"result":"API Error: root failure"}`))
	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, `{"type":"result","is_error":true,"result":"API Error: root failure"}`, string(sink.Messages()[0].Content))
}

func TestHandleOutput_AHeldAPIErrorFlushesOnceAcrossConcurrentCalls(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	raw := claudeAPIErrorForTest(t, "API Error: failure", "")
	a.HandleOutput(raw)
	var group sync.WaitGroup
	for range 16 {
		group.Go(a.flushHeldAPIError)
	}
	group.Wait()
	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, string(raw), string(sink.Messages()[0].Content))
}

func TestHandleOutput_AHeldAPIErrorKeepsAnIndependentCopy(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	raw := claudeAPIErrorForTest(t, "API Error: failure", "")
	want := bytes.Clone(raw)
	a.HandleOutput(raw)
	clear(raw)
	a.flushHeldAPIError()
	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, string(want), string(sink.Messages()[0].Content))
}

func TestHandleOutput_LocalCommandOutputIsNotHeld(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	// The local_commands probe uses this model without the API-error flag.
	raw := []byte(`{"type":"assistant","message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"Local command output"}]}}`)
	a.HandleOutput(raw)
	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, string(raw), string(sink.Messages()[0].Content))
}

func TestHandleOutput_AManualCompactionPublishesALiveStartAndStoresOnlyItsEnd(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	// Trimmed from compact_ok. The native final status precedes its boundary.
	for _, frame := range []string{
		`{"type":"system","subtype":"session_state_changed","state":"running"}`,
		`{"type":"system","subtype":"status","status":"compacting"}`,
		`{"type":"system","subtype":"status","status":null,"compact_result":"success"}`,
		`{"type":"system","subtype":"init","session_id":"s-1"}`,
		`{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"manual","pre_tokens":15,"post_tokens":890}}`,
		`{"type":"user","isSynthetic":true,"message":{"content":[{"type":"text","text":"Summary"}]}}`,
		`{"type":"system","subtype":"local_command_output","content":"Context compacted"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":""}`,
		`{"type":"system","subtype":"session_state_changed","state":"idle"}`,
	} {
		a.HandleOutput([]byte(frame))
	}
	assert.Equal(t, []interface{}{map[string]interface{}{}, nil}, sink.SessionInfoValues("compaction"))
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.Contains(t, string(sink.PersistedNotifications()[0].Content), `"compact_boundary"`)
	require.Len(t, sink.Messages(), 1)
	assert.Contains(t, string(sink.Messages()[0].Content), `"type":"result"`)
}

func TestHandleOutput_AFailedManualCompactionStoresOneFailureRow(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	// Trimmed from compact_fail400. The local command repeats the failed status's error.
	a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":"compacting"}`))
	failed := `{"type":"system","subtype":"status","status":null,"compact_result":"failed","compact_error":"Error during compaction: API Error: 400 probe"}`
	a.HandleOutput([]byte(failed))
	a.HandleOutput([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"Error during compaction: API Error: 400 probe"}]},"local_command_run":{"command":"compact","args":""},"local_command_outcome":{"kind":"failed"}}`))
	a.HandleOutput([]byte(`{"type":"result","is_error":false,"result":""}`))
	assert.Equal(t, []interface{}{map[string]interface{}{}, nil}, sink.SessionInfoValues("compaction"))
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.JSONEq(t, failed, string(sink.PersistedNotifications()[0].Content))
	require.Len(t, sink.Messages(), 1)
	assert.Contains(t, string(sink.Messages()[0].Content), `"type":"result"`)
}

// --- The automatic-retry verdict of a compaction turn ---

// claudeCompactionEnded is the timeline entry of the verdict at the end of a
// compaction turn. It cancels the server-error retry and schedules nothing.
var claudeCompactionEnded = agenttest.TimelineEntry{
	Op: "cancel_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
	Reason: providerkit.TurnEndedOnCompaction,
}

// claudeServerErrorResult is a root `result` whose 500 the classifier reads as
// a transient failure.
const claudeServerErrorResult = `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":500,"result":"API Error: 500 Internal Server Error"}`

// claudeFailFirstWriteStdin is a process stdin that refuses its first write
// and accepts every later one.
type claudeFailFirstWriteStdin struct {
	written bytes.Buffer
	failed  bool
}

func (s *claudeFailFirstWriteStdin) Write(data []byte) (int, error) {
	if !s.failed {
		s.failed = true
		return 0, assert.AnError
	}
	return s.written.Write(data)
}

func (*claudeFailFirstWriteStdin) Close() error { return nil }

// A `/compact` that fails on an error that the classifier reads as retryable
// schedules no retry. The recorded failure (compact_fail400) ends on a `result`
// with `is_error: false`. Each `result` here states the failure instead, as a
// CLI that reports it would.
func TestClaudeCompaction_AFailedCompactCommandSchedulesNoRetry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		result string
	}{
		{"a server error", claudeServerErrorResult},
		{"an overload", `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":529,"result":"API Error: 529 Overloaded"}`},
		{"a rate limit", `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":429,"result":"API Error: 429 {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"Number of concurrent connections has exceeded your rate limit. Please try again later.\"}}"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
			require.NoError(t, a.SendInput("/compact", nil))

			a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":"compacting"}`))
			a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":null,"compact_result":"failed","compact_error":"Error during compaction: API Error: 500 Internal Server Error"}`))
			a.HandleOutput([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"Error during compaction: API Error: 500 Internal Server Error"}]},"local_command_run":{"command":"compact","args":""},"local_command_outcome":{"kind":"failed"}}`))
			a.HandleOutput([]byte(tc.result))

			assert.Equal(t, []agenttest.TimelineEntry{{Op: "notification"}, claudeTurnEndRow, claudeCompactionEnded}, sink.Timeline())
			assert.Zero(t, sink.AutoScheduleCount())
		})
	}
}

// A `/compact` with arguments runs the CLI's compaction too. Its success ends
// the turn on the compaction verdict, and the idle after the `result` applies
// no second verdict.
func TestClaudeCompaction_ASuccessfulCompactCommandAppliesTheCompactionVerdictOnce(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
	require.NoError(t, a.SendInput("/compact keep the plan", nil))

	// Trimmed from compact_ok.
	for _, frame := range []string{
		`{"type":"system","subtype":"session_state_changed","state":"running"}`,
		`{"type":"system","subtype":"status","status":"compacting"}`,
		`{"type":"system","subtype":"status","status":null,"compact_result":"success"}`,
		`{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"manual","pre_tokens":15,"post_tokens":890}}`,
		`{"type":"system","subtype":"local_command_output","content":"Context compacted"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":""}`,
		`{"type":"system","subtype":"session_state_changed","state":"idle"}`,
	} {
		a.HandleOutput([]byte(frame))
	}

	assert.Equal(t, []agenttest.TimelineEntry{{Op: "notification"}, claudeTurnEndRow, claudeCompactionEnded}, sink.Timeline())
	last, published := sink.LastTurnActive()
	require.True(t, published)
	assert.False(t, last, "the idle ends the compaction turn")
}

// A stop of the reader ends a compaction turn on the compaction verdict, not on
// the verdict of a stopped conversation turn.
func TestClaudeCompaction_AStoppedCompactCommandAppliesTheCompactionVerdict(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
	require.NoError(t, a.SendInput("/compact", nil))

	a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":"compacting"}`))
	a.noteInterruptRequested()
	a.HandleOutput([]byte(claudeResultAbortedStreaming))

	assert.Equal(t, []agenttest.TimelineEntry{claudeTurnEndRow, claudeCompactionEnded}, sink.Timeline())
}

// The `result` of the compaction spends the mark, so the next conversation turn
// applies its own verdict.
func TestClaudeCompaction_TheTurnAfterACompactionAppliesItsOwnVerdict(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
	require.NoError(t, a.SendInput("/compact", nil))
	a.HandleOutput([]byte(`{"type":"result","subtype":"success","is_error":false,"result":""}`))

	require.NoError(t, a.SendInput("hello", nil))
	a.HandleOutput([]byte(claudeServerErrorResult))

	assert.Equal(t, []agenttest.TimelineEntry{
		claudeTurnEndRow, claudeCompactionEnded,
		claudeTurnEndRow, claudeServerErrorScheduled,
	}, sink.Timeline())
}

// The CLI runs a message that the reader steered into a compaction after the
// compaction, and it ends each with its own `result`. Only the first `result`
// ends the compaction, so the steered message keeps its own verdict.
func TestClaudeCompaction_OnlyTheFirstResultOfTheTurnAppliesTheCompactionVerdict(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
	require.NoError(t, a.SendInput("/compact", nil))
	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"running"}`))
	require.NoError(t, a.SteerInput("hello", nil))

	a.HandleOutput([]byte(`{"type":"result","subtype":"success","is_error":false,"result":""}`))
	a.HandleOutput([]byte(claudeServerErrorResult))
	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"idle"}`))

	assert.Equal(t, []agenttest.TimelineEntry{
		claudeTurnEndRow, claudeCompactionEnded,
		claudeTurnEndRow, claudeServerErrorScheduled,
	}, sink.Timeline())
}

// The queue never steers a compaction item, but an ordinary message that opens
// with `/compact` can be steered. A steer joins the running turn, so that turn
// keeps its own verdict.
func TestClaudeCompaction_ACompactTextSteeredIntoATurnLeavesTheTurnItsOwnVerdict(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
	require.NoError(t, a.SendInput("hello", nil))
	require.NoError(t, a.SteerInput("/compact keep the plan", nil))

	a.HandleOutput([]byte(claudeServerErrorResult))

	assert.Equal(t, []agenttest.TimelineEntry{claudeTurnEndRow, claudeServerErrorScheduled}, sink.Timeline())
}

// A message that carries an attachment is not a compaction command, whatever
// its text says.
func TestClaudeCompaction_ACompactTextWithAnAttachmentIsAConversationTurn(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
	attachments := []*leapmuxv1.Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("keep the plan")}}
	require.NoError(t, a.SendInput("/compact", attachments))

	a.HandleOutput([]byte(claudeServerErrorResult))

	assert.Equal(t, []agenttest.TimelineEntry{claudeTurnEndRow, claudeServerErrorScheduled}, sink.Timeline())
}

// A `/compact` that never reached the CLI starts no compaction, so the next
// turn keeps its own verdict.
func TestClaudeCompaction_AFailedCompactWriteLeavesTheNextTurnItsOwnVerdict(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	stdin := &claudeFailFirstWriteStdin{}
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "test-agent", Stdin: stdin}),
		sink:    agent.NewProviderServices(sink),
	}
	require.Error(t, a.SendInput("/compact", nil))
	require.NoError(t, a.SendInput("hello", nil))

	a.HandleOutput([]byte(claudeServerErrorResult))

	assert.Equal(t, []agenttest.TimelineEntry{claudeTurnEndRow, claudeServerErrorScheduled}, sink.Timeline())
}

func TestHandleOutput_AnAutomaticFailureBelowTheBlockingLimitStoresTheFailure(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	// The autocompact_fail400_u975000 probe continues after too_few_groups.
	a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":"compacting","compact_output_chars":0}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":null,"compact_result":"failed","compact_error":"too_few_groups"}`))
	a.HandleOutput([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"answer"}]}}`))
	a.HandleOutput([]byte(`{"type":"result","is_error":false,"result":"answer"}`))
	assert.Equal(t, []interface{}{map[string]interface{}{}, nil}, sink.SessionInfoValues("compaction"))
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.Contains(t, string(sink.PersistedNotifications()[0].Content), "too_few_groups")
	assert.Len(t, sink.Messages(), 2)
}

func TestHandleOutput_ARequestingStatusStoresNothing(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":"requesting"}`))
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
	assert.Empty(t, sink.SessionInfoValues("compaction"))
}

func TestHandleOutput_AResultEndsALiveCompaction(t *testing.T) {
	t.Parallel()
	for _, end := range []string{
		`{"type":"result","is_error":true,"result":"stopped"}`,
		`{"type":"system","subtype":"compact_boundary"}`,
		`{"type":"system","subtype":"microcompact_boundary"}`,
		`{"type":"system","subtype":"status","status":"requesting"}`,
		`{"type":"system","subtype":"status","status":"new-state"}`,
	} {
		t.Run(end, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newTestAgent(agent.NewProviderServices(sink))
			a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":"compacting"}`))
			a.HandleOutput([]byte(`{"type":"system","subtype":"status","status":"compacting"}`))
			a.HandleOutput([]byte(end))
			assert.Equal(t, []interface{}{map[string]interface{}{}, nil}, sink.SessionInfoValues("compaction"))
		})
	}
}

func TestHandleOutput_AFailedFinalStatusPersistsWithNoCompactingStatusBefore(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	raw := `{"type":"system","subtype":"status","status":null,"compact_result":"failed","compact_error":"too_few_groups"}`
	a.HandleOutput([]byte(raw))
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.JSONEq(t, raw, string(sink.PersistedNotifications()[0].Content))
	assert.Empty(t, sink.SessionInfoValues("compaction"))
}

func TestHandleOutput_RepeatedNotificationsOfOneKeyFold(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	raw := []byte(`{"type":"system","subtype":"notification","key":"stop-hook-error","message":"Stop hook error occurred"}`)
	a.HandleOutput(raw)
	a.HandleOutput(raw)
	assert.Empty(t, sink.Messages())
	// Both writes reach the reducer's same slot. The service integration test verifies the folded row.
	require.Len(t, sink.PersistedNotifications(), 2)
	for _, notice := range sink.PersistedNotifications() {
		assert.Equal(t, agent.ProviderScopedNotification("claude:notification:stop-hook-error"), claudeProvider{}.Classify(notice.Content))
	}
}

type claudeRetryOrderSink struct {
	agenttest.Sink
	resolvedBeforeMessage []bool
	notificationError     error
}

func (s *claudeRetryOrderSink) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	if s.notificationError != nil {
		return false, s.notificationError
	}
	return s.Sink.PersistNotification(source, content)
}

func (s *claudeRetryOrderSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	s.resolvedBeforeMessage = append(s.resolvedBeforeMessage, len(s.LeapMuxNotifications()) > 0)
	return s.Sink.PersistMessage(source, content, span)
}

func TestHandleOutput_TheFirstAnswerAfterAnAPIRetryResolvesIt(t *testing.T) {
	t.Parallel()
	sink := &claudeRetryOrderSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	// The flaky probe sends native retry frames before the recovered answer.
	a.HandleOutput([]byte(`{"type":"system","subtype":"api_retry","attempt":1,"max_retries":10,"retry_delay_ms":500}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"api_retry","attempt":2,"max_retries":10,"retry_delay_ms":1000}`))
	answer := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"answer"}]}}`)
	a.HandleOutput(answer)
	a.HandleOutput(answer)
	assert.Equal(t, []map[string]interface{}{{"type": "api_retry_resolved"}}, sink.LeapMuxNotifications())
	assert.Equal(t, []bool{true, true}, sink.resolvedBeforeMessage)
}

func TestHandleOutput_AFinalAPIErrorLeavesTheRetryUnresolved(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"api_retry","attempt":1}`))
	a.HandleOutput(claudeAPIErrorForTest(t, "API Error: failed", ""))
	a.HandleOutput([]byte(`{"type":"result","is_error":true,"result":"API Error: failed"}`))
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestHandleOutput_AResultResetsTheRetry(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"api_retry","attempt":1}`))
	a.HandleOutput([]byte(`{"type":"result","is_error":true,"result":"failed"}`))
	a.HandleOutput([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"next turn"}]}}`))
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestHandleOutput_AnUnstoredRetryResolvesNothing(t *testing.T) {
	t.Parallel()
	sink := &claudeRetryOrderSink{notificationError: assert.AnError}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"api_retry","attempt":1}`))
	sink.notificationError = nil
	a.HandleOutput([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"answer"}]}}`))
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestHandleOutput_KnownDroppedFrameTypesStoreNoRow(t *testing.T) {
	t.Parallel()
	for frameType, reason := range claudeDroppedFrameTypes {
		t.Run(frameType, func(t *testing.T) {
			sink := &agenttest.Sink{}
			a := newTestAgent(agent.NewProviderServices(sink))
			a.sessionID = "s-1"
			assert.NotEmpty(t, reason)
			raw, err := json.Marshal(map[string]string{"type": frameType, "session_id": "s-1"})
			require.NoError(t, err)
			a.HandleOutput(raw)
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.PersistedNotifications())
			assert.Empty(t, sink.LeapMuxNotifications())
		})
	}
}

func TestHandleOutput_CommandsChangedReplacesTheSlashCommands(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["bash"],"session_id":"s-1"}`))
	require.Equal(t, 1, sink.GoalCapabilityPublishes())
	assert.Empty(t, a.SupportedGoalActions())

	a.HandleOutput([]byte(`{"type":"system","subtype":"commands_changed","commands":[{"name":"review","description":"Review the diff"},{"name":"goal","description":"Set a goal","argumentHint":"[objective]"}],"uuid":"u-1","session_id":"s-1"}`))
	assert.Len(t, a.SupportedGoalActions(), 2, "the replacement list re-answers the /goal question")
	assert.Equal(t, 2, sink.GoalCapabilityPublishes())

	a.HandleOutput([]byte(`{"type":"system","subtype":"commands_changed","commands":[{"name":"review","description":"Review the diff"}],"uuid":"u-2","session_id":"s-1"}`))
	assert.Empty(t, a.SupportedGoalActions(), "the replacement list dropped /goal")
	assert.Equal(t, 3, sink.GoalCapabilityPublishes())
}

func TestHandleOutput_CommandsChangedWithoutAListKeepsTheSlashCommands(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["goal"],"session_id":"s-1"}`))
	require.Len(t, a.SupportedGoalActions(), 2)

	// A `null` states no list at all. The frame's contract says a client
	// replaces its cached list with the payload, and a payload that states no
	// list cannot be one. The init reader treats a null the same way.
	a.HandleOutput([]byte(`{"type":"system","subtype":"commands_changed","commands":null,"uuid":"u-1","session_id":"s-1"}`))
	assert.Len(t, a.SupportedGoalActions(), 2, "a null commands list leaves the answer alone")
	assert.Equal(t, 1, sink.GoalCapabilityPublishes())

	a.HandleOutput([]byte(`{"type":"system","subtype":"commands_changed","uuid":"u-2","session_id":"s-1"}`))
	assert.Len(t, a.SupportedGoalActions(), 2, "an absent commands list leaves the answer alone")
	assert.Equal(t, 1, sink.GoalCapabilityPublishes())

	// A real array REPLACES the cached list, an empty one included.
	a.HandleOutput([]byte(`{"type":"system","subtype":"commands_changed","commands":[],"uuid":"u-3","session_id":"s-1"}`))
	assert.Empty(t, a.SupportedGoalActions(), "an empty array replaces the cached list")
	assert.Equal(t, 2, sink.GoalCapabilityPublishes())
}

func TestHandleOutput_CommandsChangedRepublishesOnlyOnAChangedAnswer(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["goal","review"],"session_id":"s-1"}`))
	require.Equal(t, 1, sink.GoalCapabilityPublishes())

	// A publish states that the answer CHANGED. A replacement that re-states
	// the same answer changes nothing the browser reads, so it must not
	// re-publish.
	a.HandleOutput([]byte(`{"type":"system","subtype":"commands_changed","commands":[{"name":"review"},{"name":"goal"}],"uuid":"u-1","session_id":"s-1"}`))
	assert.Equal(t, 1, sink.GoalCapabilityPublishes(), "a re-stated answer states no change")
	assert.Len(t, a.SupportedGoalActions(), 2)

	a.HandleOutput([]byte(`{"type":"system","subtype":"commands_changed","commands":[{"name":"goal"},{"name":"review"}],"uuid":"u-2","session_id":"s-1"}`))
	assert.Equal(t, 1, sink.GoalCapabilityPublishes(), "a re-ordered list states the same answer")
	assert.Len(t, a.SupportedGoalActions(), 2)
}

func TestObserveGoalCommand_RefusesAfterAReplacementDroppedTheCommand(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"system","subtype":"init","slash_commands":["goal"],"session_id":"s-1"}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"commands_changed","commands":[{"name":"review"}],"uuid":"u-1","session_id":"s-1"}`))

	a.ObserveGoalCommand(agent.GoalDeliverySend, "/goal ship it")
	assert.Empty(t, sink.Goals(), "the replacement list dropped the command, so the write is refused")

	a.HandleOutput([]byte(`{"type":"system","subtype":"commands_changed","commands":[{"name":"goal"}],"uuid":"u-2","session_id":"s-1"}`))
	a.ObserveGoalCommand(agent.GoalDeliverySend, "/goal ship it")
	goals := sink.Goals()
	require.Len(t, goals, 1, "the replacement list restored the command")
	assert.Equal(t, "ship it", goals[0].Objective)
}

func TestHandleOutput_ElicitationCompleteAcceptsAndWithdrawsTheMatchingRequest(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a, stdin := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"control_request","request_id":"r1","request":{"subtype":"elicitation","mcp_server_name":"github","message":"Sign in","mode":"url","url":"https://example.invalid/auth","elicitation_id":"e1"}}`))
	require.Len(t, sink.PublishedControls(), 1)

	a.HandleOutput([]byte(`{"type":"system","subtype":"elicitation_complete","mcp_server_name":"github","elicitation_id":"e-unknown","uuid":"u-1","session_id":"s-1"}`))
	assert.Empty(t, stdin.String(), "an unknown elicitation id writes nothing")
	assert.Empty(t, sink.CanceledControls(), "an unknown elicitation id withdraws nothing")

	// Claude Code 2.1.300 only logs the completion and writes this frame. Its
	// headless elicitation handler still waits for an answer, so the MCP call
	// stays blocked until the worker answers the request.
	a.HandleOutput([]byte(`{"type":"system","subtype":"elicitation_complete","mcp_server_name":"github","elicitation_id":"e1","uuid":"u-2","session_id":"s-1"}`))
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"r1","response":{"action":"accept"}}}`, stdin.String(),
		"the worker accepts the request that the completion ends")
	assert.Equal(t, []string{"r1"}, sink.CanceledControls(), "the matching card is withdrawn")

	stdin.Reset()
	a.HandleOutput([]byte(`{"type":"system","subtype":"elicitation_complete","mcp_server_name":"github","elicitation_id":"e1","uuid":"u-3","session_id":"s-1"}`))
	assert.Empty(t, stdin.String(), "the request is retired, so a repeated completion writes nothing")
	assert.Equal(t, []string{"r1"}, sink.CanceledControls(), "the request is retired")
}

// answeringControlSink starts the answer of each control request before its
// publish call returns, as a fast reader can: the publish broadcasts the card,
// and the answer can reach the provider on another goroutine at once.
type answeringControlSink struct {
	agenttest.ControlSink
	answer   func(requestID string) error
	answered chan error
}

func (s *answeringControlSink) PublishControlRequest(request agent.ControlRequest) error {
	if err := s.ControlSink.PublishControlRequest(request); err != nil {
		return err
	}
	go func() { s.answered <- s.answer(request.RequestID) }()
	return nil
}

func TestHandleOutput_AnElicitationAnsweredDuringItsPublishKeepsNoEntry(t *testing.T) {
	t.Parallel()

	sink := &answeringControlSink{answered: make(chan error, 1)}
	a, stdin := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
	sink.answer = func(requestID string) error {
		answer := fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":%q,"response":{"action":"decline"}}}`, requestID)
		return a.SendRawInput([]byte(answer), agent.StopContext{})
	}

	a.HandleOutput([]byte(`{"type":"control_request","request_id":"r1","request":{"subtype":"elicitation","mcp_server_name":"github","message":"Sign in","elicitation_id":"e1"}}`))
	require.NoError(t, <-sink.answered, "the answer waits for the publish, then reaches the CLI")
	assert.Empty(t, elicitationEntries(a), "the answer claimed the request, so no entry outlives it")

	answered := stdin.String()
	a.HandleOutput([]byte(`{"type":"system","subtype":"elicitation_complete","mcp_server_name":"github","elicitation_id":"e1","uuid":"u-1","session_id":"s-1"}`))
	assert.Equal(t, answered, stdin.String(), "the reader answered already, so the worker writes no second answer")
	assert.Empty(t, sink.CanceledControls(), "no card is left to withdraw")
}

func TestHandleOutput_AnElicitationThatFailsToPublishKeepsNoEntry(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{PublicationError: errors.New("storage unavailable")}
	a, stdin := newClaudeAgentWithStdin(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{"type":"control_request","request_id":"r1","request":{"subtype":"elicitation","mcp_server_name":"github","message":"Sign in","elicitation_id":"e1"}}`))
	assert.Empty(t, elicitationEntries(a), "a request that no reader can see keeps no entry")
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"error","request_id":"r1","error":"LeapMux could not store this control request."}}`, stdin.String(),
		"the provider learns that the request failed")

	stdin.Reset()
	a.HandleOutput([]byte(`{"type":"system","subtype":"elicitation_complete","mcp_server_name":"github","elicitation_id":"e1","uuid":"u-1","session_id":"s-1"}`))
	assert.Empty(t, stdin.String(), "the failed request gets no second answer")
	assert.Empty(t, sink.CanceledControls())
}

func TestHandleOutput_AControlRequestWithAMistypedOptionalFieldStillPublishes(t *testing.T) {
	t.Parallel()

	// Only `request_id` is required to publish. A mistyped optional field must
	// cost only that field: an unpublished request leaves Claude Code waiting
	// for an answer that no reader can give.
	for name, line := range map[string]string{
		"tool_use_id":    `{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":42}}`,
		"agent_id":       `{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"toolu_1","agent_id":{"id":"task-1"}}}`,
		"elicitation_id": `{"type":"control_request","request_id":"r1","request":{"subtype":"elicitation","mcp_server_name":"github","message":"Sign in","elicitation_id":7}}`,
		"subtype":        `{"type":"control_request","request_id":"r1","request":{"subtype":["can_use_tool"],"tool_name":"Bash","input":{}}}`,
		"request":        `{"type":"control_request","request_id":"r1","request":"can_use_tool"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sink := &agenttest.ControlSink{}
			a, stdin := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
			a.HandleOutput([]byte(line))

			published := sink.PublishedControls()
			require.Len(t, published, 1, "the request reaches the reader")
			assert.Equal(t, "r1", published[0].RequestID)
			assert.Empty(t, stdin.String(), "a published request needs no failure answer")
			assert.Empty(t, elicitationEntries(a), "a mistyped elicitation field records no entry")
		})
	}
}

func TestHandleOutput_AControlRequestWithAMistypedAgentIDStillClearsTheCheck(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	sink.UpdateSessionID("root-session")

	// A mistyped `agent_id` states no subagent, which is the root. The correct
	// `tool_use_id` beside it still ends the check of the call it escalates.
	a.HandleOutput([]byte(`{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"toolu_1","agent_id":42}}`))

	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyRunningTool)
	require.True(t, ok, "the clear lands on the root sink")
	update, ok := value.(map[string]interface{})
	require.True(t, ok, "running_tool must be an object, got %T", value)
	assert.Equal(t, "toolu_1", update[contracts.RunningToolFieldSpanId])
	assert.Equal(t, "root-session", update[contracts.RunningToolFieldAgentSessionId])
	assert.Contains(t, update, contracts.RunningToolFieldPermissionCheck)
	assert.Nil(t, update[contracts.RunningToolFieldPermissionCheck])
}

func TestHandleOutput_AnsweredAndCancelledElicitationsForgetTheirRequest(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a, stdin := newClaudeAgentWithStdin(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{"type":"control_request","request_id":"r1","request":{"subtype":"elicitation","mcp_server_name":"github","message":"Sign in","elicitation_id":"e1"}}`))
	assert.Equal(t, map[string]string{"e1": "r1"}, elicitationEntries(a),
		"a published elicitation records its request id")
	require.NoError(t, a.SendRawInput([]byte(`{"type":"control_response","response":{"request_id":"r1","response":{"action":"accept","content":{"token":"x"}}}}`), agent.StopContext{}))
	assert.Empty(t, elicitationEntries(a), "the answered request is forgotten")
	answered := stdin.String()
	a.HandleOutput([]byte(`{"type":"system","subtype":"elicitation_complete","mcp_server_name":"github","elicitation_id":"e1","uuid":"u-1","session_id":"s-1"}`))
	assert.Empty(t, sink.CanceledControls(), "the reader already answered, so there is no card left to withdraw")
	assert.Equal(t, answered, stdin.String(), "the reader already answered, so the worker writes no second answer")

	a.HandleOutput([]byte(`{"type":"control_request","request_id":"r2","request":{"subtype":"elicitation","mcp_server_name":"github","message":"Sign in","elicitation_id":"e2"}}`))
	a.HandleOutput([]byte(`{"type":"control_cancel_request","request_id":"r2"}`))
	assert.Equal(t, []string{"r2"}, sink.CanceledControls(), "the cancelled request retires its card")
	assert.Empty(t, elicitationEntries(a), "the cancelled request is forgotten")
	a.HandleOutput([]byte(`{"type":"system","subtype":"elicitation_complete","mcp_server_name":"github","elicitation_id":"e2","uuid":"u-2","session_id":"s-1"}`))
	assert.Equal(t, []string{"r2"}, sink.CanceledControls(), "the cancelled request is retired")
}

func TestSendRawInput_AFrameThatAnswersNothingKeepsTheElicitation(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"control_request","request_id":"r1","request":{"subtype":"elicitation","mcp_server_name":"github","message":"Sign in","elicitation_id":"e1"}}`))

	// A frame that answers no request carries no request id, so it changes no
	// bookkeeping and the pending card is still withdrawn at the completion.
	require.NoError(t, a.SendRawInput([]byte(`{"type":"user","message":{"role":"user","content":"hello"}}`), agent.StopContext{}))
	a.HandleOutput([]byte(`{"type":"system","subtype":"elicitation_complete","mcp_server_name":"github","elicitation_id":"e1","uuid":"u-1","session_id":"s-1"}`))
	assert.Equal(t, []string{"r1"}, sink.CanceledControls(), "the entry outlived the input")
}

func TestSendRawInput_AnAnswerRetiresOnlyItsOwnElicitation(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"control_request","request_id":"r1","request":{"subtype":"elicitation","mcp_server_name":"github","message":"Sign in","elicitation_id":"e1"}}`))
	a.HandleOutput([]byte(`{"type":"control_request","request_id":"r2","request":{"subtype":"elicitation","mcp_server_name":"gitlab","message":"Sign in","elicitation_id":"e2"}}`))

	require.NoError(t, a.SendRawInput([]byte(`{"type":"control_response","response":{"request_id":"r1","response":{"action":"accept","content":{"token":"x"}}}}`), agent.StopContext{}))

	a.HandleOutput([]byte(`{"type":"system","subtype":"elicitation_complete","mcp_server_name":"gitlab","elicitation_id":"e2","uuid":"u-1","session_id":"s-1"}`))
	assert.Equal(t, []string{"r2"}, sink.CanceledControls(),
		"the answer for r1 leaves e2 pending")

	a.HandleOutput([]byte(`{"type":"system","subtype":"elicitation_complete","mcp_server_name":"github","elicitation_id":"e1","uuid":"u-2","session_id":"s-1"}`))
	assert.Equal(t, []string{"r2"}, sink.CanceledControls(),
		"the answer for r1 already retired its own entry")
}

// elicitationEntries maps the elicitation id of each pending elicitation
// request that no answer claimed to its request id, for an assertion.
func elicitationEntries(a *Agent) map[string]string {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	entries := make(map[string]string)
	for requestID, record := range a.incomingControls {
		if record.elicitationID != "" && !record.answering && !record.answered {
			entries[record.elicitationID] = requestID
		}
	}
	return entries
}
