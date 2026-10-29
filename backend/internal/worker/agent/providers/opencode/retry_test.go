package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixtures below are verbatim from R/opencode-kilo-mimo.md (.tmp/probe/fixtures.txt),
// captured from OpenCode 1.19.4 against a fake model server, unless a comment states
// otherwise.
const (
	// fixtureS500Session is the session of the s500 probe run.
	fixtureS500Session = "ses_e7ffd92d1ffeAn331gYTeNaaSU"
	// fixtureS500Error is "OC s500 error".
	fixtureS500Error = `{"id":"evt_1800277c50012NQ6C0tRiEH8ua","type":"session.error","properties":{"sessionID":"ses_e7ffd92d1ffeAn331gYTeNaaSU","error":{"name":"APIError","data":{"message":"Internal server error","statusCode":500,"isRetryable":true,"responseHeaders":{"content-length":"71","content-type":"application/json","date":"Wed, 28 Oct 2026 00:56:47 GMT","retry-after-ms":"300","server":"BaseHTTP Python/3.15.1"},"responseBody":"{\"error\": {\"message\": \"Internal server error\", \"type\": \"server_error\"}}","metadata":{"url":"http://127.0.0.1:18765/s500/v1/chat/completions"}}}}}`
	// fixtureS500ACPFailure is "OC s500 acp", the answer that the ACP adapter
	// gives the failed prompt. It keeps the message alone.
	fixtureS500ACPFailure = `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error: Internal server error","data":{"service":"session","errorName":"APIError"}}}`
	// fixtureGo429Session is the session of the go429 probe run.
	fixtureGo429Session = "ses_e7ffd4ef4ffePSnIMZOSL7HWFU"
	// fixtureGo429Retry is "OC go429 retry1".
	fixtureGo429Retry = `{"id":"evt_18002b60c001VybpJEfJ23vQ3Q","type":"session.status","properties":{"sessionID":"ses_e7ffd4ef4ffePSnIMZOSL7HWFU","status":{"type":"retry","attempt":1,"message":"5 hour usage limit reached. It will reset in 1 minute. To continue using this model now, enable usage from your available balance - https://opencode.ai/workspace/wrk_probe/go","action":{"reason":"account_rate_limit","provider":"fake","title":"Go limit reached","message":"5 hour usage limit reached. It will reset in 1 minute. To continue using this model now, enable usage from your available balance","label":"open settings","link":"https://opencode.ai/workspace/wrk_probe/go"},"next":1793149025756}}}`
	// fixtureGo429LongSession and fixtureGo429LongRetry are the retry status of
	// the go429long probe run (.tmp/probe/opencode/run-go429long/wire.jsonl),
	// whose fake server answered with a retry-after of five hours.
	fixtureGo429LongSession = "ses_e7feef442ffebUpP4h00YaJwng"
	fixtureGo429LongRetry   = `{"id":"evt_18011138e001gHla2GJ2DUtKOf","type":"session.status","properties":{"sessionID":"ses_e7feef442ffebUpP4h00YaJwng","status":{"type":"retry","attempt":1,"message":"5 hour usage limit reached. It will reset in 5 hours. To continue using this model now, enable usage from your available balance - https://opencode.ai/workspace/wrk_probe/go","action":{"reason":"account_rate_limit","provider":"fake","title":"Go limit reached","message":"5 hour usage limit reached. It will reset in 5 hours. To continue using this model now, enable usage from your available balance","label":"open settings","link":"https://opencode.ai/workspace/wrk_probe/go"},"next":1793167965198}}}`
	// fixtureGo429LongCancelled is the probe's answer to the prompt that it cancelled.
	fixtureGo429LongCancelled = `{"stopReason":"end_turn","usage":{"inputTokens":0,"outputTokens":0,"totalTokens":0},"_meta":{}}`
	// fixtureGo429Error is "OC go429 error".
	fixtureGo429Error = `{"id":"evt_18002dd5e001VFW95R50vPEYEq","type":"session.error","properties":{"sessionID":"ses_e7ffd4ef4ffePSnIMZOSL7HWFU","error":{"name":"APIError","data":{"message":"Go subscription 5 hour limit exceeded. Retry in 5hr.","statusCode":429,"isRetryable":true,"responseHeaders":{"content-length":"187","content-type":"application/json","date":"Wed, 28 Oct 2026 00:57:13 GMT","retry-after":"2","server":"BaseHTTP Python/3.15.1"},"responseBody":"{\"type\": \"error\", \"error\": {\"type\": \"GoUsageLimitError\", \"message\": \"Go subscription 5 hour limit exceeded. Retry in 5hr.\"}, \"metadata\": {\"workspace\": \"wrk_probe\", \"limitName\": \"5 hour\"}}","metadata":{"url":"http://127.0.0.1:18765/go429/v1/chat/completions"}}}}}`
	// fixtureCreditsSession is the session of the credits401 probe run.
	fixtureCreditsSession = "ses_e7ffce47bffeOL7IayTq4Oc7vU"
	// fixtureCreditsError is "OC credits error".
	fixtureCreditsError = `{"id":"evt_1800321e1001CLhh0jGqIO3NHb","type":"session.error","properties":{"sessionID":"ses_e7ffce47bffeOL7IayTq4Oc7vU","error":{"name":"APIError","data":{"message":"Insufficient balance. Manage your billing here: https://opencode.ai/workspace/wrk_probe/billing","statusCode":401,"isRetryable":false,"responseHeaders":{"content-length":"162","content-type":"application/json","date":"Wed, 28 Oct 2026 00:57:31 GMT","server":"BaseHTTP Python/3.15.1"},"responseBody":"{\"type\": \"error\", \"error\": {\"type\": \"CreditsError\", \"message\": \"Insufficient balance. Manage your billing here: https://opencode.ai/workspace/wrk_probe/billing\"}}","metadata":{"url":"http://127.0.0.1:18765/credits401/v1/chat/completions"}}}}}`
	// fixtureRefusedError is "OC refused error": a connection failure, which
	// states no status.
	fixtureRefusedError = `{"id":"evt_1802150d60014DuncnyeFNDtMN","type":"session.error","properties":{"sessionID":"ses_e7fdfaf7effenBgtJ2FKH81h4h","error":{"name":"APIError","data":{"message":"Cannot connect to API: ECONNREFUSED: Unable to connect. Is the computer able to access the url?","isRetryable":true,"metadata":{"url":"http://127.0.0.1:18999/refused/v1/chat/completions"}}}}}`
	// fixtureOKACP is "OC ok acp".
	fixtureOKACP = `{"stopReason":"end_turn","usage":{"inputTokens":10,"outputTokens":2,"totalTokens":12},"_meta":{}}`

	// fixtureZAI1310Session and fixtureZAI1310Error are the real Z.AI 1310 of
	// R/transcripts-mimo-opencode.md: the error that OpenCode 1.18.31 stored on
	// the failed assistant message (.tmp/transcripts/mimo-opencode/oc-apierror.log).
	// The set-cookie value is redacted, as the research table redacts it.
	fixtureZAI1310Session = "ses_f4c263b75ffehEhXzEvMrL6aNw"
	fixtureZAI1310Message = "msg_0b3d9c5a6001UAtQobzclpAZAv"
	fixtureZAI1310Created = 1789723788710
	fixtureZAI1310Error   = `{"name":"APIError","data":{"message":"Weekly/Monthly Limit Exhausted. Your limit will reset at 2026-09-19 20:42:58","statusCode":429,"isRetryable":true,"responseHeaders":{"alt-svc":"h3=\":443\"; ma=3600","connection":"keep-alive","date":"Fri, 18 Sep 2026 09:30:59 GMT","ga-traceid":"bfce2c8d27dd0f797286ec11c9765756","transfer-encoding":"chunked","vary":"Origin, Access-Control-Request-Method, Access-Control-Request-Headers, Origin, Access-Control-Request-Method, Access-Control-Request-Headers, Origin","x-log-id":"20260918173058334b5be18fb74ea3","x-request-id":"7f056c4d-c887-43b7-89c5-eca8f922854a","set-cookie":"acw_tc=<redacted>;path=/;HttpOnly;Max-Age=1800"},"responseBody":"{\"error\":{\"code\":\"1310\",\"message\":\"Weekly/Monthly Limit Exhausted. Your limit will reset at 2026-09-19 20:42:58\"}}","metadata":{"url":"https://api.z.ai/api/coding/paas/v4/chat/completions"}}}`

	// fixtureRateLimitWithDeadline is CONSTRUCTED: no probe captured it. It is
	// the reviewed collision, a 429 whose body holds Z.AI 1302 "Rate limit
	// reached for requests" (R/vendors.md section 1), with a retry-after of one
	// hour beside the date header.
	fixtureRateLimitWithDeadline = `{"type":"session.error","properties":{"sessionID":"ses_e7ffd92d1ffeAn331gYTeNaaSU","error":{"name":"APIError","data":{"message":"Rate limit reached for requests","statusCode":429,"isRetryable":true,"responseHeaders":{"content-type":"application/json","date":"Wed, 28 Oct 2026 00:56:47 GMT","retry-after":"3600"},"responseBody":"{\"error\":{\"code\":\"1302\",\"message\":\"Rate limit reached for requests\"}}"}}}}`
)

