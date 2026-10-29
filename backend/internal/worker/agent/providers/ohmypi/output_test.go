package ohmypi

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real frames from `omp 18.2.11` in rpc-ui mode (probe s1 and s2), shortened
// where a field is irrelevant to the case.
const (
	frameAssistantText = `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"The user wants a greeting.","thinkingSignature":"reasoning_content"},{"type":"text","text":"Hello from the mock."}],"api":"openai-completions","provider":"mock","model":"mock-model","usage":{"input":1200,"output":30,"cacheRead":0,"cacheWrite":0,"totalTokens":1230,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0.0021}},"stopReason":"stop","timestamp":1790187118735}}`
	frameUserEcho      = `{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"say hello"}],"attribution":"user","timestamp":1790187225212}}`
	frameToolResult    = `{"type":"message_end","message":{"role":"toolResult","toolCallId":"call_1","toolName":"bash","content":[{"type":"text","text":"probe-output\n"}],"details":{},"isError":false}}`
	frameBashStart     = `{"type":"tool_execution_start","toolCallId":"call_1","toolName":"bash","args":{"command":"echo probe-output"}}`
	frameBashUpdate    = `{"type":"tool_execution_update","toolCallId":"call_1","toolName":"bash","args":{"command":"echo probe-output"},"partialResult":{"content":[{"type":"text","text":"probe-output\n"}],"details":{}}}`
	frameBashEnd       = `{"type":"tool_execution_end","toolCallId":"call_1","toolName":"bash","result":{"content":[{"type":"text","text":"probe-output\n\n\nWall time: 0.05 seconds"}],"details":{"timeoutSeconds":300,"wallTimeMs":49.99}},"isError":false}`
	frameAgentEnd      = `{"type":"agent_end","isTerminal":true,"messages":[{"role":"user","content":[{"type":"text","text":"say hello"}]},{"role":"assistant","content":[{"type":"text","text":"Hello."}],"stopReason":"stop"}]}`
)

// TestTurnFrames lists the frames that move the turn flag. Every other frame of
// omp's vocabulary -- and one that does not exist yet -- must publish nothing.
func TestTurnFrames(t *testing.T) {
	t.Parallel()
	cases := []agenttest.TurnFrameCase{
		{Name: "agent_start", Line: `{"type":"agent_start"}`, Moves: true},
		{Name: "agent_end", Line: frameAgentEnd, Moves: true},
		// omp's report that a prompt started no run releases the arm that prompt
		// took, and it publishes so the input queue settles the dispatch.
		{Name: "prompt_result", Line: `{"type":"prompt_result","id":"leapmux-1","agentInvoked":false}`, Moves: true},
		{Name: "an unclaimed prompt failure", Line: `{"type":"response","id":"leapmux-1","command":"prompt","success":false,"error":"late"}`, Moves: true},
		{Name: "turn_start", Line: `{"type":"turn_start"}`},
		{Name: "turn_end", Line: `{"type":"turn_end","message":{"role":"assistant","content":[]}}`},
		{Name: "message_start", Line: `{"type":"message_start","message":{"role":"assistant","content":[]}}`},
		{Name: "message_update", Line: `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"x"}}`},
		{Name: "message_end", Line: frameAssistantText},
		{Name: "tool_execution_start", Line: frameBashStart},
		{Name: "tool_execution_end", Line: frameBashEnd},
		{Name: "auto_retry_start", Line: `{"type":"auto_retry_start","attempt":1,"maxAttempts":10,"delayMs":92,"errorMessage":"400"}`},
		{Name: "auto_compaction_start", Line: `{"type":"auto_compaction_start","reason":"threshold","action":"snapcompact"}`},
		{Name: "todo_reminder", Line: `{"type":"todo_reminder","todos":[{"content":"Write code","status":"in_progress"}],"attempt":1,"maxAttempts":3}`},
		{Name: "notice", Line: `{"type":"notice","level":"info","message":"hi"}`},
		{Name: "extension_ui_request setWidget", Line: `{"type":"extension_ui_request","id":"w1","method":"setWidget","widgetKey":"autoresearch"}`},
		{Name: "advisor_cost_changed", Line: `{"type":"advisor_cost_changed"}`},
		{Name: "a frame a later omp adds", Line: `{"type":"turn_spinning_up"}`},
	}
	agenttest.AssertTurnFrames(t, cases, func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		r := newRig(t)
		r.emit(tc.Line)
		return r.sink.TurnActives()
	})
}

func TestTheTurnSpansRetriesAndEndsAtAgentEnd(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := testutil.DeadlineContext(t)
	start := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	r.clock.Set(start).MustWait(ctx)

	r.emit(`{"type":"agent_start"}`)
	r.awaitStatsRead()
	r.clock.Set(start.Add(2 * time.Second)).MustWait(ctx)
	// A retry starts a second run with no agent_end between.
	r.emit(`{"type":"auto_retry_start","attempt":1,"maxAttempts":10,"delayMs":92,"errorMessage":"400"}`, `{"type":"agent_start"}`)
	r.emit(frameBashStart, frameBashEnd)
	r.clock.Set(start.Add(5 * time.Second)).MustWait(ctx)
	r.emit(frameAgentEnd)

	messages := r.sink.Messages()
	end := messages[len(messages)-1]
	require.True(t, end.TurnEnd)
	var metadata map[string]json.Number
	require.NoError(t, json.Unmarshal(end.Metadata, &metadata))
	assert.Equal(t, json.Number("5000"), metadata[contracts.MessageMetadataFieldDurationMs], "the turn is timed from its FIRST run")
	assert.Equal(t, json.Number("1"), metadata[contracts.MessageMetadataFieldToolUses])
	lifecycle := r.sink.TurnLifecycle()
	require.GreaterOrEqual(t, len(lifecycle), 3)
	assert.Equal(t, []string{"turn_end", "reset_spans", "turn_active:false"}, lifecycle[len(lifecycle)-3:],
		"the turn end and the span reset reach the sink before the flag clears")
	assert.Equal(t, 1, r.sink.ResetSpanCount())
}

func TestARunOmpStartedByItselfIsATurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)

	// A background job finished: omp starts a run with no prompt.
	r.emit(`{"type":"agent_start"}`)
	active, _ := r.sink.LastTurnActive()
	assert.True(t, active)
	r.emit(`{"type":"agent_end","isTerminal":true,"messages":[]}`)
	active, _ = r.sink.LastTurnActive()
	assert.False(t, active)
}

