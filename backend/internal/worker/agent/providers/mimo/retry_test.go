package mimo

import (
	"encoding/json"
	"errors"
	"net/http"
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

// The session.error frames below are verbatim from the MiMo Code 0.1.15
// fixtures of R/opencode-kilo-mimo.md (.tmp/probe/fixtures.txt), unless a
// comment states otherwise. A test moves each into the test session with
// inTestSession, because the agent reads only its own session.
const (
	// mimoRefusedError is "MIMO refused error": a refused connection, which
	// states no status.
	mimoRefusedError = `{"type":"session.error","properties":{"sessionID":"ses_ffe5e7fde803dffewgQ7s30tZ7","error":{"name":"APIError","data":{"message":"Cannot connect to API: Unable to connect. Is the computer able to access the url?","isRetryable":true,"metadata":{"message":"Cannot connect to API: Unable to connect. Is the computer able to access the url?","causeChain":"[{\"depth\":0,\"name\":\"AI_APICallError\",\"message\":\"Cannot connect to API: Unable to connect. Is the computer able to access the url?\",\"source\":\"root\"},{\"depth\":1,\"name\":\"Error\",\"message\":\"Unable to connect. Is the computer able to access the url?\",\"code\":\"ConnectionRefused\",\"source\":\"cause\"}]"}}}}}`
	// mimoFreeLimitError is "MIMO free error".
	mimoFreeLimitError = `{"type":"session.error","properties":{"sessionID":"ses_ffe5e7fe59d4bffeXAah9l3Afo","error":{"name":"APIError","data":{"message":"Rate limit exceeded. Please try again later.","statusCode":429,"isRetryable":true,"responseHeaders":{"content-length":"134","content-type":"application/json","date":"Wed, 28 Oct 2026 01:22:57 GMT","retry-after":"2","server":"BaseHTTP Python/3.15.1"},"responseBody":"{\"type\": \"error\", \"error\": {\"type\": \"FreeUsageLimitError\", \"message\": \"Rate limit exceeded. Please try again later.\"}, \"metadata\": {}}","metadata":{"providerID":"fake","allow404Retry":"true","url":"http://127.0.0.1:18765/free429/v1/chat/completions"}}}}}`
	// mimoPaymentError is "MIMO 402 error".
	mimoPaymentError = `{"type":"session.error","properties":{"sessionID":"ses_ffe5e7fdfbbb3ffe3r3kioy8Sz","error":{"name":"APIError","data":{"message":"Insufficient account balance","statusCode":402,"isRetryable":false,"responseHeaders":{"content-length":"101","content-type":"application/json","date":"Wed, 28 Oct 2026 01:29:21 GMT","server":"BaseHTTP Python/3.15.1"},"responseBody":"{\"error\": {\"code\": \"402\", \"type\": \"insufficient_balance\", \"message\": \"Insufficient account balance\"}}","metadata":{"providerID":"fake","allow404Retry":"true","url":"http://127.0.0.1:18766/mimo402/v1/chat/completions"}}}}}`
	// mimoMembershipError is "MIMO member error".
	mimoMembershipError = `{"type":"session.error","properties":{"sessionID":"ses_ffe5e7fdfe412ffehw9m2WS72x","error":{"name":"APIError","data":{"message":"未开通会员或会员已到期，请订阅后使用","statusCode":403,"isRetryable":false,"responseHeaders":{"content-length":"212","content-type":"application/json","date":"Wed, 28 Oct 2026 01:29:11 GMT","server":"BaseHTTP Python/3.15.1"},"responseBody":"{\"error\": {\"code\": \"membership_required\", \"type\": \"permission_error\", \"biz_code\": 30012, \"message\": \"\\u672a\\u5f00\\u901a\\u4f1a\\u5458\\u6216\\u4f1a\\u5458\\u5df2\\u5230\\u671f\\uff0c\\u8bf7\\u8ba2\\u9605\\u540e\\u4f7f\\u7528\"}}","metadata":{"providerID":"fake","allow404Retry":"true","url":"http://127.0.0.1:18766/mimomember403/v1/chat/completions"}}}}}`
	// mimoXiaomiServerError is the real Xiaomi 500 of
	// R/transcripts-mimo-opencode.md. MiMo logged only the AI SDK error
	// (.tmp/transcripts/mimo-opencode/tables.md), so the session.error envelope
	// is CONSTRUCTED around its status, headers, and body, which are verbatim.
	mimoXiaomiServerError = `{"type":"session.error","properties":{"sessionID":"ses_ffe5f2791fe34ffeQ55uzj4YY9","error":{"name":"APIError","data":{"message":"Internal Server Error","statusCode":500,"isRetryable":true,"responseHeaders":{"connection":"keep-alive","content-length":"102","content-type":"application/json","date":"Fri, 25 Sep 2026 15:56:51 GMT","server":"MiFE/3.4.29","vary":"Origin, Access-Control-Request-Method, Access-Control-Request-Headers","x-mife-upstream-status":"500"},"responseBody":"{\"error\":{\"code\":\"503\",\"message\":\"Internal Server Error\",\"param\":\"\",\"type\":\"ServiceUnavailableError\"}}","metadata":{"url":"https://token-plan-sgp.xiaomimimo.com/v1/chat/completions"}}}}}`
	// mimoWeeklyQuotaError is CONSTRUCTED: MiMo 0.1.15 retries Xiaomi's weekly
	// quota for ever, so no capture holds its session.error. The body is the
	// gateway's 403 of R/vendors.md section 4.
	mimoWeeklyQuotaError = `{"type":"session.error","properties":{"sessionID":"ses_test","error":{"name":"APIError","data":{"message":"本周用量已满，请等待额度重置或升级套餐","statusCode":403,"isRetryable":false,"responseBody":"{\"error\":{\"code\":\"subscription_quota_exhausted\",\"type\":\"permission_error\",\"biz_code\":30011,\"message\":\"本周用量已满，请等待额度重置或升级套餐\"}}"}}}}`
	// mimoRateLimitWithDeadline is CONSTRUCTED: the reviewed collision, a 429
	// whose body holds Z.AI 1302 (R/vendors.md section 1), with a retry-after
	// of one hour beside the date header.
	mimoRateLimitWithDeadline = `{"type":"session.error","properties":{"sessionID":"ses_test","error":{"name":"APIError","data":{"message":"Rate limit reached for requests","statusCode":429,"isRetryable":true,"responseHeaders":{"content-type":"application/json","date":"Wed, 28 Oct 2026 01:22:57 GMT","retry-after":"3600"},"responseBody":"{\"error\":{\"code\":\"1302\",\"message\":\"Rate limit reached for requests\"}}"}}}}`
)

// inTestSession moves a fixture frame into the test session.
func inTestSession(t *testing.T, frame string) []byte {
	t.Helper()
	start := strings.Index(frame, `"sessionID":"`)
	require.NotEqual(t, -1, start)
	start += len(`"sessionID":"`)
	end := strings.IndexByte(frame[start:], '"')
	require.NotEqual(t, -1, end)
	return []byte(frame[:start] + testSessionID + frame[start+end:])
}

func mustMiMoTime(t *testing.T, value string) time.Time {
	t.Helper()
	instant, err := time.Parse(time.RFC3339, value)
	require.NoError(t, err)
	return instant
}

// mimoRetryTimeline states each sink operation in call order, with the reset
// instant of a usage-limit schedule.
func mimoRetryTimeline(sink *agenttest.Sink) []string {
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

// A failed main turn persists its divider, then applies the verdict of its
// failure, with the failure as the payload.
func TestMiMoFailedTurnAppliesTheVerdictAfterTheDivider(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		frame      []byte
		observedAt string
		want       []string
	}{
		{"the real Xiaomi 500 is transient", inTestSession(t, mimoXiaomiServerError), "2026-09-25T15:56:51Z",
			[]string{"turn_end", "schedule_server_error"}},
		{"a refused connection is transient", inTestSession(t, mimoRefusedError), "2026-10-28T01:22:57Z",
			[]string{"turn_end", "schedule_server_error"}},
		{"a free limit lifts at the date header plus the retry-after", inTestSession(t, mimoFreeLimitError), "2026-10-28T01:22:57Z",
			[]string{"turn_end", "schedule_usage_limit@2026-10-28T01:22:59Z", "cancel_server_error"}},
		{"a free limit whose deadline passed is a rate limit", inTestSession(t, mimoFreeLimitError), "2026-10-28T01:23:00Z",
			[]string{"turn_end", "schedule_server_error"}},
		{"a 402 cancels both retries", inTestSession(t, mimoPaymentError), "2026-10-28T01:29:21Z",
			[]string{"turn_end", "cancel_usage_limit", "cancel_server_error"}},
		{"a missing membership cancels both retries", inTestSession(t, mimoMembershipError), "2026-10-28T01:29:11Z",
			[]string{"turn_end", "cancel_usage_limit", "cancel_server_error"}},
		{"the weekly quota retries on the server-error backoff", []byte(mimoWeeklyQuotaError), "2026-10-28T01:29:11Z",
			[]string{"turn_end", "schedule_server_error"}},
		{"the collision of a Z.AI 1302 and a retry-after lifts at the deadline", []byte(mimoRateLimitWithDeadline), "2026-10-28T01:22:57Z",
			[]string{"turn_end", "schedule_usage_limit@2026-10-28T02:22:57Z", "cancel_server_error"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			clock := useMockClock(t, a)
			clock.Set(mustMiMoTime(t, tc.observedAt)).MustWait(testutil.DeadlineContext(t))
			feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), tc.frame, statusEvent(t, contracts.MiMoStatusTypeIdle))
			require.Equal(t, tc.want, mimoRetryTimeline(sink))
			if sink.AutoScheduleCount() > 0 {
				assert.JSONEq(t, string(tc.frame), string(sink.LastAutoSchedule().SourcePayload))
			}
		})
	}
}