// mustTime parses an RFC 3339 instant of a fixture.
func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	instant, err := time.Parse(time.RFC3339Nano, value)
	require.NoError(t, err)
	return instant
}

// newRetryTestAgent is the daemon rig of newDaemonGenerationAgent, in the
// session of a fixture, with the mock clock at now. Its stdin records every
// frame that the agent writes, so a test counts the session/cancel of a stop.
// The prompt is active and prepared, as SendInput leaves it.
func newRetryTestAgent(t *testing.T, sessionID string, now time.Time) (*Agent, *agenttest.Sink, *storedMessagePeer, *quartz.Mock, *agenttest.Stdin) {
	t.Helper()
	a, sink, peer := newDaemonGenerationAgent(t)
	clock := a.Clock().(*quartz.Mock)
	clock.Set(now).MustWait(testutil.DeadlineContext(t))
	stdin := &agenttest.Stdin{}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID: "test-agent", ProviderName: "opencode", Ctx: t.Context(), Clock: clock, Stdin: stdin,
	})
	a.SetSessionIDForTest(sessionID)
	a.HooksForTest().PromptPrepared()
	return a, sink, peer, clock, stdin
}

// feedFrame hands one verbatim daemon event to the bridge, as the event stream does.
func feedFrame(t *testing.T, a *Agent, frame string) {
	t.Helper()
	a.Questions.handleEvent(t.Context(), []byte(frame))
}

// observeAssistant feeds the message.updated that starts an assistant message
// of the prompt, as the daemon sends it before the model request.
func observeAssistant(t *testing.T, a *Agent, sessionID, messageID string, created time.Time) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"type": "message.updated", "properties": map[string]any{
		"sessionID": sessionID,
		"info": map[string]any{
			"id": messageID, "sessionID": sessionID, "role": "assistant", "time": map[string]any{"created": created.UnixMilli()},
		},
	}})
	require.NoError(t, err)
	a.Questions.handleEvent(t.Context(), raw)
}

// sessionErrorObject returns the error object that a session.error frame carries.
func sessionErrorObject(t *testing.T, frame string) string {
	t.Helper()
	var event struct {
		Properties struct {
			Error json.RawMessage `json:"error"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal([]byte(frame), &event))
	require.NotEmpty(t, event.Properties.Error)
	return string(event.Properties.Error)
}

// storedAssistantInfo is the info of a stored assistant message that holds daemonError.
func storedAssistantInfo(sessionID, messageID string, created time.Time, daemonError string) string {
	return `{"id":"` + messageID + `","sessionID":"` + sessionID + `","role":"assistant","time":{"created":` +
		strconv.FormatInt(created.UnixMilli(), 10) + `},"error":` + daemonError + `}`
}

// storedMessagePage is the answer of /session/{id}/message?limit=1: the newest message.
func storedMessagePage(info string) string {
	return `[{"info":` + info + `,"parts":[]}]`
}

// acpFailure decodes the error of an ACP answer, as the JSON-RPC peer does.
func acpFailure(t *testing.T, frame string) error {
	t.Helper()
	var envelope struct {
		Error *providerkit.JSONRPCResponseError `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(frame), &envelope))
	require.NotNil(t, envelope.Error)
	return envelope.Error
}

