package muse

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The frames below come verbatim from the probe runs that
// .tmp/retry-research/copilot-muse.md summarizes (.tmp/probe/muse/runs), except
// the quota frame, which the research constructed from the MSP schema because no
// probe reached the quota class. A frame that a test composes says so.

// museRetryCase is one root turn end of a probe run.
type museRetryCase struct {
	name string
	// frame is the turn/completed frame that ends the turn.
	frame string
	// clockAt is the agent clock, which a frame without emittedAtMs reads.
	clockAt time.Time
	want    providerkit.TurnFailure
}

var museNotRetryable = providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}

func museTransient(kind string) providerkit.TurnFailure {
	return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Muse reported a retryable " + kind + " failure"}
}

// museQuotaFrame is the constructed quota failure of the research. Its reset is
// 1793161211000, Unix milliseconds.
const museQuotaFrame = `{"jsonrpc":"2.0","method":"turn/completed","params":{"sessionId":"01a18016-1998-76f1-b118-9866699d2565","viewCursor":"v4:01a18016-1998-76f1-b118-9866699d2565:9","sourceRange":{"stream":{"kind":"session","id":"01a18016-1998-76f1-b118-9866699d2565"},"first":{"id":"01a18016-1bd4-73f2-82d5-9de30fc0d01d","sequence":62},"last":{"id":"01a18016-1bd4-73f2-82d5-9de30fc0d01d","sequence":62}},"turnId":"01a18016-1a84-7763-9344-fce5c4df1272","terminal":"failed","reason":"usage limit reached","error":{"kind":"modelError","message":"usage limit reached","retryable":false,"quota":{"limitKind":"tierLimit","resetsAtMs":1793161211000}}}}`