// A turn that ends well cancels the server-error retry.
func TestMiMoTurnThatEndsWellCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), statusEvent(t, contracts.MiMoStatusTypeIdle))
	assert.Equal(t, []string{"turn_end", "cancel_server_error"}, mimoRetryTimeline(sink))
}

// A failure that states no error name is still the turn's failure, and its
// turn end applies one verdict.
func TestMiMoFailureWithoutANameAppliesOneVerdict(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
		eventJSON(t, contracts.MiMoEventSessionError, map[string]any{"sessionID": testSessionID, "error": map[string]any{"data": map[string]any{"message": "no name"}}}),
		statusEvent(t, contracts.MiMoStatusTypeIdle))
	assert.Equal(t, []string{"turn_end", "cancel_server_error"}, mimoRetryTimeline(sink))
}

// The reader's stop wins over the failure that the stopped turn reported. The
// failure becomes a notification after the divider.
func TestMiMoInterruptedTurnCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	clock := useMockClock(t, a)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))
	interruptOnClock(t, a, clock)
	feed(a, inTestSession(t, mimoXiaomiServerError), statusEvent(t, contracts.MiMoStatusTypeIdle))
	assert.Equal(t, []string{"turn_end", "cancel_server_error", "notification"}, mimoRetryTimeline(sink))
}

