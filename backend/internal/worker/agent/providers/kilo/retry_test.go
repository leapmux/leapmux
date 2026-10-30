package kilo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/kilo/kilotest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The error objects below are verbatim from the Kilo 7.8.17 fixtures of
// R/opencode-kilo-mimo.md (.tmp/probe/fixtures.txt), unless a comment states
// otherwise. Each is the error of a session.error frame, which the daemon also
// stores on the failed assistant message.
const (
	// kiloFreeLimitError is "KILO free error".
	kiloFreeLimitError = `{"name":"APIError","data":{"message":"Rate limit exceeded. Please try again later.","statusCode":429,"isRetryable":true,"responseHeaders":{"content-length":"134","content-type":"application/json","date":"Wed, 28 Oct 2026 01:16:51 GMT","retry-after":"2","server":"BaseHTTP Python/3.15.1"},"responseBody":"{\"type\": \"error\", \"error\": {\"type\": \"FreeUsageLimitError\", \"message\": \"Rate limit exceeded. Please try again later.\"}, \"metadata\": {}}","metadata":{"url":"http://127.0.0.1:18765/free429/v1/chat/completions"}}}`
	// kiloPromotionError is "KILO promo error".
	kiloPromotionError = `{"name":"APIError","data":{"message":"Promotion limit reached","statusCode":401,"isRetryable":false,"responseHeaders":{"content-length":"90","content-type":"application/json","date":"Wed, 28 Oct 2026 01:16:55 GMT","server":"BaseHTTP Python/3.15.1"},"responseBody":"{\"error\": {\"code\": \"PROMOTION_MODEL_LIMIT_REACHED\", \"message\": \"Promotion limit reached\"}}","metadata":{"url":"http://127.0.0.1:18765/kilopromo/v1/chat/completions"}}}`
	// kiloPaymentError is "KILO s402 error". Its body states the code as a number.
	kiloPaymentError = `{"name":"APIError","data":{"message":"Insufficient balance -- add credits to continue","statusCode":402,"isRetryable":false,"responseHeaders":{"content-length":"86","content-type":"application/json","date":"Wed, 28 Oct 2026 01:17:08 GMT","server":"BaseHTTP Python/3.15.1"},"responseBody":"{\"error\": {\"message\": \"Insufficient balance -- add credits to continue\", \"code\": 402}}","metadata":{"url":"http://127.0.0.1:18765/s402/v1/chat/completions"}}}`
	// kiloServerError is the error of "OC s500 error": Kilo runs the same daemon code.
	kiloServerError = `{"name":"APIError","data":{"message":"Internal server error","statusCode":500,"isRetryable":true,"responseHeaders":{"content-length":"71","content-type":"application/json","date":"Wed, 28 Oct 2026 00:56:47 GMT","retry-after-ms":"300","server":"BaseHTTP Python/3.15.1"},"responseBody":"{\"error\": {\"message\": \"Internal server error\", \"type\": \"server_error\"}}","metadata":{"url":"http://127.0.0.1:18765/s500/v1/chat/completions"}}}`
	// kiloPromotionOnRateLimit is CONSTRUCTED: the promotion code on a 429
	// with a deadline, which no probe captured. The family recipe alone would
	// schedule a usage-limit retry for it.
	kiloPromotionOnRateLimit = `{"name":"APIError","data":{"message":"Promotion limit reached","statusCode":429,"isRetryable":true,"responseHeaders":{"date":"Wed, 28 Oct 2026 01:16:55 GMT","retry-after":"3600"},"responseBody":"{\"error\": {\"code\": \"PROMOTION_MODEL_LIMIT_REACHED\", \"message\": \"Promotion limit reached\"}}"}}`
	// kiloACPFailure is the ACP adapter's answer to the failed prompt, as
	// "OC s500 acp" shows it. It keeps the message alone.
	kiloACPFailure = `{"code":-32603,"message":"Internal error: Internal server error","data":{"service":"session","errorName":"APIError"}}`
	// kiloEndTurn is the ACP answer of a prompt that ended well.
	kiloEndTurn = `{"stopReason":"end_turn","usage":{"inputTokens":10,"outputTokens":2,"totalTokens":12},"_meta":{}}`
	// kiloRetrySession is the session of every test agent below.
	kiloRetrySession = "session-1"
)

