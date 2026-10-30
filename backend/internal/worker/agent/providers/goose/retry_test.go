package goose

import (
	"encoding/json"
	"fmt"
	"strings"
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
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The frames of .tmp/retry-research/goose.md and .tmp/probe/goose/out/<mode>/
// wire.log, Goose 1.56.0 against a fake model, copied verbatim. Every failure
// ends the prompt with {"stopReason":"end_turn"} after one agent message.
const (
	gooseSuccessUpdate          = `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"pong"},"messageId":"msg_fake","_meta":{"goose":{"created":1793148750,"messageId":"msg_fake","inference":{"provider":"anthropic","requestedModel":"claude-fake"}}}}`
	gooseServerErrorUpdate      = `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Ran into this error: Server error: Server error (500 Internal Server Error) at http://127.0.0.1:18901/v1/messages: Internal server error (provider request id: req_fake_1793148778390).\n\nPlease retry if you think this is a transient or recoverable error."},"messageId":"msg_4af50c75-5599-49b9-84a5-f1a2fc53363d","_meta":{"goose":{"created":1793148778,"messageId":"msg_4af50c75-5599-49b9-84a5-f1a2fc53363d"}}}`
	gooseOverloadedUpdate       = `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Ran into this error: Server error: Server error (529 <unknown status code>) at http://127.0.0.1:18901/v1/messages: Overloaded (provider request id: req_fake_1793148787804).\n\nPlease retry if you think this is a transient or recoverable error."},"messageId":"msg_6beced98-d129-4663-a754-4e3150154c45","_meta":{"goose":{"created":1793148787,"messageId":"msg_6beced98-d129-4663-a754-4e3150154c45"}}}`
	gooseNetworkUpdate          = `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Network error: Could not connect to 127.0.0.1:18999 — check your network connection and try again.\n\nPlease resend your message to try again."},"messageId":"msg_4e67cf71-2e0b-42ca-84fc-3d89d701a43a","_meta":{"goose":{"created":1793148864,"messageId":"msg_4e67cf71-2e0b-42ca-84fc-3d89d701a43a"}}}`
	gooseStreamDropUpdate       = `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Network error: Stream decode error: error decoding response body\n\nPlease resend your message to try again."},"messageId":"msg_e43a3402-02aa-4dda-aa60-74212f993db8","_meta":{"goose":{"created":1793148828,"messageId":"msg_e43a3402-02aa-4dda-aa60-74212f993db8"}}}`
	gooseRateLimitUpdate        = `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Ran into this error: Rate limit exceeded: Number of request tokens has exceeded your per-minute rate limit (provider request id: req_fake_1793148796006).\n\nPlease retry if you think this is a transient or recoverable error."},"messageId":"msg_a65ede29-79d1-4c3f-bdd9-a676838d963f","_meta":{"goose":{"created":1793148796,"messageId":"msg_a65ede29-79d1-4c3f-bdd9-a676838d963f"}}}`
	gooseSpendLimitUpdate       = `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Ran into this error: Rate limit exceeded: You have reached your specified API usage limits. You will regain access on 2026-11-01 at 00:00 UTC. (provider request id: req_fake_1793148801197).\n\nPlease retry if you think this is a transient or recoverable error."},"messageId":"msg_dff99086-dd59-458f-8391-fbed85421398","_meta":{"goose":{"created":1793148801,"messageId":"msg_dff99086-dd59-458f-8391-fbed85421398"}}}`
	gooseBadRequestUpdate       = `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Ran into this error: Request failed: Bad request (400): messages: text content blocks must be non-empty (provider request id: req_fake_1793148815208).\n\nPlease retry if you think this is a transient or recoverable error."},"messageId":"msg_d303aef0-0e7f-4000-b4e7-b8c57a2637a7","_meta":{"goose":{"created":1793148815,"messageId":"msg_d303aef0-0e7f-4000-b4e7-b8c57a2637a7"}}}`
	gooseContextUpdate          = `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Context length exceeded: prompt is too long: 250000 tokens > 200000 maximum (provider request id: req_fake_1793148817393)\n\nThe conversation is too long for the model's context window."},"messageId":"msg_ad45ba89-0438-46a8-aa97-3f4d2543b41e","_meta":{"goose":{"created":1793148817,"messageId":"msg_ad45ba89-0438-46a8-aa97-3f4d2543b41e"}}}`
	gooseEmptyResponseUpdate    = `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"The model returned an empty response. Please resend your message to continue."},"messageId":"msg_0d3da146-f292-480c-94f9-15ba7ef3b270","_meta":{"goose":{"created":1793148819,"messageId":"msg_0d3da146-f292-480c-94f9-15ba7ef3b270"}}}`
	gooseCompactionFailedUpdate = `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Ran into this error trying to compact: Failed to compact: the base prompt (system prompt, tool schemas, and conversation) exceeds the model's effective context window, and there are no tool responses to remove. Use a model or configuration with a larger usable context, disable some extensions to reduce the tool-schema payload, or start a new session..\n\nPlease try again or create a new session"},"messageId":"msg_7bc9a87b-aad0-412a-85a2-3239cf6f61ff","_meta":{"goose":{"created":1793148817,"messageId":"msg_7bc9a87b-aad0-412a-85a2-3239cf6f61ff"}}}`
	gooseCreditsExhaustedLine   = `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Please check your account with your provider to add more credits, then resend your message to continue.","data":{"reason":"credits_exhausted"}}}`
	gooseAuthRequiredLine       = `{"jsonrpc":"2.0","id":3,"error":{"code":-32000,"message":"Authentication required"}}`
	// Constructed: Goose source after 1.56.0 (commit 122834086b) sends the same
	// message, then fails the prompt with this shape. No release sent it yet.
	gooseTurnFailedLine = `{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Ran into this error: Server error: Server error (500 Internal Server Error) at http://127.0.0.1:18901/v1/messages: Internal server error (provider request id: req_fake_1793148778390).\n\nPlease retry if you think this is a transient or recoverable error.","data":{"reason":"turn_failed"}}}`
)

// gooseCapturedAt is the `created` stamp of the 429 usage-limit capture, so
// its reset instant lies four days ahead.
var gooseCapturedAt = time.Unix(1793148801, 0).UTC()

// gooseUpdateText reads the message text of one update fixture.
func gooseUpdateText(t *testing.T, update string) string {
	t.Helper()
	var message struct {
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal([]byte(update), &message))
	return message.Content.Text
}

// gooseResponseError reads the error of one JSON-RPC response fixture.
func gooseResponseError(t *testing.T, line string) error {
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

func TestGooseTurnFailureClassifiesEveryCapture(t *testing.T) {
	t.Parallel()
	spendLimitReset := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		update   string
		err      string
		stopped  bool
		want     providerkit.TurnFailureKind
		resetAt  time.Time
		noUpdate bool
	}{
		"a successful answer":                  {update: gooseSuccessUpdate, want: providerkit.TurnFailureNotRetryable},
		"a 500":                                {update: gooseServerErrorUpdate, want: providerkit.TurnFailureTransient},
		"a 529":                                {update: gooseOverloadedUpdate, want: providerkit.TurnFailureTransient},
		"a refused connection":                 {update: gooseNetworkUpdate, want: providerkit.TurnFailureTransient},
		"a dropped stream":                     {update: gooseStreamDropUpdate, want: providerkit.TurnFailureTransient},
		"a per-minute 429":                     {update: gooseRateLimitUpdate, want: providerkit.TurnFailureRateLimited},
		"a spend limit with a stated reset":    {update: gooseSpendLimitUpdate, want: providerkit.TurnFailureUsageLimited, resetAt: spendLimitReset},
		"a 400":                                {update: gooseBadRequestUpdate, want: providerkit.TurnFailureNotRetryable},
		"an exceeded context":                  {update: gooseContextUpdate, want: providerkit.TurnFailureNotRetryable},
		"an empty model response":              {update: gooseEmptyResponseUpdate, want: providerkit.TurnFailureNotRetryable},
		"a failed compaction":                  {update: gooseCompactionFailedUpdate, want: providerkit.TurnFailureNotRetryable},
		"exhausted credits":                    {err: gooseCreditsExhaustedLine, want: providerkit.TurnFailureBillingBlocked, noUpdate: true},
		"a missing credential":                 {err: gooseAuthRequiredLine, want: providerkit.TurnFailureNotRetryable, noUpdate: true},
		"a turn_failed prompt error":           {update: gooseServerErrorUpdate, err: gooseTurnFailedLine, want: providerkit.TurnFailureTransient},
		"a turn_failed error with no message":  {err: gooseTurnFailedLine, want: providerkit.TurnFailureTransient, noUpdate: true},
		"a 500 after a stop":                   {update: gooseServerErrorUpdate, stopped: true, want: providerkit.TurnFailureNotRetryable},
		"an error message, then a failed call": {update: gooseServerErrorUpdate, err: gooseAuthRequiredLine, want: providerkit.TurnFailureNotRetryable},
		"no message at all":                    {want: providerkit.TurnFailureNotRetryable, noUpdate: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var last gooseLastMessage
			if tc.update != "" {
				last = gooseLastMessage{text: gooseUpdateText(t, tc.update), update: json.RawMessage(tc.update)}
				last.inference = tc.update == gooseSuccessUpdate
			}
			var err error
			if tc.err != "" {
				err = gooseResponseError(t, tc.err)
			}
			verdict := gooseTurnFailure(err, tc.stopped, last, gooseCapturedAt)
			assert.Equal(t, tc.want, verdict.Failure.Kind)
			assert.True(t, tc.resetAt.Equal(verdict.Failure.ResetAt), "reset at %v, want %v", verdict.Failure.ResetAt, tc.resetAt)
			if tc.want == providerkit.TurnFailureNotRetryable {
				// A cancellation carries no payload, so none is asserted.
				return
			}
			if tc.noUpdate {
				assert.NotEmpty(t, verdict.Payload, "the error response is the payload when no message carried the failure")
				return
			}
			assert.JSONEq(t, tc.update, string(verdict.Payload), "the payload is the raw update of the error message")
		})
	}
}

