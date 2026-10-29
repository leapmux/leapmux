package droid

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// The research frames of Droid 0.249.0 (.tmp/retry-research/amp-droid.md),
// copied verbatim.
const (
	droidUnavailableTurnEnd = `{"jsonrpc":"2.0","type":"notification","factoryApiVersion":"1.0.0","factoryProtocolVersion":"1.275.0","method":"droid.session_notification","params":{"sessionId":"8d96b98f-856e-4f8e-8784-0144e850c3fa","notification":{"type":"agent_turn_completed","reason":"model_provider_unavailable","turnId":"6eac8be0-f8c0-43d8-a8e2-160b109fa34b","tokenUsage":{"inputTokens":10,"outputTokens":2,"cacheCreationTokens":0,"cacheReadTokens":0,"thinkingTokens":0,"factoryCredits":0},"cumulativeTokenUsage":{"inputTokens":10,"outputTokens":2,"cacheCreationTokens":0,"cacheReadTokens":0,"thinkingTokens":0,"factoryCredits":0},"childTokenUsage":{"inputTokens":0,"outputTokens":0,"cacheCreationTokens":0,"cacheReadTokens":0,"thinkingTokens":0,"factoryCredits":0},"cumulativeChildTokenUsage":{"inputTokens":0,"outputTokens":0,"cacheCreationTokens":0,"cacheReadTokens":0,"thinkingTokens":0,"factoryCredits":0},"durationMs":67}}}`
	droidOverloadedError    = `{"jsonrpc":"2.0","type":"notification","factoryApiVersion":"1.0.0","factoryProtocolVersion":"1.275.0","method":"droid.session_notification","params":{"sessionId":"8d96b98f-856e-4f8e-8784-0144e850c3fa","notification":{"type":"error","message":"529 Overloaded","errorType":"Error","timestamp":"2026-10-28T01:24:48.700Z","error":{"name":"Error","message":"529 Overloaded"}}}}`
	droidExhaustedTurnEnd   = `{"jsonrpc":"2.0","type":"notification","factoryApiVersion":"1.0.0","factoryProtocolVersion":"1.275.0","method":"droid.session_notification","params":{"sessionId":"2acc066b-2411-45f3-af51-79b1654c00f0","notification":{"type":"agent_turn_completed","reason":"model_usage_exhausted","turnId":"cb4f1cb3-d876-4c49-92c5-0bfdf74ebf36","tokenUsage":{"inputTokens":10,"outputTokens":2,"cacheCreationTokens":0,"cacheReadTokens":0,"thinkingTokens":0,"factoryCredits":0},"cumulativeTokenUsage":{"inputTokens":10,"outputTokens":2,"cacheCreationTokens":0,"cacheReadTokens":0,"thinkingTokens":0,"factoryCredits":0},"childTokenUsage":{"inputTokens":0,"outputTokens":0,"cacheCreationTokens":0,"cacheReadTokens":0,"thinkingTokens":0,"factoryCredits":0},"cumulativeChildTokenUsage":{"inputTokens":0,"outputTokens":0,"cacheCreationTokens":0,"cacheReadTokens":0,"thinkingTokens":0,"factoryCredits":0},"durationMs":64}}}`
	droidOverloadedRetry    = `{"jsonrpc":"2.0","type":"notification","factoryApiVersion":"1.0.0","factoryProtocolVersion":"1.275.0","method":"droid.session_notification","params":{"sessionId":"aca8501c-b7d8-4f47-8d1f-1add833f8166","notification":{"type":"llm_retry","attempt":1,"reason":"overloaded"}}}`
	droidCompletedTurnEnd   = `{"jsonrpc":"2.0","type":"notification","factoryApiVersion":"1.0.0","factoryProtocolVersion":"1.275.0","method":"droid.session_notification","params":{"sessionId":"aca8501c-b7d8-4f47-8d1f-1add833f8166","notification":{"type":"agent_turn_completed","reason":"completed","turnId":"3943f52a-6248-4d06-94cb-c52d74250957","tokenUsage":{"inputTokens":20,"outputTokens":4,"cacheCreationTokens":0,"cacheReadTokens":0,"thinkingTokens":0,"factoryCredits":0},"cumulativeTokenUsage":{"inputTokens":20,"outputTokens":4,"cacheCreationTokens":0,"cacheReadTokens":0,"thinkingTokens":0,"factoryCredits":0},"childTokenUsage":{"inputTokens":0,"outputTokens":0,"cacheCreationTokens":0,"cacheReadTokens":0,"thinkingTokens":0,"factoryCredits":0},"cumulativeChildTokenUsage":{"inputTokens":0,"outputTokens":0,"cacheCreationTokens":0,"cacheReadTokens":0,"thinkingTokens":0,"factoryCredits":0},"durationMs":10870}}}`
)

