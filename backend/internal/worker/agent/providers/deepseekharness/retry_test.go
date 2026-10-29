package deepseekharness

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
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

// The research events of dsh 0.2.1-rc.2 (.tmp/retry-research/cline-dsh.md),
// copied verbatim: a Z.AI 1308 under QUOTA, a RATE_LIMIT with a stated wait, a
// SERVER 529, a Z.AI 1113 under QUOTA, a completed turn, and an llm/retry.
const (
	dshUsageLimitEnd = `{"type":"turn/end","seq":19,"time":1793150195985,"data":{"turn":1,"reason":{"kind":"error","error":{"message":"429: {\"code\":\"1308\",\"message\":\"Usage limit reached for 5 hour. Your limit will reset at 2026-10-28 15:00:00\"}","code":"QUOTA"}}}}`
	dshRateLimitEnd  = `{"type":"turn/end","seq":19,"time":1793150195895,"data":{"turn":1,"reason":{"kind":"error","error":{"message":"Rate limit reached","code":"RATE_LIMIT","status":429,"providerRetryAfterMs":30000,"requestId":"req-probe-1793150195886"}}}}`
	dshOverloadedEnd = `{"type":"turn/end","seq":34,"time":1793150210622,"data":{"turn":1,"reason":{"kind":"error","error":{"message":"Overloaded","code":"SERVER","status":529,"requestId":"req-probe-1793150210620"}}}}`
	dshBalanceEnd    = `{"type":"turn/end","seq":19,"time":1793150195458,"data":{"turn":1,"reason":{"kind":"error","error":{"message":"429: {\"code\":\"1113\",\"message\":\"Insufficient balance or no resource package. Please recharge.\"}","code":"QUOTA"}}}}`
	dshCompletedEnd  = `{"type":"turn/end","seq":20,"time":1793150166396,"data":{"turn":1,"reason":{"kind":"completed"}}}`
	dshRetryEvent    = `{"type":"llm/retry","seq":18,"time":1793150195097,"data":{"retryId":"777cdc35-e3be-4506-86b7-9317158f2f30","turn":1,"step":1,"provider":"deepseek-official","mode":"normal","policyKey":"[\"normal\",5,[\"EMPTY_RESPONSE\",\"RATE_LIMIT\",\"SERVER\",\"TIMEOUT\",\"TRANSPORT\"],500,10000,0.1]","retry":1,"maxRetries":5,"delayMs":457.6248154529355,"failure":{"message":"Overloaded","code":"SERVER","status":529,"requestId":"req-probe-1793150195090"}}}`
)

// dshShiftedUsageLimitEnd is dshUsageLimitEnd with its time moved into the
// window of its stamp. The capture failed at 01:16:35Z, more than five hours
// before its stamp, 15:00:00 at UTC+8, which a real 1308 never states. The
// shifted time is 03:00:00Z.
const dshShiftedUsageLimitEnd = `{"type":"turn/end","seq":19,"time":1793156400000,"data":{"turn":1,"reason":{"kind":"error","error":{"message":"429: {\"code\":\"1308\",\"message\":\"Usage limit reached for 5 hour. Your limit will reset at 2026-10-28 15:00:00\"}","code":"QUOTA"}}}}`

// dshClockNow is the agent clock of the retry tests. Only an event that states
// no time reads it.
var dshClockNow = time.Date(2026, 7, 14, 10, 34, 45, 0, time.UTC)

// dshImplausibleZAIUsageLimit is the vendor's verdict for a Z.AI usage limit
// whose reset stamp falls outside its window, with the window that its text
// states.
func dshImplausibleZAIUsageLimit(window providerkit.UsageWindow) providerkit.TurnFailure {
	return providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: window, Cause: "Z.AI reported a usage limit with an implausible reset stamp"}
}

// newDSHRetryAgent builds an offline agent whose clock reads dshClockNow and
// whose root turn runs.
func newDSHRetryAgent(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	clock.Set(dshClockNow).MustWait(testutil.DeadlineContext(t))
	sink := &agenttest.Sink{}
	a := newOfflineAgentOn(t, sink, clock)
	a.active = true
	return a, sink
}

