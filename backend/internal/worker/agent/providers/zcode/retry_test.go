package zcode

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodeRetryPrecedesTurnRelease(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name     string
		retained bool
		failed   bool
	}{{"success", false, false}, {"retained success", true, false}, {"failure", false, true}, {"retained failure", true, true}} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			a.sink = agent.NewOrderedProviderServices(agent.OutputQueueFor(a.sink), a.sink)
			frame := []byte(zcodeTurnFailed500)
			op := "schedule_server_error"
			if !scenario.failed {
				frame = zcodeEventLine(t, 12, contracts.ZCodeEventTurnCompleted, `{"response":"done","resultType":"success"}`)
				op = "cancel_server_error"
			}
			zcodePrepareCapturedTurn(t, a, string(frame), 1)
			a.PublishTurnActive()
			if scenario.retained {
				sink.PersistErr = assert.AnError
			}
			a.HandleOutput(frame)
			sink.PersistErr = nil
			require.NoError(t, agent.FlushPendingOutput(a.sink))
			assert.Equal(t, []string{"turn_end", op, "idle"}, providerkittest.TurnEndRetryAndIdleOpNames(sink.Operations()))
			if scenario.failed {
				assert.Equal(t, 1, sink.AutoScheduleCount())
			} else {
				assert.Zero(t, sink.AutoScheduleCount())
			}
		})
	}
}