// A subagent's failure stays the subagent's. The main turn ends well.
func TestMiMoSubagentFailureLeavesTheMainTurnNotRetryable(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	feed(a,
		inTestSession(t, mimoXiaomiServerError),
		failedMessageEvent(t, "msg_c2", actorID, "APIError", "Internal Server Error"),
		statusEvent(t, contracts.MiMoStatusTypeIdle),
	)
	timeline := mimoRetryTimeline(sink)
	require.NotEmpty(t, timeline)
	assert.Equal(t, "cancel_server_error", timeline[len(timeline)-1])
	assert.Zero(t, sink.AutoScheduleCount())
	assert.Equal(t, 1, sink.AutoCancelCount(), "the main turn end decides once")
}

// A divider whose write failed keeps its verdict. The idle that retries the
// write decides nothing again.
func TestMiMoRetainedTurnEndDecidesOnce(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &rejectedFailureReceiptSink{ProviderServices: a.sink}
	a.sink = controlled
	controlled.dividerReject.Store(1)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), inTestSession(t, mimoXiaomiServerError),
		statusEvent(t, contracts.MiMoStatusTypeIdle))
	assert.Equal(t, []string{"schedule_server_error"}, mimoRetryTimeline(sink), "the verdict follows the write attempt")
	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle), statusEvent(t, contracts.MiMoStatusTypeIdle))
	assert.Equal(t, []string{"schedule_server_error", "turn_end"}, mimoRetryTimeline(sink))
	assert.Equal(t, 1, sink.AutoScheduleCount())
}