func mustKiloTime(t *testing.T, value string) time.Time {
	t.Helper()
	instant, err := time.Parse(time.RFC3339, value)
	require.NoError(t, err)
	return instant
}

// kiloStoredPage is the answer of /session/{id}/message?limit=1 when the newest
// message is the prompt's failed assistant message, created at created.
func kiloStoredPage(created time.Time, daemonError string) string {
	return `[{"info":{"id":"msg_failed","sessionID":"` + kiloRetrySession + `","role":"assistant","time":{"created":` +
		strconv.FormatInt(created.UnixMilli(), 10) + `},"error":` + daemonError + `},"parts":[]}]`
}

// newKiloRetryAgent builds the agent of newAgent with a recording stdin, the
// mock clock at now, and a fake daemon. The daemon answers the read of the
// newest stored message with page. The first prompt is active and prepared,
// as SendInput leaves it.
func newKiloRetryAgent(t *testing.T, now time.Time, page string) (*Agent, *agenttest.Sink, *agenttest.Stdin, *kilotest.FakeDaemon) {
	t.Helper()
	daemon := kilotest.NewFakeDaemon(t, EventRoute, QuestionRoot)
	daemon.HandleFunc("/session/"+kiloRetrySession+"/message", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, page)
	})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	clock := testutil.NewQuartzMock(t)
	clock.Set(now).MustWait(testutil.DeadlineContext(t))
	stdin := &agenttest.Stdin{}
	a := newAgent(false)
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID: "test-agent", ProviderName: "kilo", Stdin: stdin, Ctx: ctx, Cancel: cancel, Clock: clock,
	})
	a.SetSessionIDForTest(kiloRetrySession)
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	*a.HooksForTest() = kiloHooks(a)
	a.BeginQuestionsForTest(ctx, a.Sink(), "test-agent", daemon.Server.URL)
	a.SetPromptActiveForTest(true)
	a.HooksForTest().PromptPrepared()
	return a, sink, stdin, daemon
}

// kiloRetryTimeline states each sink operation in call order, with the reset
// instant of a usage-limit schedule.
func kiloRetryTimeline(sink *agenttest.Sink) []string {
	var ops []string
	for _, entry := range sink.Timeline() {
		op := entry.Op
		if !entry.ResetAt.IsZero() {
			op += "@" + entry.ResetAt.UTC().Format(time.RFC3339Nano)
		}
		ops = append(ops, op)
	}
	return ops
}

// kiloACPError decodes the ACP adapter's error answer, as the JSON-RPC peer does.
func kiloACPError(t *testing.T) error {
	t.Helper()
	var failure providerkit.JSONRPCResponseError
	require.NoError(t, json.Unmarshal([]byte(kiloACPFailure), &failure))
	return &failure
}

// lastRequestID returns the id of the newest request that the agent wrote.
func lastRequestID(t *testing.T, stdin *agenttest.Stdin) int64 {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(stdin.String()), "\n")
	var request struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &request))
	require.NotZero(t, request.ID)
	return request.ID
}

// answerSteerLast answers the steer request id with reply after the prompt
// ended, and waits until the steer's end ended the turn.
func answerSteerLast(t *testing.T, a *Agent, id int64, reply agenttest.RPCReply) {
	t.Helper()
	require.True(t, a.Deliver(id, agenttest.JSONRPCResponse(id, reply)))
	testutil.RequireEventually(t, func() bool { return !a.PromptActive() })
}

// answerSteerFirst runs the steer's callback inline with response and err,
// before the prompt ends. Deliver would hand the answer to the steer's own
// goroutine, and while the prompt runs, the steer's end changes no state that
// a test can wait on. The steer request stays registered until the test ends,
// and the callback that its release runs then finds no steer left.
func answerSteerFirst(a *Agent, response json.RawMessage, err error) {
	a.finishKiloSteer(kiloRetrySession, response, err, true)
}

// The Kilo gateway's free limit states its deadline in retry-after.
func TestKiloFreeUsageLimitSchedulesAtTheStatedDeadline(t *testing.T) {
	t.Parallel()
	now := mustKiloTime(t, "2026-10-28T01:16:51Z")
	a, sink, _, _ := newKiloRetryAgent(t, now, kiloStoredPage(now, kiloFreeLimitError))
	a.FinishPromptRequestForTest(kiloRetrySession, nil, kiloACPError(t))
	assert.Equal(t, []string{
		"leapmux_notification", "schedule_usage_limit@2026-10-28T01:16:53Z", "cancel_server_error",
	}, kiloRetryTimeline(sink))
}