// A message that carries `_meta.goose.inference` is the model's own text, even
// when it reads like a Goose error.
func TestGooseTurnFailureIgnoresAnErrorTextThatTheModelWrote(t *testing.T) {
	t.Parallel()
	last := gooseLastMessage{text: gooseUpdateText(t, gooseNetworkUpdate), inference: true, update: json.RawMessage(gooseNetworkUpdate)}
	verdict := gooseTurnFailure(nil, false, last, gooseCapturedAt)
	assert.Equal(t, providerkit.TurnFailureNotRetryable, verdict.Failure.Kind)
}

// Only one of Goose's own error messages reaches the vendor parser. Goose's
// other messages carry no inference either, and a message that quotes a
// vendor's wording must not decide the turn's end.
func TestGooseTurnFailureReadsOnlyGooseErrorMessages(t *testing.T) {
	t.Parallel()
	for name, text := range map[string]string{
		"the max-turns question": contracts.GooseGoalTextMaxTurnsMessage,
		// Constructed: a Goose message with the vendor wording of an
		// Anthropic overload, which the vendor parser reads as transient.
		"a message that quotes an overload": `The last attempt failed with {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}.`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			verdict := gooseTurnFailure(nil, false, gooseLastMessage{text: text, update: json.RawMessage(`{}`)}, gooseCapturedAt)
			assert.Equal(t, providerkit.TurnFailureNotRetryable, verdict.Failure.Kind)
			assert.Nil(t, verdict.Payload)
		})
	}
}