// decodeMiMoError decodes the error of a session.error frame, as handleSessionError does.
func decodeMiMoError(t *testing.T, frame string) mimoError {
	t.Helper()
	var event struct {
		Properties mimoErrorEvent `json:"properties"`
	}
	require.NoError(t, json.Unmarshal([]byte(frame), &event))
	return event.Properties.Error
}

// mimoErrorFrame is a session.error of the test session that carries errorObject.
func mimoErrorFrame(errorObject string) string {
	return `{"type":"session.error","properties":{"sessionID":"ses_test","error":` + errorObject + `}}`
}

func TestMiMoTurnFailure(t *testing.T) {
	t.Parallel()
	type shape struct {
		Kind    providerkit.TurnFailureKind
		ResetAt time.Time
		Window  providerkit.UsageWindow
	}
	notRetryable := shape{Kind: providerkit.TurnFailureNotRetryable}
	transient := shape{Kind: providerkit.TurnFailureTransient}
	rateLimited := shape{Kind: providerkit.TurnFailureRateLimited}
	billing := shape{Kind: providerkit.TurnFailureBillingBlocked}
	weekly := shape{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess}
	for _, tc := range []struct {
		name       string
		frame      string
		observedAt string
		want       shape
	}{
		{"the real Xiaomi 500 is transient", mimoXiaomiServerError, "2026-09-25T15:56:51Z", transient},
		{"a refused connection is transient", mimoRefusedError, "2026-10-28T01:22:57Z", transient},
		{"a free limit lifts at the date header plus the retry-after", mimoFreeLimitError, "2026-10-28T01:22:57Z",
			shape{Kind: providerkit.TurnFailureUsageLimited, ResetAt: mustMiMoTime(t, "2026-10-28T01:22:59Z")}},
		{"a free limit whose deadline passed is a rate limit", mimoFreeLimitError, "2026-10-28T01:22:59Z", rateLimited},
		{"a 402 is a billing block", mimoPaymentError, "2026-10-28T01:29:21Z", billing},
		{"a missing membership is a billing block", mimoMembershipError, "2026-10-28T01:29:11Z", billing},
		{"the weekly quota states no reset", mimoWeeklyQuotaError, "2026-10-28T01:29:11Z", weekly},
		{"the collision of a Z.AI 1302 and a retry-after lifts at the deadline", mimoRateLimitWithDeadline, "2026-10-28T01:22:57Z",
			shape{Kind: providerkit.TurnFailureUsageLimited, ResetAt: mustMiMoTime(t, "2026-10-28T02:22:57Z")}},
		// CONSTRUCTED from the shapes of R/vendors.md section 4 and R/opencode-kilo-mimo.md.
		{"the weekly quota's biz_code alone states the quota", mimoErrorFrame(`{"name":"APIError","data":{"message":"Weekly quota spent","statusCode":403,"isRetryable":false,"responseBody":"{\"error\":{\"type\":\"permission_error\",\"biz_code\":30011}}"}}`), "2026-10-28T01:00:00Z", weekly},
		{"the membership biz_code as text alone is a billing block", mimoErrorFrame(`{"name":"APIError","data":{"message":"Forbidden","statusCode":403,"isRetryable":false,"responseBody":"{\"error\":{\"type\":\"permission_error\",\"biz_code\":\"30012\"}}"}}`), "2026-10-28T01:00:00Z", billing},
		{"the Zen CreditsError is a billing block", mimoErrorFrame(`{"name":"APIError","data":{"message":"Payment needed","statusCode":401,"isRetryable":false,"responseBody":"{\"type\": \"error\", \"error\": {\"type\": \"CreditsError\", \"message\": \"Payment needed\"}}"}}`), "2026-10-28T01:00:00Z", billing},
		{"a 429 that states no deadline is a rate limit", mimoErrorFrame(`{"name":"APIError","data":{"message":"请求超限","statusCode":429,"isRetryable":true}}`), "2026-10-28T01:00:00Z", rateLimited},
		{"a Go usage limit with no status is a rate limit", mimoErrorFrame(`{"name":"APIError","data":{"message":"Go limit","isRetryable":false,"responseBody":"{\"type\": \"error\", \"error\": {\"type\": \"GoUsageLimitError\"}}"}}`), "2026-10-28T01:00:00Z", rateLimited},
		{"a deadline more than 31 days ahead leaves a rate limit", mimoErrorFrame(`{"name":"APIError","data":{"message":"Too many","statusCode":429,"isRetryable":true,"responseHeaders":{"date":"Wed, 28 Oct 2026 01:00:00 GMT","retry-after":"2764800"}}}`), "2026-10-28T01:00:00Z", rateLimited},
		{"an answer with nothing usable is transient", mimoErrorFrame(`{"name":"InvalidOutputError","data":{"message":"empty output"}}`), "2026-10-28T01:00:00Z", transient},
		{"a content filter is not retryable", mimoErrorFrame(`{"name":"ContentFilterError","data":{"message":"The response was withheld by the model provider's content safety filter."}}`), "2026-10-28T01:00:00Z", notRetryable},
		{"a 400 is not retryable", mimoErrorFrame(`{"name":"APIError","data":{"message":"Bad request","statusCode":400,"isRetryable":false}}`), "2026-10-28T01:00:00Z", notRetryable},
		{"an error that is a bare string in the body is read by its status", mimoErrorFrame(`{"name":"APIError","data":{"message":"Internal Server Error","isRetryable":false,"statusCode":502,"responseBody":"{\"error\":\"Internal Server Error\"}"}}`), "2026-10-28T01:00:00Z", transient},
		{"fields of an unexpected type read as absent", mimoErrorFrame(`{"name":"APIError","data":{"message":"The upstream refused the request","isRetryable":false,"statusCode":"500","responseHeaders":{"retry-after":7},"responseBody":{"error":"x"}}}`), "2026-10-28T01:00:00Z", notRetryable},
		{"an empty error is not retryable", mimoErrorFrame(`{}`), "2026-10-28T01:00:00Z", notRetryable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mimoTurnFailure(decodeMiMoError(t, tc.frame), mustMiMoTime(t, tc.observedAt))
			assert.Equal(t, tc.want, shape{Kind: got.Kind, ResetAt: got.ResetAt, Window: got.Window})
		})
	}
}

