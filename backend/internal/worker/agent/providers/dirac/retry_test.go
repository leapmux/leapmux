package dirac

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
)

// The frames of .tmp/retry-research/dirac.md and .tmp/probe/dirac/wire-*.log,
// Dirac 0.5.23 against a fake model, copied verbatim. Each failure raises the
// failed-request card, repeats it pending, and asks the dialog.
const (
	dirac500Session = "4a401f33-ce45-42f4-aebd-69363bfd48d7"
	dirac500Card    = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"4a401f33-ce45-42f4-aebd-69363bfd48d7","update":{"sessionUpdate":"tool_call","toolCallId":"1793148722806-9","name":"API Request Failed","title":"API Request Failed","kind":"other","status":"pending","rawInput":{"body":"500 The server had an error while processing your request."},"rawOutput":{"body":"500 The server had an error while processing your request."},"_meta":{"dev.dirac/seq":21}}}}`
	dirac500Pending = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"4a401f33-ce45-42f4-aebd-69363bfd48d7","update":{"sessionUpdate":"tool_call_update","toolCallId":"1793148722806-9","name":"API Request Failed","title":"API Request Failed","status":"pending","rawInput":{"body":"500 The server had an error while processing your request."},"rawOutput":{"body":"500 The server had an error while processing your request."},"_meta":{"dev.dirac/seq":22}}}}`
	dirac500Dialog  = `{"jsonrpc":"2.0","id":0,"method":"session/request_permission","params":{"sessionId":"4a401f33-ce45-42f4-aebd-69363bfd48d7","toolCall":{"toolCallId":"1793148722806-9","name":"API Request Failed","title":"API Request Failed","kind":"other","status":"pending","rawInput":{"body":"500 The server had an error while processing your request."},"rawOutput":{"body":"500 The server had an error while processing your request."}},"options":[{"kind":"allow_once","optionId":"allow_once","name":"Approve once"},{"kind":"allow_always","optionId":"allow_always","name":"Always approve"},{"kind":"reject_once","optionId":"reject_once","name":"Reject once"},{"kind":"reject_always","optionId":"reject_always","name":"Always reject"}]}}`
	dirac500Failed  = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"4a401f33-ce45-42f4-aebd-69363bfd48d7","update":{"sessionUpdate":"tool_call_update","toolCallId":"1793148722806-9","name":"API Request Failed","title":"API Request Failed","status":"failed","rawInput":{"body":"500 The server had an error while processing your request."},"rawOutput":{"body":"500 The server had an error while processing your request."},"_meta":{"dev.dirac/seq":23}}}}`
	diracEndTurn    = `{"stopReason":"end_turn","usage":{"totalTokens":0,"inputTokens":0,"outputTokens":0,"thoughtTokens":0,"cachedReadTokens":0,"cachedWriteTokens":0}}`

	diracZAISession = "599241b3-e058-4bba-8b12-ac6aefdf513e"
	diracZAICard    = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"599241b3-e058-4bba-8b12-ac6aefdf513e","update":{"sessionUpdate":"tool_call","toolCallId":"1793148933369-9","name":"API Request Failed","title":"API Request Failed","kind":"other","status":"pending","rawInput":{"body":"429 Usage limit reached for 5 hour. Your limit will reset at 2026-10-28 23:59:59"},"rawOutput":{"body":"429 Usage limit reached for 5 hour. Your limit will reset at 2026-10-28 23:59:59"},"_meta":{"dev.dirac/seq":21}}}}`
	diracZAIDialog  = `{"jsonrpc":"2.0","id":0,"method":"session/request_permission","params":{"sessionId":"599241b3-e058-4bba-8b12-ac6aefdf513e","toolCall":{"toolCallId":"1793148933369-9","name":"API Request Failed","title":"API Request Failed","kind":"other","status":"pending","rawInput":{"body":"429 Usage limit reached for 5 hour. Your limit will reset at 2026-10-28 23:59:59"},"rawOutput":{"body":"429 Usage limit reached for 5 hour. Your limit will reset at 2026-10-28 23:59:59"}},"options":[{"kind":"allow_once","optionId":"allow_once","name":"Approve once"},{"kind":"allow_always","optionId":"allow_always","name":"Always approve"},{"kind":"reject_once","optionId":"reject_once","name":"Reject once"},{"kind":"reject_always","optionId":"reject_always","name":"Always reject"}]}}`

	dirac402Session = "b35b7dd0-e697-4bba-8aa0-e101ddbe19f0"
	dirac402Sibling = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"b35b7dd0-e697-4bba-8aa0-e101ddbe19f0","update":{"sessionUpdate":"tool_call","toolCallId":"1793148788575-2","name":"API Error (Payment Required)","title":"API Error (Payment Required)","kind":"other","status":"failed","rawInput":{"body":"402 Insufficient credits. Add more using https://example.invalid/credits\n\nPayment is required by the configured provider. Check its billing, credits, and model access."},"rawOutput":{"body":"402 Insufficient credits. Add more using https://example.invalid/credits\n\nPayment is required by the configured provider. Check its billing, credits, and model access."},"_meta":{"dev.dirac/seq":5}}}}`
	dirac402Card    = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"b35b7dd0-e697-4bba-8aa0-e101ddbe19f0","update":{"sessionUpdate":"tool_call","toolCallId":"1793148788581-3","name":"API Request Failed","title":"API Request Failed","kind":"other","status":"pending","rawInput":{"body":"402 Insufficient credits. Add more using https://example.invalid/credits\n\nPayment is required by the configured provider. Check its billing, credits, and model access."},"rawOutput":{"body":"402 Insufficient credits. Add more using https://example.invalid/credits\n\nPayment is required by the configured provider. Check its billing, credits, and model access."},"_meta":{"dev.dirac/seq":6}}}}`
	dirac402Dialog  = `{"jsonrpc":"2.0","id":0,"method":"session/request_permission","params":{"sessionId":"b35b7dd0-e697-4bba-8aa0-e101ddbe19f0","toolCall":{"toolCallId":"1793148788581-3","name":"API Request Failed","title":"API Request Failed","kind":"other","status":"pending","rawInput":{"body":"402 Insufficient credits. Add more using https://example.invalid/credits\n\nPayment is required by the configured provider. Check its billing, credits, and model access."},"rawOutput":{"body":"402 Insufficient credits. Add more using https://example.invalid/credits\n\nPayment is required by the configured provider. Check its billing, credits, and model access."}},"options":[{"kind":"allow_once","optionId":"allow_once","name":"Approve once"},{"kind":"allow_always","optionId":"allow_always","name":"Always approve"},{"kind":"reject_once","optionId":"reject_once","name":"Reject once"},{"kind":"reject_always","optionId":"reject_always","name":"Always reject"}]}}`
	dirac402Failed  = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"b35b7dd0-e697-4bba-8aa0-e101ddbe19f0","update":{"sessionUpdate":"tool_call_update","toolCallId":"1793148788581-3","name":"API Request Failed","title":"API Request Failed","status":"failed","rawInput":{"body":"402 Insufficient credits. Add more using https://example.invalid/credits\n\nPayment is required by the configured provider. Check its billing, credits, and model access."},"rawOutput":{"body":"402 Insufficient credits. Add more using https://example.invalid/credits\n\nPayment is required by the configured provider. Check its billing, credits, and model access."},"_meta":{"dev.dirac/seq":8}}}}`

	dirac401Session = "2c889192-5460-459e-b4fc-cf93838dbd36"
	dirac401Sibling = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"2c889192-5460-459e-b4fc-cf93838dbd36","update":{"sessionUpdate":"tool_call","toolCallId":"1793148787419-2","name":"API Error (Authentication)","title":"API Error (Authentication)","kind":"other","status":"failed","rawInput":{"body":"401 Incorrect API key provided: fake-***. You can find your API key at https://platform.openai.com/account/api-keys.\n\nAuthentication failed. Please verify your API key configuration."},"rawOutput":{"body":"401 Incorrect API key provided: fake-***. You can find your API key at https://platform.openai.com/account/api-keys.\n\nAuthentication failed. Please verify your API key configuration."},"_meta":{"dev.dirac/seq":5}}}}`
	dirac401Dialog  = `{"jsonrpc":"2.0","id":0,"method":"session/request_permission","params":{"sessionId":"2c889192-5460-459e-b4fc-cf93838dbd36","toolCall":{"toolCallId":"1793148787426-3","name":"API Request Failed","title":"API Request Failed","kind":"other","status":"pending","rawInput":{"body":"401 Incorrect API key provided: fake-***. You can find your API key at https://platform.openai.com/account/api-keys.\n\nAuthentication failed. Please verify your API key configuration."},"rawOutput":{"body":"401 Incorrect API key provided: fake-***. You can find your API key at https://platform.openai.com/account/api-keys.\n\nAuthentication failed. Please verify your API key configuration."}},"options":[{"kind":"allow_once","optionId":"allow_once","name":"Approve once"},{"kind":"allow_always","optionId":"allow_always","name":"Always approve"},{"kind":"reject_once","optionId":"reject_once","name":"Reject once"},{"kind":"reject_always","optionId":"reject_always","name":"Always reject"}]}}`

	diracContextSession = "2d8af653-48ef-4e44-a095-89b39cdf3023"
	diracContextCard    = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"2d8af653-48ef-4e44-a095-89b39cdf3023","update":{"sessionUpdate":"tool_call","toolCallId":"1793148803896-9","name":"API Request Failed","title":"API Request Failed","kind":"other","status":"pending","rawInput":{"body":"Context window exceeded. Click retry to truncate the conversation and try again."},"rawOutput":{"body":"Context window exceeded. Click retry to truncate the conversation and try again."},"_meta":{"dev.dirac/seq":21}}}}`
	diracContextDialog  = `{"jsonrpc":"2.0","id":0,"method":"session/request_permission","params":{"sessionId":"2d8af653-48ef-4e44-a095-89b39cdf3023","toolCall":{"toolCallId":"1793148803896-9","name":"API Request Failed","title":"API Request Failed","kind":"other","status":"pending","rawInput":{"body":"Context window exceeded. Click retry to truncate the conversation and try again."},"rawOutput":{"body":"Context window exceeded. Click retry to truncate the conversation and try again."}},"options":[{"kind":"allow_once","optionId":"allow_once","name":"Approve once"},{"kind":"allow_always","optionId":"allow_always","name":"Always approve"},{"kind":"reject_once","optionId":"reject_once","name":"Reject once"},{"kind":"reject_always","optionId":"reject_always","name":"Always reject"}]}}`

	diracAllowSession   = "c323ee79-950d-436a-aff8-80105486699b"
	diracAllowCard      = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"c323ee79-950d-436a-aff8-80105486699b","update":{"sessionUpdate":"tool_call","toolCallId":"1793149019017-9","name":"API Request Failed","title":"API Request Failed","kind":"other","status":"pending","rawInput":{"body":"500 The server had an error while processing your request."},"rawOutput":{"body":"500 The server had an error while processing your request."},"_meta":{"dev.dirac/seq":21}}}}`
	diracAllowCompleted = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"c323ee79-950d-436a-aff8-80105486699b","update":{"sessionUpdate":"tool_call_update","toolCallId":"1793149019017-9","name":"API Request Failed","title":"API Request Failed","status":"completed","rawInput":{"body":"500 The server had an error while processing your request."},"rawOutput":{"body":"500 The server had an error while processing your request."},"_meta":{"dev.dirac/seq":23}}}}`
	diracAllowDone      = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"c323ee79-950d-436a-aff8-80105486699b","update":{"sessionUpdate":"tool_call","toolCallId":"1793149019200-11","name":"respond","title":"Task Completed","kind":"other","status":"pending","rawInput":{"tool":"respond","operation":"complete","text":"Done."},"rawOutput":{"body":"Done."},"_meta":{"dev.dirac/seq":31}}}}`
)

