package commandcode

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
)

// The research frames of Command Code 1.88.0 (.tmp/retry-research/commandcode-letta.md),
// copied verbatim, U+2192 included: a 429 plan limit with a reset instant, a
// 529, a stream that ended at status 0, a 400 for credits, and a success.
const (
	commandcodePlanLimitEnd  = `{"jsonrpc":"2.0","method":"turn/completed","params":{"seq":9,"turnId":"turn_1","stopReason":"run_error","finalText":"","turnCount":1,"error":{"name":"TransportError","message":"POST /alpha/generate → 429 rate_limit_error: You've reached the usage limit for your plan. Your 5-hour limit resets at 2026-10-28T15:00:00.000Z","status":429}}}`
	commandcodeOverloadedEnd = `{"jsonrpc":"2.0","method":"turn/completed","params":{"seq":16,"turnId":"turn_1","stopReason":"run_error","finalText":"","turnCount":1,"error":{"name":"TransportError","message":"POST /alpha/generate → 529 overloaded_error: Overloaded","status":529}}}`
	commandcodeStreamEnd     = `{"jsonrpc":"2.0","method":"turn/completed","params":{"seq":11,"turnId":"turn_1","stopReason":"run_error","finalText":"","turnCount":1,"error":{"name":"TransportError","message":"Stream failed: Response stream ended without a finish reason.","status":0}}}`
	commandcodeCreditsEnd    = `{"jsonrpc":"2.0","method":"turn/completed","params":{"seq":9,"turnId":"turn_1","stopReason":"run_error","finalText":"","turnCount":1,"error":{"name":"TransportError","message":"POST /alpha/generate → 400 invalid_request_error: Insufficient credits. Buy more at https://commandcode.ai/billing","status":400}}}`
	commandcodeSuccessEnd    = `{"jsonrpc":"2.0","method":"turn/completed","params":{"seq":13,"turnId":"turn_1","stopReason":"end_turn","finalText":"PROBE-OK","turnCount":1}}`
)

// commandcodeOverloadedFailure is the verdict of the research 529: the vendor
// reads Anthropic's overloaded_error in its message.
var commandcodeOverloadedFailure = providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "the vendor reported a transient failure"}

var (
	// commandcodeObservedAt lies inside the five hours before the plan
	// limit's reset instant.
	commandcodeObservedAt = time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	commandcodeResetAt    = time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC)
)

// newCommandCodeRetryAgent builds a test agent whose clock reads
// commandcodeObservedAt and whose turn turn_1 runs.
func newCommandCodeRetryAgent(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	a, sink := testAgent(t)
	clock := testutil.NewQuartzMock(t)
	clock.Set(commandcodeObservedAt).MustWait(testutil.DeadlineContext(t))
	a.clock = clock
	a.startTurn("turn_1")
	return a, sink
}