func museRetryCases() []museRetryCase {
	// The quota frame states no emittedAtMs, so it reads the agent clock. The
	// clock stands at the emission of the probed 400 in the same session, about
	// three hours before the reset.
	quotaObservedAt := museUnixMilli(1793150295005)
	return []museRetryCase{
		{
			name:    "a quota failure schedules at its reset",
			frame:   museQuotaFrame,
			clockAt: quotaObservedAt,
			want: providerkit.TurnFailure{
				Kind: providerkit.TurnFailureUsageLimited, ResetAt: museUnixMilli(1793161211000),
				Cause: "Muse reported an exhausted tierLimit quota (the provider states when to retry)",
			},
		},
		{
			name:  "a 500 after the provider attempts is transient",
			frame: `{"jsonrpc":"2.0","method":"turn/completed","params":{"sessionId":"01a18014-aa0a-7ea1-9852-8bc110159315","viewCursor":"v4:01a18014-aa0a-7ea1-9852-8bc110159315:18","sourceRange":{"stream":{"kind":"session","id":"01a18014-aa0a-7ea1-9852-8bc110159315"},"first":{"id":"01a18014-d633-7642-865f-92a13cd641ec","sequence":80},"last":{"id":"01a18014-d633-7642-865f-92a13cd641ec","sequence":80}},"turnId":"01a18014-abb6-7342-b725-deb73957d6bb","terminal":"failed","reason":"API error 500: Internal Server Error (server_error) (after 10 provider attempts)","durationMs":10842,"error":{"kind":"modelError","message":"API error 500: Internal Server Error (server_error) (after 10 provider attempts)","retryable":true}},"emittedAtMs":1793150211651}`,
			want:  museTransient("modelError"),
		},
		{
			name:  "a 429 after the provider attempts is transient",
			frame: `{"jsonrpc":"2.0","method":"turn/completed","params":{"sessionId":"01a1801e-2372-7750-8451-e824930ab994","viewCursor":"v4:01a1801e-2372-7750-8451-e824930ab994:18","sourceRange":{"stream":{"kind":"session","id":"01a1801e-2372-7750-8451-e824930ab994"},"first":{"id":"01a1801e-4a14-7592-8704-816729b0ed7e","sequence":80},"last":{"id":"01a1801e-4a14-7592-8704-816729b0ed7e","sequence":80}},"turnId":"01a1801e-24a5-7afa-b4e4-5574db01cfd0","terminal":"failed","reason":"API error 429: Rate limit reached. (rate_limit_exceeded) (after 10 provider attempts)","durationMs":9573,"error":{"kind":"modelError","message":"API error 429: Rate limit reached. (rate_limit_exceeded) (after 10 provider attempts)","retryable":true}},"emittedAtMs":1793150831141}`,
			want:  museTransient("modelError"),
		},
		{
			name:  "a truncated stream after the attempts is transient",
			frame: `{"jsonrpc":"2.0","method":"turn/completed","params":{"sessionId":"01a1801e-54c0-7572-a700-6f47176e36bf","viewCursor":"v4:01a1801e-54c0-7572-a700-6f47176e36bf:28","sourceRange":{"stream":{"kind":"session","id":"01a1801e-54c0-7572-a700-6f47176e36bf"},"first":{"id":"01a1801f-586c-7af1-8ca1-9080b2944548","sequence":171},"last":{"id":"01a1801f-586c-7af1-8ca1-9080b2944548","sequence":171}},"turnId":"01a1801e-55c8-7f12-8137-7b1c425966fc","terminal":"failed","reason":"model failed after 10 attempts: stream ended before terminal event (response: resp_probe)","durationMs":66203,"timeToFirstTokenMs":264,"error":{"kind":"modelError","message":"model failed after 10 attempts: stream ended before terminal event (response: resp_probe)","retryable":true}},"emittedAtMs":1793150900342}`,
			want:  museTransient("modelError"),
		},
		{
			name:  "an invalid request is not retryable",
			frame: `{"jsonrpc":"2.0","method":"turn/completed","params":{"sessionId":"01a18016-1998-76f1-b118-9866699d2565","viewCursor":"v4:01a18016-1998-76f1-b118-9866699d2565:9","sourceRange":{"stream":{"kind":"session","id":"01a18016-1998-76f1-b118-9866699d2565"},"first":{"id":"01a18016-1bd4-73f2-82d5-9de30fc0d01d","sequence":62},"last":{"id":"01a18016-1bd4-73f2-82d5-9de30fc0d01d","sequence":62}},"turnId":"01a18016-1a84-7763-9344-fce5c4df1272","terminal":"failed","reason":"API error 400: The request is invalid. (invalid_request_error)","durationMs":328,"error":{"kind":"modelError","message":"API error 400: The request is invalid. (invalid_request_error)","retryable":false}},"emittedAtMs":1793150295005}`,
			want:  museNotRetryable,
		},
		{
			name:  "a rejected API key is not retryable",
			frame: `{"jsonrpc":"2.0","method":"turn/completed","params":{"sessionId":"01a18016-2026-7c20-8f40-ea2ecf8d7538","viewCursor":"v4:01a18016-2026-7c20-8f40-ea2ecf8d7538:9","sourceRange":{"stream":{"kind":"session","id":"01a18016-2026-7c20-8f40-ea2ecf8d7538"},"first":{"id":"01a18016-226d-7772-be76-cb1f6412d522","sequence":62},"last":{"id":"01a18016-226d-7772-be76-cb1f6412d522","sequence":62}},"turnId":"01a18016-211d-70df-88ac-1e24a2cfbdc4","terminal":"failed","reason":"your API key from META_API_KEY was rejected — update or unset it","durationMs":328,"error":{"kind":"authRequired","message":"your API key from META_API_KEY was rejected — update or unset it","retryable":false}},"emittedAtMs":1793150296700}`,
			want:  museNotRetryable,
		},
		{
			name:  "a required payment is not retryable",
			frame: `{"jsonrpc":"2.0","method":"turn/completed","params":{"sessionId":"01a18016-2d04-7ee3-9a1f-ee87a5fae22d","viewCursor":"v4:01a18016-2d04-7ee3-9a1f-ee87a5fae22d:9","sourceRange":{"stream":{"kind":"session","id":"01a18016-2d04-7ee3-9a1f-ee87a5fae22d"},"first":{"id":"01a18016-2f43-7b42-beb6-617854c9ca04","sequence":62},"last":{"id":"01a18016-2f43-7b42-beb6-617854c9ca04","sequence":62}},"turnId":"01a18016-2df9-7f4f-9f48-11eb9a23cc64","terminal":"failed","reason":"API error 402: Payment required. (payment_required)","durationMs":321,"error":{"kind":"modelError","message":"API error 402: Payment required. (payment_required)","retryable":false}},"emittedAtMs":1793150299984}`,
			want:  museNotRetryable,
		},
		{
			name:  "a forbidden request is not retryable",
			frame: `{"jsonrpc":"2.0","method":"turn/completed","params":{"sessionId":"01a18016-268d-7122-b601-be89253b98db","viewCursor":"v4:01a18016-268d-7122-b601-be89253b98db:9","sourceRange":{"stream":{"kind":"session","id":"01a18016-268d-7122-b601-be89253b98db"},"first":{"id":"01a18016-28bd-7112-b364-b51d9e9162ff","sequence":62},"last":{"id":"01a18016-28bd-7112-b364-b51d9e9162ff","sequence":62}},"turnId":"01a18016-277a-77f1-91f8-8fe7f1de51ca","terminal":"failed","reason":"API error 403: Forbidden. (permission_denied)","durationMs":316,"error":{"kind":"modelError","message":"API error 403: Forbidden. (permission_denied)","retryable":false}},"emittedAtMs":1793150298314}`,
			want:  museNotRetryable,
		},
		{
			// Muse does not retry a stream that the server broke, and states so.
			name:  "a broken stream that Muse calls final is not retryable",
			frame: `{"jsonrpc":"2.0","method":"turn/completed","params":{"sessionId":"01a18015-3264-7f71-ba0f-3f7b1ae0b141","viewCursor":"v4:01a18015-3264-7f71-ba0f-3f7b1ae0b141:10","sourceRange":{"stream":{"kind":"session","id":"01a18015-3264-7f71-ba0f-3f7b1ae0b141"},"first":{"id":"01a18015-34a5-7390-9f38-d4e2057e99b7","sequence":63},"last":{"id":"01a18015-34a5-7390-9f38-d4e2057e99b7","sequence":63}},"turnId":"01a18015-334e-7343-abde-096ac37c2cac","terminal":"failed","reason":"server_error: The stream broke. (response: resp_probe)","durationMs":336,"timeToFirstTokenMs":269,"error":{"kind":"modelError","message":"server_error: The stream broke. (response: resp_probe)","retryable":false}},"emittedAtMs":1793150235830}`,
			want:  museNotRetryable,
		},
		{
			name:  "a plan limit without a quota detail is not retryable",
			frame: `{"jsonrpc":"2.0","method":"turn/completed","params":{"sessionId":"01a18020-6b8b-7b30-b35b-c8077c9726c4","viewCursor":"v4:01a18020-6b8b-7b30-b35b-c8077c9726c4:10","sourceRange":{"stream":{"kind":"session","id":"01a18020-6b8b-7b30-b35b-c8077c9726c4"},"first":{"id":"01a18020-6de5-7112-bb13-8bf6676e4dc0","sequence":63},"last":{"id":"01a18020-6de5-7112-bb13-8bf6676e4dc0","sequence":63}},"turnId":"01a18020-6c8f-7e72-8ad0-958726bda66d","terminal":"failed","reason":"tier_limit: You have reached your plan limit. (response: resp_probe)","durationMs":334,"timeToFirstTokenMs":265,"error":{"kind":"modelError","message":"tier_limit: You have reached your plan limit. (response: resp_probe)","retryable":false}},"emittedAtMs":1793150971376}`,
			want:  museNotRetryable,
		},
		{
			name:  "a tier usage limit without a quota detail is not retryable",
			frame: `{"jsonrpc":"2.0","method":"turn/completed","params":{"sessionId":"01a18020-7211-7c00-a90c-cf2272358484","viewCursor":"v4:01a18020-7211-7c00-a90c-cf2272358484:10","sourceRange":{"stream":{"kind":"session","id":"01a18020-7211-7c00-a90c-cf2272358484"},"first":{"id":"01a18020-747f-7413-87b9-42337d2707ad","sequence":63},"last":{"id":"01a18020-747f-7413-87b9-42337d2707ad","sequence":63}},"turnId":"01a18020-7313-7734-bf98-22b616e9d1c5","terminal":"failed","reason":"tier_limit: Usage limit reached. (response: resp_probe)","durationMs":357,"timeToFirstTokenMs":277,"error":{"kind":"modelError","message":"tier_limit: Usage limit reached. (response: resp_probe)","retryable":false}},"emittedAtMs":1793150973066}`,
			want:  museNotRetryable,
		},
		{
			name:  "an exceeded usage limit without a quota detail is not retryable",
			frame: `{"jsonrpc":"2.0","method":"turn/completed","params":{"sessionId":"01a18020-7928-70a1-a5bb-53fe089c6996","viewCursor":"v4:01a18020-7928-70a1-a5bb-53fe089c6996:10","sourceRange":{"stream":{"kind":"session","id":"01a18020-7928-70a1-a5bb-53fe089c6996"},"first":{"id":"01a18020-7b84-7f30-a4fe-f29bf9fcefd7","sequence":63},"last":{"id":"01a18020-7b84-7f30-a4fe-f29bf9fcefd7","sequence":63}},"turnId":"01a18020-7a74-77f0-a885-b2599077645a","terminal":"failed","reason":"usage_limit_exceeded: Usage limit reached. (response: resp_probe)","durationMs":260,"timeToFirstTokenMs":208,"error":{"kind":"modelError","message":"usage_limit_exceeded: Usage limit reached. (response: resp_probe)","retryable":false}},"emittedAtMs":1793150974861}`,
			want:  museNotRetryable,
		},
		{
			name:  "a completed turn is not retryable",
			frame: `{"jsonrpc":"2.0","method":"turn/completed","params":{"sessionId":"01a18012-e3dd-7292-a4e3-8a1aafb82cc6","viewCursor":"v4:01a18012-e3dd-7292-a4e3-8a1aafb82cc6:17","sourceRange":{"stream":{"kind":"session","id":"01a18012-e3dd-7292-a4e3-8a1aafb82cc6"},"first":{"id":"01a18012-e9fe-7571-bc11-45c3deb3d5b3","sequence":81},"last":{"id":"01a18012-e9fe-7571-bc11-45c3deb3d5b3","sequence":81}},"turnId":"01a18012-e4e7-7685-b6d2-911287de49f7","terminal":"completed","durationMs":1252,"timeToFirstTokenMs":402},"emittedAtMs":1793150085642}`,
			want:  museNotRetryable,
		},
	}
}