// The Kilo gateway answers a spent balance with HTTP 402.
func TestKiloPaymentRequiredCancelsBothRetries(t *testing.T) {
	t.Parallel()
	now := mustKiloTime(t, "2026-10-28T01:17:08Z")
	a, sink, _, _ := newKiloRetryAgent(t, now, kiloStoredPage(now, kiloPaymentError))
	a.FinishPromptRequestForTest(kiloRetrySession, nil, kiloACPError(t))
	assert.Equal(t, []string{"leapmux_notification", "cancel_usage_limit", "cancel_server_error"}, kiloRetryTimeline(sink))
}

// A spent promotion lifts only by the reader's own action, so the turn
// schedules no retry, also when the answer states a deadline.
func TestKiloPromotionLimitSchedulesNothing(t *testing.T) {
	t.Parallel()
	now := mustKiloTime(t, "2026-10-28T01:16:55Z")
	for name, daemonError := range map[string]string{
		"the captured 401":      kiloPromotionError,
		"a 429 with a deadline": kiloPromotionOnRateLimit,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, _, _ := newKiloRetryAgent(t, now, kiloStoredPage(now, daemonError))
			a.FinishPromptRequestForTest(kiloRetrySession, nil, kiloACPError(t))
			assert.Equal(t, []string{"leapmux_notification", "cancel_server_error"}, kiloRetryTimeline(sink))
		})
	}
}

// The prompt and the steer that joined it both fail. Whichever answer comes
// first, the turn ends once, and the backoff is written once.
func TestKiloFailedSteerDecidesOnceInEitherResponseOrder(t *testing.T) {
	t.Parallel()
	now := mustKiloTime(t, "2026-10-28T00:56:47Z")
	for name, steerFirst := range map[string]bool{"the prompt answers first": false, "the steer answers first": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, stdin, _ := newKiloRetryAgent(t, now, kiloStoredPage(now, kiloServerError))
			require.NoError(t, a.SteerInput("guide the turn", nil))
			steer := lastRequestID(t, stdin)
			if steerFirst {
				answerSteerFirst(a, nil, kiloACPError(t))
				assert.True(t, a.PromptActive(), "the prompt's own end still ends the turn")
				a.FinishPromptRequestForTest(kiloRetrySession, nil, kiloACPError(t))
			} else {
				a.FinishPromptRequestForTest(kiloRetrySession, nil, kiloACPError(t))
				assert.True(t, a.PromptActive(), "the steer keeps the turn")
				answerSteerLast(t, a, steer, agenttest.RPCReply{Error: json.RawMessage(kiloACPFailure)})
			}
			assert.False(t, a.PromptActive())
			assert.Equal(t, []string{"leapmux_notification", "leapmux_notification", "schedule_server_error"}, kiloRetryTimeline(sink))
			assert.Equal(t, 1, sink.AutoScheduleCount())
		})
	}
}

// The steer ends well after the prompt failed. The steer's end is the turn's
// end, so the turn is not retryable.
func TestKiloSteerThatEndsWellDecidesTheTurnInEitherResponseOrder(t *testing.T) {
	t.Parallel()
	now := mustKiloTime(t, "2026-10-28T00:56:47Z")
	for name, steerFirst := range map[string]bool{"the prompt answers first": false, "the steer answers first": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, stdin, _ := newKiloRetryAgent(t, now, kiloStoredPage(now, kiloServerError))
			require.NoError(t, a.SteerInput("guide the turn", nil))
			steer := lastRequestID(t, stdin)
			if steerFirst {
				answerSteerFirst(a, json.RawMessage(kiloEndTurn), nil)
				a.FinishPromptRequestForTest(kiloRetrySession, nil, kiloACPError(t))
			} else {
				a.FinishPromptRequestForTest(kiloRetrySession, nil, kiloACPError(t))
				answerSteerLast(t, a, steer, agenttest.RPCReply{Result: json.RawMessage(kiloEndTurn)})
			}
			assert.False(t, a.PromptActive())
			assert.Equal(t, []string{"leapmux_notification", "turn_end", "cancel_server_error"}, kiloRetryTimeline(sink))
			assert.Zero(t, sink.AutoScheduleCount())
		})
	}
}