// retryTimeline states each sink operation in call order, with the reset
// instant of a usage-limit schedule.
func retryTimeline(sink *agenttest.Sink) []string {
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

// sessionCancels counts the session/cancel notifications that the agent wrote.
func sessionCancels(stdin *agenttest.Stdin) int {
	return strings.Count(stdin.String(), `"method":"session/cancel"`)
}

// readPaths returns the daemon routes that the agent read.
func readPaths(peer *storedMessagePeer) []string {
	peer.mu.Lock()
	defer peer.mu.Unlock()
	return append([]string(nil), peer.paths...)
}

// The ACP answer arrives before the session.error, so the turn's own stored
// message decides. The late event changes nothing.
func TestOpenCodeFailedPromptReadsTheStoredErrorWhenTheEventIsLate(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	a, sink, peer, _, _ := newRetryTestAgent(t, fixtureS500Session, now)
	observeAssistant(t, a, fixtureS500Session, "msg_failed", now)
	info := storedAssistantInfo(fixtureS500Session, "msg_failed", now, sessionErrorObject(t, fixtureS500Error))
	peer.reply(storedMessagePage(info), http.StatusOK)

	a.FinishPromptRequestForTest(fixtureS500Session, nil, acpFailure(t, fixtureS500ACPFailure))
	require.Equal(t, []string{"leapmux_notification", "schedule_server_error"}, retryTimeline(sink))
	assert.JSONEq(t, info, string(sink.LastAutoSchedule().SourcePayload), "the payload is the record that holds the failure")
	require.Equal(t, []string{"/session/" + fixtureS500Session + "/message"}, readPaths(peer))

	feedFrame(t, a, fixtureS500Error)
	assert.Equal(t, []string{"leapmux_notification", "schedule_server_error"}, retryTimeline(sink),
		"an event that arrives after the turn end decides nothing")
}

// The session.error arrived during the turn and the read fails, so the held
// event decides.
func TestOpenCodeFailedPromptUsesTheHeldEventWhenTheReadFails(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:57:13Z")
	a, sink, peer, _, _ := newRetryTestAgent(t, fixtureGo429Session, now)
	observeAssistant(t, a, fixtureGo429Session, "msg_failed", now)
	feedFrame(t, a, fixtureGo429Error)
	peer.reply("the store is unavailable", http.StatusServiceUnavailable)

	a.FinishPromptRequestForTest(fixtureGo429Session, nil, acpFailure(t, fixtureS500ACPFailure))
	// The date header plus the retry-after of 2 s.
	require.Equal(t, []string{
		"leapmux_notification", "schedule_usage_limit@2026-10-28T00:57:15Z", "cancel_server_error",
	}, retryTimeline(sink))
	assert.JSONEq(t, fixtureGo429Error, string(sink.LastAutoSchedule().SourcePayload))
}

// The read of the stored message waits for its deadline on the agent clock,
// and the held event then decides.
func TestOpenCodeFailedPromptUsesTheHeldEventWhenTheReadTimesOut(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	a, sink, peer, clock, _ := newRetryTestAgent(t, fixtureS500Session, now)
	trap := clock.Trap().AfterFunc(LatestAssistantErrorReadTimerTag)
	t.Cleanup(trap.Close)
	observeAssistant(t, a, fixtureS500Session, "msg_failed", now)
	feedFrame(t, a, fixtureS500Error)
	started := make(chan struct{})
	peer.beforeResponse = func(ctx context.Context) { close(started); <-ctx.Done() }

	done := make(chan struct{})
	go func() {
		a.FinishPromptRequestForTest(fixtureS500Session, nil, acpFailure(t, fixtureS500ACPFailure))
		close(done)
	}()
	guard := testutil.DeadlineContext(t)
	assert.Equal(t, 2*time.Second, testutil.WaitForTimer(t, guard, trap))
	select {
	case <-started:
	case <-guard.Done():
		t.Fatal("the read of the stored message did not start")
	}
	clock.Advance(2 * time.Second).MustWait(guard)
	select {
	case <-done:
	case <-guard.Done():
		t.Fatal("the read did not stop at its deadline")
	}
	require.Equal(t, []string{"leapmux_notification", "schedule_server_error"}, retryTimeline(sink))
	assert.JSONEq(t, fixtureS500Error, string(sink.LastAutoSchedule().SourcePayload))
}

// A session.error that precedes every assistant message of the prompt cannot be
// shown to belong to it, so it decides nothing when the read fails.
func TestOpenCodeHeldEventBeforeTheAssistantDecidesNothing(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	a, sink, peer, _, _ := newRetryTestAgent(t, fixtureS500Session, now)
	feedFrame(t, a, fixtureS500Error)
	observeAssistant(t, a, fixtureS500Session, "msg_failed", now)
	peer.reply("the store is unavailable", http.StatusServiceUnavailable)

	a.FinishPromptRequestForTest(fixtureS500Session, nil, acpFailure(t, fixtureS500ACPFailure))
	require.Equal(t, []string{"leapmux_notification", "cancel_server_error"}, retryTimeline(sink),
		"the answer of the ACP adapter alone states no retryable class")
}

// The newest stored message is an assistant message of an earlier prompt: its
// id is not the prompt's, and the daemon created it before the prompt started.
func TestOpenCodeStoredErrorOfAnEarlierPromptDecidesNothing(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	a, sink, peer, _, _ := newRetryTestAgent(t, fixtureS500Session, now)
	observeAssistant(t, a, fixtureS500Session, "msg_current", now)
	peer.reply(storedMessagePage(storedAssistantInfo(fixtureS500Session, "msg_earlier", now.Add(-3*time.Second),
		sessionErrorObject(t, fixtureS500Error))), http.StatusOK)

	a.FinishPromptRequestForTest(fixtureS500Session, nil, acpFailure(t, fixtureS500ACPFailure))
	assert.Equal(t, []string{"leapmux_notification", "cancel_server_error"}, retryTimeline(sink))
}

// The stored message that the daemon created within the slack of the prompt's
// start belongs to the prompt, also when the bridge never saw its id.
func TestOpenCodeStoredErrorCreatedAfterThePromptStartDecides(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	a, sink, peer, _, _ := newRetryTestAgent(t, fixtureS500Session, now)
	peer.reply(storedMessagePage(storedAssistantInfo(fixtureS500Session, "msg_unseen", now.Add(-2*time.Second),
		sessionErrorObject(t, fixtureS500Error))), http.StatusOK)

	a.FinishPromptRequestForTest(fixtureS500Session, nil, acpFailure(t, fixtureS500ACPFailure))
	assert.Equal(t, []string{"leapmux_notification", "schedule_server_error"}, retryTimeline(sink))
}

// The first prompt's session.error arrives during the second prompt. The
// second prompt fails too, and nothing proves that the late error is its own.
func TestOpenCodeEarlierPromptsLateErrorDecidesNothingForTheNextPrompt(t *testing.T) {
	t.Parallel()
	first := mustTime(t, "2026-10-28T00:56:46Z")
	a, sink, peer, clock, _ := newRetryTestAgent(t, fixtureS500Session, first)
	observeAssistant(t, a, fixtureS500Session, "msg_first", first)
	firstInfo := storedAssistantInfo(fixtureS500Session, "msg_first", first, sessionErrorObject(t, fixtureS500Error))
	peer.reply(storedMessagePage(firstInfo), http.StatusOK)
	a.FinishPromptRequestForTest(fixtureS500Session, nil, acpFailure(t, fixtureS500ACPFailure))
	require.Equal(t, []string{"leapmux_notification", "schedule_server_error"}, retryTimeline(sink))

	clock.Advance(time.Minute).MustWait(testutil.DeadlineContext(t))
	a.SetPromptActiveForTest(true)
	a.HooksForTest().PromptPrepared()
	feedFrame(t, a, fixtureS500Error)
	a.FinishPromptRequestForTest(fixtureS500Session, nil, acpFailure(t, fixtureS500ACPFailure))
	assert.Equal(t, []string{
		"leapmux_notification", "schedule_server_error", "leapmux_notification", "cancel_server_error",
	}, retryTimeline(sink))
}

// The real Z.AI 1310 states its reset in UTC+8 inside the message, and no
// retry-after header.
func TestOpenCodeRealZAIWeeklyLimitSchedulesAtTheStatedReset(t *testing.T) {
	t.Parallel()
	// The prompt starts when the daemon created the message. OpenCode retried
	// for 70 s, and the turn ends at the date header of the last answer.
	created := time.UnixMilli(fixtureZAI1310Created)
	a, sink, peer, clock, _ := newRetryTestAgent(t, fixtureZAI1310Session, created)
	observeAssistant(t, a, fixtureZAI1310Session, fixtureZAI1310Message, created)
	clock.Set(mustTime(t, "2026-09-18T09:30:59Z")).MustWait(testutil.DeadlineContext(t))
	peer.reply(storedMessagePage(storedAssistantInfo(fixtureZAI1310Session, fixtureZAI1310Message, created, fixtureZAI1310Error)), http.StatusOK)

	a.FinishPromptRequestForTest(fixtureZAI1310Session, nil,
		acpFailure(t, `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error: Weekly/Monthly Limit Exhausted. Your limit will reset at 2026-09-19 20:42:58","data":{"service":"session","errorName":"APIError"}}}`))
	assert.Equal(t, []string{
		"leapmux_notification", "schedule_usage_limit@2026-09-19T12:42:59Z", "cancel_server_error",
	}, retryTimeline(sink))
}

// The reviewed collision: a rate limit that states a deadline is a usage limit
// that lifts at that deadline.
func TestOpenCodeRateLimitWithADeadlineSchedulesAtTheDeadline(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:47Z")
	a, sink, peer, _, _ := newRetryTestAgent(t, fixtureS500Session, now)
	observeAssistant(t, a, fixtureS500Session, "msg_failed", now)
	feedFrame(t, a, fixtureRateLimitWithDeadline)
	peer.reply("the store is unavailable", http.StatusServiceUnavailable)

	a.FinishPromptRequestForTest(fixtureS500Session, nil, acpFailure(t, fixtureS500ACPFailure))
	assert.Equal(t, []string{
		"leapmux_notification", "schedule_usage_limit@2026-10-28T01:56:47Z", "cancel_server_error",
	}, retryTimeline(sink))
}

// A billing block cancels both retries.
func TestOpenCodeCreditsErrorCancelsBothRetries(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:57:31Z")
	a, sink, peer, _, _ := newRetryTestAgent(t, fixtureCreditsSession, now)
	observeAssistant(t, a, fixtureCreditsSession, "msg_failed", now)
	feedFrame(t, a, fixtureCreditsError)
	peer.reply("the store is unavailable", http.StatusServiceUnavailable)

	a.FinishPromptRequestForTest(fixtureCreditsSession, nil, acpFailure(t, fixtureS500ACPFailure))
	assert.Equal(t, []string{"leapmux_notification", "cancel_usage_limit", "cancel_server_error"}, retryTimeline(sink))
}

// A turn that ends well cancels the server-error retry, and reads nothing.
func TestOpenCodeSucceededPromptCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	a, sink, peer, _, _ := newRetryTestAgent(t, fixtureS500Session, now)
	observeAssistant(t, a, fixtureS500Session, "msg_answer", now)
	feedFrame(t, a, fixtureS500Error)

	a.FinishPromptRequestForTest(fixtureS500Session, json.RawMessage(fixtureOKACP), nil)
	assert.Equal(t, []string{"turn_end", "cancel_server_error"}, retryTimeline(sink))
	assert.Empty(t, readPaths(peer))
}