// zaiUsageLimitMessage is the real Z.AI 1308 text that Pi received
// (.tmp/retry-research/transcripts-kimi-pi-omp.md). Its stamp reads as UTC+8,
// so it lifts at 2026-07-14T11:06:14Z for a failure seen at
// zaiUsageLimitObservedAt.
const zaiUsageLimitMessage = `429: {"code":"1308","message":"Usage limit reached for 5 hour. Your limit will reset at 2026-07-14 19:06:13"}`

var (
	zaiUsageLimitObservedAt = time.Date(2026, 7, 14, 10, 34, 45, 0, time.UTC)
	zaiUsageLimitResetAt    = time.Date(2026, 7, 14, 11, 6, 14, 0, time.UTC)
)

func TestDroidTurnFailure(t *testing.T) {
	t.Parallel()
	billing := providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Droid reported that the model usage is exhausted"}
	transient := providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Droid reported that the model provider is unavailable"}
	rateLimited := providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Droid reported a rate limit"}
	vendorBilling := providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the vendor reported an exceeded quota"}
	for _, tc := range []struct {
		name        string
		reason      string
		message     string
		interrupted bool
		want        providerkit.TurnFailure
	}{
		{name: "an unavailable provider is transient", reason: "model_provider_unavailable", message: "529 Overloaded", want: transient},
		{name: "an unreachable provider is transient", reason: "model_provider_unreachable", want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Droid reported that the model provider is unreachable"}},
		{name: "exhausted model usage is a billing block", reason: "model_usage_exhausted", message: "402 Payment required: insufficient credits", want: billing},
		{name: "exhausted model usage with no error is a billing block", reason: "model_usage_exhausted", want: billing},
		{name: "a rate limit with no error is rate limited", reason: "model_rate_limited", want: rateLimited},
		{
			name:    "a rate limit whose error states an exceeded quota is a billing block",
			reason:  "model_rate_limited",
			message: "429 You exceeded your current quota, please check your plan and billing details.",
			want:    vendorBilling,
		},
		{
			name:    "a Z.AI usage limit schedules at its stamp",
			reason:  "error",
			message: zaiUsageLimitMessage,
			want:    providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: zaiUsageLimitResetAt, Cause: "Z.AI reported a usage limit that lifts at a stated time"},
		},
		{
			// Droid merges every 429 into model_rate_limited. The vendor's text
			// states a monthly limit, which a retry within the week does not lift.
			name:    "a rate limit whose error states a monthly vendor limit is a longer usage limit",
			reason:  "model_rate_limited",
			message: "429 You have reached your monthly usage limit.",
			want:    providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowLonger, Cause: "Kimi reported its monthly usage limit"},
		},
		{
			name:    "a rate limit whose error the vendor reads as a rate limit takes the vendor verdict",
			reason:  "model_rate_limited",
			message: "429 Rate limit reached for requests",
			want:    providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "the vendor reported a rate limit"},
		},
		{
			name:    "an unavailable provider whose error states a monthly vendor limit is a longer usage limit",
			reason:  "model_provider_unavailable",
			message: "500 You have reached your monthly usage limit.",
			want:    providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowLonger, Cause: "Kimi reported its monthly usage limit"},
		},
		{
			name:    "exhausted model usage decides before a vendor's indicative wording",
			reason:  "model_usage_exhausted",
			message: "429 Rate limit reached for requests",
			want:    billing,
		},
		{
			name:    "a generic error with an indicative vendor wording is not retryable",
			reason:  "error",
			message: "429 Rate limit reached for requests",
			want:    providerkit.TurnFailure{},
		},
		{name: "a rejected authentication is not retryable", reason: "model_authentication_failed", message: "401 Unauthorized", want: providerkit.TurnFailure{}},
		{name: "a rejected request is not retryable", reason: "model_request_rejected", message: "400 Bad Request", want: providerkit.TurnFailure{}},
		{name: "a generic error is not retryable", reason: "error", message: "boom", want: providerkit.TurnFailure{}},
		{name: "an unknown reason is not retryable", reason: "a_reason_droid_adds_later", want: providerkit.TurnFailure{}},
		{name: "a completed turn is not retryable", reason: "completed", want: providerkit.TurnFailure{}},
		{name: "a completed turn ignores an error message", reason: "completed", message: "429 You exceeded your current quota, please check your plan and billing details.", want: providerkit.TurnFailure{}},
		{name: "a cancelled turn is not retryable", reason: "cancelled", message: "529 Overloaded", want: providerkit.TurnFailure{}},
		{name: "a process exit is not retryable", reason: "process_exit", message: zaiUsageLimitMessage, want: providerkit.TurnFailure{}},
		{name: "an empty reason completes the turn", reason: "", message: "529 Overloaded", want: providerkit.TurnFailure{}},
		{name: "a stopped turn's unavailable provider is not retryable", reason: "model_provider_unavailable", message: "529 Overloaded", interrupted: true, want: providerkit.TurnFailure{}},
		{name: "a stopped turn's rate limit is not retryable", reason: "model_rate_limited", interrupted: true, want: providerkit.TurnFailure{}},
		{name: "a stopped turn's Z.AI usage limit is not retryable", reason: "error", message: zaiUsageLimitMessage, interrupted: true, want: providerkit.TurnFailure{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, droidTurnFailure(tc.reason, tc.message, tc.interrupted, zaiUsageLimitObservedAt))
		})
	}
}

