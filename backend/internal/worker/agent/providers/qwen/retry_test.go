package qwen

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// qwenFixtureSession is the session of the lifecycle fixtures below, which
// .tmp/probe/qwen/wire-401.log and wire-500.log recorded against Qwen Code
// 0.26.0.
const qwenFixtureSession = "b5edb63d-105e-48a9-9be6-60329f807925"

// The request lifecycle of a fail-fast 401 (wire-401.log): one request starts
// and ends with an error, and the prompt fails at once. Qwen states no retry.
const (
	qwenFixtureRequestStarted = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"b5edb63d-105e-48a9-9be6-60329f807925","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""},"_meta":{"executionLifecycle":{"v":1,"kind":"request","executionId":"d84e3e92-11fe-4b83-ba3b-1fa8adc2f759","sessionId":"b5edb63d-105e-48a9-9be6-60329f807925","promptId":"b5edb63d-105e-48a9-9be6-60329f807925########1","model":"fake-model","startedAt":1793149838277,"phase":"started"}}}}}`
	qwenFixtureRequestFailed  = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"b5edb63d-105e-48a9-9be6-60329f807925","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""},"_meta":{"executionLifecycle":{"v":1,"kind":"request","executionId":"d84e3e92-11fe-4b83-ba3b-1fa8adc2f759","sessionId":"b5edb63d-105e-48a9-9be6-60329f807925","promptId":"b5edb63d-105e-48a9-9be6-60329f807925########1","model":"fake-model","startedAt":1793149838277,"phase":"ended","endedAt":1793149838358,"durationMs":81,"outcome":"error"}}}}}`
	qwenFixture401Error       = `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error","data":{"details":"401 Incorrect API key provided."}}}`
)

// qwenFixtureRetryStarted is the start of the retry of the request that
// qwenFixtureRequestFailed ended. Qwen gives each attempt its own executionId
// and keeps the promptId (wire-500.log); this line takes the shape of
// qwenFixtureRequestStarted with a new executionId.
const qwenFixtureRetryStarted = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"b5edb63d-105e-48a9-9be6-60329f807925","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""},"_meta":{"executionLifecycle":{"v":1,"kind":"request","executionId":"378faed3-8d10-4624-84e6-a6e74d88b03c","sessionId":"b5edb63d-105e-48a9-9be6-60329f807925","promptId":"b5edb63d-105e-48a9-9be6-60329f807925########1","model":"fake-model","startedAt":1793149841602,"phase":"started"}}}}}`

// qwenFixtureNextPromptStarted is the first request of the NEXT prompt of the
// same session: Qwen counts its prompts in the promptId suffix.
const qwenFixtureNextPromptStarted = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"b5edb63d-105e-48a9-9be6-60329f807925","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""},"_meta":{"executionLifecycle":{"v":1,"kind":"request","executionId":"9a09982f-92a2-45fa-8920-8a0e7bec1b45","sessionId":"b5edb63d-105e-48a9-9be6-60329f807925","promptId":"b5edb63d-105e-48a9-9be6-60329f807925########2","model":"fake-model","startedAt":1793149851602,"phase":"started"}}}}}`

// The prompt responses of R/qwen.md and .tmp/probe/qwen/wire-*.log, verbatim.
const (
	qwenFixtureSuccess          = `{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn","_meta":{"qwen.branchPoint":{"assistantRecordUuid":"af07ed62-3172-43b3-aca0-65c8eec75c51","checkpointUuid":"72493e2c-1706-4670-8071-4f413128cb4b"}}}}`
	qwenFixture500Error         = `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error","data":{"details":"500 The server had an error while processing your request."}}}`
	qwenFixture503Error         = `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error","data":{"details":"503 The engine is currently overloaded, please try again later."}}}`
	qwenFixture529Error         = `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error","data":{"details":"529 Overloaded"}}}`
	qwenFixtureConnectionError  = `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error","data":{"details":"Connection error."}}}`
	qwenFixtureStreamOverloaded = `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error","data":{"details":"Overloaded"}}}`
	qwenFixtureQuotaExhausted   = `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error","data":{"details":"Quota exhausted: Your token-plan 1-week quota has been exhausted. The quota will reset at 07-27 09:25:00 UTC.\n\nPlease retry after the reset time, or switch to another API key / auth method."}}}`
	qwenFixtureRateLimit        = `{"jsonrpc":"2.0","id":3,"error":{"code":429,"message":"Rate limit exceeded. Try again later."}}`
	qwenFixtureContextLength    = `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error","data":{"details":"400 This model's maximum context length is 8192 tokens. However, your messages resulted in 90000 tokens."}}}`
)