// A failure whose HTTP fields have an unexpected type is still the turn's
// failure: its divider records it, and its verdict reads the rest.
func TestMiMoFailureWithMalformedHTTPFieldsStaysTheDivider(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	frame := []byte(mimoErrorFrame(`{"name":"APIError","data":{"message":"The upstream refused the request","isRetryable":true,"statusCode":"500","responseHeaders":["date"],"responseBody":7}}`))
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), frame, statusEvent(t, contracts.MiMoStatusTypeIdle))
	require.Equal(t, []string{"turn_end", "schedule_server_error"}, mimoRetryTimeline(sink), "isRetryable still decides")
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Equal(t, agent.MessageCompletionError, rows[0].Completion)
	assert.JSONEq(t, string(frame), string(rows[0].Content))
}

// The verdict of one turn epoch applies once, whatever ends that turn again.
// No path ends one epoch twice today: endTurn clears the active turn, and only
// beginTurn starts the next epoch. The guard keeps a second end harmless.
func TestMiMoTurnVerdictAppliesOncePerEpoch(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	failure := &mimoFailure{raw: inTestSession(t, mimoXiaomiServerError), err: decodeMiMoError(t, mimoXiaomiServerError)}
	a.applyTurnVerdict(1, failure)
	a.applyTurnVerdict(1, failure)
	a.applyTurnVerdict(1, nil)
	a.applyTurnVerdict(2, nil)
	assert.Equal(t, []string{"schedule_server_error", "cancel_server_error"}, mimoRetryTimeline(sink))
}