// A stopped turn is not retryable, whatever error it held, and reads nothing.
func TestOpenCodeStoppedPromptCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	a, sink, peer, _, _ := newRetryTestAgent(t, fixtureS500Session, now)
	observeAssistant(t, a, fixtureS500Session, "msg_failed", now)
	feedFrame(t, a, fixtureS500Error)
	a.SetStoppedForTest(true)

	a.FinishPromptRequestForTest(fixtureS500Session, nil, acpFailure(t, fixtureS500ACPFailure))
	assert.Equal(t, []string{"cancel_server_error"}, retryTimeline(sink))
	assert.Empty(t, readPaths(peer))
}

// With no daemon address and no held event, the turn's own ACP answer is the
// one record of the failure, and the vendor's own sentence in it decides.
func TestOpenCodeACPAnswerDecidesWhenTheDaemonIsUnreachable(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-09-18T09:30:59Z")
	a, sink, _, _, _ := newRetryTestAgent(t, fixtureZAI1310Session, now)
	a.Questions.mu.Lock()
	a.Questions.baseURL = ""
	a.Questions.mu.Unlock()

	a.FinishPromptRequestForTest(fixtureZAI1310Session, nil,
		acpFailure(t, `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error: Weekly/Monthly Limit Exhausted. Your limit will reset at 2026-09-19 20:42:58","data":{"service":"session","errorName":"APIError"}}}`))
	assert.Equal(t, []string{
		"leapmux_notification", "schedule_usage_limit@2026-09-19T12:42:59Z", "cancel_server_error",
	}, retryTimeline(sink))
}

// longWaitRetry is a retry status of the s500 session whose next attempt lies
// at next. It is "OC s500 retry1" with its next instant moved, because the
// probe's fake server answered with a delay of 300 ms.
func longWaitRetry(next time.Time) string {
	return `{"id":"evt_1800271c6001oDg7zb2IFMEVHf","type":"session.status","properties":{"sessionID":"ses_e7ffd92d1ffeAn331gYTeNaaSU","status":{"type":"retry","attempt":1,"message":"Internal server error","next":` +
		strconv.FormatInt(next.UnixMilli(), 10) + `}}}`
}

// A daemon retry five hours ahead ends the turn once, and the queue retries at
// the stated instant (user decision 5).
func TestOpenCodeLongWaitStopsTheTurnAndSchedulesAtTheNextRetry(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	a, sink, _, _, stdin := newRetryTestAgent(t, fixtureS500Session, now)
	observeAssistant(t, a, fixtureS500Session, "msg_waiting", now)
	next := now.Add(5 * time.Hour)
	feedFrame(t, a, longWaitRetry(next))
	assert.Equal(t, 1, sessionCancels(stdin), "the long wait stops the turn")
	feedFrame(t, a, longWaitRetry(next.Add(time.Minute)))
	assert.Equal(t, 1, sessionCancels(stdin), "a second long wait of the same turn stops nothing more")

	// The daemon answers the cancelled prompt with end_turn, as the go429long probe shows.
	a.FinishPromptRequestForTest(fixtureS500Session, json.RawMessage(fixtureOKACP), nil)
	require.Equal(t, []string{
		"notification", "notification", "turn_end", "schedule_usage_limit@2026-10-28T05:56:46Z", "cancel_server_error",
	}, retryTimeline(sink))
	assert.JSONEq(t, longWaitRetry(next), string(sink.LastAutoSchedule().SourcePayload))
}

// The captured five-hour wait of the go429long probe ends the turn, and the
// queue retries at the instant that the status states.
func TestOpenCodeCapturedFiveHourWaitStopsTheTurn(t *testing.T) {
	t.Parallel()
	// The probe's user message, its assistant message, and the retry status
	// five hours before its next attempt.
	a, sink, _, clock, stdin := newRetryTestAgent(t, fixtureGo429LongSession, time.UnixMilli(1793149963769))
	observeAssistant(t, a, fixtureGo429LongSession, "msg_180110e01001xz2fOZ17b9566w", time.UnixMilli(1793149963777))
	clock.Set(time.UnixMilli(1793167965198).Add(-5 * time.Hour)).MustWait(testutil.DeadlineContext(t))
	feedFrame(t, a, fixtureGo429LongRetry)
	assert.Equal(t, 1, sessionCancels(stdin))

	a.FinishPromptRequestForTest(fixtureGo429LongSession, json.RawMessage(fixtureGo429LongCancelled), nil)
	require.Equal(t, []string{
		"notification", "turn_end", "schedule_usage_limit@2026-10-28T06:12:45.198Z", "cancel_server_error",
	}, retryTimeline(sink))
	assert.JSONEq(t, fixtureGo429LongRetry, string(sink.LastAutoSchedule().SourcePayload))
}

// The captured retry status one minute before the next attempt stays the
// daemon's own wait.
func TestOpenCodeCapturedOneMinuteWaitKeepsTheTurn(t *testing.T) {
	t.Parallel()
	a, sink, _, _, stdin := newRetryTestAgent(t, fixtureGo429Session, time.UnixMilli(1793149025756).Add(-time.Minute))
	observeAssistant(t, a, fixtureGo429Session, "msg_waiting", time.UnixMilli(1793149025756).Add(-time.Minute))
	feedFrame(t, a, fixtureGo429Retry)
	assert.Zero(t, sessionCancels(stdin))
	assert.Equal(t, []string{"notification"}, retryTimeline(sink))
}

// A daemon retry that waits ten minutes or less stays the daemon's own.
func TestOpenCodeShortWaitKeepsTheTurn(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	for name, wait := range map[string]time.Duration{
		"five minutes ahead":        5 * time.Minute,
		"exactly ten minutes ahead": 10 * time.Minute,
		"a minute in the past":      -time.Minute,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, _, _, stdin := newRetryTestAgent(t, fixtureS500Session, now)
			observeAssistant(t, a, fixtureS500Session, "msg_waiting", now)
			feedFrame(t, a, longWaitRetry(now.Add(wait)))
			assert.Zero(t, sessionCancels(stdin))
			a.FinishPromptRequestForTest(fixtureS500Session, json.RawMessage(fixtureOKACP), nil)
			assert.Equal(t, []string{"notification", "turn_end", "cancel_server_error"}, retryTimeline(sink))
		})
	}
}

// OpenCode 1.19.4 adds an unrounded retry-after-ms wait to the clock, so
// `next` can hold a fraction. The retry status still writes its notice, and a
// long wait still stops the turn at the stated instant. A malformed `next`
// keeps the notice and stops nothing.
func TestOpenCodeRetryStatusReadsAFractionalOrMalformedNext(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	statusFrame := func(next string) string {
		return `{"id":"evt_1800271c6001oDg7zb2IFMEVHf","type":"session.status","properties":{"sessionID":"ses_e7ffd92d1ffeAn331gYTeNaaSU","status":{"type":"retry","attempt":1,"message":"Internal server error","next":` + next + `}}}`
	}
	t.Run("a fractional next", func(t *testing.T) {
		t.Parallel()
		a, sink, _, _, stdin := newRetryTestAgent(t, fixtureS500Session, now)
		observeAssistant(t, a, fixtureS500Session, "msg_waiting", now)
		feedFrame(t, a, statusFrame(strconv.FormatInt(now.Add(5*time.Hour).UnixMilli(), 10)+".5"))
		assert.Equal(t, 1, sessionCancels(stdin), "the long wait stops the turn")
		a.FinishPromptRequestForTest(fixtureS500Session, json.RawMessage(fixtureOKACP), nil)
		assert.Equal(t, []string{
			"notification", "turn_end", "schedule_usage_limit@2026-10-28T05:56:46.0005Z", "cancel_server_error",
		}, retryTimeline(sink))
	})
	t.Run("a malformed next", func(t *testing.T) {
		t.Parallel()
		a, sink, _, _, stdin := newRetryTestAgent(t, fixtureS500Session, now)
		feedFrame(t, a, statusFrame(`"soon"`))
		assert.Zero(t, sessionCancels(stdin))
		assert.Len(t, sink.PersistedNotifications(), 1, "the retry status still writes its notice")
	})
}