// Each root turn end reaches the real handler, and its verdict follows the
// turn-end row.
func TestDSHRootTurnEndsApplyTheirVerdictAfterTheTurnEnd(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		events []string
		want   providerkit.TurnFailure
	}{
		{
			name: "a Z.AI usage limit under QUOTA schedules at its stamp", events: []string{dshShiftedUsageLimitEnd},
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: time.Date(2026, 10, 28, 7, 0, 1, 0, time.UTC), Cause: "Z.AI reported a usage limit that lifts at a stated time"},
		},
		{
			// The capture's own time makes its stamp implausible, so the 5-hour
			// window decides, and the QUOTA code does not cancel a pending
			// usage-limit retry.
			name: "a Z.AI usage limit with an implausible stamp schedules a server-error retry", events: []string{dshUsageLimitEnd},
			want: dshImplausibleZAIUsageLimit(providerkit.UsageWindowWeekOrLess),
		},
		{
			name: "a rate limit with a stated wait schedules at the event time plus the wait", events: []string{dshRateLimitEnd},
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: time.UnixMilli(1793150195895 + 30000).UTC(), Cause: "DeepSeek Harness reported a rate limit (the provider states when to retry)"},
		},
		{
			name: "a 529 after dsh's own retry is transient", events: []string{dshRetryEvent, dshOverloadedEnd},
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "DeepSeek Harness reported a transient failure"},
		},
		{
			name: "a Z.AI balance block under QUOTA cancels both retries", events: []string{dshBalanceEnd},
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Z.AI reported a billing or plan block"},
		},
		{name: "a completed turn cancels the server-error retry", events: []string{dshCompletedEnd}, want: providerkit.TurnFailure{}},
		{
			// The reviewed collision: Z.AI 1302 reads as a rate limit, and the
			// stated wait still makes it a usage-limit retry at the deadline.
			name:   "a Z.AI rate limit with a stated wait schedules at the deadline",
			events: []string{`{"type":"turn/end","seq":19,"time":1793150195895,"data":{"turn":1,"reason":{"kind":"error","error":{"message":"429: {\"code\":\"1302\",\"message\":\"Rate limit reached for requests\"}","code":"RATE_LIMIT","status":429,"providerRetryAfterMs":45000}}}}`},
			want:   providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: time.UnixMilli(1793150195895 + 45000).UTC(), Cause: "the vendor reported a rate limit (the provider states when to retry)"},
		},
		{
			// With no time on the event, a stated wait counts from the agent
			// clock, never from the Unix epoch.
			name:   "a rate limit with no event time counts its wait from the agent clock",
			events: []string{`{"type":"turn/end","seq":19,"data":{"turn":1,"reason":{"kind":"error","error":{"message":"Rate limit reached","code":"RATE_LIMIT","status":429,"providerRetryAfterMs":30000}}}}`},
			want:   providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: dshClockNow.Add(30 * time.Second), Cause: "DeepSeek Harness reported a rate limit (the provider states when to retry)"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newDSHRetryAgent(t)
			for _, event := range tc.events {
				require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(event)))
			}
			assert.Equal(t, providerkittest.TurnEndThen(tc.want), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

// serveDSHCancel answers the agent's native requests with an accepted receipt
// and returns the paths that the agent requested.
func serveDSHCancel(t *testing.T, a *Agent) func() []string {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID string `json:"rpcId"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": request.ID, "result": map[string]any{"ok": true, "value": map[string]any{"accepted": true}}}))
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	a.rpc = remoteRPC{endpoint: endpoint}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(paths)
	}
}

// A failure that dsh reports after the user stopped the turn won the race
// against the cancel. The user asked to stop, so the failure must not re-send
// the stopped prompt.
func TestDSHFailureAfterAStopDoesNotRetry(t *testing.T) {
	t.Parallel()
	a, sink := newDSHRetryAgent(t)
	requested := serveDSHCancel(t, a)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.Equal(t, []string{"/api/session/cancel"}, requested())
	require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(dshOverloadedEnd)))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{}), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// The stop of one turn never decides the next turn: the next turn start clears
// it.
func TestDSHStopDoesNotOutliveItsTurn(t *testing.T) {
	t.Parallel()
	a, sink := newDSHRetryAgent(t)
	serveDSHCancel(t, a)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	root := a.streams["root"]
	require.NoError(t, a.handleSessionEvent(root, []byte(`{"type":"turn/end","seq":1,"time":1793150195000,"data":{"turn":1,"reason":{"kind":"aborted"}}}`)))
	require.NoError(t, a.handleSessionEvent(root, []byte(`{"type":"turn/start","seq":2,"time":1793150196000,"data":{"turn":2}}`)))
	require.NoError(t, a.handleSessionEvent(root, []byte(dshOverloadedEnd)))

	stopped := providerkittest.TurnEndThen(providerkit.TurnFailure{})
	timeline := providerkittest.TurnEndAndRetryOps(sink.Timeline())
	require.Greater(t, len(timeline), len(stopped))
	assert.Equal(t, stopped, timeline[:len(stopped)])
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "DeepSeek Harness reported a transient failure"}),
		timeline[len(stopped):], "the second turn was not stopped, so its failure retries")
}

// A stop with no turn running notes nothing, so the next turn's failure still
// retries.
func TestDSHStopWhileIdleDoesNotDecideTheNextTurn(t *testing.T) {
	t.Parallel()
	a, sink := newDSHRetryAgent(t)
	a.active = false
	requested := serveDSHCancel(t, a)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.Empty(t, requested(), "an idle agent sends no cancel")
	a.active = true
	require.NoError(t, a.handleSessionEvent(a.streams["root"], []byte(dshOverloadedEnd)))
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "DeepSeek Harness reported a transient failure"}),
		providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// A child stream's turn end makes no retry call in either transcript.
func TestDSHChildTurnEndMakesNoRetryCall(t *testing.T) {
	t.Parallel()
	a, sink := newDSHRetryAgent(t)
	child := &sessionStream{sessionID: "native-child", parentSessionID: "native-root", childAgentID: "stored-child", lastSeq: -1, pending: map[string][]byte{}, ready: make(chan struct{})}
	a.streams["child"] = child
	require.NoError(t, a.handleSessionEvent(child, []byte(dshOverloadedEnd)))
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
	for _, entry := range providerkittest.TurnEndAndRetryOps(sink.Child("stored-child").Timeline()) {
		assert.Equal(t, "turn_end", entry.Op, "a child turn applies no verdict to its own transcript")
	}
}

// A stream failure ends the running root turn with no turn/end, and that end
// cancels the server-error retry once, after the error row.
func TestDSHStreamFailureDuringATurnCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	a, sink := newDSHRetryAgent(t)
	done := make(chan struct{})
	close(done)
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "deepseek-test", ProviderName: "dsh", Ctx: a.Context(), Cancel: func() {}, ProcessDone: done, Stdin: agenttest.NopStdin(io.Discard)})
	a.reportStreamFailure(errors.New("the native Remote connection failed"))

	reference := &agenttest.Sink{}
	providerkit.ApplyTurnFailure(reference, providerkit.TurnFailure{}, nil)
	require.Equal(t, reference.Timeline(), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
	timeline := sink.Timeline()
	require.GreaterOrEqual(t, len(timeline), 2)
	assert.Equal(t, "leapmux_notification", timeline[len(timeline)-2].Op, "the cancellation follows the error row")
}

// A stream failure with no turn running ends no turn, so it keeps a retry that
// an earlier turn scheduled.
func TestDSHStreamFailureWhileIdleAppliesNoVerdict(t *testing.T) {
	t.Parallel()
	a, sink := newDSHRetryAgent(t)
	a.active = false
	done := make(chan struct{})
	close(done)
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "deepseek-test", ProviderName: "dsh", Ctx: a.Context(), Cancel: func() {}, ProcessDone: done, Stdin: agenttest.NopStdin(io.Discard)})
	a.reportStreamFailure(errors.New("the native Remote connection failed"))
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

func TestDSHTurnEndFailure(t *testing.T) {
	t.Parallel()
	transient := providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "DeepSeek Harness reported a transient failure"}
	rateLimited := providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "DeepSeek Harness reported a rate limit"}
	rateLimitedUntil := func(at time.Time) providerkit.TurnFailure {
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: at, Cause: "DeepSeek Harness reported a rate limit (the provider states when to retry)"}
	}
	errorEnd := func(errorObject string) string {
		return `{"type":"turn/end","seq":1,"time":1793150195895,"data":{"turn":1,"reason":{"kind":"error","error":` + errorObject + `}}}`
	}
	eventTime := time.UnixMilli(1793150195895).UTC()
	for _, tc := range []struct {
		name        string
		event       string
		interrupted bool
		want        providerkit.TurnFailure
	}{
		{
			name: "the research Z.AI usage limit, moved into its window, lifts at its stamp", event: dshShiftedUsageLimitEnd,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: time.Date(2026, 10, 28, 7, 0, 1, 0, time.UTC), Cause: "Z.AI reported a usage limit that lifts at a stated time"},
		},
		{
			// At its own time the stamp is implausible, so the vendor reads the
			// usage limit from its 5-hour window. dsh files Z.AI 1308 under QUOTA,
			// and the code must not turn the usage limit into a billing block.
			name: "the research Z.AI usage limit at its own time is a usage limit of a week or less", event: dshUsageLimitEnd,
			want: dshImplausibleZAIUsageLimit(providerkit.UsageWindowWeekOrLess),
		},
		{
			// dsh files Z.AI 1310 under RATE_LIMIT on the pi-ai route. The stamp lies
			// more than 31 days after the event, and the code must not turn a
			// monthly limit into a rate limit that retries forever.
			name:  "an implausible weekly or monthly Z.AI limit under RATE_LIMIT is a longer usage limit",
			event: errorEnd(`{"message":"429: {\"code\":\"1310\",\"message\":\"Weekly/Monthly Limit Exhausted. Your limit will reset at 2026-12-30 10:00:00\"}","code":"RATE_LIMIT"}`),
			want:  dshImplausibleZAIUsageLimit(providerkit.UsageWindowLonger),
		},
		{
			name:  "a stated wait lifts an implausible Z.AI usage limit at the wait",
			event: errorEnd(`{"message":"429: {\"code\":\"1310\",\"message\":\"Weekly/Monthly Limit Exhausted. Your limit will reset at 2026-12-30 10:00:00\"}","code":"RATE_LIMIT","status":429,"providerRetryAfterMs":60000}`),
			want:  providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: eventTime.Add(time.Minute), Window: providerkit.UsageWindowLonger, Cause: "Z.AI reported a usage limit with an implausible reset stamp (the provider states when to retry)"},
		},
		{
			// DashScope reuses OpenAI's quota sentence for a per-minute throttle and
			// marks it with its #token-limit anchor.
			name:  "a vendor rate limit under QUOTA is rate limited",
			event: errorEnd(`{"message":"429: {\"error\":{\"message\":\"You exceeded your current quota, please check your plan and billing details. For details, see: https://help.aliyun.com/zh/model-studio/error-code#token-limit\",\"type\":\"insufficient_quota\",\"code\":\"insufficient_quota\"}}","code":"QUOTA"}`),
			want:  providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "the vendor reported a rate limit"},
		},
		{
			name: "a QUOTA whose text no vendor rule reads is a billing block", event: errorEnd(`{"message":"429: quota exhausted","code":"QUOTA"}`),
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "DeepSeek Harness reported QUOTA"},
		},
		{name: "the research rate limit lifts at the event time plus its wait", event: dshRateLimitEnd, want: rateLimitedUntil(eventTime.Add(30 * time.Second))},
		{name: "the research 529 is transient", event: dshOverloadedEnd, want: transient},
		{
			name: "the research Z.AI balance block is a billing block", event: dshBalanceEnd,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Z.AI reported a billing or plan block"},
		},
		{name: "the research completed turn is not retryable", event: dshCompletedEnd, want: providerkit.TurnFailure{}},
		{name: "the research llm/retry states no turn end", event: dshRetryEvent, want: providerkit.TurnFailure{}},
		{
			name: "the account quota is a billing block", event: errorEnd(`{"message":"Your account quota is used up.","code":"ACCOUNT_QUOTA"}`),
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "DeepSeek Harness reported ACCOUNT_QUOTA"},
		},
		{name: "a timeout is transient", event: errorEnd(`{"message":"timed out","code":"TIMEOUT"}`), want: transient},
		{name: "a transport failure is transient", event: errorEnd(`{"message":"Stream ended without finish_reason","code":"TRANSPORT"}`), want: transient},
		{name: "an empty response is transient", event: errorEnd(`{"message":"empty","code":"EMPTY_RESPONSE"}`), want: transient},
		{name: "a rate limit with no wait is rate limited", event: errorEnd(`{"message":"Rate limit reached","code":"RATE_LIMIT","status":429}`), want: rateLimited},
		{name: "a rate limit with a zero wait is rate limited", event: errorEnd(`{"message":"Rate limit reached","code":"RATE_LIMIT","providerRetryAfterMs":0}`), want: rateLimited},
		{name: "a rate limit with a negative wait is rate limited", event: errorEnd(`{"message":"Rate limit reached","code":"RATE_LIMIT","providerRetryAfterMs":-5000}`), want: rateLimited},
		{name: "a rate limit with a wait of more than 31 days is rate limited", event: errorEnd(`{"message":"Rate limit reached","code":"RATE_LIMIT","providerRetryAfterMs":2764800000}`), want: rateLimited},
		{name: "a rate limit with an enormous wait is rate limited", event: errorEnd(`{"message":"Rate limit reached","code":"RATE_LIMIT","providerRetryAfterMs":1e300}`), want: rateLimited},
		{name: "a rate limit with a fractional wait lifts at the whole wait", event: errorEnd(`{"message":"Rate limit reached","code":"RATE_LIMIT","providerRetryAfterMs":1500.5}`), want: rateLimitedUntil(eventTime.Add(1500500 * time.Microsecond))},
		{
			name:  "a weekly Z.AI limit on the pi-ai route lifts at its stamp",
			event: errorEnd(`{"message":"429: {\"code\":\"1310\",\"message\":\"Weekly/Monthly Limit Exhausted. Your limit will reset at 2026-11-03 10:00:00\"}","code":"RATE_LIMIT"}`),
			want:  providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: time.Date(2026, 11, 3, 2, 0, 1, 0, time.UTC), Cause: "Z.AI reported a usage limit that lifts at a stated time"},
		},
		{
			name:  "a definitive vendor verdict wins over a transient code",
			event: errorEnd(`{"message":"500: Insufficient balance or no resource package. Please recharge.","code":"SERVER","status":500}`),
			want:  providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "the vendor reported a billing or plan block"},
		},
		{
			name:  "an indicative vendor verdict decides an unknown code",
			event: errorEnd(`{"message":"529: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}","code":"HTTP_529"}`),
			want:  providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "the vendor reported a transient failure"},
		},
		{name: "a rejected key is not retryable", event: errorEnd(`{"message":"401 Unauthorized","code":"AUTH","status":401}`), want: providerkit.TurnFailure{}},
		{name: "an error with no error object is not retryable", event: `{"type":"turn/end","seq":1,"time":1000,"data":{"reason":{"kind":"error"}}}`, want: providerkit.TurnFailure{}},
		{name: "an aborted turn is not retryable", event: `{"type":"turn/end","seq":1,"time":1000,"data":{"reason":{"kind":"aborted","error":{"message":"Overloaded","code":"SERVER"}}}}`, want: providerkit.TurnFailure{}},
		{name: "a stopped turn's transient failure is not retryable", event: dshOverloadedEnd, interrupted: true, want: providerkit.TurnFailure{}},
		{name: "a stopped turn's rate limit with a stated wait is not retryable", event: dshRateLimitEnd, interrupted: true, want: providerkit.TurnFailure{}},
		{name: "a stopped turn's Z.AI usage limit is not retryable", event: dshShiftedUsageLimitEnd, interrupted: true, want: providerkit.TurnFailure{}},
		{name: "an unreadable error is not retryable", event: errorEnd(`{"message":"Overloaded","code":529}`), want: providerkit.TurnFailure{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var event sessionEvent
			require.NoError(t, json.Unmarshal([]byte(tc.event), &event))
			assert.Equal(t, tc.want, dshTurnEndFailure(event.Data, tc.interrupted, dshEventTime(event, dshClockNow)))
		})
	}
}

func TestDSHEventTime(t *testing.T) {
	t.Parallel()
	assert.Equal(t, time.UnixMilli(1793150195895).UTC(), dshEventTime(sessionEvent{Time: 1793150195895}, dshClockNow))
	assert.Equal(t, dshClockNow, dshEventTime(sessionEvent{}, dshClockNow), "an absent time reads the clock")
	assert.Equal(t, dshClockNow, dshEventTime(sessionEvent{Time: -1}, dshClockNow), "a negative time reads the clock")
}