// omp classifies each failure in the errorId of its message. A failed turn
// whose message states no retryable class, and no vendor sentence, cancels a
// pending server-error retry once, after its divider.
func TestATurnEndWithoutARetryableFailureCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`,
		`{"type":"agent_end","isTerminal":true,"messages":[{"role":"assistant","stopReason":"error","errorMessage":"500 server error"}]}`)

	require.Equal(t, []string{"turn_end", "cancel_server_error"}, providerkittest.TurnEndAndRetryOpNames(r.sink.Timeline()))
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, r.sink.LastAutoCancel())
	assert.Equal(t, providerkit.TurnEndedWithoutRetryableFailure, r.sink.LastAutoCancelReason())
}

// Captured frames of omp 18.12.6. The four agent_end frames are the fixtures of
// .tmp/retry-research/partial-providers.md (section 2), and the 429 pair is the
// probe of .tmp/probe/ohmypi/run-fake429.jsonl. The MiMo 400 is a real failure
// (.tmp/retry-research/transcripts-kimi-pi-omp.md) whose message the transcript
// stores; the frame around it follows the fixtures.
const (
	ompAgentEnd500 = `{"type":"agent_end","messages":[{"role":"assistant","content":[],"api":"openai-completions","provider":"fake500","model":"fake-model","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"error","timestamp":1793150670954,"errorStatus":500,"errorId":135168,"errorMessage":"500 Internal server error\nInternal server error (type=server_error)","duration":15520.980958}],"isTerminal":true,"yielded":true}`
	// ompAgentEndUsage1308 shifts the fixture's timestamp from 1793150803724
	// (01:26:43.724 UTC) to 11:00 UTC. The fake server's stamp, 12:06 UTC, lies
	// more than the stated five hours after the probe, so the vendor parser
	// would read it as implausible.
	ompAgentEndUsage1308 = `{"type":"agent_end","messages":[{"role":"assistant","content":[],"api":"openai-completions","provider":"fakeglm","model":"fake-model","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"error","timestamp":1793185200000,"errorStatus":429,"errorId":659456,"errorMessage":"429 Usage limit reached for 5 hour. Your limit will reset at 2026-10-28 20:06:00\nUsage limit reached for 5 hour. Your limit will reset at 2026-10-28 20:06:00 (type=1308)","duration":3.6251669999999194}],"isTerminal":true,"yielded":true}`
	// ompAutoRetryEndUsage1308 is the probe's own wait for the 1308, which omp
	// computes from the stamp read as UTC.
	ompAutoRetryEndUsage1308 = `{"type":"auto_retry_end","success":false,"attempt":1,"finalError":"Provider requested 67156270ms wait, exceeds retry.maxDelayMs (300000ms). Original error: 429 Usage limit reached for 5 hour. Your limit will reset at 2026-10-28 20:06:00\nUsage limit reached for 5 hour. Your limit will reset at 2026-10-28 20:06:00 (type=1308)"}`
	ompAgentEndDrop          = `{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"Hel"}],"api":"openai-completions","provider":"fakedrop","model":"fake-model","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"error","timestamp":1793150806332,"responseId":"chatcmpl-1","errorId":135168,"errorMessage":"OpenAI completions stream closed before a finish_reason was received","duration":1.3223749999999654,"ttft":1.2107080000000678}],"isTerminal":true,"yielded":true}`
	ompAutoRetryEnd429       = `{"type":"auto_retry_end","success":false,"attempt":1,"finalError":"Provider requested 3600000ms wait, exceeds retry.maxDelayMs (300000ms). Original error: 429 Rate limit reached for requests retry-after-ms=3600000\nRate limit reached for requests (type=rate_limit_error)"}`
	ompAgentEnd429           = `{"type":"agent_end","messages":[{"role":"user","content":[{"type":"text","text":"Say hello."}],"attribution":"user","timestamp":1793150800582},{"role":"assistant","content":[],"api":"openai-completions","provider":"fake429","model":"fake-model","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"error","timestamp":1793150801117,"errorStatus":429,"errorId":135168,"errorMessage":"429 Rate limit reached for requests retry-after-ms=3600000\nRate limit reached for requests (type=rate_limit_error)","duration":1.243249999999989}],"isTerminal":true,"yielded":true}`
	ompAgentEndMiMo400       = `{"type":"agent_end","messages":[{"role":"assistant","content":[],"api":"openai-completions","provider":"xiaomi-token-plan-sgp","model":"mimo-v2.6-pro","stopReason":"error","timestamp":1792177755143,"errorStatus":400,"errorId":400,"errorMessage":"400 data:{\"error\":{\"code\":\"400\",\"message\":\"Request failed\",\"param\":\"Connection prematurely closed BEFORE response\",\"type\":\"\"}}\n\n\nraw-http-request=/Users/trustin/.omp/logs/http-400-requests/1792177755143-3g4p3lfjmwwi0.json"}],"isTerminal":true,"yielded":true}`
)

// ompAgentEndWithError renders a terminal agent_end whose assistant message
// failed with an errorId and an errorStatus. The text states no vendor
// sentence, so only the omp fields decide.
func ompAgentEndWithError(errorID int64, errorStatus int) string {
	return fmt.Sprintf(`{"type":"agent_end","messages":[{"role":"assistant","content":[],"stopReason":"error","timestamp":1793150670954,"errorStatus":%d,"errorId":%d,"errorMessage":"the request failed"}],"isTerminal":true,"yielded":true}`, errorStatus, errorID)
}

// The terminal agent_end applies one verdict after its divider, and the retry
// carries that frame.
func TestATerminalAgentEndAppliesItsVerdictAfterTheDivider(t *testing.T) {
	t.Parallel()
	// probeClock is the clock of the 429 probe, just after its failure.
	probeClock := time.UnixMilli(1793150801200).UTC()
	for _, tc := range []struct {
		name   string
		clock  time.Time
		frames []string
		want   []string
	}{
		{"a 500 that omp gave up on schedules a server-error retry", probeClock, []string{ompAgentEnd500}, []string{"turn_end", "schedule_server_error"}},
		{
			"a dated 1308 schedules a usage-limit retry at its stamp",
			time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC), []string{ompAgentEndUsage1308},
			[]string{"turn_end", "schedule_usage_limit@2026-10-28T12:06:01Z", "cancel_server_error"},
		},
		{
			// omp reads the naive stamp as UTC and requests 18.65 hours. The
			// vendor's stamp, read as UTC+8, decides.
			"the vendor's 1308 stamp wins over omp's requested wait",
			time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC), []string{ompAutoRetryEndUsage1308, ompAgentEndUsage1308},
			[]string{"turn_end", "schedule_usage_limit@2026-10-28T12:06:01Z", "cancel_server_error"},
		},
		{"a stream that dropped schedules a server-error retry", probeClock, []string{ompAgentEndDrop}, []string{"turn_end", "schedule_server_error"}},
		{
			// The reviewed collision: a 429 rate limit with a requested wait is
			// a usage limit that lifts when the wait ends. The frame states no
			// time, so the wait counts from the moment the worker reads it.
			"a 429 with a requested wait schedules a usage-limit retry when the wait ends",
			probeClock, []string{ompAutoRetryEnd429, ompAgentEnd429},
			[]string{"turn_end", "schedule_usage_limit@" + probeClock.Add(time.Hour).Format(time.RFC3339Nano), "cancel_server_error"},
		},
		{"a 429 with no requested wait schedules a server-error retry", probeClock, []string{ompAgentEnd429}, []string{"turn_end", "schedule_server_error"}},
		{"a MiMo connection failure sent as a 400 schedules a server-error retry", probeClock, []string{ompAgentEndMiMo400}, []string{"turn_end", "schedule_server_error"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.clock.Set(tc.clock).MustWait(testutil.DeadlineContext(t))
			r.emit(`{"type":"agent_start"}`)
			r.emit(tc.frames...)
			assert.Equal(t, tc.want, providerkittest.TurnEndAndRetryOpNames(r.sink.Timeline()))
			require.Positive(t, r.sink.AutoScheduleCount())
			assert.JSONEq(t, tc.frames[len(tc.frames)-1], string(r.sink.LastAutoSchedule().SourcePayload), "the retry carries the agent_end")
		})
	}
}

// A requested wait belongs to the run that failed. A later run, a wait that
// does not parse, and a wait out of range state no reset.
func TestARequestedWaitAppliesOnlyToTheRunThatFailed(t *testing.T) {
	t.Parallel()
	probeClock := time.UnixMilli(1793150801200).UTC()
	for _, tc := range []struct {
		name   string
		frames []string
	}{
		{"a run that starts after the wait clears it", []string{ompAutoRetryEnd429, `{"type":"agent_start"}`, ompAgentEnd429}},
		{"a later turn end does not inherit the wait", []string{ompAutoRetryEnd429, ompAgentEnd500, ompAgentEnd429}},
		{"a successful retry states no wait", []string{`{"type":"auto_retry_end","success":true,"attempt":1,"finalError":"Provider requested 3600000ms wait, exceeds retry.maxDelayMs (300000ms)."}`, ompAgentEnd429}},
		{"a wait of 0 ms states no reset", []string{`{"type":"auto_retry_end","success":false,"attempt":1,"finalError":"Provider requested 0ms wait, exceeds retry.maxDelayMs (300000ms)."}`, ompAgentEnd429}},
		{"a wait beyond 31 days states no reset", []string{`{"type":"auto_retry_end","success":false,"attempt":1,"finalError":"Provider requested 2678400001ms wait, exceeds retry.maxDelayMs (300000ms)."}`, ompAgentEnd429}},
		{"a wait that overflows states no reset", []string{`{"type":"auto_retry_end","success":false,"attempt":1,"finalError":"Provider requested 99999999999999999999ms wait, exceeds retry.maxDelayMs (300000ms)."}`, ompAgentEnd429}},
		{"a final error of another shape states no wait", []string{`{"type":"auto_retry_end","success":false,"attempt":3,"finalError":"500 Internal server error"}`, ompAgentEnd429}},
		{"a malformed retry end states no wait", []string{`{"type":"auto_retry_end","success":"no","finalError":7}`, ompAgentEnd429}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.clock.Set(probeClock).MustWait(testutil.DeadlineContext(t))
			r.emit(`{"type":"agent_start"}`)
			r.emit(tc.frames...)
			ops := providerkittest.TurnEndAndRetryOpNames(r.sink.Timeline())
			require.NotEmpty(t, ops)
			assert.Equal(t, []string{"turn_end", "schedule_server_error"}, ops[len(ops)-2:], "the 429 schedules a server-error retry")
			assert.NotContains(t, strings.Join(ops, " "), "schedule_usage_limit")
		})
	}
}