// A wrapped error still reveals its JSON-RPC response.
func TestGooseTurnFailureUnwrapsTheResponseError(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("session/prompt: %w", gooseResponseError(t, gooseCreditsExhaustedLine))
	verdict := gooseTurnFailure(err, false, gooseLastMessage{}, gooseCapturedAt)
	assert.Equal(t, providerkit.TurnFailureBillingBlocked, verdict.Failure.Kind)
}

// A spend limit whose reset instant lies more than 31 days ahead states no
// usable instant. The vendor rule then gives no verdict with a reset, and
// Goose's rate-limit class schedules a server-error retry.
func TestGooseTurnFailureRefusesAFarSpendLimitReset(t *testing.T) {
	t.Parallel()
	last := gooseLastMessage{text: gooseUpdateText(t, gooseSpendLimitUpdate), update: json.RawMessage(gooseSpendLimitUpdate)}
	verdict := gooseTurnFailure(nil, false, last, gooseCapturedAt.AddDate(0, -2, 0))
	assert.Equal(t, providerkit.TurnFailureRateLimited, verdict.Failure.Kind)
	assert.True(t, verdict.Failure.ResetAt.IsZero())
}

// newGooseRetryAgent builds a Goose agent with the hooks that Start installs,
// whose prompt end runs on the agent clock.
func newGooseRetryAgent(t *testing.T, sink *agenttest.Sink, now time.Time) *Agent {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	clock.Set(now)
	a := &Agent{}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "goose-1", ProviderName: "goose", Clock: clock})
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest("s1")
	*a.HooksForTest() = a.configure()
	a.SetPromptActiveForTest(true)
	return a
}

// gooseUpdateLine wraps one update of session s1 in its session/update line.
func gooseUpdateLine(update string) []byte {
	return []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":` + update + `}}`)
}