// A retry status that states no next instant, or one beyond what the queue
// schedules, stops nothing.
func TestOpenCodeLongWaitNeedsAnInstantThatTheQueueSchedules(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	for name, frame := range map[string]string{
		"no next instant": `{"type":"session.status","properties":{"sessionID":"ses_e7ffd92d1ffeAn331gYTeNaaSU","status":{"type":"retry","attempt":1,"message":"Internal server error"}}}`,
		"beyond 31 days":  longWaitRetry(now.Add(32 * 24 * time.Hour)),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, _, _, _, stdin := newRetryTestAgent(t, fixtureS500Session, now)
			feedFrame(t, a, frame)
			assert.Zero(t, sessionCancels(stdin))
		})
	}
}

// A long wait outside a prompt stops nothing: no turn runs.
func TestOpenCodeLongWaitWithoutAPromptStopsNothing(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	a, _, _, _, stdin := newRetryTestAgent(t, fixtureS500Session, now)
	a.SetPromptActiveForTest(false)
	feedFrame(t, a, longWaitRetry(now.Add(5*time.Hour)))
	assert.Zero(t, sessionCancels(stdin))
}

// The reader's own stop wins over a long wait that LeapMux already acted on.
func TestOpenCodeUserStopAfterALongWaitSchedulesNothing(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	// The reader stops through Interrupt, or through the raw session/cancel
	// that the worker service also reads as a stop.
	for name, stop := range map[string]func(a *Agent) error{
		"through Interrupt": func(a *Agent) error { return a.Interrupt(agent.StopContext{}) },
		"through a raw session/cancel": func(a *Agent) error {
			return a.SendRawInput([]byte(`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"`+fixtureS500Session+`"}}`), agent.StopContext{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, _, _, stdin := newRetryTestAgent(t, fixtureS500Session, now)
			observeAssistant(t, a, fixtureS500Session, "msg_waiting", now)
			feedFrame(t, a, longWaitRetry(now.Add(5*time.Hour)))
			require.NoError(t, stop(a))
			assert.Equal(t, 2, sessionCancels(stdin))

			a.FinishPromptRequestForTest(fixtureS500Session, json.RawMessage(fixtureOKACP), nil)
			assert.Equal(t, []string{"notification", "turn_end", "cancel_server_error"}, retryTimeline(sink))
		})
	}
}

// The next prompt starts without the long wait or the stop of the one before.
func TestOpenCodeNextPromptForgetsTheLongWaitAndTheStop(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	for name, stop := range map[string]bool{"after a long wait": false, "after a stop": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, peer, _, _ := newRetryTestAgent(t, fixtureS500Session, now)
			feedFrame(t, a, longWaitRetry(now.Add(5*time.Hour)))
			if stop {
				require.NoError(t, a.Interrupt(agent.StopContext{}))
			}
			a.FinishPromptRequestForTest(fixtureS500Session, json.RawMessage(fixtureOKACP), nil)
			sinkOps := len(sink.Timeline())

			a.SetPromptActiveForTest(true)
			a.HooksForTest().PromptPrepared()
			observeAssistant(t, a, fixtureS500Session, "msg_next", now)
			peer.reply(storedMessagePage(storedAssistantInfo(fixtureS500Session, "msg_next", now, sessionErrorObject(t, fixtureS500Error))), http.StatusOK)
			a.FinishPromptRequestForTest(fixtureS500Session, nil, acpFailure(t, fixtureS500ACPFailure))
			assert.Equal(t, []string{"leapmux_notification", "schedule_server_error"}, retryTimeline(sink)[sinkOps:])
		})
	}
}

// startPrompt ends the prompt that newRetryTestAgent left active and starts a
// prompt of content through send, as the worker's queue starts a message.
func startPrompt(t *testing.T, a *Agent, send func(a *Agent, content string) error, content string) {
	t.Helper()
	a.SetPromptActiveForTest(false)
	require.NoError(t, send(a, content))
	require.True(t, a.PromptActive())
}

// sendInput starts a prompt through SendInput.
func sendInput(a *Agent, content string) error {
	return a.SendInput(content, nil)
}

// observeSummary feeds the message.updated that starts the assistant message
// of a compaction, as the daemon sends it before the summary request.
func observeSummary(t *testing.T, a *Agent, sessionID, messageID string, created time.Time) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"type": "message.updated", "properties": map[string]any{
		"sessionID": sessionID,
		"info": map[string]any{
			"id": messageID, "sessionID": sessionID, "role": "assistant", "mode": "compaction", "summary": true,
			"time": map[string]any{"created": created.UnixMilli()},
		},
	}})
	require.NoError(t, err)
	a.Questions.handleEvent(t.Context(), raw)
}

// failWithStoredServerError ends the running prompt of the s500 session with
// the ACP adapter's answer. The daemon stores the 500 on messageID and states
// it in a session.error, so the recipe would retry the failure.
func failWithStoredServerError(t *testing.T, a *Agent, peer *storedMessagePeer, messageID string, created time.Time) {
	t.Helper()
	feedFrame(t, a, fixtureS500Error)
	peer.reply(storedMessagePage(storedAssistantInfo(fixtureS500Session, messageID, created, sessionErrorObject(t, fixtureS500Error))), http.StatusOK)
	a.FinishPromptRequestForTest(fixtureS500Session, nil, acpFailure(t, fixtureS500ACPFailure))
}

// The ACP adapter of OpenCode 1.19.4 runs a prompt of the /compact command,
// with or without arguments, as session.summarize. The compaction's failure
// schedules no retry, even for a 500 that the recipe retries, and the
// compaction's end reads no stored error. The worker's queue sends the command
// through SendInput, and a control answer sends it through SendInputForSession.
func TestOpenCodeFailedCompactionSchedulesNoRetry(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	routes := map[string]func(a *Agent, content string) error{
		"SendInput": sendInput,
		"SendInputForSession": func(a *Agent, content string) error {
			return a.SendInputForSession(fixtureS500Session, content, nil)
		},
	}
	for _, content := range []string{"/compact", "/compact keep the plan", "  /compact\n"} {
		for route, send := range routes {
			t.Run(route+" "+strconv.Quote(content), func(t *testing.T) {
				t.Parallel()
				a, sink, peer, _, _ := newRetryTestAgent(t, fixtureS500Session, now)
				startPrompt(t, a, send, content)
				observeSummary(t, a, fixtureS500Session, "msg_summary", now)

				failWithStoredServerError(t, a, peer, "msg_summary", now)
				assert.Equal(t, providerkittest.TurnEndThen(providerkit.CompactionEnd())[1:], providerkittest.TurnEndAndRetryOps(sink.Timeline()),
					"the failed compaction writes no turn end, and cancels the server-error retry once")
				assert.Zero(t, sink.AutoScheduleCount())
				assert.Empty(t, readPaths(peer), "the compaction's end reads no stored error")
			})
		}
	}
}

// A compaction that ends well, or that the reader stops, cancels the
// server-error retry with the compaction's own reason.
func TestOpenCodeCompactionEndStatesTheCompactionReason(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	for name, stop := range map[string]bool{"a compaction that ends well": false, "a compaction that the reader stops": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, _, _, stdin := newRetryTestAgent(t, fixtureS500Session, now)
			startPrompt(t, a, sendInput, "/compact")
			observeSummary(t, a, fixtureS500Session, "msg_summary", now)
			response := fixtureOKACP
			if stop {
				require.NoError(t, a.Interrupt(agent.StopContext{}))
				require.Equal(t, 1, sessionCancels(stdin))
				response = `{"stopReason":"cancelled","_meta":{}}`
			}

			a.FinishPromptRequestForTest(fixtureS500Session, json.RawMessage(response), nil)
			assert.Equal(t, providerkittest.TurnEndThen(providerkit.CompactionEnd()), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			assert.Equal(t, providerkit.TurnEndedOnCompaction, sink.LastAutoCancelReason())
		})
	}
}