// A turn end that is not a final failure of omp's applies the default verdict.
func TestANonRetryableAgentEndCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(r *rig)
		frame string
	}{
		{"a run that omp continues", nil, `{"type":"agent_end","isTerminal":false,"messages":[{"role":"assistant","stopReason":"error","errorId":135168,"errorStatus":500,"errorMessage":"500 Internal server error"}]}`},
		{"an interrupted turn", func(r *rig) {
			r.agent.Mu.Lock()
			r.agent.turn.interruptRequested = true
			r.agent.Mu.Unlock()
		}, ompAgentEnd500},
		{"a turn that ends while the agent stops", func(r *rig) { r.agent.NoteIntentionalStop() }, ompAgentEnd500},
		{"an aborted run", nil, `{"type":"agent_end","isTerminal":true,"messages":[{"role":"assistant","stopReason":"aborted","errorId":135168,"errorMessage":"Request was aborted"}]}`},
		{"a completed run", nil, frameAgentEnd},
		{"a run with no message", nil, `{"type":"agent_end","isTerminal":true,"messages":[]}`},
		{"a malformed frame", nil, `{"type":"agent_end","isTerminal":true,"messages":"none"}`},
		{"an authentication failure", nil, ompAgentEndWithError(0x1000|0x20000|0x1000000, 401)},
		{"an exceeded context", nil, ompAgentEndWithError(0x1000|0x20000|0x800000, 400)},
		{"a blocked content", nil, ompAgentEndWithError(0x1000|0x20000|0x8000, 400)},
		{"an account policy", nil, ompAgentEndWithError(0x1000|0x20000|0x4000, 403)},
		// omp sends errorId as a signed 32-bit integer, so the top bit reads
		// as a negative number: PayloadRejected|Transient|Class.
		{"a rejected payload", nil, ompAgentEndWithError(int64(int32(-0x80000000|0x20000|0x1000)), 413)},
		{"a raw HTTP status in errorId", nil, ompAgentEndWithError(503, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.emit(`{"type":"agent_start"}`)
			if tc.setup != nil {
				tc.setup(r)
			}
			r.emit(tc.frame)
			require.Equal(t, []string{"turn_end", "cancel_server_error"}, providerkittest.TurnEndAndRetryOpNames(r.sink.Timeline()))
			assert.Equal(t, providerkit.TurnEndedWithoutRetryableFailure, r.sink.LastAutoCancelReason())
		})
	}
}

// The Transient and Timeout bits schedule a server-error retry, and the
// UsageLimit bit is a usage limit with no stated window.
func TestTheErrorIDClassOfAFailedTurn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		frame string
		want  []string
	}{
		{"the transient bit", ompAgentEndWithError(0x1000|0x20000, 0), []string{"turn_end", "schedule_server_error"}},
		{"the timeout bit", ompAgentEndWithError(0x1000|0x40000, 0), []string{"turn_end", "schedule_server_error"}},
		{"the usage-limit bit", ompAgentEndWithError(0x1000|0x80000, 0), []string{"turn_end", "cancel_server_error"}},
		{"the usage-limit bit with the transient bit", ompAgentEndWithError(0x1000|0x80000|0x20000, 0), []string{"turn_end", "cancel_server_error"}},
		{"a 429 with no class", ompAgentEndWithError(0, 429), []string{"turn_end", "schedule_server_error"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.emit(`{"type":"agent_start"}`, tc.frame)
			assert.Equal(t, tc.want, providerkittest.TurnEndAndRetryOpNames(r.sink.Timeline()))
		})
	}
}

// ompTurnFailure reads the captured failures in the order of the classifier
// recipe, and a requested wait completes the verdict.
func TestOmpTurnFailure(t *testing.T) {
	t.Parallel()
	fallback := time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC)
	failedAt429 := time.UnixMilli(1793150801117).UTC()
	for _, tc := range []struct {
		name     string
		frame    string
		stopped  bool
		deadline time.Time
		kind     providerkit.TurnFailureKind
		window   providerkit.UsageWindow
		resetAt  time.Time
	}{
		{"a 500 is transient", ompAgentEnd500, false, time.Time{}, providerkit.TurnFailureTransient, 0, time.Time{}},
		{"a dated 1308 lifts at its stamp", ompAgentEndUsage1308, false, time.Time{}, providerkit.TurnFailureUsageLimited, 0, time.Date(2026, 10, 28, 12, 6, 1, 0, time.UTC)},
		{
			"a requested wait does not replace the 1308 stamp", ompAgentEndUsage1308, false, fallback.Add(67156270 * time.Millisecond),
			providerkit.TurnFailureUsageLimited, 0, time.Date(2026, 10, 28, 12, 6, 1, 0, time.UTC),
		},
		{"a dropped stream is transient", ompAgentEndDrop, false, time.Time{}, providerkit.TurnFailureTransient, 0, time.Time{}},
		{"a 429 is a rate limit", ompAgentEnd429, false, time.Time{}, providerkit.TurnFailureRateLimited, 0, time.Time{}},
		{
			"a 429 with a requested wait lifts at the deadline", ompAgentEnd429, false, failedAt429.Add(time.Hour),
			providerkit.TurnFailureUsageLimited, 0, failedAt429.Add(time.Hour),
		},
		{
			"a requested wait in the past leaves the rate limit", ompAgentEnd429, false, failedAt429.Add(-time.Second),
			providerkit.TurnFailureRateLimited, 0, time.Time{},
		},
		{
			"a requested wait beyond 31 days leaves the rate limit", ompAgentEnd429, false, failedAt429.Add(32 * 24 * time.Hour),
			providerkit.TurnFailureRateLimited, 0, time.Time{},
		},
		{
			"a requested wait completes a usage limit with no reset", ompAgentEndWithError(0x1000|0x80000, 0), false, fallback.Add(30 * time.Minute),
			providerkit.TurnFailureUsageLimited, 0, fallback.Add(30 * time.Minute),
		},
		{
			"the usage-limit flag is a usage limit with no stated window", ompAgentEndWithError(0x1000|0x80000|0x20000, 429), false, time.Time{},
			providerkit.TurnFailureUsageLimited, providerkit.UsageWindowUnstated, time.Time{},
		},
		{"a requested wait does not make a transient failure a usage limit", ompAgentEnd500, false, fallback.Add(time.Hour), providerkit.TurnFailureTransient, 0, time.Time{}},
		{"a MiMo connection failure sent as a 400 is transient", ompAgentEndMiMo400, false, time.Time{}, providerkit.TurnFailureTransient, 0, time.Time{}},
		{"a stop is not retryable", ompAgentEnd500, true, time.Time{}, providerkit.TurnFailureNotRetryable, 0, time.Time{}},
		{"a stop is not retryable with a requested wait", ompAgentEnd429, true, failedAt429.Add(time.Hour), providerkit.TurnFailureNotRetryable, 0, time.Time{}},
		{"the auth flag is not retryable", ompAgentEndWithError(0x1000|0x20000|0x1000000, 401), false, time.Time{}, providerkit.TurnFailureNotRetryable, 0, time.Time{}},
		{
			"a run that omp continues is not retryable",
			`{"type":"agent_end","isTerminal":false,"messages":[{"role":"assistant","stopReason":"error","errorId":135168,"errorStatus":500,"errorMessage":"500 Internal server error"}]}`,
			false, time.Time{}, providerkit.TurnFailureNotRetryable, 0, time.Time{},
		},
		{
			"an absent isTerminal reads as terminal",
			`{"type":"agent_end","messages":[{"role":"assistant","stopReason":"error","errorId":135168,"errorStatus":500,"errorMessage":"500 Internal server error"}]}`,
			false, time.Time{}, providerkit.TurnFailureTransient, 0, time.Time{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var env agentEndEnvelope
			require.NoError(t, json.Unmarshal([]byte(tc.frame), &env))
			failure := ompTurnFailure(env, tc.stopped, tc.deadline, fallback)
			assert.Equal(t, tc.kind, failure.Kind)
			assert.Equal(t, tc.window, failure.Window)
			assert.True(t, tc.resetAt.Equal(failure.ResetAt), "reset %v, want %v", failure.ResetAt, tc.resetAt)
		})
	}
}