// diracZAIObservedAt lies four hours before the reset of the Z.AI capture,
// 2026-10-28 23:59:59 at UTC+8. The capture itself ran at 00:55:33Z, about 15
// hours before that stamp, which a 5-hour limit cannot state, so the test
// shifts the clock into the window.
var diracZAIObservedAt = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// diracZAIResetAt is the stamp read at UTC+8, plus the second that the stamp
// drops.
var diracZAIResetAt = time.Date(2026, 10, 28, 16, 0, 0, 0, time.UTC)

func TestDiracBodyFailureClassifiesEveryCapture(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		body       string
		observedAt time.Time
		want       providerkit.TurnFailure
	}{
		"a 500": {
			body: "500 The server had an error while processing your request.",
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient},
		},
		"a 529":                {body: "529 Overloaded", want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}},
		"a refused connection": {body: "Connection error.", want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}},
		// The research states the next two forms of the OpenAI SDK, and no
		// probe captured them.
		"a timeout":          {body: "Request timed out.", want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}},
		"a 503 with no body": {body: "503 status code (no body)", want: providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}},
		"a 429": {
			body: "429 Rate limit reached for requests. Please try again in 1s.",
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited},
		},
		"a Z.AI usage limit inside its window": {
			body: "429 Usage limit reached for 5 hour. Your limit will reset at 2026-10-28 23:59:59", observedAt: diracZAIObservedAt,
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: diracZAIResetAt},
		},
		"a Z.AI usage limit whose stamp lies outside its window": {
			body: "429 Usage limit reached for 5 hour. Your limit will reset at 2026-10-28 23:59:59", observedAt: time.UnixMilli(1793148933369).UTC(),
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess},
		},
		// The research states this Z.AI billing text, and no probe captured
		// it.
		"a Z.AI billing block behind a 429": {
			body: "429 Insufficient balance or no resource package. Please recharge.",
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked},
		},
		// Constructed: a vendor's own wording behind a 429 is more specific
		// than the status, and a monthly limit must not retry.
		"a monthly limit behind a 429": {
			body: "429 You've reached your monthly usage limit for this billing cycle.",
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowLonger},
		},
		"a 402": {
			body: "402 Insufficient credits. Add more using https://example.invalid/credits\n\nPayment is required by the configured provider. Check its billing, credits, and model access.",
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked},
		},
		"a 401": {
			body: "401 Incorrect API key provided: fake-***. You can find your API key at https://platform.openai.com/account/api-keys.\n\nAuthentication failed. Please verify your API key configuration.",
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable},
		},
		"an exceeded context": {
			body: "Context window exceeded. Click retry to truncate the conversation and try again.",
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable},
		},
		// The SDK form of the research, which no probe captured.
		"a 400 with no body": {body: "400 status code (no body)", want: providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}},
		"an empty body":      {body: "", want: providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			observedAt := tc.observedAt
			if observedAt.IsZero() {
				observedAt = diracZAIObservedAt
			}
			got := diracBodyFailure(tc.body, observedAt)
			assert.Equal(t, tc.want.Kind, got.Kind)
			assert.True(t, tc.want.ResetAt.Equal(got.ResetAt), "reset at %v, want %v", got.ResetAt, tc.want.ResetAt)
			assert.Equal(t, tc.want.Window, got.Window)
		})
	}
}