// qwenOAuthDiscontinuedError is the fast failure of the discontinued Qwen OAuth
// free tier. Qwen Code 0.26.0 writes the text in packages/core/src/utils/retry.ts,
// and the SDK wraps it as every other cause.
const qwenOAuthDiscontinuedError = `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error","data":{"details":"Qwen OAuth free tier has been discontinued as of 2026-04-15.\n\nTo continue using Qwen Code, try one of these alternatives:\n  - OpenRouter:    https://openrouter.ai/docs/quickstart\n  - Fireworks AI:  https://docs.fireworks.ai/api-reference/introduction\n  - ModelStudio:   https://help.aliyun.com/zh/model-studio/coding-plan\n\nAfter setting up your API key, run /auth to configure your provider."}}}`

// qwenRequestTimeoutError is the timeout that the OpenAI content generator of
// Qwen Code 0.26.0 writes (packages/core/src/core/openaiContentGenerator/errorHandler.ts).
// No probe reached it.
const qwenRequestTimeoutError = `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error","data":{"details":"Request timeout after 120s. Try reducing input length or increasing timeout in config."}}}`

// qwenResponseError reads the error member of one JSON-RPC response line, as
// the base hands it to Hooks.PromptEnded.
func qwenResponseError(t *testing.T, line string) *providerkit.JSONRPCResponseError {
	t.Helper()
	var response struct {
		Error struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(line), &response))
	return &providerkit.JSONRPCResponseError{Code: response.Error.Code, Message: response.Error.Message, Data: response.Error.Data}
}