// The daemon states the compaction's end twice on its event stream, in
// session.compacted and in the idle status, and the ACP adapter answers the
// prompt. The two streams have no order between them. In either order, only
// the prompt's answer applies a verdict, and it applies one.
func TestOpenCodeCompactionEndAppliesOneVerdict(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	daemonEnd := []string{
		`{"type":"session.compacted","properties":{"sessionID":"` + fixtureS500Session + `"}}`,
		`{"type":"session.status","properties":{"sessionID":"` + fixtureS500Session + `","status":{"type":"idle"}}}`,
	}
	for name, eventsFirst := range map[string]bool{"the events arrive first": true, "the answer arrives first": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, _, _, _ := newRetryTestAgent(t, fixtureS500Session, now)
			startPrompt(t, a, sendInput, "/compact")
			observeSummary(t, a, fixtureS500Session, "msg_summary", now)
			if eventsFirst {
				for _, frame := range daemonEnd {
					feedFrame(t, a, frame)
				}
				assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()), "the daemon's events end no turn")
			}
			a.FinishPromptRequestForTest(fixtureS500Session, json.RawMessage(fixtureOKACP), nil)
			if !eventsFirst {
				for _, frame := range daemonEnd {
					feedFrame(t, a, frame)
				}
			}
			assert.Equal(t, providerkittest.TurnEndThen(providerkit.CompactionEnd()), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			assert.Equal(t, 1, sink.AutoCancelCount())
		})
	}
}

// The daemon can wait out a usage limit for hours inside a compaction. LeapMux
// leaves that wait to the daemon: a stop would only abandon the compaction,
// and the compaction's end schedules no retry.
func TestOpenCodeLongWaitDuringACompactionStopsNothing(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	a, sink, peer, _, stdin := newRetryTestAgent(t, fixtureS500Session, now)
	startPrompt(t, a, sendInput, "/compact")
	observeSummary(t, a, fixtureS500Session, "msg_summary", now)
	feedFrame(t, a, longWaitRetry(now.Add(5*time.Hour)))
	assert.Zero(t, sessionCancels(stdin), "the status stream's long wait stops nothing")
	statuses := `{"` + fixtureS500Session + `":` + longWaitStatus(now.Add(5*time.Hour)) + `}`
	peer.reply(statuses, http.StatusOK)
	a.readMissedSessionStatus(t.Context())
	assert.Zero(t, sessionCancels(stdin), "the missed status that the connection reads stops nothing")

	a.FinishPromptRequestForTest(fixtureS500Session, json.RawMessage(fixtureOKACP), nil)
	assert.Equal(t, providerkittest.TurnEndThen(providerkit.CompactionEnd()), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
	assert.Zero(t, sink.AutoScheduleCount())
}

// longWaitStatus is the retry status of longWaitRetry alone, as the daemon's
// session status route lists it.
func longWaitStatus(next time.Time) string {
	return `{"type":"retry","attempt":1,"message":"Internal server error","next":` + strconv.FormatInt(next.UnixMilli(), 10) + `}`
}

// The mark of a compaction belongs to its own prompt. The prompt after it
// takes the recipe's verdict, and its long wait ends the turn again.
func TestOpenCodePromptAfterACompactionAppliesItsOwnVerdict(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	t.Run("a failure schedules the server-error retry", func(t *testing.T) {
		t.Parallel()
		a, sink, peer, _, _ := newRetryTestAgent(t, fixtureS500Session, now)
		startPrompt(t, a, sendInput, "/compact")
		a.FinishPromptRequestForTest(fixtureS500Session, json.RawMessage(fixtureOKACP), nil)
		compactionOps := len(sink.Timeline())

		require.NoError(t, a.SendInput("Continue the task.", nil))
		observeAssistant(t, a, fixtureS500Session, "msg_next", now)
		failWithStoredServerError(t, a, peer, "msg_next", now)
		assert.Equal(t, []string{"leapmux_notification", "schedule_server_error"}, retryTimeline(sink)[compactionOps:])
	})
	t.Run("a long wait ends the turn", func(t *testing.T) {
		t.Parallel()
		a, _, _, _, stdin := newRetryTestAgent(t, fixtureS500Session, now)
		startPrompt(t, a, sendInput, "/compact")
		a.FinishPromptRequestForTest(fixtureS500Session, json.RawMessage(fixtureOKACP), nil)

		require.NoError(t, a.SendInput("Continue the task.", nil))
		feedFrame(t, a, longWaitRetry(now.Add(5*time.Hour)))
		assert.Equal(t, 1, sessionCancels(stdin))
	})
}

// Only a prompt of the /compact command alone starts a compaction. Every
// other prompt takes the recipe's verdict, so its 500 schedules the
// server-error retry.
func TestOpenCodeOtherInputIsNoCompaction(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	attachment := []*leapmuxv1.Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("Keep the plan.")}}
	for name, input := range map[string]struct {
		content     string
		attachments []*leapmuxv1.Attachment
	}{
		"the command with an attachment":      {"/compact", attachment},
		"summarize, which the adapter skips":  {"/summarize", nil},
		"a longer command name":               {"/compactor", nil},
		"the word without a slash":            {"compact", nil},
		"a message that mentions the command": {"Run /compact later.", nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, peer, _, _ := newRetryTestAgent(t, fixtureS500Session, now)
			startPrompt(t, a, func(a *Agent, content string) error { return a.SendInput(content, input.attachments) }, input.content)
			observeAssistant(t, a, fixtureS500Session, "msg_failed", now)

			failWithStoredServerError(t, a, peer, "msg_failed", now)
			assert.Equal(t, []string{"leapmux_notification", "schedule_server_error"}, retryTimeline(sink))
		})
	}
}

// A /compact text that the reader steers into a running prompt joins that
// conversation turn and marks nothing. The turn's 500 still schedules the
// server-error retry.
func TestOpenCodeSteeredCompactCommandMarksNothing(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	a, sink, peer, _, _ := newRetryTestAgent(t, fixtureS500Session, now)
	startPrompt(t, a, sendInput, "Fix the failing test.")
	require.NoError(t, a.SteerInput("/compact keep the plan", nil))
	observeAssistant(t, a, fixtureS500Session, "msg_failed", now)

	failWithStoredServerError(t, a, peer, "msg_failed", now)
	assert.Equal(t, []string{"leapmux_notification", "schedule_server_error"}, retryTimeline(sink))
}

// A /compact for a session that is no longer current starts no prompt, as the
// base refuses any input for it.
func TestOpenCodeCompactionForAnotherSessionStartsNothing(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	a, _, _, _, stdin := newRetryTestAgent(t, fixtureS500Session, now)
	a.SetPromptActiveForTest(false)

	require.ErrorIs(t, a.SendInputForSession("ses_replaced", "/compact", nil), agent.ErrInputSessionChanged)
	assert.False(t, a.PromptActive())
	assert.NotContains(t, stdin.String(), `"method":"session/prompt"`)
}

// The preparation of a compaction prompt checks the session that the base
// claimed for the prompt, because the session can change after the check
// before the send. It marks only a prompt of the expected session.
func TestOpenCodeCompactionPreparationChecksTheClaimedSession(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	expected := fixtureS500Session
	for name, tc := range map[string]struct {
		expected *string
		claimed  string
		marks    bool
	}{
		"the expected session":            {&expected, fixtureS500Session, true},
		"a session that replaced it":      {&expected, "ses_replaced", false},
		"any session for the current one": {nil, "ses_replaced", true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, _, _, _, _ := newRetryTestAgent(t, fixtureS500Session, now)
			err := a.prepareCompactionPrompt(tc.expected)(tc.claimed)
			if tc.marks {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, agent.ErrInputSessionChanged)
			}
			assert.Equal(t, tc.marks, a.TurnEndVerdict(nil, false).Failure == providerkit.CompactionEnd())
		})
	}
}

// Kilo's offline status is the daemon's own wait for the network. It ends no
// turn, so it makes no retry call.
func TestOpenCodeOfflineStatusMakesNoRetryCall(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T01:16:51Z")
	a, sink, _, _, stdin := newRetryTestAgent(t, "ses_e7fea913affefUN8JqZHnkYI1C", now)
	// "KILO offline" of R/opencode-kilo-mimo.md.
	feedFrame(t, a, `{"id":"evt_180157100001f64AwjiJ6oL8Gj","type":"session.status","properties":{"sessionID":"ses_e7fea913affefUN8JqZHnkYI1C","status":{"type":"offline","requestID":"que_1801570ff001IejDSaBFl3rUzk","message":"Connection refused"}}}`)
	assert.Empty(t, sink.Timeline())
	assert.Zero(t, sessionCancels(stdin))
}