// A long wait during a steered turn ends the turn, and the steer's end, which
// ends the visible turn, schedules the usage-limit retry.
func TestKiloLongWaitDecidesTheSteersTurnEnd(t *testing.T) {
	t.Parallel()
	now := mustKiloTime(t, "2026-10-28T01:16:51Z")
	a, sink, stdin, daemon := newKiloRetryAgent(t, now, kiloStoredPage(now, kiloServerError))
	require.NoError(t, a.SteerInput("guide the turn", nil))
	steer := lastRequestID(t, stdin)
	next := now.Add(5 * time.Hour)
	// The retry status of the Kilo go429long probe run, in the test session,
	// with its next instant five hours after now.
	daemon.Send(`data: {"id":"evt_18016f2a8001vR6t9NWXIFadBq","type":"session.status","properties":{"sessionID":"session-1","status":{"type":"retry","attempt":1,"message":"Go subscription 5 hour limit exceeded. Retry in 5hr.","next":` +
		strconv.FormatInt(next.UnixMilli(), 10) + "}}}\n\n")
	testutil.RequireEventually(t, func() bool { return strings.Contains(stdin.String(), `"method":"session/cancel"`) })

	// The daemon answers both cancelled prompts with end_turn.
	a.FinishPromptRequestForTest(kiloRetrySession, json.RawMessage(kiloEndTurn), nil)
	answerSteerLast(t, a, steer, agenttest.RPCReply{Result: json.RawMessage(kiloEndTurn)})
	assert.False(t, a.PromptActive())
	assert.Equal(t, []string{
		"notification", "turn_end", "turn_end", "schedule_usage_limit@2026-10-28T06:16:51Z", "cancel_server_error",
	}, kiloRetryTimeline(sink))
}

func TestKiloMemberFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		failure string
		decides bool
	}{
		{"the captured promotion code decides", kiloPromotionError, true},
		{"the promotion code on a 429 decides", kiloPromotionOnRateLimit, true},
		{"the paid-model code decides", `{"name":"APIError","data":{"message":"Paid model requires an account","statusCode":401,"isRetryable":false,"responseBody":"{\"error\":{\"code\":\"PAID_MODEL_AUTH_REQUIRED\"}}"}}`, true},
		{"a numeric code is the family's", kiloPaymentError, false},
		{"a Zen free limit is the family's", kiloFreeLimitError, false},
		{"a server error is the family's", kiloServerError, false},
		{"another code is the family's", `{"name":"APIError","data":{"message":"Bad request","statusCode":400,"responseBody":"{\"error\":{\"code\":\"BAD_REQUEST\"}}"}}`, false},
		{"an error that is a bare string is the family's", `{"name":"APIError","data":{"message":"Internal Server Error","responseBody":"{\"error\":\"Internal Server Error\"}"}}`, false},
		{"a body that is no JSON is the family's", `{"name":"APIError","data":{"message":"Bad gateway","statusCode":502,"responseBody":"<html>Bad gateway</html>"}}`, false},
		{"an error with no body is the family's", `{"name":"UnknownError","data":{"message":"PROMOTION_MODEL_LIMIT_REACHED"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var failure DaemonError
			require.NoError(t, json.Unmarshal([]byte(tc.failure), &failure))
			verdict, decides := kiloGatewayFailure(failure)
			assert.Equal(t, tc.decides, decides)
			if tc.decides {
				assert.Equal(t, providerkit.TurnFailureNotRetryable, verdict.Kind)
			} else {
				assert.Equal(t, providerkit.TurnFailure{}, verdict)
			}
		})
	}
}

func TestKiloGatewayFailurePrecedesTheGenericRecipe(t *testing.T) {
	t.Parallel()
	failure := DaemonError{Data: DaemonErrorData{StatusCode: 429, ResponseBody: `{"error":{"code":"PROMOTION_MODEL_LIMIT_REACHED"}}`}}
	assert.Equal(t, providerkit.TurnFailureNotRetryable, classifyDaemonError(failure, time.Now()).Kind)
}