const (
	zcodeTurnFailed500         = `{"method":"session/event","params":{"sessionId":"sess_560831a6-2312-4743-92aa-f3bdfd451ff5","seq":12,"type":"turn.failed","payload":{"error":{"type":"unknown_error","attribution":{"source":"provider","reason":"server_error","errorPhase":"stream","exceptionKind":"api_call","providerId":"zai-standard-api","modelId":"GLM-5.3","providerKind":"openai-compatible","transport":"sse","statusCode":500,"retryable":true,"highspeedCardId":null},"code":"model_request_failed","message":"Provider returned a server error.","detail":"Turn execution failed\nprovider=zai-standard-api model=GLM-5.3 request=8e262066-89de-4d4c-bd0f-058f6e522cf3 reason=server_error status=500 retryable=true\nInternal server error","underlyingErrorMessage":"Internal server error"},"turnPhase":"processing_input","inputId":"in1"}}}`
	zcodeTurnFailedUsage1308   = `{"method":"session/event","params":{"sessionId":"sess_e3598e37-9316-47d9-b4ba-83658001b9a5","seq":6,"type":"turn.failed","payload":{"error":{"type":"unknown_error","attribution":{"source":"provider","reason":"rate_limited","errorPhase":"stream","exceptionKind":"provider_business","providerId":"zai-standard-api","modelId":"GLM-5.3","providerKind":"openai-compatible","transport":"sse","statusCode":429,"providerErrorCode":"1308","retryable":false,"highspeedCardId":null},"code":"1308","message":"Usage limit reached for 5 hour. Your limit will reset at 2026-10-28 20:06:00","detail":"Turn execution failed\nprovider=zai-standard-api provider_code=1308 model=GLM-5.3 request=09bbc2ea-13cb-49f7-8b28-8732dab03142 reason=rate_limited status=429 retryable=false","underlyingErrorMessage":"Usage limit reached for 5 hour. Your limit will reset at 2026-10-28 20:06:00"},"turnPhase":"processing_input","inputId":"in1"}}}`
	zcodeTurnFailedRefused     = `{"method":"session/event","params":{"sessionId":"sess_c03a7225-8041-4d34-b532-c98984046a68","seq":12,"type":"turn.failed","payload":{"error":{"type":"unknown_error","attribution":{"source":"network","reason":"network_error","errorPhase":"stream","exceptionKind":"transport","providerId":"zai-standard-api","modelId":"GLM-5.3","providerKind":"openai-compatible","transport":"sse","providerErrorCode":"ECONNREFUSED","retryable":true,"highspeedCardId":null},"code":"ECONNREFUSED","message":"Cannot connect to API: connect ECONNREFUSED 127.0.0.1:18999","detail":"Turn execution failed\nprovider=zai-standard-api provider_code=ECONNREFUSED model=GLM-5.3 request=f0d5cefe-4bd3-4109-a408-5eb31a65502a reason=network_error retryable=true","underlyingErrorMessage":"Cannot connect to API: connect ECONNREFUSED 127.0.0.1:18999"},"turnPhase":"processing_input","inputId":"in1"}}}`
	zcodeTurnFailedOverloaded  = `{"method":"session/event","params":{"deliveryKind":"desktop-continuous","eventId":"9e987d9d-a69b-45a3-b003-4df62bfb77da","payload":{"error":{"type":"unknown_error","attribution":{"source":"provider","reason":"provider_overloaded","errorPhase":"stream","exceptionKind":"api_call","providerId":"zai-standard-api","modelId":"GLM-5.3","providerKind":"openai-compatible","transport":"sse","statusCode":529,"providerErrorCode":"overloaded_error","retryable":true,"highspeedCardId":null},"code":"overloaded_error","message":"Overloaded","detail":"Turn execution failed\nprovider=zai-standard-api provider_code=overloaded_error model=GLM-5.3 request=22e65d98-8583-4389-881b-99137a0f822d reason=provider_overloaded status=529 retryable=true","underlyingErrorMessage":"Overloaded"},"turnPhase":"processing_input","inputId":"in1"},"seq":12,"sessionId":"sess_ec311c18-b9f2-4ac0-a8c6-1e7c6dbc1d17","timestamp":1793152677588,"traceId":"ffde3fc2-3716-4d03-a58e-0c3edeb8735d","turnId":"turn_f252b2d8-59e3-49f0-acfb-abb217078428","type":"turn.failed"}}`
	zcodeTurnFailedRateLimited = `{"method":"session/event","params":{"deliveryKind":"desktop-continuous","eventId":"21dfd834-9ab3-4b10-878f-288a89e92937","payload":{"error":{"type":"unknown_error","attribution":{"source":"provider","reason":"rate_limited","errorPhase":"stream","exceptionKind":"api_call","providerId":"zai-standard-api","modelId":"GLM-5.3","providerKind":"openai-compatible","transport":"sse","statusCode":429,"providerErrorCode":"rate_limit_error","retryable":true,"highspeedCardId":null},"code":"rate_limit_error","message":"Rate limit reached for requests","detail":"Turn execution failed\nprovider=zai-standard-api provider_code=rate_limit_error model=GLM-5.3 request=54837ad6-7eb6-4e1e-846c-4817bb1eea3b reason=rate_limited status=429 retryable=true","underlyingErrorMessage":"Rate limit reached for requests"},"turnPhase":"processing_input","inputId":"in1"},"seq":12,"sessionId":"sess_8acf5b9f-fe93-45cd-9f78-e933f01ac946","timestamp":1793152682400,"traceId":"0061406e-d5e5-4179-923d-ed08998d7287","turnId":"turn_74835b0c-7544-4931-907c-aa60ecdbb578","type":"turn.failed"}}`
	zcodeTurnFailedConcurrency = `{"method":"session/event","params":{"deliveryKind":"desktop-continuous","eventId":"fb493473-05d8-465a-b649-fb08c370c23d","payload":{"error":{"type":"unknown_error","attribution":{"source":"provider","reason":"rate_limited","errorPhase":"stream","exceptionKind":"provider_business","providerId":"zai-standard-api","modelId":"GLM-5.3","providerKind":"openai-compatible","transport":"sse","statusCode":429,"providerErrorCode":"1302","retryable":true,"highspeedCardId":null},"code":"1302","message":"High concurrency usage of this API, please reduce concurrency or contact customer service to increase limits","detail":"Turn execution failed\nprovider=zai-standard-api provider_code=1302 model=GLM-5.3 request=2277e185-d035-48af-97b3-ddc9714f60d0 reason=rate_limited status=429 retryable=true","underlyingErrorMessage":"High concurrency usage of this API, please reduce concurrency or contact customer service to increase limits"},"turnPhase":"processing_input","inputId":"in1"},"seq":12,"sessionId":"sess_2012aadb-1036-4cf6-b1f4-6cef1c05d3d7","timestamp":1793152688520,"traceId":"6fb3a355-d004-4f37-83d2-a50a7d9a0a7a","turnId":"turn_244af721-1ad1-40eb-b3b6-59d61b2b3485","type":"turn.failed"}}`
)