// decodeDaemonError decodes an error object of a fixture.
func decodeDaemonError(t *testing.T, raw string) DaemonError {
	t.Helper()
	var failure DaemonError
	require.NoError(t, json.Unmarshal([]byte(raw), &failure))
	return failure
}

// verdictShape is what the queue acts on: the kind, the reset instant, and the
// window. The cause is the log's.
type verdictShape struct {
	Kind    providerkit.TurnFailureKind
	ResetAt time.Time
	Window  providerkit.UsageWindow
}

func shapeOf(failure providerkit.TurnFailure) verdictShape {
	return verdictShape{Kind: failure.Kind, ResetAt: failure.ResetAt, Window: failure.Window}
}

func TestClassifyDaemonError(t *testing.T) {
	t.Parallel()
	notRetryable := verdictShape{Kind: providerkit.TurnFailureNotRetryable}
	transient := verdictShape{Kind: providerkit.TurnFailureTransient}
	rateLimited := verdictShape{Kind: providerkit.TurnFailureRateLimited}
	billing := verdictShape{Kind: providerkit.TurnFailureBillingBlocked}
	usageAt := func(instant string) verdictShape {
		return verdictShape{Kind: providerkit.TurnFailureUsageLimited, ResetAt: mustTime(t, instant)}
	}
	for _, tc := range []struct {
		name       string
		failure    string
		observedAt string
		want       verdictShape
	}{
		{"a 500 is transient, and its retry-after-ms changes nothing", sessionErrorObject(t, fixtureS500Error), "2026-10-28T00:56:47Z", transient},
		{"a Go usage limit with a retry-after lifts at the date header plus the delay", sessionErrorObject(t, fixtureGo429Error), "2026-10-28T00:57:13Z", usageAt("2026-10-28T00:57:15Z")},
		{"a Go usage limit whose deadline passed is a rate limit", sessionErrorObject(t, fixtureGo429Error), "2026-10-28T00:57:16Z", rateLimited},
		{"the Zen CreditsError is a billing block", sessionErrorObject(t, fixtureCreditsError), "2026-10-28T00:57:31Z", billing},
		{"a refused connection states no status and is transient", sessionErrorObject(t, fixtureRefusedError), "2026-10-28T01:00:00Z", transient},
		{"the real Z.AI 1310 lifts at its UTC+8 stamp plus one second", fixtureZAI1310Error, "2026-09-18T09:30:59Z", usageAt("2026-09-19T12:42:59Z")},
		{"the collision of a Z.AI 1302 and a retry-after lifts at the deadline", sessionErrorObject(t, fixtureRateLimitWithDeadline), "2026-10-28T00:56:47Z", usageAt("2026-10-28T01:56:47Z")},
		// Real captures of R/transcripts-mimo-opencode.md (.tmp/transcripts/mimo-opencode/oc-apierror.log).
		{"the real Zen 503 is transient", `{"name":"APIError","data":{"message":"Error from provider (Console): Upstream request failed: Endpoint is unavailable.","statusCode":503,"isRetryable":true,"responseHeaders":{"cf-placement":"remote-ORD","cf-ray":"a31a948b29f87654-SEA","connection":"keep-alive","content-length":"126","content-type":"application/json","date":"Thu, 27 Aug 2026 11:01:47 GMT","server":"cloudflare"},"responseBody":"{\"error\":{\"type\":\"server_error\",\"message\":\"Error from provider (Console): Upstream request failed: Endpoint is unavailable.\"}}","metadata":{"url":"https://opencode.ai/zen/v1/chat/completions"}}}`, "2026-08-27T11:01:47Z", transient},
		{"the real Z.AI 400 is not retryable", `{"name":"APIError","data":{"message":"reasoning_effort must be one of: none, minimal, low, medium, high, xhigh, max","statusCode":400,"isRetryable":false,"responseHeaders":{"connection":"keep-alive","date":"Wed, 15 Jul 2026 11:12:17 GMT","keep-alive":"timeout=6","server":"nginx","strict-transport-security":"max-age=31536000; includeSubDomains","transfer-encoding":"chunked","vary":"Origin, Access-Control-Request-Method, Access-Control-Request-Headers, Origin, Access-Control-Request-Method, Access-Control-Request-Headers","x-log-id":"202607151912170038eeeb1e32448c"},"responseBody":"{\"error\":{\"code\":\"1210\",\"message\":\"reasoning_effort must be one of: none, minimal, low, medium, high, xhigh, max\"}}","metadata":{"url":"https://api.z.ai/api/coding/paas/v4/chat/completions"}}}`, "2026-07-15T11:12:17Z", notRetryable},
		{"the real abort is not retryable", `{"name":"MessageAbortedError","data":{"message":"Aborted"}}`, "2026-10-28T01:00:00Z", notRetryable},
		// CONSTRUCTED from the shapes of R/opencode-kilo-mimo.md: no probe captured them.
		{"a 429 that states no deadline is a rate limit", `{"name":"APIError","data":{"message":"Rate limit exceeded. Please try again later.","statusCode":429,"isRetryable":true,"responseBody":"{\"type\": \"error\", \"error\": {\"type\": \"RateLimitError\", \"message\": \"Rate limit exceeded. Please try again later.\"}}"}}`, "2026-10-28T01:00:00Z", rateLimited},
		{"a Zen usage limit with no status is a rate limit", `{"name":"APIError","data":{"message":"Go subscription weekly limit exceeded.","isRetryable":false,"responseBody":"{\"type\": \"error\", \"error\": {\"type\": \"GoUsageLimitError\", \"message\": \"Go subscription weekly limit exceeded.\"}}"}}`, "2026-10-28T01:00:00Z", rateLimited},
		{"a deadline more than 31 days ahead leaves a rate limit", `{"name":"APIError","data":{"message":"Rate limit exceeded.","statusCode":429,"isRetryable":true,"responseHeaders":{"date":"Wed, 28 Oct 2026 01:00:00 GMT","retry-after":"2764800"}}}`, "2026-10-28T01:00:00Z", rateLimited},
		{"an HTTP 402 is a billing block", `{"name":"APIError","data":{"message":"Payment required","statusCode":402,"isRetryable":false}}`, "2026-10-28T01:00:00Z", billing},
		{"the Zen CreditsError type alone is a billing block", `{"name":"APIError","data":{"message":"Payment needed","statusCode":401,"isRetryable":false,"responseBody":"{\"type\": \"error\", \"error\": {\"type\": \"CreditsError\", \"message\": \"Payment needed\"}}"}}`, "2026-10-28T01:00:00Z", billing},
		{"a Zen error that states no type is the status's", `{"name":"APIError","data":{"message":"Bad request","statusCode":400,"isRetryable":false,"responseBody":"{\"error\":\"Bad request\"}"}}`, "2026-10-28T01:00:00Z", notRetryable},
		{"a DashScope token throttle is a rate limit, not a billing block", `{"name":"APIError","data":{"message":"You exceeded your current quota, please check your plan and billing details. See https://help.aliyun.com/zh/model-studio/error-code#token-limit","statusCode":429,"isRetryable":true,"responseBody":"{\"error\":{\"code\":\"insufficient_quota\",\"message\":\"You exceeded your current quota. See https://help.aliyun.com/zh/model-studio/error-code#token-limit\"}}"}}`, "2026-10-28T01:00:00Z", rateLimited},
		{"a Z.AI overload on a 429 is transient, and a deadline does not change it", `{"name":"APIError","data":{"message":"The service may be temporarily overloaded, please try again later","statusCode":429,"isRetryable":true,"responseHeaders":{"date":"Wed, 28 Oct 2026 01:00:00 GMT","retry-after":"3600"},"responseBody":"{\"error\":{\"code\":\"1305\",\"message\":\"The service may be temporarily overloaded, please try again later\"}}"}}`, "2026-10-28T01:00:00Z", transient},
		{"the ACP answer alone states no retryable class", `{"name":"APIError","data":{"message":"Internal error: Internal server error"}}`, "2026-10-28T01:00:00Z", notRetryable},
		{"an empty error is not retryable", `{}`, "2026-10-28T01:00:00Z", notRetryable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyDaemonError(decodeDaemonError(t, tc.failure), nil, mustTime(t, tc.observedAt))
			assert.Equal(t, tc.want, shapeOf(got))
		})
	}
}

