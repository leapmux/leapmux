package qoder

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
)

// The research frames of qodercli 1.1.78 (.tmp/retry-research/codebuddy-qoder.md),
// copied verbatim: an api_retry, the codes 10500, 10408, and 113, and a success.
const (
	qoderAPIRetryFrame   = `{"type":"system","subtype":"api_retry","attempt":1,"max_retries":10,"retry_delay_ms":1000,"error_status":500,"error":"server_error","uuid":"367f48b1-5ce8-410c-8350-114ea4b85e65","session_id":"d94c91cd-1ddb-4401-baf7-27af7ade937a"}`
	qoderUnavailableEnd  = `{"type":"result","subtype":"error_during_execution","duration_ms":10303,"duration_api_ms":10100,"is_error":true,"num_turns":1,"stop_reason":"stop_sequence","total_cost_usd":0,"total_credits":0,"usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0,"server_tool_use":{"web_search_requests":0,"web_fetch_requests":0},"service_tier":"standard","cache_creation":{"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":0},"inference_geo":"","iterations":[],"speed":"standard"},"modelUsage":{"<synthetic>":{"inputTokens":0,"outputTokens":0,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"webSearchRequests":0,"costUSD":0,"contextWindow":0,"maxOutputTokens":0}},"permission_denials":[],"errors":["The model service is temporarily unavailable. Please try again later."],"error_code":10500,"fast_mode_state":"off","uuid":"5ede5651-b5f5-4f5f-9428-e49060a38b32","session_id":"d94c91cd-1ddb-4401-baf7-27af7ade937a"}`
	qoderTimedOutEnd     = `{"type":"result","subtype":"error_during_execution","duration_ms":836021,"duration_api_ms":835879,"is_error":true,"num_turns":1,"stop_reason":"stop_sequence","total_cost_usd":0,"total_credits":0,"usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0,"server_tool_use":{"web_search_requests":0,"web_fetch_requests":0},"service_tier":"standard","cache_creation":{"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":0},"inference_geo":"","iterations":[],"speed":"standard"},"modelUsage":{"<synthetic>":{"inputTokens":0,"outputTokens":0,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"webSearchRequests":0,"costUSD":0,"contextWindow":0,"maxOutputTokens":0}},"permission_denials":[],"errors":["The model service did not respond in time. Please try again."],"error_code":10408,"fast_mode_state":"off","uuid":"3fa01f2d-cef8-4ec7-bd84-b6d77e073b96","session_id":"cbd68728-da39-4c52-88b5-8f18bc37bf50"}`
	qoderUsageLimitedEnd = `{"type":"result","subtype":"error_during_execution","duration_ms":10181,"duration_api_ms":10058,"is_error":true,"num_turns":1,"stop_reason":"stop_sequence","total_cost_usd":0,"total_credits":0,"usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0,"server_tool_use":{"web_search_requests":0,"web_fetch_requests":0},"service_tier":"standard","cache_creation":{"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":0},"inference_geo":"","iterations":[],"speed":"standard"},"modelUsage":{"<synthetic>":{"inputTokens":0,"outputTokens":0,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"webSearchRequests":0,"costUSD":0,"contextWindow":0,"maxOutputTokens":0}},"permission_denials":[],"errors":["The usage limit has been reached. Please check your quota and try again later."],"error_code":113,"fast_mode_state":"off","uuid":"ff4dee01-7371-4a9b-88ae-0aeafabb467e","session_id":"62128041-5c1b-4f91-925d-d998bd21ce19"}`
	qoderSuccessEnd      = `{"type":"result","subtype":"success","duration_ms":152,"duration_api_ms":35,"is_error":false,"num_turns":1,"result":"pong","stop_reason":"end_turn","total_cost_usd":0,"total_credits":0,"usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0,"server_tool_use":{"web_search_requests":0,"web_fetch_requests":0},"service_tier":"standard","cache_creation":{"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":0},"inference_geo":"","iterations":[],"speed":"standard","request_id":"5dff21a3-5654-4dfd-8fc3-88671aba7f31","context_usage_ratio":0.0000390625},"modelUsage":{"mockprov/fake-model":{"inputTokens":0,"outputTokens":0,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"webSearchRequests":0,"costUSD":0,"contextWindow":0,"maxOutputTokens":0}},"permission_denials":[],"fast_mode_state":"off","uuid":"fd4fd4f8-efef-413c-95f7-ef167e0eccc6","session_id":"79bd76ef-9817-46b4-a00c-c255f08ed206"}`
)