// gooseRetryTimeline splits the timeline at its last row. It returns that row's
// operation and every retry operation after it, and fails when a retry
// operation precedes the row.
func gooseRetryTimeline(t *testing.T, timeline []agenttest.TimelineEntry) (lastRow string, retries []agenttest.TimelineEntry) {
	t.Helper()
	for _, entry := range timeline {
		switch entry.Op {
		case "message", "turn_end", "notification", "leapmux_notification":
			require.Empty(t, retries, "a retry operation precedes the row %q", entry.Op)
			lastRow = entry.Op
		default:
			retries = append(retries, entry)
		}
	}
	return lastRow, retries
}

func gooseRetryOps(entries []agenttest.TimelineEntry) []string {
	ops := make([]string, 0, len(entries))
	for _, entry := range entries {
		ops = append(ops, entry.Op)
	}
	return ops
}

func TestGoosePromptEndAppliesOneVerdictAfterTheTurnEndRow(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		update  string
		wantOps []string
		resetAt time.Time
	}{
		"a 500 schedules a server-error retry": {
			update: gooseServerErrorUpdate, wantOps: []string{"schedule_server_error"},
		},
		"a refused connection schedules a server-error retry": {
			update: gooseNetworkUpdate, wantOps: []string{"schedule_server_error"},
		},
		"a per-minute 429 schedules a server-error retry": {
			update: gooseRateLimitUpdate, wantOps: []string{"schedule_server_error"},
		},
		"a spend limit schedules a usage-limit retry at its reset": {
			update: gooseSpendLimitUpdate, wantOps: []string{"schedule_usage_limit", "cancel_server_error"},
			resetAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		},
		"a 400 cancels the server-error retry": {
			update: gooseBadRequestUpdate, wantOps: []string{"cancel_server_error"},
		},
		"a successful answer cancels the server-error retry": {
			update: gooseSuccessUpdate, wantOps: []string{"cancel_server_error"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newGooseRetryAgent(t, sink, gooseCapturedAt)
			a.HandleOutput(gooseUpdateLine(tc.update))
			a.FinishPromptRequestForTest("s1", json.RawMessage(`{"stopReason":"end_turn"}`), nil)

			lastRow, retries := gooseRetryTimeline(t, sink.Timeline())
			assert.Equal(t, "turn_end", lastRow)
			require.Equal(t, tc.wantOps, gooseRetryOps(retries))
			if !tc.resetAt.IsZero() {
				assert.True(t, tc.resetAt.Equal(retries[0].ResetAt), "reset at %v", retries[0].ResetAt)
			}
			if retries[0].Op == "schedule_server_error" || retries[0].Op == "schedule_usage_limit" {
				assert.JSONEq(t, tc.update, string(sink.LastAutoSchedule().SourcePayload), "the payload is the raw update")
			}
		})
	}
}

func TestGooseExhaustedCreditsCancelBothRetries(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newGooseRetryAgent(t, sink, gooseCapturedAt)
	a.FinishPromptRequestForTest("s1", nil, gooseResponseError(t, gooseCreditsExhaustedLine))

	lastRow, retries := gooseRetryTimeline(t, sink.Timeline())
	assert.Equal(t, "leapmux_notification", lastRow, "the failure note ends the failed turn")
	require.Equal(t, []string{"cancel_usage_limit", "cancel_server_error"}, gooseRetryOps(retries))
	assert.Contains(t, retries[0].Reason, "only a billing or plan change lifts")
}

func TestGooseStopCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newGooseRetryAgent(t, sink, gooseCapturedAt)
	a.HandleOutput(gooseUpdateLine(gooseServerErrorUpdate))
	a.NoteInterruptRequestedForTest()
	a.FinishPromptRequestForTest("s1", json.RawMessage(`{"stopReason":"cancelled"}`), nil)

	_, retries := gooseRetryTimeline(t, sink.Timeline())
	require.Equal(t, []string{"cancel_server_error"}, gooseRetryOps(retries))
	assert.Equal(t, providerkit.TurnEndedWithoutRetryableFailure, retries[0].Reason)
}