// qwenResponseErrorObject returns the error member of one JSON-RPC response
// line, verbatim, as the test peer answers it.
func qwenResponseErrorObject(t *testing.T, line string) json.RawMessage {
	t.Helper()
	var response struct {
		Error json.RawMessage `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(line), &response))
	require.NotEmpty(t, response.Error)
	return response.Error
}

// apiRetryRows returns the api_retry notifications of one transcript.
func apiRetryRows(sink *agenttest.ControlSink) []map[string]any {
	var rows []map[string]any
	for _, notice := range sink.Notifications() {
		if notice["type"] == "api_retry" {
			rows = append(rows, notice)
		}
	}
	return rows
}

func qwenAt(t *testing.T, value string) time.Time {
	t.Helper()
	instant, err := time.Parse(time.RFC3339, value)
	require.NoError(t, err)
	return instant
}

func TestQwenTurnFailure(t *testing.T) {
	t.Parallel()
	notRetryable := providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
	transient := providerkit.TurnFailureTransient
	for _, tc := range []struct {
		name       string
		response   string
		observedAt string
		wantKind   providerkit.TurnFailureKind
		wantReset  string
		wantWindow providerkit.UsageWindow
	}{
		{name: "a 500 is transient", response: qwenFixture500Error, wantKind: transient},
		{name: "a 503 is transient", response: qwenFixture503Error, wantKind: transient},
		{name: "a 529 is transient", response: qwenFixture529Error, wantKind: transient},
		{name: "a network failure is transient", response: qwenFixtureConnectionError, wantKind: transient},
		{name: "an overload in the stream is transient", response: qwenFixtureStreamOverloaded, wantKind: transient},
		{name: "a request timeout is transient", response: qwenRequestTimeoutError, wantKind: transient},
		{name: "the 429 that Qwen gave up on is a rate limit", response: qwenFixtureRateLimit, wantKind: providerkit.TurnFailureRateLimited},
		{name: "a 401 is not retryable", response: qwenFixture401Error, wantKind: providerkit.TurnFailureNotRetryable},
		{name: "a context overflow is not retryable", response: qwenFixtureContextLength, wantKind: providerkit.TurnFailureNotRetryable},
		{name: "the discontinued OAuth tier is a billing block", response: qwenOAuthDiscontinuedError, wantKind: providerkit.TurnFailureBillingBlocked},
		{
			// The capture states no date of its own. observedAt lies three
			// days before the stamp, inside its one-week window.
			name: "a quota with a reset stamp waits for the stamp", response: qwenFixtureQuotaExhausted,
			observedAt: "2026-07-24T09:25:00Z", wantKind: providerkit.TurnFailureUsageLimited, wantReset: "2026-07-27T09:25:00Z",
			wantWindow: providerkit.UsageWindowWeekOrLess,
		},
		{
			// The probe ran on 2026-10-28, after the stamp of this year, so
			// the stamp reads as next year's and lies too far ahead. The
			// window of the quota then decides.
			name: "a quota whose stamp is past waits for its one-week window", response: qwenFixtureQuotaExhausted,
			observedAt: "2026-10-28T01:00:00Z", wantKind: providerkit.TurnFailureUsageLimited, wantWindow: providerkit.UsageWindowWeekOrLess,
		},
		{
			name: "a quota whose stamp is now reads as next year's", response: qwenFixtureQuotaExhausted,
			observedAt: "2026-07-27T09:25:00Z", wantKind: providerkit.TurnFailureUsageLimited, wantWindow: providerkit.UsageWindowWeekOrLess,
		},
		{
			// The stamp of the capture, shifted to the turn of the year: the
			// January stamp lies in the next year.
			name:       "a january stamp observed in december reads as next year's",
			response:   `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error","data":{"details":"Quota exhausted: Your token-plan 1-week quota has been exhausted. The quota will reset at 01-02 09:25:00 UTC.\n\nPlease retry after the reset time, or switch to another API key / auth method."}}}`,
			observedAt: "2026-12-30T00:00:00Z", wantKind: providerkit.TurnFailureUsageLimited, wantReset: "2027-01-02T09:25:00Z",
			wantWindow: providerkit.UsageWindowWeekOrLess,
		},
		{
			// The stamp of the capture, shifted to a leap day: 2027 has none,
			// and the 2028 date lies too far ahead.
			name:       "a leap-day stamp in a common year falls back to the window",
			response:   `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error","data":{"details":"Quota exhausted: Your token-plan 1-week quota has been exhausted. The quota will reset at 02-29 09:25:00 UTC.\n\nPlease retry after the reset time, or switch to another API key / auth method."}}}`,
			observedAt: "2027-02-25T00:00:00Z", wantKind: providerkit.TurnFailureUsageLimited, wantWindow: providerkit.UsageWindowWeekOrLess,
		},
		{
			// R/qwen.md reports quotas that state a date alone, with no time
			// and no zone. This text, which takes that form, reads as no
			// reset instant.
			name:       "a quota with a date alone falls back to the window",
			response:   `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error","data":{"details":"Quota exhausted: Your monthly quota has been exhausted. The quota will reset at 2026-07-28."}}}`,
			observedAt: "2026-07-24T09:25:00Z", wantKind: providerkit.TurnFailureUsageLimited, wantWindow: providerkit.UsageWindowLonger,
		},
		{
			// GLM through Qwen Code, with the text of modelvendor's Pi
			// capture: the vendor's own stamp decides.
			name:       "a vendor usage limit with a reset instant waits for the stamp",
			response:   `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error","data":{"details":"429 {\"code\":\"1308\",\"message\":\"Usage limit reached for 5 hour. Your limit will reset at 2026-07-14 19:06:13\"}"}}}`,
			observedAt: "2026-07-14T10:34:45Z", wantKind: providerkit.TurnFailureUsageLimited, wantReset: "2026-07-14T11:06:14Z",
		},
		{
			// The SDK puts a cause that is JSON in place of the whole data.
			name:     "a vendor code in a JSON cause is read",
			response: `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error","data":{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","code":"insufficient_quota"}}}}`,
			wantKind: providerkit.TurnFailureBillingBlocked,
		},
		{
			name:     "an indicative vendor verdict decides after Qwen's own classes",
			response: `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error","data":{"details":"429 Rate limit reached for gpt-5 in organization org-1 on requests per min (RPM): Limit 500, Used 500, Requested 1."}}}`,
			wantKind: providerkit.TurnFailureRateLimited,
		},
		{
			// A 4xx that names an overload is the client's fault.
			name:     "an overload wording after a 4xx is not retryable",
			response: `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error","data":{"details":"413 The request overloaded the context window."}}}`,
			wantKind: providerkit.TurnFailureNotRetryable,
		},
		{
			name:     "a workspace change is not retryable",
			response: `{"jsonrpc":"2.0","id":3,"error":{"code":-32000,"message":"A workspace change is being applied; retry the prompt."}}`,
			wantKind: providerkit.TurnFailureNotRetryable,
		},
		{
			name:     "an internal error with no data is not retryable",
			response: `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error"}}`,
			wantKind: providerkit.TurnFailureNotRetryable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			observedAt := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
			if tc.observedAt != "" {
				observedAt = qwenAt(t, tc.observedAt)
			}
			rpcErr := qwenResponseError(t, tc.response)
			failure, payload := qwenTurnFailure(rpcErr, false, observedAt)
			assert.Equal(t, tc.wantKind, failure.Kind)
			assert.Equal(t, tc.wantWindow, failure.Window)
			if tc.wantReset == "" {
				assert.True(t, failure.ResetAt.IsZero(), "no reset instant: %s", failure.ResetAt)
			} else {
				assert.Equal(t, qwenAt(t, tc.wantReset), failure.ResetAt)
			}
			if failure.Kind != providerkit.TurnFailureNotRetryable {
				assert.NotEmpty(t, failure.Cause)
			}
			assert.JSONEq(t, string(qwenResponseErrorObject(t, tc.response)), string(payload), "the payload is the wire's error")
		})
	}

	t.Run("a stop is not retryable", func(t *testing.T) {
		t.Parallel()
		failure, payload := qwenTurnFailure(qwenResponseError(t, qwenFixture500Error), true, time.Time{})
		assert.Equal(t, notRetryable, failure)
		assert.Nil(t, payload)
	})
	t.Run("a success is not retryable", func(t *testing.T) {
		t.Parallel()
		failure, payload := qwenTurnFailure(nil, false, time.Time{})
		assert.Equal(t, notRetryable, failure)
		assert.Nil(t, payload)
	})
	t.Run("a failure with no JSON-RPC error is not retryable", func(t *testing.T) {
		t.Parallel()
		failure, payload := qwenTurnFailure(errors.New("agent process exited"), false, time.Time{})
		assert.Equal(t, notRetryable, failure)
		assert.Nil(t, payload)
	})
	t.Run("a wrapped JSON-RPC error is read", func(t *testing.T) {
		t.Parallel()
		failure, _ := qwenTurnFailure(fmt.Errorf("session/prompt: %w", qwenResponseError(t, qwenFixture500Error)), false, time.Time{})
		assert.Equal(t, providerkit.TurnFailureTransient, failure.Kind)
	})
}