// newDroidRetryAgent builds an agent whose main session is sessionID and whose
// clock reads now. Its process accepts every prompt and every interrupt.
func newDroidRetryAgent(t *testing.T, sessionID string, now time.Time) (*Agent, *agenttest.Sink) {
	t.Helper()
	a, sink, stdin := newSteerAgent(t)
	stdin.respond = func(request droidEnvelope) {
		switch request.Method {
		case droidMethodAddUserMessage, droidMethodInterruptSession:
			answerDroidInput(t, a, request.ID, false)
		}
	}
	clock := testutil.NewQuartzMock(t)
	clock.Set(now).MustWait(testutil.DeadlineContext(t))
	a.clock = clock
	a.Mu.Lock()
	a.sessionID = sessionID
	a.Mu.Unlock()
	return a, sink
}

// droidFixtureLines reads every line of a capture in testdata.
func droidFixtureLines(t *testing.T, name string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// The research frames reach the real handler: the error notification first,
// then the turn end, whose verdict follows the turn-end row.
func TestDroidResearchFramesApplyTheirVerdictAfterTheTurnEnd(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		sessionID string
		frames    []string
		want      providerkit.TurnFailure
	}{
		{
			name:      "an overload schedules a server-error retry",
			sessionID: "8d96b98f-856e-4f8e-8784-0144e850c3fa",
			frames:    []string{droidOverloadedError, droidUnavailableTurnEnd},
			want:      providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Droid reported that the model provider is unavailable"},
		},
		{
			name:      "exhausted model usage cancels both retries",
			sessionID: "2acc066b-2411-45f3-af51-79b1654c00f0",
			frames:    []string{droidExhaustedTurnEnd},
			want:      providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Droid reported that the model usage is exhausted"},
		},
		{
			name:      "a turn that recovered after Droid's own retry cancels the server-error retry",
			sessionID: "aca8501c-b7d8-4f47-8d1f-1add833f8166",
			frames:    []string{droidOverloadedRetry, droidCompletedTurnEnd},
			want:      providerkit.TurnFailure{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newDroidRetryAgent(t, tc.sessionID, zaiUsageLimitObservedAt)
			for _, frame := range tc.frames {
				a.HandleOutput([]byte(frame))
			}
			assert.Equal(t, providerkittest.TurnEndThen(tc.want), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

// The whole stdout of the real 429 quota run (429-quota-q1): Droid merges an
// exhausted quota into model_rate_limited, and the error message decides a
// billing block.
func TestDroidProbeQuotaRunIsABillingBlock(t *testing.T) {
	t.Parallel()
	a, sink := newDroidRetryAgent(t, "8081e52d-5b5f-43bb-993b-1ec1c2a6666b", zaiUsageLimitObservedAt)
	lines := droidFixtureLines(t, "probe_429_quota.jsonl")
	for _, line := range lines {
		a.HandleOutput([]byte(line))
	}
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{
		Kind: providerkit.TurnFailureBillingBlocked, Cause: "the vendor reported an exceeded quota",
	}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// Droid's error notification ends the turn flag about 7 ms before its
// agent_turn_completed arrives (429-quota-q1). The input queue can send a
// queued prompt in that window. The new prompt must not erase the error that
// the failed turn's completion reads.
func TestDroidProbeQuotaRunWithAQueuedPromptIsABillingBlock(t *testing.T) {
	t.Parallel()
	a, sink := newDroidRetryAgent(t, "8081e52d-5b5f-43bb-993b-1ec1c2a6666b", zaiUsageLimitObservedAt)
	lines := droidFixtureLines(t, "probe_429_quota.jsonl")
	errorAt := slices.IndexFunc(lines, func(line string) bool { return strings.Contains(line, `"notification":{"type":"error"`) })
	require.Positive(t, errorAt, "the capture holds the error notification")
	for _, line := range lines[:errorAt+1] {
		a.HandleOutput([]byte(line))
	}
	require.NoError(t, a.SendInput("the queued prompt", nil), "the error ended the turn flag, so the queue can send")
	for _, line := range lines[errorAt+1:] {
		a.HandleOutput([]byte(line))
	}
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{
		Kind: providerkit.TurnFailureBillingBlocked, Cause: "the vendor reported an exceeded quota",
	}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// A failure that Droid reports after the user stopped the turn won the race
// against the interrupt. The user asked to stop, so the failure must not
// re-send the stopped prompt.
func TestDroidFailureAfterAStopDoesNotRetry(t *testing.T) {
	t.Parallel()
	a, sink := newDroidRetryAgent(t, "8d96b98f-856e-4f8e-8784-0144e850c3fa", zaiUsageLimitObservedAt)
	require.NoError(t, a.SendInput("go", nil))
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	a.HandleOutput([]byte(droidOverloadedError))
	a.HandleOutput([]byte(droidUnavailableTurnEnd))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// The error notification ends the turn flag before agent_turn_completed, so a
// queued prompt can start the next turn first. The stop still belongs to the
// turn that the user stopped.
func TestDroidStopSurvivesAQueuedPromptBeforeTheTurnEnd(t *testing.T) {
	t.Parallel()
	a, sink := newDroidRetryAgent(t, "8d96b98f-856e-4f8e-8784-0144e850c3fa", zaiUsageLimitObservedAt)
	require.NoError(t, a.SendInput("go", nil))
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	a.HandleOutput([]byte(droidOverloadedError))
	require.NoError(t, a.SendInput("the queued prompt", nil))
	a.HandleOutput([]byte(droidUnavailableTurnEnd))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// The stop of one turn never decides the next turn: the next turn start clears
// it.
func TestDroidStopDoesNotOutliveItsTurn(t *testing.T) {
	t.Parallel()
	const session = "8d96b98f-856e-4f8e-8784-0144e850c3fa"
	a, sink := newDroidRetryAgent(t, session, zaiUsageLimitObservedAt)
	require.NoError(t, a.SendInput("go", nil))
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	a.HandleOutput(droidAuditNotification(t, session, []byte(`{"type":"agent_turn_completed","reason":"cancelled"}`)))
	require.NoError(t, a.SendInput("next", nil))
	a.HandleOutput([]byte(droidOverloadedError))
	a.HandleOutput([]byte(droidUnavailableTurnEnd))

	stopped := providerkittest.TurnEndThen(providerkit.TurnFailure{})
	timeline := providerkittest.TurnEndAndRetryOps(sink.Timeline())
	require.Greater(t, len(timeline), len(stopped))
	assert.Equal(t, stopped, timeline[:len(stopped)])
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Droid reported that the model provider is unavailable"}),
		timeline[len(stopped):], "the second turn was not stopped, so its failure retries")
}

// A stop whose turn ended with no agent_turn_completed, such as at an idle
// working state, leaves no note for the next turn.
func TestDroidStopOfATurnWithNoTurnEndDoesNotDecideTheNextTurn(t *testing.T) {
	t.Parallel()
	const session = "8d96b98f-856e-4f8e-8784-0144e850c3fa"
	a, sink := newDroidRetryAgent(t, session, zaiUsageLimitObservedAt)
	require.NoError(t, a.SendInput("go", nil))
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	a.HandleOutput(droidAuditNotification(t, session, []byte(`{"type":"droid_working_state_changed","newState":"idle"}`)))
	require.NoError(t, a.SendInput("next", nil))
	a.HandleOutput([]byte(droidOverloadedError))
	a.HandleOutput([]byte(droidUnavailableTurnEnd))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Droid reported that the model provider is unavailable"}),
		providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// A stop with no turn running sends nothing and notes nothing.
func TestDroidStopWhileIdleDoesNotDecideTheNextTurn(t *testing.T) {
	t.Parallel()
	a, sink := newDroidRetryAgent(t, "8d96b98f-856e-4f8e-8784-0144e850c3fa", zaiUsageLimitObservedAt)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.NoError(t, a.SendInput("go", nil))
	a.HandleOutput([]byte(droidOverloadedError))
	a.HandleOutput([]byte(droidUnavailableTurnEnd))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Droid reported that the model provider is unavailable"}),
		providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// The vendor's reset stamp is read against the agent's clock, not the host's.
func TestDroidUsageLimitSchedulesAtTheStampByTheAgentClock(t *testing.T) {
	t.Parallel()
	a, sink := newDroidRetryAgent(t, "main-session", zaiUsageLimitObservedAt)
	require.NoError(t, a.SendInput("go", nil))
	a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"error","message":`+jsonQuote(t, zaiUsageLimitMessage)+`,"errorType":"Error"}`)))
	a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"agent_turn_completed","reason":"error"}`)))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{
		Kind: providerkit.TurnFailureUsageLimited, ResetAt: zaiUsageLimitResetAt, Cause: "Z.AI reported a usage limit that lifts at a stated time",
	}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// The error of one turn never decides a later turn: the turn end consumes it.
func TestDroidErrorMessageDoesNotOutliveItsTurn(t *testing.T) {
	t.Parallel()
	a, sink := newDroidRetryAgent(t, "main-session", zaiUsageLimitObservedAt)
	quota := `{"type":"error","message":"429 You exceeded your current quota, please check your plan and billing details.","errorType":"Error"}`
	require.NoError(t, a.SendInput("go", nil))
	a.HandleOutput(droidAuditNotification(t, "main-session", []byte(quota)))
	a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"agent_turn_completed","reason":"model_rate_limited"}`)))
	require.NoError(t, a.SendInput("next", nil))
	a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"agent_turn_completed","reason":"model_rate_limited"}`)))

	timeline := providerkittest.TurnEndAndRetryOps(sink.Timeline())
	billing := providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the vendor reported an exceeded quota"})
	require.Greater(t, len(timeline), len(billing))
	assert.Equal(t, billing, timeline[:len(billing)])
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Droid reported a rate limit"}),
		timeline[len(billing):], "the second turn sent no error, so its reason alone decides")
}

// Droid sends the idle working state after agent_turn_completed in every
// capture. An idle that arrives first ends a turn that Droid will not complete,
// so the error of that turn must not decide the next turn.
func TestDroidErrorOfATurnWithNoTurnEndDoesNotDecideTheNextTurn(t *testing.T) {
	t.Parallel()
	a, sink := newDroidRetryAgent(t, "main-session", zaiUsageLimitObservedAt)
	quota := `{"type":"error","message":"429 You exceeded your current quota, please check your plan and billing details.","errorType":"Error"}`
	require.NoError(t, a.SendInput("go", nil))
	a.HandleOutput(droidAuditNotification(t, "main-session", []byte(quota)))
	a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"droid_working_state_changed","newState":"idle"}`)))
	require.NoError(t, a.SendInput("next", nil))
	a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"agent_turn_completed","reason":"model_rate_limited"}`)))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Droid reported a rate limit"}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// An error that arrives with no turn running belongs to no turn end. The next