// A message of another session, a subagent's, decides nothing.
func TestGooseIgnoresTheErrorMessageOfAnotherSession(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newGooseRetryAgent(t, sink, gooseCapturedAt)
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"child","update":` + gooseServerErrorUpdate + `}}`))
	a.FinishPromptRequestForTest("s1", json.RawMessage(`{"stopReason":"end_turn"}`), nil)

	_, retries := gooseRetryTimeline(t, sink.Timeline())
	assert.Equal(t, []string{"cancel_server_error"}, gooseRetryOps(retries))
}

// One snapshot serves both consumers: a goal round that ends on a network error
// keeps the goal and schedules the retry.
func TestGooseGoalRoundAndRetryReadOneSnapshot(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newGooseRetryAgent(t, sink, gooseCapturedAt)
	gooseGoalPrompt(t, a, true, "msg_net", gooseUpdateText(t, gooseNetworkUpdate), false)

	assert.Empty(t, sink.Goals(), "the network error keeps the goal")
	_, retries := gooseRetryTimeline(t, sink.Timeline())
	assert.Equal(t, []string{"schedule_server_error"}, gooseRetryOps(retries))

	// The prompt end consumed the message, so the next round reads its own.
	before := len(sink.Timeline())
	a.SetPromptActiveForTest(true)
	a.FinishPromptRequestForTest("s1", json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	lastRow, retries := gooseRetryTimeline(t, sink.Timeline()[before:])
	assert.Equal(t, "turn_end", lastRow)
	assert.Equal(t, []string{"cancel_server_error"}, gooseRetryOps(retries))
}

// A Goose error message that arrived before the prompt started, from a session
// replay or an earlier turn, does not decide the prompt's end: PromptPrepared
// clears it.
func TestGooseStaleErrorMessageBeforeThePromptDecidesNothing(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, _ := acptest.NewAgentForRPCWithResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		func(method string) agenttest.RPCReply {
			if method == acp.MethodSessionPrompt {
				return agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		},
	)
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest("s1")
	*a.HooksForTest() = a.configure()

	a.HandleOutput(gooseUpdateLine(gooseNetworkUpdate))
	require.NoError(t, a.SendInput("hello", nil))
	testutil.RequireEventually(t, func() bool { return !a.PromptActive() })

	ops := gooseRetryOps(sink.Timeline())
	assert.NotContains(t, ops, "schedule_server_error", "the stale message must not schedule")
	assert.Contains(t, ops, "cancel_server_error")
	assert.Equal(t, 0, sink.AutoScheduleCount())
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, sink.LastAutoCancel())
}

// A malformed update records nothing and leaves the record as it was.
func TestGooseObserverIgnoresAMalformedUpdate(t *testing.T) {
	t.Parallel()
	a := newGooseRetryAgent(t, &agenttest.Sink{}, gooseCapturedAt)
	assert.False(t, a.observeGooseSessionUpdate("s1", nil, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":"not an object","messageId":"m1"}`)))
	assert.False(t, a.observeGooseSessionUpdate("s1", nil, json.RawMessage(gooseNetworkUpdate)))
	assert.False(t, a.observeGooseSessionUpdate("s1", nil, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"x"},"messageId":"m2","_meta":"broken"}`)))
	last := a.takeGooseLastMessage()
	assert.Equal(t, gooseUpdateText(t, gooseNetworkUpdate), last.text)
	assert.JSONEq(t, gooseNetworkUpdate, string(last.update))
	assert.Equal(t, gooseLastMessage{}, a.takeGooseLastMessage(), "the take clears the record")
}

func TestGooseStreamedMessageSnapshotSurvivesTheNextMessage(t *testing.T) {
	t.Parallel()
	a := newGooseRetryAgent(t, &agenttest.Sink{}, gooseCapturedAt)
	chunk := json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"abcdefgh"},"messageId":"many"}`)
	for range 4096 {
		a.observeGooseSessionUpdate("s1", nil, chunk)
	}
	first := a.takeGooseLastMessage()
	require.Equal(t, strings.Repeat("abcdefgh", 4096), first.text)
	assert.JSONEq(t, string(chunk), string(first.update))
	assert.Equal(t, gooseLastMessage{}, a.takeGooseLastMessage())
	a.observeGooseSessionUpdate("s1", nil, json.RawMessage(gooseSuccessUpdate))
	second := a.takeGooseLastMessage()
	assert.Equal(t, "pong", second.text)
	assert.Equal(t, strings.Repeat("abcdefgh", 4096), first.text)
	a.observeGooseSessionUpdate("s1", nil, chunk)
	a.observeGooseSessionUpdate("s1", nil, json.RawMessage(gooseSuccessUpdate))
	assert.Equal(t, "pong", a.takeGooseLastMessage().text, "a new message ID replaces the previous text")
}

func BenchmarkGooseStreamedMessage(b *testing.B) {
	for _, chunks := range []int{256, 1024, 4096} {
		b.Run(fmt.Sprint(chunks), func(b *testing.B) {
			a := &Agent{}
			chunk := json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"abcdefgh"},"messageId":"many"}`)
			b.ReportAllocs()
			b.SetBytes(int64(chunks * 8))
			for b.Loop() {
				for range chunks {
					a.observeGooseSessionUpdate("", nil, chunk)
				}
				a.takeGooseLastMessage()
			}
		})
	}
}