// The ZCode transcripts (.tmp/retry-research/transcripts-zcode.md) store each
// failure as a model_network_status record and never as turn.failed, because
// every one of those turns recovered. These frames put the fields of a real
// record (.tmp/transcripts/zcode/errors_v2.jsonl) where the probe frames above
// carry them. The transcripts redact each request ID; the IDs here keep its
// shape: a UTC+8 stamp of 14 digits, then 30 lowercase hex digits.
const (
	zcodeTurnFailedRateLimit1302 = `{"method":"session/event","params":{"sessionId":"sess-1","seq":12,"type":"turn.failed","payload":{"error":{"type":"unknown_error","attribution":{"source":"provider","reason":"rate_limited","errorPhase":"stream","exceptionKind":"provider_business","providerId":"builtin:zai-coding-plan","modelId":"GLM-5.3","providerKind":"anthropic","transport":"sse","statusCode":429,"providerErrorCode":"1302","retryable":true},"code":"model_rate_limited","message":"[1302][Rate limit reached for requests][20260815234636a1b2c3d4e5f60718293a4b5c6d7e8f]","underlyingErrorMessage":"[1302][Rate limit reached for requests][20260815234636a1b2c3d4e5f60718293a4b5c6d7e8f]"},"turnPhase":"processing_input","inputId":"in1"}}}`
	zcodeTurnFailedTimeout524    = `{"method":"session/event","params":{"sessionId":"sess-1","seq":12,"type":"turn.failed","payload":{"error":{"type":"unknown_error","attribution":{"source":"provider","reason":"server_error","errorPhase":"stream","exceptionKind":"api_call","providerId":"builtin:zai-coding-plan","modelId":"GLM-5.3","providerKind":"anthropic","transport":"sse","statusCode":524,"retryable":true},"code":"model_request_failed","message":"Provider returned a server error."},"turnPhase":"processing_input","inputId":"in1"}}}`
	zcodeTurnFailedNetwork1234   = `{"method":"session/event","params":{"sessionId":"sess-1","seq":12,"type":"turn.failed","payload":{"error":{"type":"unknown_error","attribution":{"source":"provider","reason":"network_error","errorPhase":"stream","exceptionKind":"provider_business","providerId":"builtin:zai-coding-plan","modelId":"GLM-5.3","providerKind":"anthropic","transport":"sse","providerErrorCode":"1234","retryable":true},"code":"model_request_failed","message":"[1234][Internal network failure, error id: 20260815232705c4d5e6f708192a3b4c5d6e7f80910a2b, please try again later.][20260815232705c4d5e6f708192a3b4c5d6e7f80910a2b]","underlyingErrorMessage":"[1234][Internal network failure, error id: 20260815232705c4d5e6f708192a3b4c5d6e7f80910a2b, please try again later.][20260815232705c4d5e6f708192a3b4c5d6e7f80910a2b]"},"turnPhase":"processing_input","inputId":"in1"}}}`
)