// museProbe returns the probe case of the given name.
func museProbe(t *testing.T, name string) museRetryCase {
	t.Helper()
	for _, tc := range museRetryCases() {
		if tc.name == name {
			return tc
		}
	}
	require.FailNow(t, "no probe case", name)
	return museRetryCase{}
}

// museFrameIdentity reads the session and the turn of a turn frame.
func museFrameIdentity(t *testing.T, frame string) (string, string) {
	t.Helper()
	var envelope struct {
		Params struct {
			SessionID string `json:"sessionId"`
			TurnID    string `json:"turnId"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal([]byte(frame), &envelope))
	return envelope.Params.SessionID, envelope.Params.TurnID
}

// newMuseRetryAgent builds the agent of testAgent for one root session, on a mock
// clock that stands at clockAt. The session has a running turn.
func newMuseRetryAgent(t *testing.T, sessionID, turnID string, clockAt time.Time) (*Agent, *agenttest.Sink) {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	if !clockAt.IsZero() {
		clock.Set(clockAt)
	}
	a, sink := testAgentWithClock(t, clock)
	state := a.sessions["session"]
	delete(a.sessions, "session")
	state.log = newNativeLog(sessionID)
	a.sessionID = sessionID
	a.sessions[sessionID] = state
	a.startTurn(sessionID, turnID)
	return a, sink
}

func TestMuseStopIntentPrecedesConcurrentFailure(t *testing.T) {
	t.Parallel()
	for _, refused := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "refused"}[refused], func(t *testing.T) {
			t.Parallel()
			failure := museProbe(t, "a 500 after the provider attempts is transient")
			session, turn := museFrameIdentity(t, failure.frame)
			a, sink := newMuseRetryAgent(t, session, turn, time.Time{})
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
				assert.Equal(t, methodTurnInterrupt, method)
				a.HandleOutput([]byte(failure.frame))
				if refused {
					return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"interruption refused"}`)}
				}
				var params map[string]any
				require.NoError(t, json.Unmarshal(raw, &params))
				result, err := json.Marshal(map[string]any{"commandId": params["commandId"], "status": "accepted"})
				require.NoError(t, err)
				return agenttest.RPCReply{Result: result}
			}})
			err := a.Interrupt(agent.StopContext{})
			if refused {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Zero(t, sink.AutoScheduleCount())
			assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			a.startTurn(session, "next-turn")
			feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": session, "turnId": "next-turn", "terminal": contracts.MuseTurnOutcomeFailed,
				"error": map[string]any{"kind": "server", "message": "service unavailable", "retryable": true}})
			assert.Equal(t, 1, sink.AutoScheduleCount(), "the next turn was not stopped")
		})
	}
}