func TestDiracLeapMuxRetriesOnlyWhatItSchedules(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		failure providerkit.TurnFailure
		want    bool
	}{
		"a transient failure":           {providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}, true},
		"a rate limit":                  {providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited}, true},
		"a usage limit with a reset":    {providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, ResetAt: diracZAIResetAt}, true},
		"a usage limit of a week":       {providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess}, true},
		"a usage limit of a month":      {providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowLonger}, false},
		"a usage limit with no window":  {providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited}, false},
		"a billing block":               {providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked}, false},
		"a failure that is not retried": {providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.failure.RetryDisposition() != providerkit.RetryNone)
		})
	}
}

// diracRetryRig is a Dirac agent with its production hooks over a fake peer,
// a sink that records control publications, and a pinned agent clock.
type diracRetryRig struct {
	a        *Agent
	sink     *agenttest.ControlSink
	requests func() []agenttest.RecordedRequest
}

func newDiracRetryRig(t *testing.T, sessionID string, now time.Time) *diracRetryRig {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	clock.Set(now)
	a, requests := acptest.NewAgentForRPC(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
	)
	*a.HooksForTest() = a.configure(clock)
	sink := &agenttest.ControlSink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest(sessionID)
	a.SetPromptActiveForTest(true)
	return &diracRetryRig{a: a, sink: sink, requests: requests}
}