// Each turn.failed applies exactly one verdict, after the turn-end row. ZCode
// states `retryable` only inside the attribution.
func TestHandleZCodeOutput_TurnFailed_SchedulesAutoRetryOnlyWhenRetryable(t *testing.T) {
	t.Parallel()

	// The 1308 stamp reads 2026-10-28 12:06:00 UTC. The clock sits inside its
	// 5-hour window.
	insideWindow := time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		frame string
		// now is the agent's clock. Zero keeps insideWindow.
		now  time.Time
		want []string
	}{
		{"a 500 schedules a server-error retry", zcodeTurnFailed500, time.Time{}, []string{"turn_end", "schedule_server_error"}},
		{
			"a dated 1308 usage limit schedules a usage-limit retry at its stamp", zcodeTurnFailedUsage1308, time.Time{},
			[]string{"turn_end", "schedule_usage_limit@2026-10-28T12:06:01Z", "cancel_server_error"},
		},
		{
			// The probe ran at 01:58 UTC against a fake server whose stamp lies
			// ten hours ahead, outside the stated 5-hour window.
			"a 1308 stamp outside its window falls back to the window wording", zcodeTurnFailedUsage1308,
			time.UnixMilli(1793152685227).UTC(), []string{"turn_end", "schedule_server_error"},
		},
		{"a refused connection schedules a server-error retry", zcodeTurnFailedRefused, time.Time{}, []string{"turn_end", "schedule_server_error"}},
		{"a 529 overload schedules a server-error retry", zcodeTurnFailedOverloaded, time.Time{}, []string{"turn_end", "schedule_server_error"}},
		{"a retryable 429 schedules a server-error retry", zcodeTurnFailedRateLimited, time.Time{}, []string{"turn_end", "schedule_server_error"}},
		{"a retryable 1302 schedules a server-error retry", zcodeTurnFailedConcurrency, time.Time{}, []string{"turn_end", "schedule_server_error"}},
		{"the 1302 of the transcripts schedules a server-error retry", zcodeTurnFailedRateLimit1302, time.Time{}, []string{"turn_end", "schedule_server_error"}},
		{"the 524 of the transcripts schedules a server-error retry", zcodeTurnFailedTimeout524, time.Time{}, []string{"turn_end", "schedule_server_error"}},
		{"the 1234 of the transcripts schedules a server-error retry", zcodeTurnFailedNetwork1234, time.Time{}, []string{"turn_end", "schedule_server_error"}},
		{
			"provider not configured is never retryable",
			zcodeEventFrame(`{"error":{"type":"provider_not_configured","message":"no key","attribution":{"reason":"server_error","retryable":true}}}`),
			time.Time{}, []string{"turn_end", "cancel_server_error"},
		},
		{
			"provider not configured by code is never retryable",
			zcodeEventFrame(`{"error":{"type":"api_error","code":"provider_not_configured","attribution":{"reason":"server_error","retryable":true}}}`),
			time.Time{}, []string{"turn_end", "cancel_server_error"},
		},
		{
			"provider not configured by attribution is never retryable",
			zcodeEventFrame(`{"error":{"type":"unknown_error","code":"model_request_failed","message":"Provider returned a server error.","attribution":{"reason":"provider_not_configured","retryable":true}}}`),
			time.Time{}, []string{"turn_end", "cancel_server_error"},
		},
		{
			"a cancellation that the attribution states is not retryable",
			zcodeEventFrame(`{"error":{"type":"unknown_error","code":"model_request_failed","message":"Provider returned a server error.","attribution":{"reason":"cancelled","retryable":true}}}`),
			time.Time{}, []string{"turn_end", "cancel_server_error"},
		},
		{
			"an interrupt that the code states is not retryable",
			zcodeEventFrame(`{"error":{"type":"unknown_error","code":"interrupted","message":"Internal server error","attribution":{"reason":"server_error","retryable":true}}}`),
			time.Time{}, []string{"turn_end", "cancel_server_error"},
		},
		{
			"a quota stop with no reset cancels the server-error retry",
			zcodeTurnFailedQuotaStop,
			time.Time{}, []string{"turn_end", "cancel_server_error"},
		},
		{
			"a failure that ZCode calls not retryable is not retryable",
			zcodeEventFrame(`{"error":{"type":"unknown_error","code":"model_request_failed","message":"Bad request","attribution":{"reason":"invalid_request","statusCode":400,"retryable":false}}}`),
			time.Time{}, []string{"turn_end", "cancel_server_error"},
		},
		{
			"a transient reason without the retryable flag is not retryable",
			zcodeEventFrame(`{"error":{"type":"unknown_error","code":"model_request_failed","message":"Provider returned a server error.","attribution":{"reason":"server_error"}}}`),
			time.Time{}, []string{"turn_end", "cancel_server_error"},
		},
		{"no attribution", zcodeEventFrame(`{"error":{"type":"api_error","message":"?"}}`), time.Time{}, []string{"turn_end", "cancel_server_error"}},
		{"no error object", zcodeEventFrame(`{}`), time.Time{}, []string{"turn_end", "cancel_server_error"}},
		{"a malformed error object", zcodeEventFrame(`{"error":"boom"}`), time.Time{}, []string{"turn_end", "cancel_server_error"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now := tc.now
			if now.IsZero() {
				now = insideWindow
			}
			clock := testutil.NewQuartzMock(t)
			clock.Set(now).MustWait(testutil.DeadlineContext(t))
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgentWithClock(t, agent.NewProviderServices(sink), &zcodeRecordedStdin{}, clock)
			zcodePrepareCapturedTurn(t, a, tc.frame, 1)

			a.HandleOutput([]byte(tc.frame))

			a.Mu.Lock()
			turnActive := a.turnActive
			a.Mu.Unlock()
			assert.False(t, turnActive, "a failed turn always ends the turn")
			require.Equal(t, tc.want, providerkittest.TurnEndAndRetryOpNames(sink.Timeline()))
			require.Equal(t, 1, sink.MessageCount())
			assert.True(t, sink.Messages()[0].TurnEnd)
			if sink.AutoScheduleCount() > 0 {
				assert.Equal(t, sink.Messages()[0].Content, sink.LastAutoSchedule().SourcePayload, "the retry carries the turn-end frame")
			} else if tc.frame == zcodeTurnFailedQuotaStop {
				assert.Equal(t, "the turn ended on a usage limit that states no reset within a week", sink.LastAutoCancelReason())
			} else {
				assert.Equal(t, providerkit.TurnEndedWithoutRetryableFailure, sink.LastAutoCancelReason())
			}
		})
	}
}