// Each probed completion gives the verdict that the MSP fields and the vendor
// text imply, at the emission time of the frame.
func TestMuseTurnVerdict(t *testing.T) {
	t.Parallel()
	for _, tc := range museRetryCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			session, turn := museFrameIdentity(t, tc.frame)
			a, _ := newMuseRetryAgent(t, session, turn, tc.clockAt)
			var frame completionFrame
			require.NoError(t, json.Unmarshal([]byte(tc.frame), &frame))
			assert.Equal(t, tc.want, museTurnVerdict(frame.Params, a.completionObservedAt(frame)))
		})
	}
}

// The emission stamp limits the reset when it is a positive integer. Any other
// stamp reads the agent clock.
func TestMuseCompletionObservedAt(t *testing.T) {
	t.Parallel()
	clockAt := museUnixMilli(1793150000000)
	for _, tc := range []struct {
		name  string
		stamp string
		want  time.Time
	}{
		{name: "a positive integer is the emission", stamp: `1793150211651`, want: museUnixMilli(1793150211651)},
		{name: "an absent stamp reads the clock", stamp: ``, want: clockAt},
		{name: "a null stamp reads the clock", stamp: `null`, want: clockAt},
		{name: "a zero stamp reads the clock", stamp: `0`, want: clockAt},
		{name: "a negative stamp reads the clock", stamp: `-5`, want: clockAt},
		{name: "a fractional stamp reads the clock", stamp: `1793150211651.5`, want: clockAt},
		{name: "a quoted stamp reads the clock", stamp: `"1793150211651"`, want: clockAt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, _ := newMuseRetryAgent(t, "session", "turn", clockAt)
			got := a.completionObservedAt(completionFrame{EmittedAtMs: json.RawMessage(tc.stamp)})
			assert.True(t, tc.want.Equal(got), "got %s, want %s", got, tc.want)
		})
	}
}