// The flags of errorId keep the values that omp declares.
func TestOmpErrorFlagsMatchOmp(t *testing.T) {
	t.Parallel()
	// Flag in packages/ai/src/error/flags.ts of omp 18.12.6.
	assert.Equal(t, uint32(0x0000_4000), ompErrorAccountPolicy)
	assert.Equal(t, uint32(0x0000_8000), ompErrorContentBlocked)
	assert.Equal(t, uint32(0x0002_0000), ompErrorTransient)
	assert.Equal(t, uint32(0x0004_0000), ompErrorTimeout)
	assert.Equal(t, uint32(0x0008_0000), ompErrorUsageLimit)
	assert.Equal(t, uint32(0x0080_0000), ompErrorContextOverflow)
	assert.Equal(t, uint32(0x0100_0000), ompErrorAuthFailed)
	assert.Equal(t, uint32(0x8000_0000), ompErrorPayloadRejected)
	// The probes: 135168 is Transient|Class, and 659456 is
	// UsageLimit|Transient|Class.
	assert.Equal(t, ompErrorTransient|0x1000, ompErrorFlags(&agentEndMessage{ErrorID: 135168}))
	assert.Equal(t, ompErrorUsageLimit|ompErrorTransient|0x1000, ompErrorFlags(&agentEndMessage{ErrorID: 659456}))
	assert.Equal(t, ompErrorPayloadRejected|ompErrorTransient, ompErrorFlags(&agentEndMessage{ErrorID: int64(int32(-0x80000000 | 0x20000))}),
		"a negative errorId keeps its top flag")
}

func TestAnAgentEndThatOmpContinuesStillEndsTheTurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`, `{"type":"agent_end","isTerminal":false,"messages":[]}`)

	active, _ := r.sink.LastTurnActive()
	assert.False(t, active, "omp reports the session idle; the scheduled work starts a turn of its own")
	messages := r.sink.Messages()
	require.NotEmpty(t, messages)
	assert.True(t, messages[len(messages)-1].TurnEnd)
}

func TestAnErrorEndsTheTurnAsAnError(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"partial"}}`,
		`{"type":"agent_end","isTerminal":true,"messages":[{"role":"assistant","stopReason":"error","errorMessage":"400 bad request"}]}`)

	var kept *agenttest.Message
	for i, message := range r.sink.Messages() {
		if !message.TurnEnd && message.SpanID == "" {
			kept = &r.sink.Messages()[i]
		}
	}
	require.NotNil(t, kept, "text no message_end completed is kept")
	assert.Contains(t, string(kept.Content), string(agent.MessageCompletionError))
}

func TestMessageEndRoles(t *testing.T) {
	t.Parallel()

	t.Run("an assistant message carries the session usage", func(t *testing.T) {
		r := newRig(t)
		r.emit(frameAssistantText)
		messages := r.sink.Messages()
		// The first row is the reply's thinking (see
		// TestAnAssistantMessagePersistsItsThinkingAsARowOfItsOwn).
		require.Len(t, messages, 2)
		assert.JSONEq(t, frameAssistantText, string(messages[1].Content), "omp's own frame is the row")
		var metadata map[string]any
		require.NoError(t, json.Unmarshal(messages[1].Metadata, &metadata))
		assert.InDelta(t, 0.0021, metadata[contracts.SessionInfoKeyTotalCostUsd], 1e-9)
		assert.NotNil(t, metadata[contracts.SessionInfoKeyContextUsage])
		assert.NotZero(t, r.sink.SessionInfoCount(), "the usage is broadcast")
	})

	t.Run("the user echo and a tool result are dropped", func(t *testing.T) {
		r := newRig(t)
		r.emit(frameUserEcho, frameToolResult, `{"type":"message_end","message":{"role":"bashExecution","command":"ls"}}`)
		assert.Empty(t, r.sink.Messages())
	})

	t.Run("a displayed custom message is kept and a hidden one is dropped", func(t *testing.T) {
		r := newRig(t)
		r.emit(
			`{"type":"message_end","message":{"role":"custom","customType":"async-result","content":"Background job done.","display":true,"details":{"jobs":[]}}}`,
			`{"type":"message_end","message":{"role":"custom","customType":"magic-keyword","content":"hidden","display":false}}`,
		)
		messages := r.sink.Messages()
		require.Len(t, messages, 1)
		assert.Contains(t, string(messages[0].Content), "Background job done.")
	})

	t.Run("an unknown role reaches the transcript", func(t *testing.T) {
		r := newRig(t)
		r.emit(`{"type":"message_end","message":{"role":"hologram","content":"?"}}`)
		assert.Len(t, r.sink.Messages(), 1)
	})

	t.Run("a message_end resets the streamed text", func(t *testing.T) {
		r := newRig(t)
		r.emit(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":"Hello from the mock."}}`, frameAssistantText)
		r.emit(`{"type":"agent_start"}`, frameAgentEnd)
		var rows []agenttest.Message
		for _, message := range r.sink.Messages() {
			if !message.TurnEnd {
				rows = append(rows, message)
			}
		}
		require.Len(t, rows, 2, "no second copy of the streamed text")
		assert.Equal(t, reasoningRow("The user wants a greeting."), assembledRow(t, rows[0]))
		assert.JSONEq(t, frameAssistantText, string(rows[1].Content))
	})
}

// omp ends the assistant message that a stop cut with its partial text and a stop
// reason: `aborted`, or `error` while a tool still ran (see turnState). The row keeps
// omp's frame and states the stop, so the text draws its interruption marker.
func TestAMessageEndThatAStopCutStatesTheStop(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		stopReason string
		noted      bool
	}{
		"a clean abort":                        {stopReason: contracts.OhMyPiStopReasonAborted},
		"a clean abort that LeapMux asked for": {stopReason: contracts.OhMyPiStopReasonAborted, noted: true},
		"an abort while a tool ran":            {stopReason: contracts.OhMyPiStopReasonError, noted: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.emit(`{"type":"agent_start"}`)
			if tc.noted {
				r.agent.Mu.Lock()
				r.agent.turn.interruptRequested = true
				r.agent.Mu.Unlock()
			}
			frame := `{"type":"message_end","message":{"role":"assistant","stopReason":"` + tc.stopReason + `","content":[{"type":"text","text":"Half a sen"}]}}`
			r.emit(frame)

			messages := r.sink.Messages()
			require.Len(t, messages, 1)
			assert.JSONEq(t, frame, string(messages[0].Content), "omp's own frame is the row")
			assert.Equal(t, agent.MessageCompletionInterrupted, messages[0].Completion)
		})
	}
}