// A failure that states a stop ends the turn's streamed text as interrupted,
// and every other failure ends it as an error.
func TestHandleZCodeOutput_TurnFailed_StatesAStopAsInterrupted(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		frame string
		want  agent.MessageCompletion
	}{
		{"a cancel in the type", zcodeEventFrame(`{"error":{"type":"cancelled","message":"Turn cancelled"}}`), agent.MessageCompletionInterrupted},
		{"an interrupt in the code", zcodeEventFrame(`{"error":{"type":"unknown_error","code":"interrupted","message":"Turn interrupted"}}`), agent.MessageCompletionInterrupted},
		{
			"a cancellation in the attribution",
			zcodeEventFrame(`{"error":{"type":"unknown_error","code":"model_request_failed","message":"Request aborted","attribution":{"reason":"cancelled","retryable":false}}}`),
			agent.MessageCompletionInterrupted,
		},
		{"a server error", zcodeTurnFailed500, agent.MessageCompletionError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			zcodePrepareCapturedTurn(t, a, tc.frame, 1)
			metadata := zcodeCapturedFrameMetadata(t, tc.frame)
			a.Mu.Lock()
			a.lastSeq = metadata.Seq - 2
			a.Mu.Unlock()
			a.HandleOutput(zcodeTurnEventLineForSession(t, metadata.SessionID, metadata.Seq-1, metadata.TurnID, contracts.ZCodeEventModelStreaming, `{"kind":"text_delta","assistantMessageId":"message","delta":"Hello"}`))

			a.HandleOutput([]byte(tc.frame))

			require.Equal(t, 2, sink.MessageCount(), "the streamed text, then the turn end")
			var assembled map[string]string
			require.NoError(t, json.Unmarshal(sink.Messages()[0].Content, &assembled))
			assert.Equal(t, string(tc.want), assembled[contracts.AssembledMessageFieldCompletion])
			assert.True(t, sink.Messages()[1].TurnEnd)
		})
	}
}

// A failure that ends a turn LeapMux asked to stop is not retried.
func TestHandleZCodeOutput_TurnFailed_AfterAStopIsNotRetryable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		stop func(a *Agent)
	}{
		{"an interrupt that waits for its reply", func(a *Agent) { a.beginZCodeStop() }},
		{"an interrupt that ZCode accepted", func(a *Agent) {
			a.Mu.Lock()
			a.stopWindow.open(time.Now(), agent.StopContext{}, a.foregroundZCodeTurnLocked().id, a.foregroundZCodeTurnLocked().generation)
			a.armStoppedZCodeTurnLocked()
			a.Mu.Unlock()
		}},
		{"the agent's own stop", func(a *Agent) { a.NoteIntentionalStop() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			zcodePrepareCapturedTurn(t, a, zcodeTurnFailed500, 1)
			tc.stop(a)

			a.HandleOutput([]byte(zcodeTurnFailed500))

			require.Equal(t, []string{"turn_end", "cancel_server_error"}, providerkittest.TurnEndAndRetryOpNames(sink.Timeline()))
			assert.Equal(t, providerkit.TurnEndedWithoutRetryableFailure, sink.LastAutoCancelReason())
		})
	}
}