// The real dispatch writes the turn-end row and then exactly the retry
// operations of the verdict, with the completion as the payload.
func TestMuseTurnEndAppliesTheVerdict(t *testing.T) {
	t.Parallel()
	for _, tc := range museRetryCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			session, turn := museFrameIdentity(t, tc.frame)
			a, sink := newMuseRetryAgent(t, session, turn, tc.clockAt)
			a.HandleOutput([]byte(tc.frame))
			assert.Equal(t, providerkittest.TurnEndThen(tc.want), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			if sink.AutoScheduleCount() > 0 {
				assert.Equal(t, []byte(tc.frame), sink.LastAutoSchedule().SourcePayload)
			}
		})
	}
}

// The literal operations of the verdicts that the probes reach.
func TestMuseTurnEndRetryOperations(t *testing.T) {
	t.Parallel()
	usageKind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT
	serverKind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR
	for _, tc := range []struct {
		probe museRetryCase
		want  []agenttest.TimelineEntry
	}{
		{probe: museProbe(t, "a quota failure schedules at its reset"), want: []agenttest.TimelineEntry{
			{Op: "turn_end"},
			{Op: "schedule_usage_limit", Kind: usageKind, ResetAt: museUnixMilli(1793161211000)},
			{Op: "cancel_server_error", Kind: serverKind, Reason: "the turn ended on a usage limit that lifts at a stated time"},
		}},
		{probe: museProbe(t, "a 500 after the provider attempts is transient"), want: []agenttest.TimelineEntry{
			{Op: "turn_end"},
			{Op: "schedule_server_error", Kind: serverKind},
		}},
		{probe: museProbe(t, "an invalid request is not retryable"), want: []agenttest.TimelineEntry{
			{Op: "turn_end"},
			{Op: "cancel_server_error", Kind: serverKind, Reason: providerkit.TurnEndedWithoutRetryableFailure},
		}},
	} {
		t.Run(tc.probe.name, func(t *testing.T) {
			t.Parallel()
			session, turn := museFrameIdentity(t, tc.probe.frame)
			a, sink := newMuseRetryAgent(t, session, turn, tc.probe.clockAt)
			a.HandleOutput([]byte(tc.probe.frame))
			assert.Equal(t, tc.want, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

// museCompletionFrame composes a root turn/completed for the edge cases that no
// probe reached. A zero emittedAtMs leaves the field out.
func museCompletionFrame(t *testing.T, params map[string]any, emittedAtMs int64) string {
	t.Helper()
	frame := map[string]any{"jsonrpc": "2.0", "method": contracts.MuseMethodTurnCompleted, "params": params}
	if emittedAtMs != 0 {
		frame["emittedAtMs"] = emittedAtMs
	}
	raw, err := json.Marshal(frame)
	require.NoError(t, err)
	return string(raw)
}

// museFailedParams composes the params of a failed root turn of session.
func museFailedParams(turnError any) map[string]any {
	return map[string]any{"sessionId": "session", "turnId": "turn", "terminal": contracts.MuseTurnOutcomeFailed, "error": turnError}
}

func TestMuseTurnEndEdgeCases(t *testing.T) {
	t.Parallel()
	// The emission of the composed frames, three hours before the quota reset.
	const emittedAtMs = 1793150295005
	quotaError := func(resetsAtMs any) map[string]any {
		return map[string]any{"kind": "modelError", "message": "usage limit reached", "retryable": false, "quota": map[string]any{"limitKind": "tierLimit", "resetsAtMs": resetsAtMs}}
	}
	// A quota without a usable reset states no window, so it keeps any pending
	// usage-limit retry and cancels the server-error one.
	quotaFallback := providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowUnstated, Cause: "Muse reported an exhausted tierLimit quota"}
	for _, tc := range []struct {
		name    string
		params  map[string]any
		emitted int64
		clockAt time.Time
		want    providerkit.TurnFailure
	}{
		{
			name:   "a quota reset in the past falls back to the window rule",
			params: museFailedParams(quotaError(emittedAtMs - 1000)), emitted: emittedAtMs,
			want: quotaFallback,
		},
		{
			name:   "a quota reset at the emission falls back to the window rule",
			params: museFailedParams(quotaError(emittedAtMs)), emitted: emittedAtMs,
			want: quotaFallback,
		},
		{
			name:   "a quota reset more than 31 days ahead falls back to the window rule",
			params: museFailedParams(quotaError(emittedAtMs + (31*24*time.Hour + time.Second).Milliseconds())), emitted: emittedAtMs,
			want: quotaFallback,
		},
		{
			name:   "a quota reset of zero falls back to the window rule",
			params: museFailedParams(quotaError(0)), emitted: emittedAtMs,
			want: quotaFallback,
		},
		{
			name:   "a quota reset that is not a number falls back to the window rule",
			params: museFailedParams(quotaError("soon")), emitted: emittedAtMs,
			want: quotaFallback,
		},
		{
			name:   "a quota without a reset falls back to the window rule",
			params: museFailedParams(map[string]any{"kind": "modelError", "message": "usage limit reached", "retryable": false, "quota": map[string]any{"limitKind": "tierLimit"}}), emitted: emittedAtMs,
			want: quotaFallback,
		},
		{
			name:   "a quota wins over the retryable flag",
			params: museFailedParams(map[string]any{"kind": "modelError", "message": "usage limit reached", "retryable": true, "quota": map[string]any{"limitKind": "tierLimit", "resetsAtMs": emittedAtMs + 60_000}}), emitted: emittedAtMs,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: museUnixMilli(emittedAtMs + 60_000), Cause: "Muse reported an exhausted tierLimit quota (the provider states when to retry)"},
		},
		{
			// The frame states no emission, so the agent clock limits the reset.
			// The wall clock is past this reset, so only the agent clock admits it.
			name:    "a frame without emittedAtMs reads the agent clock",
			params:  museFailedParams(quotaError(emittedAtMs + 60_000)),
			clockAt: museUnixMilli(emittedAtMs),
			want:    providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: museUnixMilli(emittedAtMs + 60_000), Cause: "Muse reported an exhausted tierLimit quota (the provider states when to retry)"},
		},
		{
			name:    "a frame without emittedAtMs falls back when the agent clock is past the reset",
			params:  museFailedParams(quotaError(emittedAtMs + 60_000)),
			clockAt: museUnixMilli(emittedAtMs + 120_000),
			want:    quotaFallback,
		},
		{
			name:   "a cancelled turn is not retryable",
			params: map[string]any{"sessionId": "session", "turnId": "turn", "terminal": contracts.MuseTurnOutcomeCancelled, "error": map[string]any{"kind": "modelError", "message": "API error 500", "retryable": true}}, emitted: emittedAtMs,
			want: museNotRetryable,
		},
		{
			name:   "a host death that states only a reason is not retryable",
			params: map[string]any{"sessionId": "session", "turnId": "turn", "terminal": contracts.MuseTurnOutcomeFailed, "reason": "the host exited"}, emitted: emittedAtMs,
			want: museNotRetryable,
		},
		{
			name:   "an error that is not an object is not retryable",
			params: museFailedParams("API error 500"), emitted: emittedAtMs,
			want: museNotRetryable,
		},
		{
			name:   "an unknown outcome is not retryable",
			params: map[string]any{"sessionId": "session", "turnId": "turn", "terminal": "futureFinal", "error": map[string]any{"kind": "modelError", "message": "API error 500", "retryable": true}}, emitted: emittedAtMs,
			want: museNotRetryable,
		},
		{
			name:   "a definitive vendor billing text wins over the retryable flag",
			params: museFailedParams(map[string]any{"kind": "modelError", "message": `API error 429: {"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`, "retryable": true}), emitted: emittedAtMs,
			want: museVendorVerdict(t, `API error 429: {"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`, emittedAtMs, providerkit.TurnFailureBillingBlocked),
		},
		{
			name:   "an indicative vendor text decides a failure that Muse calls final",
			params: museFailedParams(map[string]any{"kind": "modelError", "message": `API error 529: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, "retryable": false}), emitted: emittedAtMs,
			want: museVendorVerdict(t, `API error 529: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, emittedAtMs, providerkit.TurnFailureTransient),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newMuseRetryAgent(t, "session", "turn", tc.clockAt)
			a.HandleOutput([]byte(museCompletionFrame(t, tc.params, tc.emitted)))
			assert.Equal(t, providerkittest.TurnEndThen(tc.want), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

// museVendorVerdict returns the vendor parser's verdict for text, after it
// checks that the parser gives the kind that the case relies on.
func museVendorVerdict(t *testing.T, text string, observedAtMs int64, kind providerkit.TurnFailureKind) providerkit.TurnFailure {
	t.Helper()
	verdict, ok := modelvendor.ClassifyError(text, museUnixMilli(observedAtMs))
	require.True(t, ok, "the vendor parser reads %q", text)
	require.Equal(t, kind, verdict.Failure.Kind, "the vendor parser's kind for %q", text)
	return verdict.Failure
}

// A child session's failure decides nothing: only the root turn end applies a
// verdict.
func TestMuseChildTurnEndAppliesNoVerdict(t *testing.T) {
	t.Parallel()
	a, root := testAgent(t)
	state, child := nativeHistoryChild(t, a, root)
	// A failed view prefix observes the native completion at once, as
	// TestFailedChildProjectionStillObservesNativeTurnCompletion states.
	state.childViewPrefix = childViewPrefixFailed
	state.turnID = "turn"
	a.viewWork.CloseAndWait()
	a.HandleOutput([]byte(museCompletionFrame(t, map[string]any{
		"sessionId": "child", "turnId": "turn", "terminal": contracts.MuseTurnOutcomeFailed,
		"error": map[string]any{"kind": "modelError", "message": "API error 500: Internal Server Error (server_error) (after 10 provider attempts)", "retryable": true},
	}, 1793150211651)))
	require.Equal(t, turnCompletionObserved, state.completed["turn"], "the child's turn ended")
	assert.Empty(t, museRetryOps(child.Timeline()), "the child applies no verdict")
	assert.Empty(t, museRetryOps(root.Timeline()), "the child's failure decides nothing for the root")
}

// museRetryOps returns the retry operations of a timeline, without its rows.
func museRetryOps(timeline []agenttest.TimelineEntry) []agenttest.TimelineEntry {
	var ops []agenttest.TimelineEntry
	for _, entry := range providerkittest.TurnEndAndRetryOps(timeline) {
		if entry.Op != "turn_end" {
			ops = append(ops, entry)
		}
	}
	return ops
}

// A replayed completion after a failed turn-end write decides once, and the
// verdict follows the turn-end row that the replay writes.
func TestMuseReplayedCompletionDecidesOnce(t *testing.T) {
	t.Parallel()
	failure := museProbe(t, "a 500 after the provider attempts is transient")
	session, turn := museFrameIdentity(t, failure.frame)
	a, recording := newMuseRetryAgent(t, session, turn, time.Time{})
	sink := &finalizationSink{Sink: recording, failEnd: 1}
	a.sink = agent.NewProviderServices(sink)
	a.sessions[session].sink = a.sink
	a.HandleOutput([]byte(failure.frame))
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(recording.Timeline()), "a turn end that failed to write applies no verdict yet")
	a.HandleOutput([]byte(failure.frame))
	a.HandleOutput([]byte(failure.frame))
	assert.Equal(t, providerkittest.TurnEndThen(failure.want), providerkittest.TurnEndAndRetryOps(recording.Timeline()))
}

func TestMuseStoppedFinalizationKeepsItsVerdictAfterANewerTurnStarts(t *testing.T) {
	t.Parallel()
	failure := museProbe(t, "a 500 after the provider attempts is transient")
	session, turn := museFrameIdentity(t, failure.frame)
	a, recording := newMuseRetryAgent(t, session, turn, time.Time{})
	sink := &finalizationSink{Sink: recording, failEnd: 1}
	a.sink = agent.NewProviderServices(sink)
	a.sessions[session].sink = a.sink
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(_ string, raw json.RawMessage) agenttest.RPCReply {
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		result, err := json.Marshal(map[string]any{"commandId": params["commandId"], "status": "accepted"})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: result}
	}})
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	a.HandleOutput([]byte(failure.frame))
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(recording.Timeline()))
	a.startTurn(session, "next-turn")
	a.HandleOutput([]byte(failure.frame))
	a.HandleOutput([]byte(failure.frame))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(recording.Timeline()))
	feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": session, "turnId": "next-turn", "terminal": contracts.MuseTurnOutcomeFailed,
		"error": map[string]any{"kind": "server", "message": "service unavailable", "retryable": true}})
	assert.Equal(t, 1, recording.AutoScheduleCount())
}