// Each turn end reaches the real handler, and its verdict follows the turn-end
// row.
func TestCommandCodeTurnEndsApplyTheirVerdictAfterTheTurnEnd(t *testing.T) {
	t.Parallel()
	transient := providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Command Code reported a transient failure"}
	for _, tc := range []struct {
		name string
		line string
		want providerkit.TurnFailure
	}{
		{
			name: "a plan limit schedules at its reset instant",
			line: commandcodePlanLimitEnd,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: commandcodeResetAt, Window: providerkit.UsageWindowWeekOrLess, Cause: "Command Code reported its plan usage limit (the provider states when to retry)"},
		},
		{name: "a 529 is transient", line: commandcodeOverloadedEnd, want: commandcodeOverloadedFailure},
		{name: "a stream failure at status 0 is transient", line: commandcodeStreamEnd, want: transient},
		{
			name: "missing credits cancel both retries",
			line: commandcodeCreditsEnd,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Command Code reported that the plan does not cover the request"},
		},
		{name: "a success cancels the server-error retry", line: commandcodeSuccessEnd, want: providerkit.TurnFailure{}},
		{
			name: "an error with no status is not retryable",
			line: `{"jsonrpc":"2.0","method":"turn/completed","params":{"turnId":"turn_1","stopReason":"run_error","error":{"name":"Error","message":"Tool registry failed"}}}`,
			want: providerkit.TurnFailure{},
		},
		{
			name: "an interrupted turn is not retryable",
			line: `{"jsonrpc":"2.0","method":"turn/completed","params":{"turnId":"turn_1","stopReason":"interrupted","error":{"name":"TransportError","message":"POST /alpha/generate → 529 overloaded_error: Overloaded","status":529}}}`,
			want: providerkit.TurnFailure{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newCommandCodeRetryAgent(t)
			a.HandleOutput([]byte(tc.line))
			assert.Equal(t, providerkittest.TurnEndThen(tc.want), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

// newCommandCodeInterruptAgent is newCommandCodeRetryAgent with a native peer
// that accepts every interrupt.
func newCommandCodeInterruptAgent(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	a, sink := commandCodeSteerAgent(t, func(method string, _ json.RawMessage) agenttest.RPCReply {
		assert.Equal(t, methodTurnInterrupt, method)
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	clock := testutil.NewQuartzMock(t)
	clock.Set(commandcodeObservedAt).MustWait(testutil.DeadlineContext(t))
	a.clock = clock
	return a, sink
}

// Command Code catches the error of a stopped request before it sets its abort
// flag, so a stopped turn can end as a run error. The user asked to stop, so
// the failure must not re-send the stopped prompt.
func TestCommandCodeFailureAfterAStopDoesNotRetry(t *testing.T) {
	t.Parallel()
	a, sink := newCommandCodeInterruptAgent(t)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	a.HandleOutput([]byte(commandcodeOverloadedEnd))
	rows := sink.Messages()
	if assert.NotEmpty(t, rows) {
		assert.Equal(t, agent.MessageCompletionError, rows[len(rows)-1].Completion, "the failure keeps its own completion")
	}
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// The stop of one turn never decides the next turn.
func TestCommandCodeStopDoesNotOutliveItsTurn(t *testing.T) {
	t.Parallel()
	a, sink := newCommandCodeInterruptAgent(t)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"turn/completed","params":{"turnId":"turn_1","stopReason":"interrupted"}}`))
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"turn/started","params":{"turnId":"turn_2"}}`))
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"turn/completed","params":{"seq":16,"turnId":"turn_2","stopReason":"run_error","finalText":"","turnCount":1,"error":{"name":"TransportError","message":"POST /alpha/generate → 529 overloaded_error: Overloaded","status":529}}}`))

	stopped := providerkittest.TurnEndThen(providerkit.TurnFailure{})
	timeline := providerkittest.TurnEndAndRetryOps(sink.Timeline())
	require.Greater(t, len(timeline), len(stopped))
	assert.Equal(t, stopped, timeline[:len(stopped)])
	assert.Equal(t, providerkittest.TurnEndThen(commandcodeOverloadedFailure),
		timeline[len(stopped):], "the second turn was not stopped, so its failure retries")
}

// A stop with no turn running notes nothing, so the next turn's failure still
// retries.
func TestCommandCodeStopWhileIdleDoesNotDecideTheNextTurn(t *testing.T) {
	t.Parallel()
	a, sink := newCommandCodeInterruptAgent(t)
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"turn/completed","params":{"turnId":"turn_1","stopReason":"end_turn"}}`))
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"turn/started","params":{"turnId":"turn_2"}}`))
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"turn/completed","params":{"turnId":"turn_2","stopReason":"run_error","error":{"name":"TransportError","message":"POST /alpha/generate → 529 overloaded_error: Overloaded","status":529}}}`))

	completed := providerkittest.TurnEndThen(providerkit.TurnFailure{})
	timeline := providerkittest.TurnEndAndRetryOps(sink.Timeline())
	require.Greater(t, len(timeline), len(completed))
	assert.Equal(t, providerkittest.TurnEndThen(commandcodeOverloadedFailure), timeline[len(completed):])
}