// qoderProbeResult returns the result frame with uuid from
// testdata/probe_results.jsonl: the result of each probe of qodercli 1.1.78
// (.tmp/probe/qoder/out-*.stdout.jsonl).
func qoderProbeResult(t *testing.T, uuid string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "probe_results.jsonl"))
	require.NoError(t, err)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var result struct {
			UUID string `json:"uuid"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &result))
		if result.UUID == uuid {
			return line
		}
	}
	t.Fatalf("no captured result has the uuid %s", uuid)
	return ""
}

// zaiUsageLimitText is the real Z.AI 1308 text that Pi received
// (.tmp/retry-research/transcripts-kimi-pi-omp.md). Its stamp reads as UTC+8,
// so it lifts at zaiUsageLimitResetAt for a failure seen at
// zaiUsageLimitObservedAt.
const zaiUsageLimitText = `429: {"code":"1308","message":"Usage limit reached for 5 hour. Your limit will reset at 2026-07-14 19:06:13"}`

var (
	zaiUsageLimitObservedAt = time.Date(2026, 7, 14, 10, 34, 45, 0, time.UTC)
	zaiUsageLimitResetAt    = time.Date(2026, 7, 14, 11, 6, 14, 0, time.UTC)
)

// kimiMonthlyLimit is the vendor's verdict for Kimi's monthly usage limit,
// which states no reset instant and a window longer than a week.
var kimiMonthlyLimit = providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowLonger, Cause: "Kimi reported its monthly usage limit"}

// newQoderRetryAgent builds an offline agent whose clock reads
// zaiUsageLimitObservedAt.
func newQoderRetryAgent(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	clock.Set(zaiUsageLimitObservedAt).MustWait(testutil.DeadlineContext(t))
	sink := &agenttest.Sink{}
	return newOfflineAgentWithRuntime(t, sink, clock, nil), sink
}

// Each result reaches the real handler, and its verdict follows the turn-end
// row.
func TestQoderResultsApplyTheirVerdictAfterTheTurnEnd(t *testing.T) {
	t.Parallel()
	transient := providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Qoder reported a transient failure"}
	for _, tc := range []struct {
		name   string
		frames []string
		want   providerkit.TurnFailure
	}{
		{name: "a 10500 after Qoder's own retry is transient", frames: []string{qoderAPIRetryFrame, qoderUnavailableEnd}, want: transient},
		{name: "a 10408 is transient", frames: []string{qoderTimedOutEnd}, want: transient},
		{
			name: "a 113 is rate limited", frames: []string{qoderUsageLimitedEnd},
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Qoder reported a rate limit"},
		},
		{name: "a success cancels the server-error retry", frames: []string{qoderSuccessEnd}, want: providerkit.TurnFailure{}},
		{
			name:   "a 113 whose text is a monthly usage limit cancels the server-error retry",
			frames: []string{`{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["You have reached your monthly usage limit."],"error_code":113,"session_id":"session-1"}`},
			want:   kimiMonthlyLimit,
		},
		{
			name: "a Z.AI usage limit schedules at its stamp by the agent clock",
			frames: []string{`{"type":"result","subtype":"error_during_execution","is_error":true,"errors":[` +
				qoderJSONString(zaiUsageLimitText) + `],"error_code":113,"session_id":"session-1"}`},
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: zaiUsageLimitResetAt, Cause: "Z.AI reported a usage limit that lifts at a stated time"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newQoderRetryAgent(t)
			for _, frame := range tc.frames {
				a.HandleOutput([]byte(frame))
			}
			assert.Equal(t, providerkittest.TurnEndThen(tc.want), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

// A failure that ends a turn the user stopped never retries.
func TestQoderStoppedTurnDoesNotRetry(t *testing.T) {
	t.Parallel()
	a, sink := newQoderRetryAgent(t)
	a.mu.Lock()
	a.interruptRequested = true
	a.mu.Unlock()
	a.HandleOutput([]byte(qoderUnavailableEnd))
	rows := sink.Messages()
	require.NotEmpty(t, rows)
	require.Equal(t, agent.MessageCompletionInterrupted, rows[len(rows)-1].Completion)
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// An api_retry is Qoder's own retry, not a turn end, so it applies no verdict.
func TestQoderAPIRetryAppliesNoVerdict(t *testing.T) {
	t.Parallel()
	a, sink := newQoderRetryAgent(t)
	a.HandleOutput([]byte(qoderAPIRetryFrame))
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// qoderJSONString encodes text as a JSON string.
func qoderJSONString(text string) string {
	encoded, err := json.Marshal(text)
	if err != nil {
		// A Go string always encodes.
		panic(err)
	}
	return string(encoded)
}

func TestQoderResultFailure(t *testing.T) {
	t.Parallel()
	transient := providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Qoder reported a transient failure"}
	rateLimited := providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Qoder reported a rate limit"}
	probe := func(uuid string) func(*testing.T) string {
		return func(t *testing.T) string { return qoderProbeResult(t, uuid) }
	}
	line := func(text string) func(*testing.T) string {
		return func(*testing.T) string { return text }
	}
	for _, tc := range []struct {
		name        string
		line        func(*testing.T) string
		interrupted bool
		want        providerkit.TurnFailure
	}{
		{name: "the research 10500 is transient", line: line(qoderUnavailableEnd), want: transient},
		{name: "the research 10408 is transient", line: line(qoderTimedOutEnd), want: transient},
		{name: "the research 113 is rate limited", line: line(qoderUsageLimitedEnd), want: rateLimited},
		{name: "the research success is not retryable", line: line(qoderSuccessEnd), want: providerkit.TurnFailure{}},
		{name: "the research api_retry is not a result", line: line(qoderAPIRetryFrame), want: providerkit.TurnFailure{}},
		{name: "an interrupted 10500 is not retryable", line: line(qoderUnavailableEnd), interrupted: true, want: providerkit.TurnFailure{}},
		{name: "the probed 401 string code is not retryable", line: probe("0b6a9582-3856-4e93-9f76-a6adf2cd5c08"), want: providerkit.TurnFailure{}},
		{name: "the probed full context string code is not retryable", line: probe("dd36e930-1c72-400d-ba0e-a223a0e1e9a4"), want: providerkit.TurnFailure{}},
		{name: "the probed custom rate limit string code is rate limited", line: probe("d1892e4e-706b-4289-9f7b-3d51f1f0345a"), want: rateLimited},
		{name: "the probed 529 is transient", line: probe("2eb3c584-29a7-4d6c-a1dc-55fbe3e874fb"), want: transient},
		{name: "the probed connection reset 500 is transient", line: probe("b699b8af-c759-45f4-9609-9538450d7707"), want: transient},
		{name: "the probed incomplete response 500 is transient", line: probe("dc592848-9dc9-4308-bc33-63635fa438af"), want: transient},
		{name: "the probed stop is not retryable", line: probe("3e1ec8b6-45ea-4e43-acd3-46e9da4d9950"), want: providerkit.TurnFailure{}},
		{
			name: "a billing code is a billing block",
			line: line(`{"type":"result","is_error":true,"errors":["Your credits have run out."],"error_code":114}`),
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Qoder reported billing code 114"},
		},
		{
			name: "a custom insufficient_quota is a billing block",
			line: line(`{"type":"result","is_error":true,"errors":["The request could not be completed. Please try again."],"error_code":"insufficient_quota"}`),
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the provider reported insufficient_quota"},
		},
		{
			name: "the daily limit is a usage limit of a week or less",
			line: line(`{"type":"result","is_error":true,"errors":["The daily usage limit has been reached."],"error_code":110}`),
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess, Cause: "Qoder reported its daily usage limit"},
		},
		{name: "a model queue is transient", line: line(`{"type":"result","is_error":true,"errors":["The model is busy."],"error_code":10605}`), want: transient},
		{name: "a numeric code in a string decides nothing", line: line(`{"type":"result","is_error":true,"errors":["x"],"error_code":"10500"}`), want: providerkit.TurnFailure{}},
		{name: "a null code decides nothing", line: line(`{"type":"result","is_error":true,"errors":["x"],"error_code":null}`), want: providerkit.TurnFailure{}},
		{name: "an absent code decides nothing", line: line(`{"type":"result","is_error":true,"errors":["x"]}`), want: providerkit.TurnFailure{}},
		{name: "a native terminal_reason prevents retry", line: line(`{"type":"result","is_error":true,"errors":["Operation aborted"],"error_code":10500,"terminal_reason":"aborted_streaming"}`), want: providerkit.TurnFailure{}},
		{name: "a malformed result is not retryable", line: line(`{"type":"result","is_error":true,"error_code":10500,"errors":[7]}`), want: providerkit.TurnFailure{}},
		{
			name: "a definitive vendor verdict wins over a transient code",
			line: line(`{"type":"result","is_error":true,"errors":["Insufficient balance or no resource package. Please recharge."],"error_code":500}`),
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the vendor reported a billing or plan block"},
		},
		{
			name: "an indicative vendor verdict decides an unknown code",
			line: line(`{"type":"result","is_error":true,"errors":["429: {\"code\":\"1302\",\"message\":\"Rate limit reached for requests\"}"],"error_code":47000}`),
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "the vendor reported a rate limit"},
		},
		{
			// Qoder writes 113 for every 429. The vendor's own wording states a
			// monthly limit, which a retry within the week does not lift.
			name: "a 113 whose text is a monthly usage limit is a longer usage limit",
			line: line(`{"type":"result","is_error":true,"errors":["429: {\"error\":{\"message\":\"You have reached your monthly usage limit.\"}}"],"error_code":113}`),
			want: kimiMonthlyLimit,
		},
		{
			name: "a custom rate_limit_exceeded whose text is a monthly usage limit is a longer usage limit",
			line: line(`{"type":"result","is_error":true,"errors":["You have reached your monthly usage limit."],"error_code":"rate_limit_exceeded"}`),
			want: kimiMonthlyLimit,
		},
		{
			// Qoder also writes 500 for a failure that it cannot classify, so the
			// vendor's wording is more specific than the code.
			name: "a 500 whose text is a monthly usage limit is a longer usage limit",
			line: line(`{"type":"result","is_error":true,"errors":["You have reached your monthly usage limit."],"error_code":500}`),
			want: kimiMonthlyLimit,
		},
		{
			name: "a 113 whose text is a vendor rate limit takes the vendor verdict",
			line: line(`{"type":"result","is_error":true,"errors":["429: {\"code\":\"1302\",\"message\":\"Rate limit reached for requests\"}"],"error_code":113}`),
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "the vendor reported a rate limit"},
		},
		{
			name: "the daily limit code decides before a vendor's monthly wording",
			line: line(`{"type":"result","is_error":true,"errors":["You have reached your monthly usage limit."],"error_code":110}`),
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess, Cause: "Qoder reported its daily usage limit"},
		},
		{
			name: "a model service code decides before a vendor's monthly wording",
			line: line(`{"type":"result","is_error":true,"errors":["You have reached your monthly usage limit."],"error_code":10500}`),
			want: transient,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, qoderResultFailure([]byte(tc.line(t)), tc.interrupted, zaiUsageLimitObservedAt))
		})
	}
}

// qoderCompactingStatus is the status frame that opens Qoder's native
// compaction (qoder/run3 frame 58).
const qoderCompactingStatus = `{"type":"system","subtype":"status","status":"compacting","session_id":"session-1"}`

// A /compact that starts a turn ends at its result, and that end never
// schedules a retry, whatever the result states. The verdict is the
// compaction's own, once, after the turn-end row.
func TestQoderManualCompactionEndNeverSchedulesARetry(t *testing.T) {
	t.Parallel()
	probe := func(uuid string) func(*testing.T) string {
		return func(t *testing.T) string { return qoderProbeResult(t, uuid) }
	}
	line := func(text string) func(*testing.T) string {
		return func(*testing.T) string { return text }
	}
	for _, tc := range []struct {
		name        string
		input       string
		frames      []func(*testing.T) string
		interrupted bool
	}{
		{name: "a success", input: "/compact", frames: []func(*testing.T) string{line(qoderCompactingStatus), line(qoderSuccessEnd)}},
		{name: "a success with arguments", input: "/compact keep the plan", frames: []func(*testing.T) string{line(qoderSuccessEnd)}},
		{name: "a 10500 after Qoder's own retry", input: "/compact", frames: []func(*testing.T) string{line(qoderCompactingStatus), line(qoderAPIRetryFrame), line(qoderUnavailableEnd)}},
		{name: "a 10408", input: "/compact", frames: []func(*testing.T) string{line(qoderTimedOutEnd)}},
		{name: "the probed 529", input: "  /compact  ", frames: []func(*testing.T) string{probe("2eb3c584-29a7-4d6c-a1dc-55fbe3e874fb")}},
		{name: "a 113", input: "/compact", frames: []func(*testing.T) string{line(qoderUsageLimitedEnd)}},
		{
			name: "a Z.AI usage limit with a stated reset", input: "/compact",
			frames: []func(*testing.T) string{line(`{"type":"result","subtype":"error_during_execution","is_error":true,"errors":[` +
				qoderJSONString(zaiUsageLimitText) + `],"error_code":113,"session_id":"session-1"}`)},
		},
		{name: "a stop", input: "/compact", frames: []func(*testing.T) string{line(qoderCompactingStatus), probe("3e1ec8b6-45ea-4e43-acd3-46e9da4d9950")}, interrupted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newQoderRetryAgent(t)
			require.NoError(t, a.SendInput(tc.input, nil))
			if tc.interrupted {
				require.NoError(t, a.Interrupt(agent.StopContext{}))
			}
			for _, frame := range tc.frames {
				a.HandleOutput([]byte(frame(t)))
			}
			assert.Equal(t, providerkittest.TurnEndThen(providerkit.CompactionEnd()), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			active, ok := sink.LastTurnActive()
			require.True(t, ok)
			assert.False(t, active, "the result releases the compaction turn")
		})
	}
}

// The compaction mark belongs to the /compact turn alone. The next turn's
// result takes the classifier's verdict again.
func TestQoderTurnAfterManualCompactionTakesItsOwnVerdict(t *testing.T) {
	t.Parallel()
	transient := providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}
	for _, tc := range []struct {
		name string
		end  func(*Agent)
	}{
		{name: "after a result", end: func(a *Agent) { a.HandleOutput([]byte(qoderUnavailableEnd)) }},
		{name: "after a turn end without a result", end: func(a *Agent) { a.setTurnActive(false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newQoderRetryAgent(t)
			require.NoError(t, a.SendInput("/compact", nil))
			tc.end(a)
			before := len(providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			require.NoError(t, a.SendInput("continue", nil))
			a.HandleOutput([]byte(qoderUnavailableEnd))
			assert.Equal(t, providerkittest.TurnEndThen(transient), providerkittest.TurnEndAndRetryOps(sink.Timeline())[before:])
		})
	}
}

// A failed write of /compact ends the turn before Qoder reads it. The next
// turn takes its own verdict.
func TestQoderFailedCompactWriteLeavesNoMark(t *testing.T) {
	t.Parallel()
	a, sink := newQoderRetryAgent(t)
	a.SetStdinForTest(agenttest.FailingStdin{})
	require.Error(t, a.SendInput("/compact", nil))
	a.SetStdinForTest(agenttest.NopStdin(io.Discard))
	require.NoError(t, a.SendInput("continue", nil))
	a.HandleOutput([]byte(qoderUnavailableEnd))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// An input that is not the bare /compact command starts an ordinary turn,
// and a /compact steered into a running turn starts no turn. Each such turn
// keeps the classifier's verdict.
func TestQoderInputThatIsNotACompactionTurnKeepsItsVerdict(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		start func(*testing.T, *Agent)
	}{
		{name: "a longer command name", start: func(t *testing.T, a *Agent) { require.NoError(t, a.SendInput("/compactly", nil)) }},
		{name: "the command inside the text", start: func(t *testing.T, a *Agent) { require.NoError(t, a.SendInput("explain /compact", nil)) }},
		{name: "a bare slash", start: func(t *testing.T, a *Agent) { require.NoError(t, a.SendInput("/ compact", nil)) }},
		{
			name: "the command with an attachment",
			start: func(t *testing.T, a *Agent) {
				require.NoError(t, a.SendInput("/compact", []*leapmuxv1.Attachment{testPNGAttachment()}))
			},
		},
		{
			name: "the command steered into a running turn",
			start: func(t *testing.T, a *Agent) {
				require.NoError(t, a.SendInput("explain the plan", nil))
				require.NoError(t, a.SteerInput("/compact keep the plan", nil))
				require.ErrorIs(t, a.SendInput("/compact", nil), agent.ErrAgentBusy)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newQoderRetryAgent(t)
			tc.start(t, a)
			a.HandleOutput([]byte(qoderUnavailableEnd))
			assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

// A process exit during a /compact turn ends that turn with the compaction's
// verdict, once: Stop and Wait both release the turn. An exit after the
// compaction's result applies no second one.
func TestQoderProcessExitEndsACompactionTurnOnce(t *testing.T) {
	t.Parallel()
	for _, resultFirst := range []bool{false, true} {
		t.Run(fmt.Sprint("the result comes first: ", resultFirst), func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			clock.Set(zaiUsageLimitObservedAt).MustWait(testutil.DeadlineContext(t))
			exited := make(chan struct{})
			close(exited)
			sink := &agenttest.Sink{}
			a := newOfflineAgentWithRuntime(t, sink, clock, exited)
			require.NoError(t, a.SendInput("/compact", nil))
			want := []agenttest.TimelineEntry{{Op: "cancel_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, Reason: providerkit.TurnEndedOnCompaction}}
			if resultFirst {
				a.HandleOutput([]byte(qoderUnavailableEnd))
				want = providerkittest.TurnEndThen(providerkit.CompactionEnd())
			}
			a.Stop()
			require.NoError(t, a.Wait())
			assert.Equal(t, want, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			active, ok := sink.LastTurnActive()
			require.True(t, ok)
			assert.False(t, active)
		})
	}
}

// A process exit during an ordinary turn applies no verdict, as before.
func TestQoderProcessExitDuringAnOrdinaryTurnAppliesNoVerdict(t *testing.T) {
	t.Parallel()
	exited := make(chan struct{})
	close(exited)
	sink := &agenttest.Sink{}
	a := newOfflineAgentWithRuntime(t, sink, nil, exited)
	require.NoError(t, a.SendInput("explain the plan", nil))
	require.NoError(t, a.Wait())
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}