// newQwenRetryAgent builds a Qwen agent whose peer answers session/prompt with
// response, and whose transcript is a recording sink that keeps the order of
// every row and every retry operation.
func newQwenRetryAgent(t *testing.T, observedAt time.Time, response string) (*Agent, *agenttest.Sink) {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	clock.Set(observedAt)
	var line struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(response), &line))
	reply := agenttest.RPCReply{Result: line.Result}
	if len(line.Error) > 0 {
		reply = agenttest.RPCReply{Error: line.Error}
	}
	a, _, _ := newQwenAgent(t, clock, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method == acp.MethodSessionPrompt {
			return reply
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	return a, sink
}

// sendQwenPrompt sends one prompt and waits for its end.
func sendQwenPrompt(t *testing.T, a *Agent) {
	t.Helper()
	require.NoError(t, a.SendInput("Say hello.", nil))
	testutil.RequireEventually(t, func() bool { return !a.PromptActive() })
}

func timelineOps(timeline []agenttest.TimelineEntry) []string {
	ops := make([]string, 0, len(timeline))
	for _, entry := range timeline {
		ops = append(ops, entry.Op)
	}
	return ops
}

// The failed prompt writes its failure note, and the verdict follows it: the
// queue's retry row then follows the reason it retries.
func TestQwenPromptEndAppliesOneVerdictAfterTheFailureNote(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		response string
		wantOps  []string
	}{
		{name: "a 500 schedules a server-error retry", response: qwenFixture500Error, wantOps: []string{"leapmux_notification", "schedule_server_error"}},
		{name: "a network failure schedules a server-error retry", response: qwenFixtureConnectionError, wantOps: []string{"leapmux_notification", "schedule_server_error"}},
		{name: "a 429 schedules a server-error retry", response: qwenFixtureRateLimit, wantOps: []string{"leapmux_notification", "schedule_server_error"}},
		{name: "a 401 cancels the server-error retry", response: qwenFixture401Error, wantOps: []string{"leapmux_notification", "cancel_server_error"}},
		{name: "the discontinued OAuth tier cancels both retries", response: qwenOAuthDiscontinuedError, wantOps: []string{"leapmux_notification", "cancel_usage_limit", "cancel_server_error"}},
		{name: "a success cancels the server-error retry", response: qwenFixtureSuccess, wantOps: []string{"turn_end", "cancel_server_error"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newQwenRetryAgent(t, time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC), tc.response)

			sendQwenPrompt(t, a)

			require.Equal(t, tc.wantOps, timelineOps(sink.Timeline()))
			if tc.wantOps[len(tc.wantOps)-1] == "schedule_server_error" {
				assert.JSONEq(t, string(qwenResponseErrorObject(t, tc.response)), string(sink.LastAutoSchedule().SourcePayload))
			}
		})
	}
}