// The completion marker distinguishes failures from stops and complete messages.
func TestMessageEndCompletionMatchesTheVisibleText(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		message string
		noted   bool
		want    agent.MessageCompletion
	}{
		"a failure that nobody asked for":     {message: `{"role":"assistant","stopReason":"error","content":[{"type":"text","text":"Half"}]}`, want: agent.MessageCompletionError},
		"a message that finished at the stop": {message: `{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"Whole."}]}`, noted: true},
		"a cut message with a tool call only": {message: `{"role":"assistant","stopReason":"aborted","content":[{"type":"toolCall","id":"call-1","name":"bash"}]}`, noted: true},
		"a cut message with blank text":       {message: `{"role":"assistant","stopReason":"aborted","content":[{"type":"text","text":" "}]}`, noted: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.emit(`{"type":"agent_start"}`)
			if tc.noted {
				r.agent.Mu.Lock()
				r.agent.turn.interruptRequested = true
				r.agent.Mu.Unlock()
			}
			r.emit(`{"type":"message_end","message":` + tc.message + `}`)

			messages := r.sink.Messages()
			require.Len(t, messages, 1)
			assert.Equal(t, tc.want, messages[0].Completion)
		})
	}
}

// assembledRow decodes one row that the worker wrote in LeapMux's assembled-message
// envelope.
func assembledRow(t *testing.T, message agenttest.Message) map[string]string {
	t.Helper()
	var row map[string]string
	require.NoError(t, json.Unmarshal(message.Content, &row))
	return row
}

// reasoningRow is the assembled-message envelope of one completed thinking row.
func reasoningRow(text string) map[string]string {
	return map[string]string{
		contracts.AssembledMessageFieldType:       contracts.AssembledMessageType,
		contracts.AssembledMessageFieldKind:       contracts.AssembledMessageKindReasoning,
		contracts.AssembledMessageFieldText:       text,
		contracts.AssembledMessageFieldCompletion: contracts.AssembledMessageCompletionComplete,
	}
}

// assembledTextRow is the assembled-message envelope of one row of streamed text
// that no message_end completed.
func assembledTextRow(text string, completion agent.MessageCompletion) map[string]string {
	return map[string]string{
		contracts.AssembledMessageFieldType:       contracts.AssembledMessageType,
		contracts.AssembledMessageFieldKind:       contracts.AssembledMessageKindText,
		contracts.AssembledMessageFieldText:       text,
		contracts.AssembledMessageFieldCompletion: string(completion),
	}
}

// omp states a reasoning model's reply as ONE message that holds a thinking block
// and a text block. One transcript row draws one kind of text, so the worker
// persists the thinking as a row of its own, before the reply's own row.
func TestAnAssistantMessagePersistsItsThinkingAsARowOfItsOwn(t *testing.T) {
	t.Parallel()

	t.Run("the thinking row comes before the reply, which keeps the usage", func(t *testing.T) {
		r := newRig(t)
		r.emit(frameAssistantText)
		messages := r.sink.Messages()
		require.Len(t, messages, 2)
		assert.Equal(t, reasoningRow("The user wants a greeting."), assembledRow(t, messages[0]))
		assert.Empty(t, messages[0].Metadata, "the usage rides on the reply's own row")
		assert.JSONEq(t, frameAssistantText, string(messages[1].Content), "omp's own frame is still the reply's row")
		assert.NotEmpty(t, messages[1].Metadata)
	})

	t.Run("several thinking blocks join into one row", func(t *testing.T) {
		r := newRig(t)
		r.emit(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"First."},{"type":"text","text":"Hi."},{"type":"thinking","thinking":"Second."}],"stopReason":"stop"}}`)
		messages := r.sink.Messages()
		require.Len(t, messages, 2)
		assert.Equal(t, reasoningRow("First.\n\nSecond."), assembledRow(t, messages[0]))
	})

	t.Run("thinking beside a tool call is a row of its own too", func(t *testing.T) {
		r := newRig(t)
		r.emit(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Run it."},{"type":"toolCall","id":"call_1","name":"bash","arguments":{"command":"ls"}}],"stopReason":"toolUse"}}`)
		messages := r.sink.Messages()
		require.Len(t, messages, 2)
		assert.Equal(t, reasoningRow("Run it."), assembledRow(t, messages[0]))
	})

	t.Run("a thinking block with no visible text adds no row", func(t *testing.T) {
		r := newRig(t)
		frame := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"  ","thinkingSignature":"sig"},{"type":"redactedThinking","data":"opaque"},{"type":"text","text":"Hi."}],"stopReason":"stop"}}`
		r.emit(frame)
		messages := r.sink.Messages()
		require.Len(t, messages, 1)
		assert.JSONEq(t, frame, string(messages[0].Content))
	})

	t.Run("a subagent's thinking reaches the child transcript", func(t *testing.T) {
		r := newRig(t)
		r.emit(frameTaskStart, frameStarted)
		childID := subagentRow(t, r, "Probe").ChildAgentID
		frame := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Count the fruit."},{"type":"text","text":"Three."}],"stopReason":"stop"}}`
		r.emit(subagentEvent("Probe", frame))
		messages := r.sink.Child(childID).Messages()
		require.Len(t, messages, 2)
		assert.Equal(t, reasoningRow("Count the fruit."), assembledRow(t, messages[0]))
		assert.JSONEq(t, frame, string(messages[1].Content))
	})
}

func TestMessageUpdateReportsProgress(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"abcd"},"message":{"role":"assistant","content":[]}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":"efgh"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_delta","contentIndex":2,"delta":"{\"command\":"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":""}}`,
	)
	assert.Equal(t, int64(2), r.sink.LastThinkingTokens(), "text and thinking deltas count; a tool-call delta does not")
}

func TestToolCallsOpenAndCloseTheirSpan(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameBashStart, frameBashUpdate, frameBashEnd)

	messages := r.sink.Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, "call_1", messages[0].SpanID)
	assert.False(t, messages[0].Closing)
	assert.NotZero(t, messages[0].SpanColor, "a tool call reserves a rail color")
	assert.Equal(t, "call_1", messages[1].SpanID)
	assert.True(t, messages[1].Closing)
	assert.Equal(t, "bash", messages[1].SpanType)
	assert.Contains(t, r.sink.ClosedSpans(), "call_1")
	assert.False(t, r.agent.HasCumulativeOutputForTest("call_1"), "the live output of an ended call is released")

	var sawTail bool
	for _, update := range r.sink.ProgressUpdates() {
		if update.Operation == agent.ProgressOutputTail && update.ScopeID == "call_1" && update.Text == "probe-output\n" {
			sawTail = true
		}
	}
	assert.True(t, sawTail, "a running call's output streams as a live tail")
}