// prompt starts a new turn, which must not read it.
func TestDroidErrorBeforeAPromptDoesNotDecideThePromptsTurn(t *testing.T) {
	t.Parallel()
	a, sink := newDroidRetryAgent(t, "main-session", zaiUsageLimitObservedAt)
	quota := `{"type":"error","message":"429 You exceeded your current quota, please check your plan and billing details.","errorType":"Error"}`
	a.HandleOutput(droidAuditNotification(t, "main-session", []byte(quota)))
	require.NoError(t, a.SendInput("hello", nil))
	a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"agent_turn_completed","reason":"model_rate_limited"}`)))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Droid reported a rate limit"}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// A stop that Droid reports as a cancelled turn never retries, even after an
// error.
func TestDroidCancelledTurnAfterAnErrorDoesNotRetry(t *testing.T) {
	t.Parallel()
	a, sink := newDroidRetryAgent(t, "8d96b98f-856e-4f8e-8784-0144e850c3fa", zaiUsageLimitObservedAt)
	a.HandleOutput([]byte(droidOverloadedError))
	a.HandleOutput(droidAuditNotification(t, "8d96b98f-856e-4f8e-8784-0144e850c3fa", []byte(`{"type":"agent_turn_completed","reason":"cancelled"}`)))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// A child session's error and turn end make no retry call in either
// transcript, and the child's error does not decide the main turn.
func TestDroidChildTurnMakesNoRetryCall(t *testing.T) {
	t.Parallel()
	a, sink := newDroidRetryAgent(t, "main-session", zaiUsageLimitObservedAt)
	a.HandleOutput([]byte(droidTaskCall))
	a.HandleOutput([]byte(droidChildAvailable))
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "droid-tool-task-1", ProviderChildKey: "child-1", Title: "Inspect the note"})
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "child-1", Kind: bgtask.KindSubagent, ChildAgentID: childID,
		ParentAgentID: a.AgentID(), Title: "Inspect the note", Status: bgtask.StatusRunning,
	}))

	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"child-1","notification":{"type":"error","message":"429 You exceeded your current quota, please check your plan and billing details.","errorType":"Error"}}}`))
	a.HandleOutput([]byte(`{"type":"notification","params":{"sessionId":"child-1","notification":{"type":"agent_turn_completed","turnId":"turn-1","reason":"model_rate_limited"}}}`))
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()), "a child turn applies no verdict to the root")
	childTimeline := providerkittest.TurnEndAndRetryOps(sink.Child(childID).Timeline())
	require.NotEmpty(t, childTimeline)
	for _, entry := range childTimeline {
		assert.Equal(t, "turn_end", entry.Op, "a child turn applies no verdict to its own transcript")
	}

	a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"agent_turn_completed","reason":"model_rate_limited"}`)))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Droid reported a rate limit"}), providerkittest.TurnEndAndRetryOps(sink.Timeline()),
		"the child's error does not decide the main turn")
}

// A turn end that does not decode ends no turn and applies no verdict.
func TestDroidMalformedTurnEndAppliesNoVerdict(t *testing.T) {
	t.Parallel()
	a, sink := newDroidRetryAgent(t, "main-session", zaiUsageLimitObservedAt)
	a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"agent_turn_completed","reason":7}`)))
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// jsonQuote encodes text as a JSON string.
func jsonQuote(t *testing.T, text string) string {
	t.Helper()
	encoded, err := json.Marshal(text)
	require.NoError(t, err)
	return string(encoded)
}