// A replayed completion of a turn that already ended decides nothing.
func TestMuseReplayedCompletionAfterTheTurnEndDecidesNothing(t *testing.T) {
	t.Parallel()
	failure := museProbe(t, "a 500 after the provider attempts is transient")
	session, turn := museFrameIdentity(t, failure.frame)
	a, sink := newMuseRetryAgent(t, session, turn, time.Time{})
	a.HandleOutput([]byte(failure.frame))
	a.HandleOutput([]byte(failure.frame))
	// A paged replay omits jsonrpc and emittedAtMs, so its bytes differ.
	var paged map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(failure.frame), &paged))
	delete(paged, "jsonrpc")
	delete(paged, "emittedAtMs")
	raw, err := json.Marshal(paged)
	require.NoError(t, err)
	a.HandleOutput(raw)
	assert.Equal(t, providerkittest.TurnEndThen(failure.want), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// The model-level retry notice of Muse becomes a notification row in the API
// retry slot, never an unknown message row.
func TestMuseModelRetryNoticeIsANotification(t *testing.T) {
	t.Parallel()
	const (
		started     = `{"jsonrpc":"2.0","method":"turn/started","params":{"sessionId":"01a18014-aa0a-7ea1-9852-8bc110159315","viewCursor":"v4:01a18014-aa0a-7ea1-9852-8bc110159315:3","sourceRange":{"stream":{"kind":"session","id":"01a18014-aa0a-7ea1-9852-8bc110159315"},"first":{"id":"01a18014-abdb-7193-9c69-c7b8b004c445","sequence":17},"last":{"id":"01a18014-abdb-7193-9c69-c7b8b004c445","sequence":17}},"turnId":"01a18014-abb6-7342-b725-deb73957d6bb","commandId":"01a18014-abb6-7342-b725-deb73957d6bb"},"emittedAtMs":1793150200802}`
		firstRetry  = `{"jsonrpc":"2.0","method":"turn/modelRetryScheduled","params":{"sessionId":"01a18014-aa0a-7ea1-9852-8bc110159315","viewCursor":"v4:01a18014-aa0a-7ea1-9852-8bc110159315:7","sourceRange":{"stream":{"kind":"session","id":"01a18014-aa0a-7ea1-9852-8bc110159315"},"first":{"id":"01a18014-ad28-7350-a33c-4e6ecdb8d22d","sequence":53},"last":{"id":"01a18014-ad28-7350-a33c-4e6ecdb8d22d","sequence":53}},"turnId":"01a18014-abb6-7342-b725-deb73957d6bb","attempt":1,"nextAttempt":2,"maxAttempts":10,"retryDelayMs":20,"system":"meta","errorKind":"server","httpStatus":500},"emittedAtMs":1793150201136}`
		secondRetry = `{"jsonrpc":"2.0","method":"turn/modelRetryScheduled","params":{"sessionId":"01a18014-aa0a-7ea1-9852-8bc110159315","viewCursor":"v4:01a18014-aa0a-7ea1-9852-8bc110159315:8","sourceRange":{"stream":{"kind":"session","id":"01a18014-aa0a-7ea1-9852-8bc110159315"},"first":{"id":"01a18014-ad53-7fa1-acd9-ecbe762056f8","sequence":55},"last":{"id":"01a18014-ad53-7fa1-acd9-ecbe762056f8","sequence":55}},"turnId":"01a18014-abb6-7342-b725-deb73957d6bb","attempt":2,"nextAttempt":3,"maxAttempts":10,"retryDelayMs":40,"system":"meta","errorKind":"server","httpStatus":500},"emittedAtMs":1793150201179}`
	)
	failure := museProbe(t, "a 500 after the provider attempts is transient")
	a, sink := testAgent(t)
	state := a.sessions["session"]
	delete(a.sessions, "session")
	a.sessionID = "01a18014-aa0a-7ea1-9852-8bc110159315"
	a.sessions[a.sessionID] = state
	for _, frame := range []string{started, firstRetry, secondRetry, failure.frame} {
		a.HandleOutput([]byte(frame))
	}
	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 2)
	assert.Equal(t, []byte(firstRetry), notifications[0].Content)
	assert.Equal(t, []byte(secondRetry), notifications[1].Content)
	for _, message := range sink.Messages() {
		assert.NotContains(t, string(message.Content), contracts.MuseMethodTurnModelRetryScheduled, "no message row holds a retry notice")
	}
	var ops []string
	for _, entry := range sink.Timeline() {
		ops = append(ops, entry.Op)
	}
	assert.Equal(t, []string{"notification", "notification", "turn_end", "schedule_server_error"}, ops)
	assert.Equal(t, agent.APIRetryNotification(), (museProvider{}).Classify([]byte(firstRetry)))
}