// The member's vocabulary decides before the family recipe, and a member that
// declines leaves the decision to the recipe.
func TestClassifyDaemonErrorAsksTheMemberFirst(t *testing.T) {
	t.Parallel()
	failure := decodeDaemonError(t, sessionErrorObject(t, fixtureGo429Error))
	observedAt := mustTime(t, "2026-10-28T00:57:13Z")
	var seen []DaemonError
	decides := func(failure DaemonError) (providerkit.TurnFailure, bool) {
		seen = append(seen, failure)
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable, Cause: "the member decided"}, true
	}
	assert.Equal(t, providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable, Cause: "the member decided"},
		classifyDaemonError(failure, decides, observedAt))
	assert.Equal(t, []DaemonError{failure}, seen, "the member reads the whole error")

	declines := func(DaemonError) (providerkit.TurnFailure, bool) { return providerkit.TurnFailure{}, false }
	assert.Equal(t, verdictShape{Kind: providerkit.TurnFailureUsageLimited, ResetAt: mustTime(t, "2026-10-28T00:57:15Z")},
		shapeOf(classifyDaemonError(failure, declines, observedAt)))
}

// A stored page that holds no assistant error of the prompt is no read, so the
// held event decides.
func TestOpenCodeStoredReadThatHoldsNoAssistantErrorFallsBackToTheHeldEvent(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:57:13Z")
	for name, page := range map[string]string{
		"an empty page":                `[]`,
		"a page that is no list":       `{"info":{}}`,
		"the newest message is a user": `[{"info":{"id":"msg_user","role":"user","time":{"created":1793149033000}},"parts":[]}]`,
		"an assistant without error":   `[{"info":{"id":"msg_failed","role":"assistant","time":{"created":1793149033000}},"parts":[]}]`,
		"an error of another shape":    `[{"info":{"id":"msg_failed","role":"assistant","time":{"created":1793149033000},"error":"failed"},"parts":[]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, peer, _, _ := newRetryTestAgent(t, fixtureGo429Session, now)
			observeAssistant(t, a, fixtureGo429Session, "msg_failed", now)
			feedFrame(t, a, fixtureGo429Error)
			peer.reply(page, http.StatusOK)
			a.FinishPromptRequestForTest(fixtureGo429Session, nil, acpFailure(t, fixtureS500ACPFailure))
			assert.Equal(t, []string{
				"leapmux_notification", "schedule_usage_limit@2026-10-28T00:57:15Z", "cancel_server_error",
			}, retryTimeline(sink))
		})
	}
}

// A session.error with no readable error, or of another session, is not held.
func TestOpenCodeSessionErrorThatStatesNoErrorIsNotHeld(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:57:13Z")
	for name, frame := range map[string]string{
		"no error":          `{"type":"session.error","properties":{"sessionID":"ses_e7ffd4ef4ffePSnIMZOSL7HWFU"}}`,
		"a malformed error": `{"type":"session.error","properties":{"sessionID":"ses_e7ffd4ef4ffePSnIMZOSL7HWFU","error":{"name":7}}}`,
		"another session":   strings.Replace(fixtureGo429Error, fixtureGo429Session, "ses_other", 1),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, peer, _, _ := newRetryTestAgent(t, fixtureGo429Session, now)
			observeAssistant(t, a, fixtureGo429Session, "msg_failed", now)
			feedFrame(t, a, frame)
			peer.reply("the store is unavailable", http.StatusServiceUnavailable)
			a.FinishPromptRequestForTest(fixtureGo429Session, nil, acpFailure(t, fixtureS500ACPFailure))
			assert.Equal(t, []string{"leapmux_notification", "cancel_server_error"}, retryTimeline(sink))
		})
	}
}

// A newer session.error replaces the held one.
func TestOpenCodeHeldEventIsTheNewest(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:57:13Z")
	a, sink, peer, _, _ := newRetryTestAgent(t, fixtureGo429Session, now)
	observeAssistant(t, a, fixtureGo429Session, "msg_failed", now)
	feedFrame(t, a, strings.Replace(fixtureS500Error, fixtureS500Session, fixtureGo429Session, 1))
	feedFrame(t, a, fixtureGo429Error)
	peer.reply("the store is unavailable", http.StatusServiceUnavailable)
	a.FinishPromptRequestForTest(fixtureGo429Session, nil, acpFailure(t, fixtureS500ACPFailure))
	assert.Equal(t, []string{
		"leapmux_notification", "schedule_usage_limit@2026-10-28T00:57:15Z", "cancel_server_error",
	}, retryTimeline(sink))
}

// The event stream connects only after discovery, so a prompt can fail before
// it is open. A long wait states one retry status and then sleeps for hours,
// so the status that the stream missed must come from the status read that
// runs when the stream connects.
func TestOpenCodeLongWaitStatedBeforeTheStreamConnectedStopsTheTurn(t *testing.T) {
	t.Parallel()
	now := mustTime(t, "2026-10-28T00:56:46Z")
	next := now.Add(5 * time.Hour)
	statuses := func(sessionID, status string) string {
		return `{"` + sessionID + `":` + status + `}`
	}
	retry := `{"type":"retry","attempt":1,"message":"Rate limit reached for requests.","next":` + strconv.FormatInt(next.UnixMilli(), 10) + `}`

	t.Run("a retry status of the current session", func(t *testing.T) {
		t.Parallel()
		a, sink, peer, _, stdin := newRetryTestAgent(t, fixtureS500Session, now)
		peer.reply(statuses(fixtureS500Session, retry), http.StatusOK)

		a.readMissedSessionStatus(t.Context())
		assert.Equal(t, 1, sessionCancels(stdin), "the missed long wait stops the turn")
		feedFrame(t, a, longWaitRetry(next))
		assert.Equal(t, 1, sessionCancels(stdin), "the same wait on the stream stops nothing more")

		a.FinishPromptRequestForTest(fixtureS500Session, json.RawMessage(fixtureOKACP), nil)
		assert.Equal(t, []string{
			"notification", "turn_end", "schedule_usage_limit@2026-10-28T05:56:46Z", "cancel_server_error",
		}, retryTimeline(sink))
		assert.JSONEq(t,
			`{"type":"session.status","properties":{"sessionID":"`+fixtureS500Session+`","status":`+retry+`}}`,
			string(sink.LastAutoSchedule().SourcePayload))
	})
	for name, reply := range map[string]struct {
		body   string
		status int
	}{
		"a busy session":                  {statuses(fixtureS500Session, `{"type":"busy"}`), http.StatusOK},
		"a retry of another session":      {statuses("ses_other", retry), http.StatusOK},
		"a daemon without the route":      {`not found`, http.StatusNotFound},
		"a malformed answer":              {`[]`, http.StatusOK},
		"a retry that waits five minutes": {statuses(fixtureS500Session, `{"type":"retry","attempt":1,"message":"x","next":`+strconv.FormatInt(now.Add(5*time.Minute).UnixMilli(), 10)+`}`), http.StatusOK},
	} {
		t.Run(name+" stops nothing", func(t *testing.T) {
			t.Parallel()
			a, _, peer, _, stdin := newRetryTestAgent(t, fixtureS500Session, now)
			peer.reply(reply.body, reply.status)

			a.readMissedSessionStatus(t.Context())
			assert.Zero(t, sessionCancels(stdin))
		})
	}
	t.Run("no prompt reads nothing", func(t *testing.T) {
		t.Parallel()
		a, _, peer, _, stdin := newRetryTestAgent(t, fixtureS500Session, now)
		a.SetPromptActiveForTest(false)
		peer.reply(statuses(fixtureS500Session, retry), http.StatusOK)

		a.readMissedSessionStatus(t.Context())
		assert.Zero(t, sessionCancels(stdin))
		peer.mu.Lock()
		defer peer.mu.Unlock()
		assert.Empty(t, peer.paths, "no turn runs, so no status is read")
	})
}