// feed hands each line to the agent as Dirac writes it.
func (r *diracRetryRig) feed(lines ...string) {
	for _, line := range lines {
		r.a.HandleOutput([]byte(line))
	}
}

// endTurn ends the prompt with Dirac's response.
func (r *diracRetryRig) endTurn(t *testing.T, sessionID string) {
	t.Helper()
	r.a.FinishPromptRequestForTest(sessionID, json.RawMessage(diracEndTurn), nil)
}

// answerTo returns the answer that LeapMux wrote to the request id, or "".
func (r *diracRetryRig) answerTo(t *testing.T, id string) string {
	t.Helper()
	syncPeer(t, r.a)
	var lines []string
	for _, request := range r.requests() {
		lines = append(lines, request.Raw)
	}
	return agenttest.JSONRPCResultsByID(t, strings.Join(lines, "\n"))[id]
}

// diracRetryTimeline splits the timeline at its last row. It returns that row's
// operation and every retry operation after it, and fails when a retry
// operation precedes the row.
func diracRetryTimeline(t *testing.T, timeline []agenttest.TimelineEntry) (lastRow string, retries []agenttest.TimelineEntry) {
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

func diracRetryOps(entries []agenttest.TimelineEntry) []string {
	ops := make([]string, 0, len(entries))
	for _, entry := range entries {
		ops = append(ops, entry.Op)
	}
	return ops
}

// diracRejectOnce is the answer LeapMux writes for a failure that it retries.
const diracRejectOnce = `{"outcome":{"outcome":"selected","optionId":"reject_once"}}`

// The 500 capture: LeapMux rejects the dialog in place of the reader, Dirac
// ends the turn, and the turn end schedules a server-error retry with the card
// as its payload.
func TestDiracRejectsA500DialogAndTheTurnEndSchedules(t *testing.T) {
	t.Parallel()
	r := newDiracRetryRig(t, dirac500Session, diracZAIObservedAt)
	r.feed(dirac500Card, dirac500Pending, dirac500Dialog)
	assert.JSONEq(t, diracRejectOnce, r.answerTo(t, "0"))
	assert.Zero(t, r.sink.PublishedControlCount(), "the reader sees no dialog")

	r.feed(dirac500Failed)
	r.endTurn(t, dirac500Session)
	lastRow, retries := diracRetryTimeline(t, r.sink.Timeline())
	assert.Equal(t, "turn_end", lastRow)
	require.Equal(t, []string{"schedule_server_error"}, diracRetryOps(retries))
	assert.JSONEq(t, diracUpdateOf(t, dirac500Card), string(r.sink.LastAutoSchedule().SourcePayload), "the payload is the card update")
}

// diracUpdateOf returns the update of one session/update line.
func diracUpdateOf(t *testing.T, line string) string {
	t.Helper()
	var frame struct {
		Params struct {
			Update json.RawMessage `json:"update"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal([]byte(line), &frame))
	return string(frame.Params.Update)
}

// The Z.AI capture, with the clock inside the 5-hour window: the dialog is
// rejected, and the turn end schedules a usage-limit retry at the stamp.
func TestDiracRejectsAZAIUsageDialogAndSchedulesAtTheReset(t *testing.T) {
	t.Parallel()
	r := newDiracRetryRig(t, diracZAISession, diracZAIObservedAt)
	r.feed(diracZAICard, diracZAIDialog)
	assert.JSONEq(t, diracRejectOnce, r.answerTo(t, "0"))

	r.endTurn(t, diracZAISession)
	_, retries := diracRetryTimeline(t, r.sink.Timeline())
	require.Equal(t, []string{"schedule_usage_limit", "cancel_server_error"}, diracRetryOps(retries))
	assert.True(t, diracZAIResetAt.Equal(retries[0].ResetAt), "reset at %v", retries[0].ResetAt)
}

// The same Z.AI capture at its real time: its stamp lies outside its 5-hour
// window, so the window wording alone decides, and the turn end schedules a
// server-error retry instead of a retry at a stamp that cannot hold.
func TestDiracTreatsAnImplausibleZAIStampByItsWindow(t *testing.T) {
	t.Parallel()
	r := newDiracRetryRig(t, diracZAISession, time.UnixMilli(1793148933369).UTC())
	r.feed(diracZAICard, diracZAIDialog)
	assert.JSONEq(t, diracRejectOnce, r.answerTo(t, "0"))

	r.endTurn(t, diracZAISession)
	_, retries := diracRetryTimeline(t, r.sink.Timeline())
	assert.Equal(t, []string{"schedule_server_error"}, diracRetryOps(retries))
}

// A failure that LeapMux does not retry stays with the reader, and the turn
// end cancels the server-error retry.
func TestDiracLeavesAContextDialogToTheReader(t *testing.T) {
	t.Parallel()
	r := newDiracRetryRig(t, diracContextSession, diracZAIObservedAt)
	r.feed(diracContextCard, diracContextDialog)
	assert.Empty(t, r.answerTo(t, "0"), "LeapMux writes no answer")
	assert.Equal(t, 1, r.sink.PublishedControlCount(), "the reader answers the dialog")

	r.endTurn(t, diracContextSession)
	_, retries := diracRetryTimeline(t, r.sink.Timeline())
	assert.Equal(t, []string{"cancel_server_error"}, diracRetryOps(retries))
}

// A subagent's dialog belongs to the child session, so LeapMux answers it
// never, and the main turn's end does not read it.
func TestDiracLeavesASubagentDialogToTheReader(t *testing.T) {
	t.Parallel()
	r := newDiracRetryRig(t, "main-session", diracZAIObservedAt)
	require.True(t, r.a.AttachChildSession(acp.ChildSessionTarget{RootSessionID: "main-session", SessionID: dirac500Session, RowKey: "child-row"}))
	r.feed(dirac500Dialog)
	assert.Empty(t, r.answerTo(t, "0"), "LeapMux writes no answer")
	assert.Equal(t, 1, r.sink.PublishedControlCount(), "the reader answers the subagent's dialog")

	r.endTurn(t, "main-session")
	_, retries := diracRetryTimeline(t, r.sink.Timeline())
	assert.Equal(t, []string{"cancel_server_error"}, diracRetryOps(retries))
}

// The base reports queued=false when it could not queue LeapMux's reject, and
// then publishes the dialog for the reader. The card is not declined, so the
// reader's own answer decides and the turn end cancels.
//
// This rig cannot make the base's queueing fail for a request that it can
// still publish: the shutdown and full-queue refusals live in the process
// internals of providerkit, and an id that the base cannot register cannot be
// published either. acp's TestAnswerControlRequest_AnUnqueuedAnswer* covers
// the base side, so this test calls the two hooks in the base's order.
func TestDiracDoesNotDeclineADialogWhoseRejectWasNotQueued(t *testing.T) {
	t.Parallel()
	r := newDiracRetryRig(t, dirac500Session, diracZAIObservedAt)
	r.feed(dirac500Card, dirac500Pending)
	line := providerkit.ParseLine([]byte(dirac500Dialog))
	answer, ok := r.a.HooksForTest().AnswerControlRequest(line)
	require.True(t, ok, "LeapMux retries a 500 itself")
	require.NotNil(t, answer)
	r.a.HooksForTest().ControlRequestAnswered(line, false)

	r.feed(dirac500Failed)
	r.endTurn(t, dirac500Session)
	_, retries := diracRetryTimeline(t, r.sink.Timeline())
	assert.Equal(t, []string{"cancel_server_error"}, diracRetryOps(retries))
}

// The 402 capture: the Payment sibling card keeps the dialog with the reader,
// whose reject ends the turn, and the turn end cancels both retries.
func TestDiracPaymentCardAndTheReadersRejectGiveABillingBlock(t *testing.T) {
	t.Parallel()
	r := newDiracRetryRig(t, dirac402Session, diracZAIObservedAt)
	r.feed(dirac402Sibling, dirac402Card, dirac402Dialog)
	assert.Empty(t, r.answerTo(t, "0"), "LeapMux writes no answer")
	assert.Equal(t, 1, r.sink.PublishedControlCount())

	r.feed(dirac402Failed)
	r.endTurn(t, dirac402Session)
	_, retries := diracRetryTimeline(t, r.sink.Timeline())
	require.Equal(t, []string{"cancel_usage_limit", "cancel_server_error"}, diracRetryOps(retries))
	assert.Contains(t, retries[0].Reason, "only a billing or plan change lifts")
}

// A Z.AI balance failure raises no Payment card: the body alone states the
// billing block. The dialog stays with the reader, and after the reader's
// reject the turn end still cancels both retries. The frames are the 500
// capture with the body of Z.AI 1113.
func TestDiracBillingBodyAndTheReadersRejectGiveABillingBlock(t *testing.T) {
	t.Parallel()
	billing := func(frame string) string {
		return strings.ReplaceAll(frame, "500 The server had an error while processing your request.",
			"429 Insufficient balance or no resource package. Please recharge.")
	}
	r := newDiracRetryRig(t, dirac500Session, diracZAIObservedAt)
	r.feed(billing(dirac500Card), billing(dirac500Pending), billing(dirac500Dialog))
	assert.Empty(t, r.answerTo(t, "0"), "LeapMux writes no answer for a billing block")
	assert.Equal(t, 1, r.sink.PublishedControlCount(), "the reader answers the dialog")

	r.feed(billing(dirac500Failed))
	r.endTurn(t, dirac500Session)
	_, retries := diracRetryTimeline(t, r.sink.Timeline())
	require.Equal(t, []string{"cancel_usage_limit", "cancel_server_error"}, diracRetryOps(retries))
	assert.Contains(t, retries[0].Reason, "only a billing or plan change lifts")
}

// The 401 capture: the Authentication sibling card keeps the dialog with the
// reader, and the turn end cancels the server-error retry only.
func TestDiracAuthenticationCardLeavesTheDialogToTheReader(t *testing.T) {
	t.Parallel()
	r := newDiracRetryRig(t, dirac401Session, diracZAIObservedAt)
	r.feed(dirac401Sibling, dirac401Dialog)
	assert.Empty(t, r.answerTo(t, "0"))
	assert.Equal(t, 1, r.sink.PublishedControlCount())

	r.endTurn(t, dirac401Session)
	_, retries := diracRetryTimeline(t, r.sink.Timeline())
	assert.Equal(t, []string{"cancel_server_error"}, diracRetryOps(retries))
}

// A sibling card belongs to the failed-request card that follows it. A 500
// dialog after an Authentication card stays with the reader, because the
// sibling states the class of the failure.
func TestDiracSiblingCardKeepsTheNextDialogWithTheReader(t *testing.T) {
	t.Parallel()
	r := newDiracRetryRig(t, dirac500Session, diracZAIObservedAt)
	sibling := strings.ReplaceAll(dirac401Sibling, dirac401Session, dirac500Session)
	r.feed(sibling, dirac500Card, dirac500Dialog)
	assert.Empty(t, r.answerTo(t, "0"))
	assert.Equal(t, 1, r.sink.PublishedControlCount())
}

// The allow capture: the reader approves, Dirac completes the card and
// retries in the same turn, and the retry succeeds. The completed update
// clears the record, so the turn end cancels the server-error retry only,
// even after a Payment card.
func TestDiracCompletedCardClearsTheRecord(t *testing.T) {
	t.Parallel()
	r := newDiracRetryRig(t, diracAllowSession, diracZAIObservedAt)
	sibling := strings.ReplaceAll(dirac402Sibling, dirac402Session, diracAllowSession)
	r.feed(sibling, diracAllowCard, diracAllowCompleted, diracAllowDone)
	r.endTurn(t, diracAllowSession)
	_, retries := diracRetryTimeline(t, r.sink.Timeline())
	assert.Equal(t, []string{"cancel_server_error"}, diracRetryOps(retries))
}

// A completed update of another tool call leaves the record as it was.
func TestDiracCompletedUpdateOfAnotherCardKeepsTheRecord(t *testing.T) {
	t.Parallel()
	r := newDiracRetryRig(t, dirac500Session, diracZAIObservedAt)
	r.feed(dirac500Card, dirac500Dialog)
	other := strings.ReplaceAll(strings.ReplaceAll(diracAllowCompleted, diracAllowSession, dirac500Session), "1793149019017-9", "other-call")
	r.feed(other)
	r.endTurn(t, dirac500Session)
	_, retries := diracRetryTimeline(t, r.sink.Timeline())
	assert.Equal(t, []string{"schedule_server_error"}, diracRetryOps(retries))
}

// A stop after LeapMux rejected the dialog ends the turn on the reader's
// decision, and the turn end cancels the server-error retry.
func TestDiracStopGivesNotRetryable(t *testing.T) {
	t.Parallel()
	r := newDiracRetryRig(t, dirac500Session, diracZAIObservedAt)
	r.feed(dirac500Card, dirac500Dialog)
	require.JSONEq(t, diracRejectOnce, r.answerTo(t, "0"))
	r.a.NoteInterruptRequestedForTest()
	r.a.FinishPromptRequestForTest(dirac500Session, json.RawMessage(`{"stopReason":"cancelled"}`), nil)
	_, retries := diracRetryTimeline(t, r.sink.Timeline())
	assert.Equal(t, []string{"cancel_server_error"}, diracRetryOps(retries))
}

// A failed prompt request is a setup failure, which no retry repairs.
func TestDiracFailedPromptRequestGivesNotRetryable(t *testing.T) {
	t.Parallel()
	r := newDiracRetryRig(t, dirac500Session, diracZAIObservedAt)
	r.feed(dirac500Card, dirac500Dialog)
	r.a.FinishPromptRequestForTest(dirac500Session, nil, &providerkit.JSONRPCResponseError{Code: -32603, Message: "Internal error"})
	_, retries := diracRetryTimeline(t, r.sink.Timeline())
	assert.Equal(t, []string{"cancel_server_error"}, diracRetryOps(retries))
}

// A card of an earlier turn does not decide the next prompt: PromptPrepared
// clears it before the prompt starts.
func TestDiracEarlierCardDecidesNothing(t *testing.T) {
	t.Parallel()
	r := newDiracRetryRig(t, dirac500Session, diracZAIObservedAt)
	r.a.SetPromptActiveForTest(false)
	r.feed(dirac500Card, dirac500Dialog)
	require.JSONEq(t, diracRejectOnce, r.answerTo(t, "0"))
	require.NoError(t, r.a.SendInput("Say hello.", nil))
	testutil.RequireEventually(t, func() bool { return !r.a.PromptActive() })
	assert.Zero(t, r.sink.AutoScheduleCount(), "the earlier card must not schedule")
	assert.Contains(t, diracRetryOps(r.sink.Timeline()), "cancel_server_error")
}

// A dialog that offers no reject option, or two, stays with the reader.
func TestDiracLeavesADialogWithoutOneRejectOptionToTheReader(t *testing.T) {
	t.Parallel()
	r := newDiracRetryRig(t, dirac500Session, diracZAIObservedAt)
	for name, options := range map[string]string{
		"no reject option": `[{"kind":"allow_once","optionId":"allow_once","name":"Approve once"}]`,
		"two reject options": `[{"kind":"reject_once","optionId":"reject_once","name":"Reject once"},` +
			`{"kind":"reject_once","optionId":"reject_twice","name":"Reject again"}]`,
	} {
		dialog := dirac500Dialog[:strings.Index(dirac500Dialog, `"options":`)] + `"options":` + options + `}}`
		_, ok := r.a.answerDiracFailedRequest(providerkit.ParseLine([]byte(dialog)))
		assert.False(t, ok, name)
	}
}

// A queued answer marks only the card of the answered tool call. An answer
// report for another dialog leaves the recorded card undeclined.
func TestDiracAnswerReportMarksOnlyTheAnsweredCard(t *testing.T) {
	t.Parallel()
	r := newDiracRetryRig(t, dirac500Session, diracZAIObservedAt)
	r.feed(dirac500Card)
	other := providerkit.ParseLine([]byte(strings.ReplaceAll(dirac500Dialog, "1793148722806-9", "other-call")))
	r.a.HooksForTest().ControlRequestAnswered(other, true)
	r.endTurn(t, dirac500Session)
	_, retries := diracRetryTimeline(t, r.sink.Timeline())
	assert.Equal(t, []string{"cancel_server_error"}, diracRetryOps(retries))
}

// Without the card update, the dialog itself carries the failure, and its
// line becomes the payload.
func TestDiracDialogWithoutItsCardStillSchedules(t *testing.T) {
	t.Parallel()
	r := newDiracRetryRig(t, dirac500Session, diracZAIObservedAt)
	r.feed(dirac500Dialog)
	require.JSONEq(t, diracRejectOnce, r.answerTo(t, "0"))
	r.endTurn(t, dirac500Session)
	_, retries := diracRetryTimeline(t, r.sink.Timeline())
	require.Equal(t, []string{"schedule_server_error"}, diracRetryOps(retries))
	assert.JSONEq(t, dirac500Dialog, string(r.sink.LastAutoSchedule().SourcePayload))
}

// diracPromptScript plays Dirac's side of one session/prompt: it hands the
// agent the frames that Dirac writes, and returns Dirac's response. prompt
// counts the prompts from 1.
type diracPromptScript func(a *Agent, prompt int) (response string)

// diracWrites is the script of a prompt that writes the frames of lines and
// then ends with end_turn.
func diracWrites(lines func(prompt int) []string) diracPromptScript {
	return func(a *Agent, prompt int) string {
		for _, line := range lines(prompt) {
			a.HandleOutput([]byte(line))
		}
		return diracEndTurn
	}
}

// newDiracScriptedRig is the rig of newDiracRetryRig with no running prompt.
// Its peer answers each session/prompt through script.
func newDiracScriptedRig(t *testing.T, sessionID string, script diracPromptScript) *diracRetryRig {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	clock.Set(diracZAIObservedAt)
	var target atomic.Pointer[Agent]
	var prompts atomic.Int32
	a, requests := acptest.NewAgentForRPCWithRequestResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		func(request agenttest.RecordedRequest) agenttest.RPCReply {
			if request.Method != acp.MethodSessionPrompt {
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(script(target.Load(), int(prompts.Add(1))))}
		},
	)
	*a.HooksForTest() = a.configure(clock)
	sink := &agenttest.ControlSink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest(sessionID)
	target.Store(a)
	return &diracRetryRig{a: a, sink: sink, requests: requests}
}

// dirac500Failure is the script of a prompt whose model request fails with
// the 500 capture: the card, its pending repeat, and the dialog, which takes
// the JSON-RPC id dialogID.
func dirac500Failure(dialogID int) []string {
	return []string{dirac500Card, dirac500Pending, strings.Replace(dirac500Dialog, `"id":0`, `"id":`+strconv.Itoa(dialogID), 1)}
}

// send starts a prompt of content through route and waits for its end.
func (r *diracRetryRig) send(t *testing.T, route func(a *Agent, content string) error, content string) {
	t.Helper()
	require.NoError(t, route(r.a, content))
	testutil.RequireEventually(t, func() bool { return !r.a.PromptActive() })
}

// diracSendInput starts a prompt through SendInput.
func diracSendInput(a *Agent, content string) error {
	return a.SendInput(content, nil)
}

// diracCompactionCancel is the retry operation of a compaction's end: one
// cancel of the server-error retry, with the compaction's reason.
var diracCompactionCancel = providerkittest.TurnEndThen(providerkit.CompactionEnd())[1:]

// Dirac 0.5.23 runs /smol and /compact, with or without arguments, as its
// condense action, and then goes on to a model request in the same prompt
// (BuiltinCommandExecutor.ts). LeapMux schedules no retry for a compaction, so
// it leaves the failed-request dialog of that request to the reader, and the
// prompt's end cancels the server-error retry with the compaction's reason.
func TestDiracCompactionLeavesTheFailedRequestDialogToTheReader(t *testing.T) {
	t.Parallel()
	routes := map[string]func(a *Agent, content string) error{
		"SendInput": diracSendInput,
		"SendInputForSession": func(a *Agent, content string) error {
			return a.SendInputForSession(dirac500Session, content, nil)
		},
	}
	for _, content := range []string{"/smol", "/compact", "/compact keep the test plan", "  /smol\n"} {
		for route, send := range routes {
			t.Run(route+" "+strconv.Quote(content), func(t *testing.T) {
				t.Parallel()
				r := newDiracScriptedRig(t, dirac500Session, diracWrites(func(int) []string { return dirac500Failure(0) }))

				r.send(t, send, content)
				assert.Empty(t, r.answerTo(t, "0"), "LeapMux writes no answer")
				assert.Equal(t, 1, r.sink.PublishedControlCount(), "the reader answers the dialog")
				lastRow, retries := diracRetryTimeline(t, r.sink.Timeline())
				assert.Equal(t, "turn_end", lastRow)
				assert.Equal(t, diracCompactionCancel, retries)
				assert.Zero(t, r.sink.AutoScheduleCount())
			})
		}
	}
}

// A compaction that ends well, or that the reader stops, cancels the
// server-error retry once, with the compaction's own reason.
func TestDiracCompactionEndStatesTheCompactionReason(t *testing.T) {
	t.Parallel()
	t.Run("a compaction that ends well", func(t *testing.T) {
		t.Parallel()
		r := newDiracScriptedRig(t, dirac500Session, diracWrites(func(int) []string { return nil }))

		r.send(t, diracSendInput, "/smol")
		assert.Equal(t, providerkittest.TurnEndThen(providerkit.CompactionEnd()), providerkittest.TurnEndAndRetryOps(r.sink.Timeline()))
	})
	t.Run("a compaction that the reader stops", func(t *testing.T) {
		t.Parallel()
		r := newDiracScriptedRig(t, dirac500Session, func(a *Agent, _ int) string {
			a.HandleOutput([]byte(dirac500Card))
			a.HandleOutput([]byte(dirac500Dialog))
			a.NoteInterruptRequestedForTest()
			return `{"stopReason":"cancelled"}`
		})

		r.send(t, diracSendInput, "/smol")
		assert.Equal(t, providerkittest.TurnEndThen(providerkit.CompactionEnd()), providerkittest.TurnEndAndRetryOps(r.sink.Timeline()))
		assert.Equal(t, 1, r.sink.AutoCancelCount())
	})
}

// The prompt after a compaction is a conversation turn again: LeapMux rejects
// its 500 dialog, and its end schedules the server-error retry.
func TestDiracPromptAfterACompactionRejectsItsDialogAndSchedules(t *testing.T) {
	t.Parallel()
	r := newDiracScriptedRig(t, dirac500Session, diracWrites(dirac500Failure))
	r.send(t, diracSendInput, "/smol")
	compactionOps := len(r.sink.Timeline())

	r.send(t, diracSendInput, "Say hello.")
	assert.Empty(t, r.answerTo(t, "1"), "the compaction's dialog stays with the reader")
	assert.JSONEq(t, diracRejectOnce, r.answerTo(t, "2"), "LeapMux rejects the next prompt's dialog")
	lastRow, retries := diracRetryTimeline(t, r.sink.Timeline()[compactionOps:])
	assert.Equal(t, "turn_end", lastRow)
	assert.Equal(t, []string{"schedule_server_error"}, diracRetryOps(retries))
}

// Only a prompt that opens with /smol or /compact, with no attachment, is a
// compaction. LeapMux rejects the 500 dialog of every other prompt and
// schedules the server-error retry.
func TestDiracOtherInputIsNoCompaction(t *testing.T) {
	t.Parallel()
	attachment := []*leapmuxv1.Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("Keep the plan.")}}
	for name, input := range map[string]struct {
		content     string
		attachments []*leapmuxv1.Attachment
	}{
		"the command with an attachment": {"/compact", attachment},
		"a longer command name":          {"/smolder", nil},
		"another command":                {"/newrule", nil},
		"a plain message":                {"Say hello.", nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newDiracScriptedRig(t, dirac500Session, diracWrites(func(int) []string { return dirac500Failure(0) }))

			r.send(t, func(a *Agent, content string) error { return a.SendInput(content, input.attachments) }, input.content)
			assert.JSONEq(t, diracRejectOnce, r.answerTo(t, "0"))
			_, retries := diracRetryTimeline(t, r.sink.Timeline())
			assert.Equal(t, []string{"schedule_server_error"}, diracRetryOps(retries))
		})
	}
}

// A compaction for a session that is no longer current starts no prompt.
func TestDiracCompactionForAnotherSessionStartsNothing(t *testing.T) {
	t.Parallel()
	r := newDiracScriptedRig(t, dirac500Session, diracWrites(func(int) []string { return nil }))

	require.ErrorIs(t, r.a.SendInputForSession("session-replaced", "/smol", nil), agent.ErrInputSessionChanged)
	assert.False(t, r.a.PromptActive())
	syncPeer(t, r.a)
	assert.Empty(t, requestsFor(r.requests(), acp.MethodSessionPrompt))
}

// The preparation of a compaction prompt checks the session that the base
// claimed for the prompt, because the session can change after the check
// before the send. It marks only a prompt of the expected session.
func TestDiracCompactionPreparationChecksTheClaimedSession(t *testing.T) {
	t.Parallel()
	expected := dirac500Session
	for name, tc := range map[string]struct {
		expected *string
		claimed  string
		marks    bool
	}{
		"the expected session":            {&expected, dirac500Session, true},
		"a session that replaced it":      {&expected, "session-replaced", false},
		"any session for the current one": {nil, "session-replaced", true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newDiracRetryRig(t, dirac500Session, diracZAIObservedAt)
			err := r.a.prepareCompactionPrompt(tc.expected)(tc.claimed)
			if tc.marks {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, agent.ErrInputSessionChanged)
			}
			r.a.retryMu.Lock()
			marked := r.a.compactionPrompt
			r.a.retryMu.Unlock()
			assert.Equal(t, tc.marks, marked)
		})
	}
}