// A model retry notice of another turn is a recovered notice: it stays visible
// as a notification, and it holds nothing back.
func TestMuseModelRetryNoticeOfAnotherTurnPersistsAtOnce(t *testing.T) {
	t.Parallel()
	a, sink := testAgent(t)
	a.startTurn("session", "turn")
	raw := feed(t, a, contracts.MuseMethodTurnModelRetryScheduled, map[string]any{"sessionId": "session", "turnId": "old-turn", "attempt": 1, "nextAttempt": 2, "maxAttempts": 10, "retryDelayMs": 20, "errorKind": "server", "httpStatus": 500})
	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 1)
	assert.Equal(t, raw, notifications[0].Content)
	assert.Empty(t, sink.Messages())
}

// A host that exits during a root turn ends that turn with NotRetryable, as
// every other turn end does, so a pending server-error retry does not outlive
// it. A child turn that the exit ends applies no verdict.
func TestMuseHostRetirementEndsTheRootTurnWithNotRetryable(t *testing.T) {
	t.Parallel()
	a, root := newMuseRetryAgent(t, "session", "root-turn", time.Time{})
	state, child := nativeHistoryChild(t, a, root)
	state.turnID = "child-turn"

	a.retireHost(agent.MessageCompletionError)

	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(root.Timeline()))
	assert.Empty(t, museRetryOps(child.Timeline()), "the child's open turn applies no verdict")
}