// zcodeEventFrame wraps a turn.failed payload in a session/event line of the
// test agent's session.
func zcodeEventFrame(payload string) string {
	return `{"method":"session/event","params":{"sessionId":"sess-1","seq":3,"type":"turn.failed","payload":` + payload + `}}`
}

// zcodeTurnFailedQuotaStop is a quota stop that states no reset: the class that
// ZCode gives every GLM quota code (rate_limited, not retryable). The message is
// constructed; the attribution follows the 1308 probe frame.
var zcodeTurnFailedQuotaStop = zcodeEventFrame(`{"error":{"type":"unknown_error","code":"1310","message":"Your quota is used up","attribution":{"reason":"rate_limited","statusCode":429,"providerErrorCode":"1310","retryable":false}}}`)

// zcodeTurnFailure reads ZCode's attribution and the vendor's text in the
// order of the classifier recipe.
func TestZCodeTurnFailure(t *testing.T) {
	t.Parallel()
	observedAt := time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC)
	decode := func(t *testing.T, frame string) zcodeTurnFailed {
		t.Helper()
		var line struct {
			Params struct {
				Payload zcodeTurnFailed `json:"payload"`
			} `json:"params"`
		}
		require.NoError(t, json.Unmarshal([]byte(frame), &line))
		return line.Params.Payload
	}
	for _, tc := range []struct {
		name    string
		frame   string
		kind    providerkit.TurnFailureKind
		resetAt time.Time
	}{
		{"a 500 is transient", zcodeTurnFailed500, providerkit.TurnFailureTransient, time.Time{}},
		{"a dated 1308 lifts at its stamp", zcodeTurnFailedUsage1308, providerkit.TurnFailureUsageLimited, time.Date(2026, 10, 28, 12, 6, 1, 0, time.UTC)},
		{"a refused connection is transient", zcodeTurnFailedRefused, providerkit.TurnFailureTransient, time.Time{}},
		{"a 529 is transient", zcodeTurnFailedOverloaded, providerkit.TurnFailureTransient, time.Time{}},
		{"a retryable 429 is a rate limit", zcodeTurnFailedRateLimited, providerkit.TurnFailureRateLimited, time.Time{}},
		{"a retryable 1302 is a rate limit", zcodeTurnFailedConcurrency, providerkit.TurnFailureRateLimited, time.Time{}},
		{"the 1302 of the transcripts is a rate limit", zcodeTurnFailedRateLimit1302, providerkit.TurnFailureRateLimited, time.Time{}},
		{"the 524 of the transcripts is transient", zcodeTurnFailedTimeout524, providerkit.TurnFailureTransient, time.Time{}},
		{"the 1234 of the transcripts is transient", zcodeTurnFailedNetwork1234, providerkit.TurnFailureTransient, time.Time{}},
		{
			// The underlying message carries the vendor's sentence when ZCode
			// replaces the message with its own summary.
			"a billing sentence in the underlying message is a billing block",
			zcodeEventFrame(`{"error":{"type":"unknown_error","code":"model_request_failed","message":"Provider returned a server error.","underlyingErrorMessage":"Your credit balance is too low to access the Anthropic API.","attribution":{"reason":"server_error","statusCode":500,"retryable":true}}}`),
			providerkit.TurnFailureBillingBlocked, time.Time{},
		},
		{
			"a quota stop with no reset is a usage limit",
			zcodeTurnFailedQuotaStop,
			providerkit.TurnFailureUsageLimited, time.Time{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			failure := zcodeTurnFailure(decode(t, tc.frame), false, observedAt)
			assert.Equal(t, tc.kind, failure.Kind)
			assert.True(t, tc.resetAt.Equal(failure.ResetAt), "reset %v, want %v", failure.ResetAt, tc.resetAt)
		})
	}
	t.Run("a requested stop is not retryable", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, providerkit.TurnFailureNotRetryable, zcodeTurnFailure(decode(t, zcodeTurnFailed500), true, observedAt).Kind)
	})
}

type zcodeCapturedIdentity struct {
	SessionID string `json:"sessionId"`
	TurnID    string `json:"turnId"`
	Seq       int64  `json:"seq"`
}