// --- The automatic-retry verdict of a manual compaction ---

// mimoCompactionEnded is the verdict at the end of a manual compaction. It
// cancels the server-error retry and schedules nothing.
var mimoCompactionEnded = agenttest.TimelineEntry{
	Op: "cancel_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
	Reason: providerkit.TurnEndedOnCompaction,
}

// mimoTurnEndRow is the timeline entry of a turn-end divider.
var mimoTurnEndRow = agenttest.TimelineEntry{Op: "turn_end"}

// failedSummaryOf is the summary message of the compaction of messageID. The
// summary failed with the error of the session.error frame.
func failedSummaryOf(t *testing.T, messageID string, frame []byte) []byte {
	t.Helper()
	var event struct {
		Properties struct {
			Error json.RawMessage `json:"error"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(frame, &event))
	return eventJSON(t, eventMessageUpdated, map[string]any{"sessionID": testSessionID, "info": map[string]any{
		"id": "summary-" + messageID, "sessionID": testSessionID, "parentID": messageID, "role": roleAssistant, "agentID": mainActorID, "summary": true,
		"error": event.Properties.Error,
	}})
}

// startManualCompaction feeds the start of a manual compaction that
// CompactContext waits for: a busy status when busy is true, the compaction's
// user message, and its start part.
func startManualCompaction(t *testing.T, a *Agent, busy bool) {
	t.Helper()
	a.Mu.Lock()
	a.compactionAck = make(chan struct{})
	a.Mu.Unlock()
	if busy {
		feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))
	}
	feed(a, messageEvent(t, "compact-message", roleUser, mainActorID, false), compactionPhaseEvent(t, "compact-part", "compact-message", false))
}

// MiMo publishes the session.error of a failed compaction before its rollback.
// The failure is the divider of the compaction's turn, but its verdict is the
// compaction's: the turn schedules no retry.
func TestMiMoFailedManualCompactionSchedulesNoRetry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		frame      []byte
		observedAt string
		claimed    bool
	}{
		{"the real Xiaomi 500, which the turn end adopts", inTestSession(t, mimoXiaomiServerError), "2026-09-25T15:56:51Z", false},
		{"the real Xiaomi 500, which the summary claims", inTestSession(t, mimoXiaomiServerError), "2026-09-25T15:56:51Z", true},
		{"a refused connection", inTestSession(t, mimoRefusedError), "2026-10-28T01:22:57Z", true},
		{"a free limit that lifts at a stated time", inTestSession(t, mimoFreeLimitError), "2026-10-28T01:22:57Z", true},
		{"the weekly quota", []byte(mimoWeeklyQuotaError), "2026-10-28T01:29:11Z", true},
		{"a rate limit with a deadline", []byte(mimoRateLimitWithDeadline), "2026-10-28T01:22:57Z", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			clock := useMockClock(t, a)
			clock.Set(mustMiMoTime(t, tc.observedAt)).MustWait(testutil.DeadlineContext(t))
			startManualCompaction(t, a, true)

			feed(a, tc.frame)
			if tc.claimed {
				feed(a, failedSummaryOf(t, "compact-message", tc.frame))
			}
			feed(a, eventJSON(t, "message.removed", map[string]any{"sessionID": testSessionID, "messageID": "compact-message"}),
				statusEvent(t, contracts.MiMoStatusTypeIdle))

			assert.Equal(t, []agenttest.TimelineEntry{mimoTurnEndRow, mimoCompactionEnded}, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			rows := sink.Messages()
			require.Len(t, rows, 1)
			assert.Equal(t, agent.MessageCompletionError, rows[0].Completion, "the divider still reports the failure")
		})
	}
}

// A completed summary ends the compaction with no divider. It applies the
// compaction verdict once, also when MiMo repeats the part and the route
// replies later.
func TestMiMoCompletedManualCompactionAppliesTheCompactionVerdictOnce(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	startManualCompaction(t, a, true)
	completed := compactionPhaseEvent(t, "compact-part", "compact-message", true)

	feed(a, completed, completed, statusEvent(t, contracts.MiMoStatusTypeIdle))
	a.settleManualCompactionRoute(testSessionID, a.compactionRequestID, nil)

	assert.Equal(t, []agenttest.TimelineEntry{mimoCompactionEnded}, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
	assert.Equal(t, []string{"notification", "cancel_server_error"}, mimoRetryTimeline(sink), "the verdict follows the stored end")
}

// A route reply that ends the compaction before its completed part applies the
// compaction verdict once, whatever the reply states.
func TestMiMoManualCompactionRouteReplyAppliesTheCompactionVerdictOnce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		routeErr error
	}{
		{"a failed route", errors.New("summarize failed")},
		{"a route that found no summary", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			startManualCompaction(t, a, true)

			a.settleManualCompactionRoute(testSessionID, a.compactionRequestID, tc.routeErr)
			feed(a, compactionPhaseEvent(t, "compact-part", "compact-message", true), statusEvent(t, contracts.MiMoStatusTypeIdle))

			assert.Equal(t, []agenttest.TimelineEntry{mimoCompactionEnded}, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

// A rollback with no turn running ends the compaction at once, so the next
// conversation turn keeps its own verdict.
func TestMiMoRolledBackCompactionWithoutATurnAppliesTheCompactionVerdictAtTheRollback(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	clock := useMockClock(t, a)
	clock.Set(mustMiMoTime(t, "2026-09-25T15:56:51Z")).MustWait(testutil.DeadlineContext(t))
	startManualCompaction(t, a, false)

	feed(a, inTestSession(t, mimoXiaomiServerError),
		eventJSON(t, "message.removed", map[string]any{"sessionID": testSessionID, "messageID": "compact-message"}))
	assert.Equal(t, []agenttest.TimelineEntry{mimoCompactionEnded}, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))

	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), inTestSession(t, mimoXiaomiServerError), statusEvent(t, contracts.MiMoStatusTypeIdle))

	assert.Equal(t, []agenttest.TimelineEntry{
		mimoCompactionEnded,
		mimoTurnEndRow, {Op: "schedule_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR},
	}, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// The turn after a rolled-back compaction applies its own verdict.
func TestMiMoTheTurnAfterACompactionAppliesItsOwnVerdict(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	startManualCompaction(t, a, true)
	feed(a, eventJSON(t, "message.removed", map[string]any{"sessionID": testSessionID, "messageID": "compact-message"}),
		statusEvent(t, contracts.MiMoStatusTypeIdle))

	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), inTestSession(t, mimoXiaomiServerError), statusEvent(t, contracts.MiMoStatusTypeIdle))

	assert.Equal(t, []agenttest.TimelineEntry{
		mimoTurnEndRow, mimoCompactionEnded,
		mimoTurnEndRow, {Op: "schedule_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR},
	}, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// A stop of the reader ends the compaction's turn on the compaction verdict,
// not on the verdict of a stopped conversation turn.
func TestMiMoStoppedManualCompactionAppliesTheCompactionVerdict(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	clock := useMockClock(t, a)
	startManualCompaction(t, a, true)

	interruptOnClock(t, a, clock)
	feed(a, eventJSON(t, "message.removed", map[string]any{"sessionID": testSessionID, "messageID": "compact-message"}),
		statusEvent(t, contracts.MiMoStatusTypeIdle))

	assert.Equal(t, []agenttest.TimelineEntry{mimoTurnEndRow, mimoCompactionEnded}, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// An automatic compaction inside a conversation turn ends no turn. A failed
// conversation turn keeps its own verdict, also when the compaction failed it.
func TestMiMoAnAutomaticCompactionLeavesTheConversationTurnItsOwnVerdict(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	auto := eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"part": map[string]any{
		"id": "auto-part", "messageID": "auto-message", "sessionID": testSessionID,
		"type": contracts.MiMoPartTypeCompaction, "auto": true,
	}})

	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), messageEvent(t, "auto-message", roleUser, mainActorID, false), auto,
		inTestSession(t, mimoXiaomiServerError),
		eventJSON(t, "message.removed", map[string]any{"sessionID": testSessionID, "messageID": "auto-message"}),
		statusEvent(t, contracts.MiMoStatusTypeIdle))

	assert.Equal(t, []agenttest.TimelineEntry{
		mimoTurnEndRow, {Op: "schedule_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR},
	}, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// A context replacement and a stop end the compaction with no verdict, as they
// end a conversation turn. The mark does not reach a later turn.
func TestMiMoContextReplacementAndStopDropTheCompactionMark(t *testing.T) {
	t.Parallel()
	t.Run("ClearContext", func(t *testing.T) {
		t.Parallel()
		a, _, _ := newSinkTestAgent(t)
		startManualCompaction(t, a, true)
		require.True(t, manualCompactionVerdictDue(a), "the compaction's start sets the mark")
		_, err := a.ClearContext()
		require.NoError(t, err)
		assert.False(t, manualCompactionVerdictDue(a))
	})
	t.Run("Stop", func(t *testing.T) {
		t.Parallel()
		a, _, _ := newSinkTestAgent(t)
		startManualCompaction(t, a, true)
		require.True(t, manualCompactionVerdictDue(a), "the compaction's start sets the mark")
		a.SimulateExitForTest()
		a.Stop()
		assert.False(t, manualCompactionVerdictDue(a))
	})
}

// manualCompactionVerdictDue reads the agent's compaction verdict mark.
func manualCompactionVerdictDue(a *Agent) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.manualCompactionVerdictDue
}

// The input that follows a completed compaction joins MiMo's loop as a new
// turn. The compaction spent its mark, so that turn applies its own verdict.
func TestMiMoTheFollowUpTurnOfACompletedCompactionAppliesItsOwnVerdict(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	server.handle("POST /session/ses_test/prompt_async", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))
		w.WriteHeader(http.StatusNoContent)
	})
	startManualCompaction(t, a, true)
	feed(a, compactionPhaseEvent(t, "compact-part", "compact-message", true))

	require.NoError(t, a.SendInput("continue", nil))
	feed(a, inTestSession(t, mimoXiaomiServerError), statusEvent(t, contracts.MiMoStatusTypeIdle))

	assert.Equal(t, []agenttest.TimelineEntry{
		mimoCompactionEnded,
		mimoTurnEndRow, {Op: "schedule_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR},
	}, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}