func TestATaskCallOwnsNoSpan(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"tool_execution_start","toolCallId":"call_1","toolName":"task","args":{"context":"c","tasks":[{"name":"ScoutOne","agent":"task","task":"Say hello."}]}}`)
	messages := r.sink.Messages()
	require.Len(t, messages, 1)
	assert.True(t, messages[0].NoSpan, "a spawn draws no rail")
	assert.Zero(t, messages[0].SpanColor)
	assert.Empty(t, r.sink.OpenSpans())
}

func TestAnUpdateAfterItsCallEndedIsDropped(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(
		`{"type":"agent_start"}`,
		`{"type":"tool_execution_start","toolCallId":"call_1","toolName":"task","args":{"tasks":[{"name":"ScoutOne","task":"t"}]}}`,
		`{"type":"tool_execution_end","toolCallId":"call_1","toolName":"task","result":{"content":[{"type":"text","text":"Spawned agent ScoutOne"}],"details":{"async":{"state":"running","jobId":"ScoutOne","type":"task"}}},"isError":false}`,
		// omp keeps updating a background task's call after its end.
		`{"type":"tool_execution_update","toolCallId":"call_1","toolName":"task","args":{},"partialResult":{"content":[{"type":"text","text":"Running agent ScoutOne..."}],"details":{}}}`,
		frameAgentEnd,
	)
	for _, message := range r.sink.Messages() {
		if message.SpanID == "call_1" {
			assert.NotEqual(t, agent.MessageCompletionInterrupted, message.Completion,
				"the ended call is not persisted again as an incomplete call")
		}
	}
	closing := 0
	for _, message := range r.sink.Messages() {
		if message.SpanID == "call_1" && message.Closing {
			closing++
		}
	}
	assert.Equal(t, 1, closing)
}

func TestIncompleteToolsKeepTheirOrderAndPartialResult(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(
		`{"type":"agent_start"}`,
		`{"type":"tool_execution_start","toolCallId":"b","toolName":"bash","args":{"command":"one"}}`,
		`{"type":"tool_execution_start","toolCallId":"a","toolName":"read","args":{"path":"x"}}`,
		`{"type":"tool_execution_update","toolCallId":"b","toolName":"bash","args":{},"partialResult":{"content":[{"type":"text","text":"so far"}],"details":{}}}`,
		`{"type":"agent_end","isTerminal":true,"messages":[{"role":"assistant","stopReason":"aborted"}]}`,
	)
	var closing []agenttest.Message
	for _, message := range r.sink.Messages() {
		if message.Closing {
			closing = append(closing, message)
		}
	}
	require.Len(t, closing, 2)
	assert.Equal(t, "b", closing[0].SpanID, "the call that started first closes first")
	assert.Equal(t, "a", closing[1].SpanID)
	assert.Equal(t, agent.MessageCompletionInterrupted, closing[0].Completion)
	var supplement contracts.OhMyPiIncompleteToolSupplement
	require.NoError(t, json.Unmarshal(closing[0].SupplementalContent, &supplement))
	assert.Equal(t, "b", supplement.ToolCallID)
	assert.Equal(t, "bash", supplement.ToolName)
	assert.Contains(t, string(supplement.PartialResult), "so far")
	assert.Empty(t, closing[1].SupplementalContent, "a call that reported nothing keeps no supplement")
}

// outputUpdates returns the live-output updates of one call, in order.
func outputUpdates(r *rig, toolCallID string, operation agent.ProgressOperation) []agent.ProgressUpdate {
	var out []agent.ProgressUpdate
	for _, update := range r.sink.ProgressUpdates() {
		if update.Operation == operation && update.ScopeID == toolCallID {
			out = append(out, update)
		}
	}
	return out
}

// partialUpdate is a tool_execution_update of call_1 whose partial result holds
// the text and the details given.
func partialUpdate(t *testing.T, text string, details map[string]any) string {
	t.Helper()
	return mustJSON(t, map[string]any{
		"type": "tool_execution_update", "toolCallId": "call_1", "toolName": "bash", "args": map[string]any{},
		"partialResult": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": text}},
			"details": details,
		},
	})
}

func TestOutputReportingWithATruncatedResult(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameBashStart, partialUpdate(t, "tail only", map[string]any{"meta": map[string]any{"truncation": map[string]any{"totalBytes": 90000}}}))

	totals := outputUpdates(r, "call_1", agent.ProgressOutputTotal)
	require.Len(t, totals, 1, "only omp's own total is reported: the text is not the whole output")
	assert.True(t, totals[0].Exact)
	assert.Equal(t, int64(90000), totals[0].Value, "omp's own total counts the whole output")
	tails := outputUpdates(r, "call_1", agent.ProgressOutputTail)
	require.Len(t, tails, 1)
	assert.Equal(t, "tail only", tails[0].Text)
	assert.True(t, tails[0].Truncated, "the tail states that earlier output is missing")
}

// A truncation that states no total still states that output is missing, and
// reports no total at all rather than the length of the tail.
func TestOutputReportingWithATruncationThatStatesNoTotal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameBashStart, partialUpdate(t, "tail only", map[string]any{"meta": map[string]any{"truncation": map[string]any{}}}))

	assert.Empty(t, outputUpdates(r, "call_1", agent.ProgressOutputTotal))
	tails := outputUpdates(r, "call_1", agent.ProgressOutputTail)
	require.Len(t, tails, 1)
	assert.True(t, tails[0].Truncated)
}

// A result that kept its head grows by appends, so its length is the total. The
// live tail keeps only the last liveOutputLimit bytes of it.
func TestOutputReportingOfALongResultKeepsTheTail(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	text := strings.Repeat("a", liveOutputLimit) + strings.Repeat("b", 100)
	r.emit(frameBashStart, partialUpdate(t, text, map[string]any{}))

	totals := outputUpdates(r, "call_1", agent.ProgressOutputTotal)
	require.Len(t, totals, 1)
	assert.Equal(t, int64(len(text)), totals[0].Value)
	assert.False(t, totals[0].Exact)
	tails := outputUpdates(r, "call_1", agent.ProgressOutputTail)
	require.Len(t, tails, 1)
	assert.Len(t, tails[0].Text, liveOutputLimit)
	assert.True(t, strings.HasSuffix(tails[0].Text, strings.Repeat("b", 100)), "the tail is the LAST bytes")
	assert.True(t, tails[0].Truncated, "the tail states that earlier output is missing")
}

func TestOutputReportingOfAResultWithNoText(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameBashStart,
		`{"type":"tool_execution_update","toolCallId":"call_1","toolName":"bash","args":{},"partialResult":{"content":[{"type":"image","data":"x"}],"details":{}}}`,
		`{"type":"tool_execution_update","toolCallId":"call_1","toolName":"bash","args":{},"partialResult":"garbled"}`,
		`{"type":"tool_execution_update","toolCallId":"call_1","toolName":"bash","args":{}}`)

	assert.Empty(t, outputUpdates(r, "call_1", agent.ProgressOutputTotal))
	assert.Empty(t, outputUpdates(r, "call_1", agent.ProgressOutputTail))
}

// A tool frame without a call id cannot be matched to its call, so the worker
// drops it rather than open a span that nothing closes.
func TestAToolFrameWithNoCallIDIsDropped(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(
		`{"type":"tool_execution_start","toolName":"bash","args":{"command":"ls"}}`,
		`{"type":"tool_execution_update","toolName":"bash","partialResult":{"content":[{"type":"text","text":"x"}]}}`,
		`{"type":"tool_execution_end","toolName":"bash","result":{"content":[]},"isError":false}`,
		`{"type":"tool_execution_start","toolCallId":7}`,
	)
	assert.Empty(t, r.sink.Messages())
	assert.Empty(t, r.sink.OpenSpans())
	assert.Empty(t, r.sink.ProgressUpdates())
}

func TestAMalformedMessageEndReachesTheTranscript(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	frame := `{"type":"message_end","message":7}`
	r.emit(frame)
	messages := r.sink.Messages()
	require.Len(t, messages, 1)
	assert.JSONEq(t, frame, string(messages[0].Content), "the reader can still inspect it")
}

// The run's text is complete at an end that a pending steer continues: only the
// turn goes on. The text is persisted once, as complete, and not again when the
// turn ends.
func TestAnAgentEndThatAQueuedSteerContinuesPersistsTheRunsText(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"First run's text"}}`)
	require.NoError(t, r.agent.SteerInput("also check the tests", nil))
	r.emit(`{"type":"agent_end","isTerminal":false,"messages":[]}`)

	messages := r.sink.Messages()
	require.Len(t, messages, 1)
	assert.Equal(t, assembledTextRow("First run's text", agent.MessageCompletionComplete), assembledRow(t, messages[0]))

	r.emit(`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"user","steering":true,"content":[{"type":"text","text":"also check the tests"}]}}`,
		frameAgentEnd)
	rows := 0
	for _, message := range r.sink.Messages() {
		if strings.Contains(string(message.Content), "First run's text") {
			rows++
		}
	}
	assert.Equal(t, 1, rows)
}

func TestAgentEndCompletion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		frame string
		want  agent.MessageCompletion
	}{
		{name: "an error stop", frame: `{"messages":[{"role":"assistant","stopReason":"error"}]}`, want: agent.MessageCompletionError},
		{name: "an abort", frame: `{"messages":[{"role":"assistant","stopReason":"aborted"}]}`, want: agent.MessageCompletionInterrupted},
		{name: "the last assistant message decides", frame: `{"messages":[{"role":"assistant","stopReason":"error"},{"role":"user"},{"role":"assistant","stopReason":"stop"}]}`, want: agent.MessageCompletionInterrupted},
		{name: "a message after the last assistant message is skipped", frame: `{"messages":[{"role":"assistant","stopReason":"error"},{"role":"toolResult"},{"role":"user"}]}`, want: agent.MessageCompletionError},
		{name: "no messages", frame: `{}`, want: agent.MessageCompletionInterrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var env agentEndEnvelope
			require.NoError(t, json.Unmarshal([]byte(tc.frame), &env))
			assert.Equal(t, tc.want, env.completion())
		})
	}
}