func zcodeCapturedFrameMetadata(t *testing.T, line string) zcodeCapturedIdentity {
	t.Helper()
	var frame struct {
		Params zcodeCapturedIdentity `json:"params"`
	}
	require.NoError(t, json.Unmarshal([]byte(line), &frame))
	return frame.Params
}

func zcodePrepareCapturedTurn(t *testing.T, a *Agent, line string, preceding int64) {
	t.Helper()
	frame := zcodeCapturedFrameMetadata(t, line)
	a.Mu.Lock()
	a.sessionID = frame.SessionID
	a.Mu.Unlock()
	a.sink.AdoptNativeSession(frame.SessionID)
	a.HandleOutput(zcodeTurnEventLineForSession(t, frame.SessionID, 0, frame.TurnID, contracts.ZCodeEventTurnStarted, `{"inputId":"in1"}`))
	a.Mu.Lock()
	a.lastSeq = frame.Seq - preceding
	a.Mu.Unlock()
}

func TestZCodeRetainedRetryRejectsANewerForeground(t *testing.T) {
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	zcodePrepareCapturedTurn(t, a, zcodeTurnFailed500, 1)
	a.PublishTurnActive()
	sink.PersistErr = assert.AnError
	a.HandleOutput([]byte(zcodeTurnFailed500))
	require.True(t, agent.OutputQueueFor(a.sink).HasPending())
	metadata := zcodeCapturedFrameMetadata(t, zcodeTurnFailed500)
	a.HandleOutput(zcodeTurnEventLineForSession(t, metadata.SessionID, metadata.Seq+1, "new-turn", contracts.ZCodeEventTurnStarted, `{"inputId":"new-input"}`))
	sink.PersistErr = nil
	require.NoError(t, agent.FlushPendingOutput(a.sink))
	assert.Zero(t, sink.AutoScheduleCount())
	// The fake records stale calls too; the worker accepts the newest sequence.
	states, sequences := sink.TurnStates(), sink.TurnSeqs()
	var latest uint64
	var effective agent.TurnState
	for i, seq := range sequences {
		if seq > latest {
			latest, effective = seq, states[i]
		}
	}
	assert.True(t, effective.Active)
}

func TestZCodeIDLessEndPreservesANewerForeground(t *testing.T) {
	for _, identity := range []string{"old input", "unknown input", "no input", "duplicate input"} {
		t.Run(identity, func(t *testing.T) {
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			zcodePrepareCapturedTurn(t, a, zcodeTurnFailed500, 1)
			a.Mu.Lock()
			old := a.foregroundZCodeTurnLocked()
			a.stopWindow.open(a.Clock().Now(), agent.StopContext{}, old.id, old.generation)
			generation := a.stopWindow.generation
			a.Mu.Unlock()
			a.endStoppedZCodeTurn(generation)
			inputID := "new-input"
			if identity == "duplicate input" {
				inputID = "in1"
			}
			a.createProvisionalZCodeTurn(a.sessionID, inputID, 0)
			a.Mu.Lock()
			current := a.foregroundZCodeTurnLocked()
			a.Mu.Unlock()
			require.NotNil(t, current)
			before := providerkittest.TurnEndAndRetryOps(sink.Timeline())
			var frame map[string]any
			require.NoError(t, json.Unmarshal([]byte(zcodeTurnFailed500), &frame))
			payload := frame["params"].(map[string]any)["payload"].(map[string]any)
			if identity == "unknown input" {
				payload["inputId"] = "unknown"
			}
			if identity == "no input" {
				delete(payload, "inputId")
			}
			raw, err := json.Marshal(frame)
			require.NoError(t, err)
			a.HandleOutput(raw)
			a.Mu.Lock()
			got := a.foregroundZCodeTurnLocked()
			oldFinalized := old.finalized
			a.Mu.Unlock()
			assert.Same(t, current, got)
			assert.True(t, a.PublishTurnActive().Active)
			assert.Equal(t, identity == "old input", oldFinalized)
			assert.Equal(t, before, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			assert.Zero(t, sink.AutoScheduleCount())
			ends := zcodeNativeTurnEndRows(t, sink.Messages())
			if identity == "old input" {
				require.Len(t, ends, 1)
				assert.False(t, ends[0].TurnEnd)
			} else {
				assert.Empty(t, ends)
			}
		})
	}
}