// A process that exits during a turn ends it on the worker's own row, which
// states no transport error, so nothing schedules.
func TestCommandCodeProcessExitDuringATurnDoesNotRetry(t *testing.T) {
	t.Parallel()
	a, sink := newCommandCodeRetryAgent(t)
	a.finishStream()
	rows := sink.Messages()
	if assert.NotEmpty(t, rows) {
		assert.Equal(t, agent.MessageCompletionError, rows[len(rows)-1].Completion)
	}
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// A repeated turn/completed of a turn that already ended applies no second
// verdict.
func TestCommandCodeRepeatedTurnEndAppliesOneVerdict(t *testing.T) {
	t.Parallel()
	a, sink := newCommandCodeRetryAgent(t)
	a.HandleOutput([]byte(commandcodeOverloadedEnd))
	a.HandleOutput([]byte(commandcodeOverloadedEnd))
	assert.Equal(t, providerkittest.TurnEndThen(commandcodeOverloadedFailure), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

func TestCommandCodeTurnFailure(t *testing.T) {
	t.Parallel()
	transient := providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Command Code reported a transient failure"}
	billing := providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Command Code reported that the plan does not cover the request"}
	planLimit := func(window providerkit.UsageWindow) providerkit.TurnFailure {
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: window, Cause: "Command Code reported its plan usage limit"}
	}
	for _, tc := range []struct {
		name        string
		line        string
		interrupted bool
		want        providerkit.TurnFailure
	}{
		{
			name: "the research plan limit lifts at its reset instant",
			line: commandcodePlanLimitEnd,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: commandcodeResetAt, Window: providerkit.UsageWindowWeekOrLess, Cause: "Command Code reported its plan usage limit (the provider states when to retry)"},
		},
		{name: "the research 529 is transient", line: commandcodeOverloadedEnd, want: commandcodeOverloadedFailure},
		{name: "the research stream failure at status 0 is transient", line: commandcodeStreamEnd, want: transient},
		{name: "the research missing credits are a billing block", line: commandcodeCreditsEnd, want: billing},
		{name: "the research success is not retryable", line: commandcodeSuccessEnd, want: providerkit.TurnFailure{}},
		{
			name: "a plan limit whose reset instant is past falls back to its window",
			line: `{"method":"turn/completed","params":{"turnId":"turn_1","stopReason":"run_error","error":{"name":"TransportError","message":"POST /alpha/generate → 429 rate_limit_error: You've reached the usage limit for your plan. Your 5-hour limit resets at 2026-10-28T09:00:00.000Z","status":429}}}`,
			want: planLimit(providerkit.UsageWindowWeekOrLess),
		},
		{
			name: "a plan limit whose reset instant is more than 31 days ahead falls back to its window",
			line: `{"method":"turn/completed","params":{"turnId":"turn_1","stopReason":"run_error","error":{"name":"TransportError","message":"POST /alpha/generate → 429 rate_limit_error: You've reached the usage limit for your plan. Your monthly limit resets at 2026-12-30T00:00:00.000Z","status":429}}}`,
			want: planLimit(providerkit.UsageWindowLonger),
		},
		{
			name: "a plan limit whose reset instant does not parse falls back to its window",
			line: `{"method":"turn/completed","params":{"turnId":"turn_1","stopReason":"run_error","error":{"name":"TransportError","message":"POST /alpha/generate → 429 rate_limit_error: Your 5-hour limit resets at 2026-13-40T99:00:00Z","status":429}}}`,
			want: planLimit(providerkit.UsageWindowWeekOrLess),
		},
		{
			name: "a plan limit with no reset instant takes its window",
			line: `{"method":"turn/completed","params":{"turnId":"turn_1","stopReason":"run_error","error":{"name":"TransportError","message":"POST /alpha/generate → 429 rate_limit_error: You've reached the usage limit for your plan.","status":429}}}`,
			want: planLimit(providerkit.UsageWindowUnstated),
		},
		{
			name: "a plain 429 is rate limited",
			line: `{"method":"turn/completed","params":{"turnId":"turn_1","stopReason":"run_error","error":{"name":"TransportError","message":"POST /alpha/generate → 429 rate_limit_error: Too many requests","status":429}}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Command Code reported a rate limit"},
		},
		{
			name: "exhausted premium credits mid-stream are a billing block",
			line: `{"method":"turn/completed","params":{"turnId":"turn_1","stopReason":"run_error","error":{"name":"TransportError","message":"PREMIUM_CREDITS_EXHAUSTED: Your premium credits are exhausted.","status":500}}}`,
			want: billing,
		},
		{
			name: "a model outside the plan is a billing block",
			line: `{"method":"turn/completed","params":{"turnId":"turn_1","stopReason":"run_error","error":{"name":"TransportError","message":"MODEL_NOT_IN_PLAN: This model is not in your plan.","status":403}}}`,
			want: billing,
		},
		{
			name: "a definitive vendor verdict wins over a 5xx",
			line: `{"method":"turn/completed","params":{"turnId":"turn_1","stopReason":"run_error","error":{"name":"TransportError","message":"POST /alpha/generate → 500 api_error: Your credit balance is too low to access the Anthropic API.","status":500}}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the vendor reported a billing or plan block"},
		},
		{
			// A status is generic. The vendor's text states a monthly limit, which a
			// retry within the week does not lift.
			name: "a 5xx whose text is a monthly vendor limit is a longer usage limit",
			line: `{"method":"turn/completed","params":{"turnId":"turn_1","stopReason":"run_error","error":{"name":"TransportError","message":"POST /alpha/generate → 500 api_error: You have reached your monthly usage limit.","status":500}}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowLonger, Cause: "Kimi reported its monthly usage limit"},
		},
		{
			name: "a plain 429 whose text the vendor reads as a rate limit takes the vendor verdict",
			line: `{"method":"turn/completed","params":{"turnId":"turn_1","stopReason":"run_error","error":{"name":"TransportError","message":"POST /alpha/generate → 429 rate_limit_error: Rate limit reached for requests","status":429}}}`,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "the vendor reported a rate limit"},
		},
		{
			name: "an error with no status is not retryable despite a vendor's indicative wording",
			line: `{"method":"turn/completed","params":{"turnId":"turn_1","stopReason":"run_error","error":{"name":"Error","message":"Rate limit reached for requests"}}}`,
			want: providerkit.TurnFailure{},
		},
		{name: "a 408 is transient", line: `{"method":"turn/completed","params":{"turnId":"turn_1","stopReason":"run_error","error":{"name":"TransportError","message":"POST /alpha/generate → 408 timeout","status":408}}}`, want: transient},
		{name: "a 400 is not retryable", line: `{"method":"turn/completed","params":{"turnId":"turn_1","stopReason":"run_error","error":{"name":"TransportError","message":"POST /alpha/generate → 400 invalid_request_error: prompt is too long","status":400}}}`, want: providerkit.TurnFailure{}},
		{name: "a 499 is not retryable", line: `{"method":"turn/completed","params":{"turnId":"turn_1","stopReason":"run_error","error":{"name":"TransportError","message":"client closed","status":499}}}`, want: providerkit.TurnFailure{}},
		{name: "an absent status is not retryable", line: `{"method":"turn/completed","params":{"turnId":"turn_1","stopReason":"run_error","error":{"name":"Error","message":"Overloaded"}}}`, want: providerkit.TurnFailure{}},
		{name: "a string error is not retryable", line: `{"method":"turn/completed","params":{"turnId":"turn_1","stopReason":"run_error","error":"Overloaded"}}`, want: providerkit.TurnFailure{}},
		{name: "an absent error is not retryable", line: `{"method":"turn/completed","params":{"turnId":"turn_1","stopReason":"run_error"}}`, want: providerkit.TurnFailure{}},
		{name: "an unreadable error is not retryable", line: `{"method":"turn/completed","params":{"turnId":"turn_1","stopReason":"run_error","error":{"status":"529"}}}`, want: providerkit.TurnFailure{}},
		{name: "an interrupted turn is not retryable", line: `{"method":"turn/completed","params":{"turnId":"turn_1","stopReason":"interrupted","error":{"name":"TransportError","message":"Overloaded","status":529}}}`, want: providerkit.TurnFailure{}},
		{name: "a stopped turn's 529 is not retryable", line: commandcodeOverloadedEnd, interrupted: true, want: providerkit.TurnFailure{}},
		{name: "a stopped turn's plan limit is not retryable", line: commandcodePlanLimitEnd, interrupted: true, want: providerkit.TurnFailure{}},
		{name: "a stopped turn's network failure is not retryable", line: commandcodeStreamEnd, interrupted: true, want: providerkit.TurnFailure{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var frame struct {
				Params struct {
					StopReason string          `json:"stopReason"`
					Error      json.RawMessage `json:"error"`
				} `json:"params"`
			}
			require.NoError(t, json.Unmarshal([]byte(tc.line), &frame))
			assert.Equal(t, tc.want, commandcodeTurnFailure(frame.Params.StopReason, commandcodeRunErrorOf(frame.Params.Error), tc.interrupted, commandcodeObservedAt))
		})
	}
}