func TestTurnDurationMs(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	assert.Nil(t, turnDurationMs(time.Time{}, start), "a turn whose start this worker never saw")
	assert.Nil(t, turnDurationMs(start, start.Add(-time.Millisecond)), "a clock that moved backwards")
	zero := turnDurationMs(start, start)
	require.NotNil(t, zero, "a real zero is stated, not left out")
	assert.Equal(t, int64(0), *zero)
	long := turnDurationMs(start, start.Add(90*time.Minute+1500*time.Millisecond))
	require.NotNil(t, long)
	assert.Equal(t, int64(5401500), *long)
}

func TestMessageText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		content string
		want    string
	}{
		{name: "a string", content: `"Do the work."`, want: "Do the work."},
		{name: "text blocks", content: `[{"type":"text","text":"First."},{"type":"image","data":"x"},{"type":"text","text":""},{"type":"text","text":"Second."}]`, want: "First.\nSecond."},
		{name: "no blocks", content: `[]`, want: ""},
		{name: "no content", content: ``, want: ""},
		{name: "another shape", content: `7`, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, messageText(json.RawMessage(tc.content)))
		})
	}
}

func TestJoinOutputTail(t *testing.T) {
	t.Parallel()
	blocks := []contentBlock{{Type: "text", Text: "héllo "}, {Type: "image"}, {Type: "text", Text: "world"}}
	total := len("héllo ") + len("world")

	whole, clipped := joinOutputTail(blocks, total, 0)
	assert.Equal(t, "héllo world", whole)
	assert.False(t, clipped)

	tail, clipped := joinOutputTail(blocks, total, 9)
	assert.True(t, clipped)
	assert.Equal(t, "llo world", tail, "the cut moves to a rune boundary")

	empty, clipped := joinOutputTail(nil, 0, 8)
	assert.Empty(t, empty)
	assert.False(t, clipped)
}

// turnEndRows returns the turn-end rows, in order.
func turnEndRows(messages []agenttest.Message) []agenttest.Message {
	var ends []agenttest.Message
	for _, message := range messages {
		if message.TurnEnd {
			ends = append(ends, message)
		}
	}
	return ends
}

// omp ends a run with isTerminal:false when a steer that arrived at its tail
// continues the work at once. The steer is LeapMux's own input to the running
// turn, so the turn stays open across the boundary: no divider, no idle state,
// and the next run belongs to the same turn.
func TestAnAgentEndThatAQueuedSteerContinuesKeepsTheTurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`, frameBashStart, frameBashEnd)
	require.NoError(t, r.agent.SteerInput("also check the tests", nil))
	r.emit(`{"type":"agent_end","isTerminal":false,"messages":[]}`)

	active, _ := r.sink.LastTurnActive()
	assert.True(t, active, "the queued steer continues the turn")
	assert.Empty(t, turnEndRows(r.sink.Messages()), "no divider between the two runs")

	r.emit(`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"user","steering":true,"content":[{"type":"text","text":"also check the tests"}]}}`,
		frameAgentEnd)

	active, _ = r.sink.LastTurnActive()
	assert.False(t, active)
	ends := turnEndRows(r.sink.Messages())
	require.Len(t, ends, 1, "one turn, one divider")
	var metadata map[string]json.Number
	require.NoError(t, json.Unmarshal(ends[0].Metadata, &metadata))
	assert.Equal(t, json.Number("1"), metadata[contracts.MessageMetadataFieldToolUses], "the turn counts the tool of its first run")
	for _, published := range r.sink.TurnActives()[:len(r.sink.TurnActives())-1] {
		assert.True(t, published, "no idle state is published before the turn ends")
	}
}

// A steer that never enters a run continues nothing. An end with `isTerminal:
// false` then comes from other scheduled work, which can start much later, so
// the end must end the turn. Otherwise the tab stays busy until that work runs.
//
// omp runs a built-in slash command BEFORE it reads `streamingBehavior`, and
// answers `agentInvoked:false` (rpc-mode.ts, the `prompt` case). An extension
// command resolves local-only after the acknowledgement, with a prompt_result.
// A steer can also fail after omp acknowledged it.
func TestASteerThatNeverEntersARunContinuesNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// reply answers the steer's prompt command.
		reply func(t *testing.T, command recordedCommand) *rigReply
		// after returns the frames that omp sends after the acknowledgement.
		after func(t *testing.T, id string) []string
	}{
		{
			name: "a slash command omp answered itself",
			reply: func(*testing.T, recordedCommand) *rigReply {
				return &rigReply{Data: json.RawMessage(`{"agentInvoked":false}`)}
			},
		},
		{
			name: "a steer that resolved local-only after the acknowledgement",
			after: func(t *testing.T, id string) []string {
				return []string{mustJSON(t, map[string]any{"type": "prompt_result", "id": id, "agentInvoked": false})}
			},
		},
		{
			name: "a steer that failed after the acknowledgement",
			after: func(t *testing.T, id string) []string {
				return []string{mustJSON(t, map[string]any{"type": "response", "id": id, "command": "prompt", "success": false, "error": "preflight failed"})}
			},
		},
		{
			// The worker reads the acknowledgement on another goroutine than the
			// read loop, so the read loop can handle a later frame of the same
			// steer first.
			name: "a local-only result that the read loop handled before the acknowledgement",
			reply: func(t *testing.T, command recordedCommand) *rigReply {
				return &rigReply{Before: []string{mustJSON(t, map[string]any{"type": "prompt_result", "id": command.ID, "agentInvoked": false})}}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.respond(func(command recordedCommand) *rigReply {
				if command.Type == CommandPrompt && tc.reply != nil {
					return tc.reply(t, command)
				}
				return nil
			})
			r.emit(`{"type":"agent_start"}`)
			require.NoError(t, r.agent.SteerInput("/session info", nil))
			if tc.after != nil {
				r.emit(tc.after(t, r.commandsOfType(CommandPrompt)[0].ID)...)
			}

			r.emit(`{"type":"agent_end","isTerminal":false,"messages":[]}`)
			active, _ := r.sink.LastTurnActive()
			assert.False(t, active, "no steer continues the turn")
			assert.Len(t, turnEndRows(r.sink.Messages()), 1)
		})
	}
}

// A steer that omp took, and a second steer that it answered itself: the first
// one still continues the turn.
func TestASteerThatOmpTookStillContinuesTheTurnBesideALocalOne(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandPrompt && command.Payload["message"] == "/session info" {
			return &rigReply{Data: json.RawMessage(`{"agentInvoked":false}`)}
		}
		return nil
	})
	r.emit(`{"type":"agent_start"}`)
	require.NoError(t, r.agent.SteerInput("also check the tests", nil))
	require.NoError(t, r.agent.SteerInput("/session info", nil))

	r.emit(`{"type":"agent_end","isTerminal":false,"messages":[]}`)
	active, _ := r.sink.LastTurnActive()
	assert.True(t, active, "the steer that omp queued continues the turn")
	assert.Empty(t, turnEndRows(r.sink.Messages()))

	r.emit(`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"user","steering":true,"content":[{"type":"text","text":"also check the tests"}]}}`,
		`{"type":"agent_end","isTerminal":false,"messages":[]}`)
	active, _ = r.sink.LastTurnActive()
	assert.False(t, active, "the queued steer was taken, and nothing else continues the turn")
	assert.Len(t, turnEndRows(r.sink.Messages()), 1)
}