// The agent's clock dates the failure, so the year-less stamp of a quota
// becomes a usage-limit retry at the stamp.
func TestQwenQuotaWithAResetStampSchedulesAUsageLimitRetry(t *testing.T) {
	t.Parallel()
	// The capture states no date of its own; the clock stands three days
	// before its stamp.
	a, sink := newQwenRetryAgent(t, time.Date(2026, 7, 24, 9, 25, 0, 0, time.UTC), qwenFixtureQuotaExhausted)

	sendQwenPrompt(t, a)

	timeline := sink.Timeline()
	assert.Equal(t, []string{"leapmux_notification", "schedule_usage_limit", "cancel_server_error"}, timelineOps(timeline))
	require.Len(t, timeline, 3)
	assert.Equal(t, time.Date(2026, 7, 27, 9, 25, 0, 0, time.UTC), timeline[1].ResetAt)
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, timeline[1].Kind)
}

// A request that Qwen retried and then gave up on states one api_retry row,
// and the failed prompt schedules one server-error retry after its note.
func TestQwenRetriedRequestThatFailsStatesOneRetryAndOneVerdict(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	recording := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(recording))
	a.SetSessionIDForTest(qwenFixtureSession)
	a.SetPromptActiveForTest(true)

	a.HandleOutput([]byte(qwenFixtureRequestStarted))
	a.HandleOutput([]byte(qwenFixtureRequestFailed))
	a.HandleOutput([]byte(qwenFixtureRetryStarted))
	a.HandleOutput([]byte(qwenFixtureRequestFailed))
	a.FinishPromptRequestForTest(qwenFixtureSession, nil, qwenResponseError(t, qwenFixture500Error))

	assert.Equal(t, []string{"leapmux_notification", "leapmux_notification", "schedule_server_error"}, timelineOps(recording.Timeline()))
	notices := recording.LeapMuxNotifications()
	require.Len(t, notices, 2)
	assert.Equal(t, map[string]any{"type": "api_retry", "count": int64(1)}, notices[0])
}

// A stopped prompt applies no failure: it cancels the server-error retry.
func TestQwenStoppedPromptCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetPromptActiveForTest(true)
	a.NoteInterruptRequestedForTest()

	a.FinishPromptRequestForTest(qwenTestSession, nil, qwenResponseError(t, qwenFixture500Error))

	require.Equal(t, []string{"cancel_server_error"}, timelineOps(sink.Timeline()))
	assert.Equal(t, providerkit.TurnEndedWithoutRetryableFailure, sink.LastAutoCancelReason())
}

// A refused manual compaction fails the prompt only to state the refusal, so
// it decides before the failure classes, and cancels with the compaction's
// reason.
func TestQwenRefusedCompactionCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetPromptActiveForTest(true)
	a.stateMu.Lock()
	a.compressPending = &qwenCompactionRequest{}
	a.stateMu.Unlock()

	a.FinishPromptRequestForTest(qwenTestSession, nil, &providerkit.JSONRPCResponseError{
		Code: qwenInternalErrorCode, Message: "Internal error", Data: json.RawMessage(`{"details":"` + qwenCompressNotBeneficial + `"}`),
	})

	assert.Equal(t, []string{"leapmux_notification", "cancel_server_error"}, timelineOps(sink.Timeline()))
	assert.Equal(t, providerkit.TurnEndedOnCompaction, sink.LastAutoCancelReason())
}

// A turn that Qwen started by itself ends with its own frame, and the default
// verdict cancels the server-error retry after that row.
func TestQwenAgentTurnEndCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	require.True(t, a.BeginAgentTurn())

	a.HandleOutput(frame(t, map[string]any{"method": contracts.QwenMethodEndTurn, "params": map[string]any{"sessionId": qwenTestSession}}))

	require.Equal(t, []string{"turn_end", "cancel_server_error"}, timelineOps(sink.Timeline()))
	assert.Equal(t, providerkit.TurnEndedWithoutRetryableFailure, sink.LastAutoCancelReason())
}