// A steer that omp already took into the run continues nothing, so a later
// end with `isTerminal: false` ends the turn, as any other one does.
func TestAnAgentEndThatOmpContinuesAfterTheSteerWasTakenEndsTheTurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`)
	require.NoError(t, r.agent.SteerInput("also check the tests", nil))
	r.emit(`{"type":"message_end","message":{"role":"user","steering":true,"content":[{"type":"text","text":"also check the tests"}]}}`,
		`{"type":"agent_end","isTerminal":false,"messages":[]}`)

	active, _ := r.sink.LastTurnActive()
	assert.False(t, active)
	assert.Len(t, turnEndRows(r.sink.Messages()), 1)
}

func TestMessageUpdateReportsProgressOnTheConversationSinkUnderTheSegmentScope(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	childSink := &agenttest.Sink{}
	child := newConversation(agent.NewProviderServices(childSink), "")
	r.agent.handleMessageUpdate(child, []byte(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":2,"delta":"Partial conversation text"}}`))
	assert.Equal(t, []agent.ProgressUpdate{agent.ModelTextProgress("omp:content:2", "Partial conversation text")}, childSink.ProgressUpdates())
	assert.Empty(t, r.sink.ProgressUpdates())
	r.agent.flushGeneration(child, agent.MessageCompletionInterrupted)
	require.Len(t, childSink.Messages(), 1)
	assert.Equal(t, assembledTextRow("Partial conversation text", agent.MessageCompletionInterrupted), assembledRow(t, childSink.Messages()[0]))
	assert.Contains(t, childSink.ProgressUpdates(), agent.CompleteModelProgress("omp:content:2"))
	assert.Empty(t, r.sink.Messages())
}

func TestFlushGenerationCompletesProgressAfterAFailedWrite(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sink := &agenttest.Sink{PersistErr: assert.AnError}
	c := newConversation(agent.NewProviderServices(sink), "")
	r.agent.handleMessageUpdate(c, []byte(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Retain this text"}}`))
	r.agent.flushGeneration(c, agent.MessageCompletionInterrupted)
	assert.Contains(t, sink.ProgressUpdates(), agent.CompleteModelProgress("omp:content:0"))
	assert.True(t, c.generation.HasPendingWrites())
	sink.PersistErr = nil
	r.agent.flushGeneration(c, agent.MessageCompletionComplete)
	require.Len(t, sink.Messages(), 2, "the recording sink includes the failed attempt")
	assert.Equal(t, assembledTextRow("Retain this text", agent.MessageCompletionInterrupted), assembledRow(t, sink.Messages()[1]))
}

func TestAutoCompactionStartIsLiveStateOnTheConversationSink(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"auto_compaction_start","reason":"threshold","action":"snapcompact"}`)
	assert.Empty(t, r.sink.Messages())
	assert.Empty(t, r.sink.PersistedNotifications())
	assert.Empty(t, r.sink.Notifications())
	assert.Contains(t, r.sink.SessionInfos(), map[string]interface{}{contracts.SessionInfoKeyCompaction: map[string]interface{}{contracts.CompactionFieldTrigger: "threshold"}})
}

func TestAutoCompactionEndStoresTheEndAndEndsLiveState(t *testing.T) {
	t.Parallel()
	for _, frame := range []string{
		`{"type":"auto_compaction_end","result":{"summary":"done"}}`,
		`{"type":"auto_compaction_end","aborted":true}`,
		`{"type":"auto_compaction_end","errorMessage":"The summary failed"}`,
	} {
		t.Run(frame, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.emit(`{"type":"auto_compaction_start"}`, frame)
			require.Len(t, r.sink.PersistedNotifications(), 1)
			assert.JSONEq(t, frame, string(r.sink.PersistedNotifications()[0].Content))
			assert.Contains(t, r.sink.SessionInfos(), map[string]interface{}{contracts.SessionInfoKeyCompaction: nil})
			r.agent.finishOutput(agent.MessageCompletionInterrupted)
			assert.Empty(t, r.sink.Notifications(), "a native end prevents a second synthetic end")
		})
	}
}

func TestFinishOutputEndsAnOpenCompaction(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"auto_compaction_start","reason":"threshold"}`)
	r.agent.finishOutput(agent.MessageCompletionInterrupted)
	r.agent.finishOutput(agent.MessageCompletionInterrupted)
	notifications := r.sink.Notifications()
	require.Len(t, notifications, 1)
	assert.Equal(t, contracts.NotificationTypeCompactionEnded, notifications[0][contracts.NotificationFieldType])
	assert.Equal(t, contracts.CompactionEndOutcomeAborted, notifications[0][contracts.NotificationFieldOutcome])
	assert.Contains(t, r.sink.SessionInfos(), map[string]interface{}{contracts.SessionInfoKeyCompaction: nil})
}

func TestFinishOutputEndsAnOpenChildCompaction(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameTaskStart, frameStarted)
	r.agent.Mu.Lock()
	child := r.agent.subagents["Probe"].conv
	r.agent.Mu.Unlock()
	r.agent.beginCompaction(child, "auto")
	r.agent.finishOutput(agent.MessageCompletionInterrupted)
	notifications := r.sink.Child(child.childID).LeapMuxNotifications()
	require.Len(t, notifications, 1)
	assert.Equal(t, contracts.NotificationTypeCompactionEnded, notifications[0][contracts.NotificationFieldType])
	assert.Equal(t, contracts.CompactionEndOutcomeAborted, notifications[0][contracts.NotificationFieldOutcome])
	assert.False(t, child.compactionOpen)
}

func TestTtsrDiscardsTheAbortedPartialAndKeepsOneTurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	// The text and lifecycle frames come from .tmp/verify-c29/ohmypi/run1.
	// The reasoning block also checks that the discard covers the whole message.
	r.emit(`{"type":"agent_start"}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"I like BANANA "}}`,
		`{"type":"ttsr_triggered","rules":[{"name":"no-banana"}]}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"discarded thought"},{"type":"text","text":"I like BANANA "}],"stopReason":"aborted"}}`)
	assert.Empty(t, r.sink.Messages())
	require.Len(t, r.sink.PersistedNotifications(), 1)
	assert.Empty(t, r.agent.rootConversation().generation.ScopeIDs())
	assert.Contains(t, r.sink.ProgressUpdates(), agent.ResetModelScopeProgress("omp:content:0"))
	r.emit(`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"OK"}],"stopReason":"stop"}}`,
		`{"type":"agent_end","isTerminal":true,"messages":[]}`)
	rows := r.sink.Messages()
	require.Len(t, rows, 2)
	assert.Contains(t, string(rows[0].Content), `"OK"`)
	assert.Contains(t, string(rows[1].Content), `"agent_end"`)
}

func TestFailedMessageEndWithTextIsMarkedError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		text    string
		stopped bool
		want    agent.MessageCompletion
	}{
		{name: "failed text", text: "partial", want: agent.MessageCompletionError},
		{name: "delivered stop", text: "partial", stopped: true, want: agent.MessageCompletionInterrupted},
		{name: "empty text"},
		{name: "blank text", text: "  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.agent.Mu.Lock()
			r.agent.turn.interruptRequested = tc.stopped
			r.agent.Mu.Unlock()
			r.emit(mustJSON(t, map[string]any{"type": "message_end", "message": map[string]any{
				"role": "assistant", "stopReason": "error", "content": []any{map[string]any{"type": "text", "text": tc.text}},
			}}))
			rows := r.sink.Messages()
			require.Len(t, rows, 1)
			assert.Equal(t, tc.want, rows[0].Completion)
		})
	}
}

func TestTtsrFlagEndsWithItsRun(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{`{"type":"agent_start"}`, `{"type":"agent_end","isTerminal":true}`} {
		t.Run(boundary, func(t *testing.T) {
			r := newRig(t)
			r.emit(`{"type":"ttsr_triggered","rules":[]}`, boundary)
			before := len(r.sink.Messages())
			r.emit(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"later partial"}],"stopReason":"aborted"}}`)
			rows := r.sink.Messages()
			require.Len(t, rows, before+1)
			assert.Equal(t, agent.MessageCompletionInterrupted, rows[before].Completion)
		})
	}
}
