package reasonix

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// reasonixTurnFixture is one probed turn of Reasonix 1.38.7
// (.tmp/probe/reasonix/runs/<name>/wire.jsonl): the last status update of the
// turn and the session/prompt response that followed it, both verbatim.
type reasonixTurnFixture struct {
	Name         string          `json:"name"`
	StatusUpdate json.RawMessage `json:"statusUpdate"`
	Response     json.RawMessage `json:"response"`
}

// sessionID is the session that the fixture's status update states.
func (f reasonixTurnFixture) sessionID(t *testing.T) string {
	t.Helper()
	var line struct {
		Params struct {
			SessionID string `json:"sessionId"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal(f.StatusUpdate, &line))
	require.NotEmpty(t, line.Params.SessionID)
	return line.Params.SessionID
}

// statusParams is the params member of the fixture's status update.
func (f reasonixTurnFixture) statusParams(t *testing.T) json.RawMessage {
	t.Helper()
	var line struct {
		Params json.RawMessage `json:"params"`
	}
	require.NoError(t, json.Unmarshal(f.StatusUpdate, &line))
	return line.Params
}

// responseError is the error of the fixture's response, as the base hands it
// to Hooks.PromptEnded, and nil for a result.
func (f reasonixTurnFixture) responseError(t *testing.T) error {
	t.Helper()
	var line struct {
		Error *struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(f.Response, &line))
	if line.Error == nil {
		return nil
	}
	return &providerkit.JSONRPCResponseError{Code: line.Error.Code, Message: line.Error.Message, Data: line.Error.Data}
}

// responseResult is the result of the fixture's response, and nil for an
// error.
func (f reasonixTurnFixture) responseResult(t *testing.T) json.RawMessage {
	t.Helper()
	var line struct {
		Result json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(f.Response, &line))
	return line.Result
}

// reasonixTurnFixtures reads testdata/turn_failure.jsonl by fixture name.
func reasonixTurnFixtures(t *testing.T) map[string]reasonixTurnFixture {
	t.Helper()
	file, err := os.Open("testdata/turn_failure.jsonl")
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	fixtures := make(map[string]reasonixTurnFixture)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var fixture reasonixTurnFixture
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &fixture))
		fixtures[fixture.Name] = fixture
	}
	require.NoError(t, scanner.Err())
	return fixtures
}

// Reasonix reports `goal.status:"failed"` in the last status of every failed
// turn, with no objective and no goal set. That report must not write a goal
// row.
func TestReasonixFailedTurnWithoutAGoalWritesNoGoalRow(t *testing.T) {
	t.Parallel()
	fixture := reasonixTurnFixtures(t)["e500"]
	sink := &agenttest.Sink{}
	a := &Agent{}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "test-agent"})
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest(fixture.sessionID(t))

	require.True(t, a.handleExtraMethod(&providerkit.ParsedLine{Method: reasonixMethodStatusUpdate, Params: fixture.statusParams(t)}))

	assert.Empty(t, sink.Goals(), "no goal report")
	// The status states no objective, so the provider clears the goal. The
	// worker's clear of an absent goal writes no row.
	assert.Equal(t, 1, sink.GoalClears())
	assert.Empty(t, sink.LeapMuxNotifications(), "no goal row joins the transcript")
	assert.Empty(t, sink.Messages())
}

func TestReasonixTurnFailure(t *testing.T) {
	t.Parallel()
	fixtures := reasonixTurnFixtures(t)
	observedAt := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		fixture  string
		wantKind providerkit.TurnFailureKind
	}{
		{"ok", providerkit.TurnFailureNotRetryable},
		{"e500", providerkit.TurnFailureTransient},
		{"e429ra", providerkit.TurnFailureRateLimited},
		{"drop", providerkit.TurnFailureTransient},
		{"e402", providerkit.TurnFailureBillingBlocked},
		{"e429quota", providerkit.TurnFailureUsageLimited},
		{"e401", providerkit.TurnFailureNotRetryable},
		{"e400ctx", providerkit.TurnFailureNotRetryable},
		{"e400policy", providerkit.TurnFailureNotRetryable},
	} {
		t.Run("the "+tc.fixture+" turn", func(t *testing.T) {
			t.Parallel()
			fixture := fixtures[tc.fixture]
			failure := reasonixTurnFailure(fixture.responseError(t), false, reasonixRecordOf(t, fixture.statusParams(t)), observedAt)
			assert.Equal(t, tc.wantKind, failure.Kind)
			assert.True(t, failure.ResetAt.IsZero())
			assert.Equal(t, providerkit.UsageWindowUnstated, failure.Window, "Reasonix states no window")
			if failure.Kind != providerkit.TurnFailureNotRetryable {
				assert.NotEmpty(t, failure.Cause)
			}
		})
	}

	failed := fixtures["e500"].responseError(t)
	record := func(kind string, status int, reason string) *reasonixFailureRecord {
		r := &reasonixFailureRecord{}
		r.outcome.Kind = "error"
		r.outcome.Reason = reason
		r.outcome.Diagnostic.Kind = kind
		r.outcome.Diagnostic.Status = status
		return r
	}
	for _, tc := range []struct {
		name     string
		err      error
		stopped  bool
		record   *reasonixFailureRecord
		wantKind providerkit.TurnFailureKind
	}{
		{name: "a stop is not retryable", err: failed, stopped: true, record: record("temporary", 0, ""), wantKind: providerkit.TurnFailureNotRetryable},
		// Reasonix 2.53.1 sends no diagnostic: the prompt's own error text decides.
		{name: "a failure with no record reads its error text", err: failed, wantKind: providerkit.TurnFailureTransient},
		{name: "a failure with no record and no provider text is not retryable", err: reasonixUnclassifiedPromptError, wantKind: providerkit.TurnFailureNotRetryable},
		{name: "a record with no failure is not retryable", record: record("temporary", 0, ""), wantKind: providerkit.TurnFailureNotRetryable},
		// The kinds that Reasonix adds after 1.39.10 (R/reasonix.md).
		{name: "a transport failure is transient", err: failed, record: record("transport_eof", 0, "stream ended"), wantKind: providerkit.TurnFailureTransient},
		{name: "a send timeout is transient", err: failed, record: record("request_send_timeout", 0, ""), wantKind: providerkit.TurnFailureTransient},
		// Reasonix does not retry an error event inside a stream that answered
		// 200, and neither does LeapMux: the kind is unknown.
		{
			name: "an unknown kind is not retryable", err: failed,
			record:   record("unknown", 0, `e500 · Anthropic Messages: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`),
			wantKind: providerkit.TurnFailureNotRetryable,
		},
		{name: "a cancelled kind is not retryable", err: failed, record: record("cancelled", 0, "context canceled"), wantKind: providerkit.TurnFailureNotRetryable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			failure := reasonixTurnFailure(tc.err, tc.stopped, tc.record, observedAt)
			assert.Equal(t, tc.wantKind, failure.Kind)
		})
	}

	t.Run("an error event with no turn outcome records nothing", func(t *testing.T) {
		t.Parallel()
		params := json.RawMessage(`{"sessionId":"reasonix-new","event":"error","status":{"phase":"error"}}`)
		assert.Nil(t, reasonixRecordOf(t, params))
	})
	t.Run("an update of another event records nothing", func(t *testing.T) {
		t.Parallel()
		params := json.RawMessage(`{"sessionId":"reasonix-new","event":"phase","status":{"turnOutcome":{"kind":"error","diagnostic":{"kind":"temporary"}}}}`)
		assert.Nil(t, reasonixRecordOf(t, params))
	})

	t.Run("a definitive vendor verdict decides before the kind", func(t *testing.T) {
		t.Parallel()
		// GLM through Reasonix, with the text of modelvendor's Pi capture
		// inside Reasonix's own wording. Reasonix waits out a 429, so the
		// kind would give a rate limit.
		reason := `provider unreachable for 9m48s (headers): zai · Chat Completions: status 429: {"code":"1308","message":"Usage limit reached for 5 hour. Your limit will reset at 2026-07-14 19:06:13"}`
		failure := reasonixTurnFailure(failed, false, record("recovery_wait_exhausted", 429, reason), time.Date(2026, 7, 14, 10, 34, 45, 0, time.UTC))
		assert.Equal(t, providerkit.TurnFailureUsageLimited, failure.Kind)
		assert.Equal(t, time.Date(2026, 7, 14, 11, 6, 14, 0, time.UTC), failure.ResetAt)
	})
}

// reasonixRecordOf records the failure of one status update as the provider
// does, and returns nil for an update that states none.
func reasonixRecordOf(t *testing.T, params json.RawMessage) *reasonixFailureRecord {
	t.Helper()
	var update reasonixStatusUpdate
	require.NoError(t, json.Unmarshal(params, &update))
	a := &Agent{}
	a.observeReasonixFailure(update, params)
	return a.failureRecord
}

// newReasonixRetryAgent builds an agent with the hooks that Start configures,
// over a peer that answers session/prompt with prompt, and a transcript that
// records every row and retry operation in order.
func newReasonixRetryAgent(t *testing.T, prompt agenttest.RPCReply) (*Agent, *agenttest.Sink) {
	t.Helper()
	a, _ := acptest.NewAgentForRPCWithRequestResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		func(request agenttest.RecordedRequest) agenttest.RPCReply {
			if request.Method == acp.MethodSessionPrompt {
				return prompt
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		})
	sink := &agenttest.Sink{}
	hooks := a.configure(context.Background(), agent.Options{HomeDir: t.TempDir(), WorkingDir: t.TempDir()}, agent.NewProviderServices(sink), "deepseek-flash")
	// The start keeps the sink of the hooks as the agent's own.
	a.SetSinkForTest(hooks.Sink)
	hooks.Sink = nil
	*a.HooksForTest() = hooks
	return a, sink
}

func reasonixTimelineOps(timeline []agenttest.TimelineEntry) []string {
	ops := make([]string, 0, len(timeline))
	for _, entry := range timeline {
		ops = append(ops, entry.Op)
	}
	return ops
}

// The failed turn's last status update records its failure, and the prompt's
// end applies one verdict after the failure note.
func TestReasonixPromptEndAppliesTheVerdictOfTheTurnStatus(t *testing.T) {
	t.Parallel()
	fixtures := reasonixTurnFixtures(t)
	for _, tc := range []struct {
		fixture string
		wantOps []string
	}{
		{"ok", []string{"turn_end", "cancel_server_error"}},
		{"e500", []string{"leapmux_notification", "schedule_server_error"}},
		{"e429ra", []string{"leapmux_notification", "schedule_server_error"}},
		{"drop", []string{"leapmux_notification", "schedule_server_error"}},
		{"e402", []string{"leapmux_notification", "cancel_usage_limit", "cancel_server_error"}},
		{"e429quota", []string{"leapmux_notification", "cancel_server_error"}},
		{"e401", []string{"leapmux_notification", "cancel_server_error"}},
	} {
		t.Run("the "+tc.fixture+" turn", func(t *testing.T) {
			t.Parallel()
			fixture := fixtures[tc.fixture]
			a, sink := newReasonixRetryAgent(t, agenttest.RPCReply{Result: json.RawMessage(`{}`)})
			sessionID := fixture.sessionID(t)
			a.SetSessionIDForTest(sessionID)
			a.SetPromptActiveForTest(true)

			a.HandleOutput(fixture.StatusUpdate)
			a.FinishPromptRequestForTest(sessionID, fixture.responseResult(t), fixture.responseError(t))

			require.Equal(t, tc.wantOps, reasonixTimelineOps(sink.Timeline()))
			if tc.wantOps[len(tc.wantOps)-1] == "schedule_server_error" {
				assert.JSONEq(t, string(fixture.statusParams(t)), string(sink.LastAutoSchedule().SourcePayload),
					"the status update that states the failure is the retry's source")
			}
		})
	}
}

// A status update of the session that a context clear replaced records
// nothing for the current session's prompt.
func TestReasonixFailureOfAnotherSessionIsNotRecorded(t *testing.T) {
	t.Parallel()
	fixture := reasonixTurnFixtures(t)["e500"]
	a, sink := newReasonixRetryAgent(t, agenttest.RPCReply{Result: json.RawMessage(`{}`)})
	a.SetSessionIDForTest("reasonix-new")
	a.SetPromptActiveForTest(true)

	a.HandleOutput(fixture.StatusUpdate)
	a.FinishPromptRequestForTest("reasonix-new", nil, reasonixUnclassifiedPromptError)

	assert.Equal(t, []string{"leapmux_notification", "cancel_server_error"}, reasonixTimelineOps(sink.Timeline()))
}

// A prompt that Reasonix rejects before its turn starts gets no status update.
// The failure that a late status update recorded after the last prompt's end
// belongs to no prompt, and the next prompt's start forgets it.
func TestReasonixPreTurnRejectionAfterAStaleFailureIsNotRetryable(t *testing.T) {
	t.Parallel()
	fixture := reasonixTurnFixtures(t)["e500"]
	// No probe captured a pre-turn rejection. ACP answers a malformed or busy
	// session/prompt with an invalid-params error (R/reasonix.md).
	a, sink := newReasonixRetryAgent(t, agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"Invalid params"}`)})
	sessionID := fixture.sessionID(t)
	a.SetSessionIDForTest(sessionID)
	a.HandleOutput(fixture.StatusUpdate)

	require.NoError(t, a.SendInput("Say hello.", nil))
	testutil.RequireEventually(t, func() bool { return !a.PromptActive() })

	require.Equal(t, []string{"leapmux_notification", "cancel_server_error"}, reasonixTimelineOps(sink.Timeline()))
	assert.Equal(t, providerkit.TurnEndedWithoutRetryableFailure, sink.LastAutoCancelReason())
}

// The end of the prompt takes the record, so the next prompt, which fails
// with no status of its own, reads no failure of the last one.
func TestReasonixPromptEndTakesTheRecord(t *testing.T) {
	t.Parallel()
	fixture := reasonixTurnFixtures(t)["drop"]
	a, sink := newReasonixRetryAgent(t, agenttest.RPCReply{Result: json.RawMessage(`{}`)})
	sessionID := fixture.sessionID(t)
	a.SetSessionIDForTest(sessionID)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(fixture.StatusUpdate)
	a.FinishPromptRequestForTest(sessionID, nil, fixture.responseError(t))

	a.SetPromptActiveForTest(true)
	a.FinishPromptRequestForTest(sessionID, nil, reasonixUnclassifiedPromptError)

	assert.Equal(t, []string{"leapmux_notification", "schedule_server_error", "leapmux_notification", "cancel_server_error"},
		reasonixTimelineOps(sink.Timeline()))
}

// A context clear forgets the failure of the session that it replaced.
func TestReasonixContextClearForgetsTheFailure(t *testing.T) {
	t.Parallel()
	fixture := reasonixTurnFixtures(t)["e500"]
	a, sink := newReasonixRetryAgent(t, agenttest.RPCReply{Result: json.RawMessage(`{}`)})
	sessionID := fixture.sessionID(t)
	a.SetSessionIDForTest(sessionID)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(fixture.StatusUpdate)

	a.HooksForTest().ClearProviderState()
	a.FinishPromptRequestForTest(sessionID, nil, reasonixUnclassifiedPromptError)

	assert.Equal(t, []string{"leapmux_notification", "cancel_server_error"}, reasonixTimelineOps(sink.Timeline()))
}

// reasonixUnclassifiedPromptError is a failed session/prompt whose text states
// no provider failure. A test that checks which record decides fails a prompt
// with it, so that only a leaked record could make the prompt retry.
var reasonixUnclassifiedPromptError = &providerkit.JSONRPCResponseError{Code: -32603, Message: "session/prompt: the request was refused"}

// Reasonix 2.53.1 states no diagnostic, so the failure text decides: the
// reason of the turn outcome, or else the message of the failed prompt. The
// texts follow the formats of its provider client
// (internal/contract/provider/retry.go and internal/model/*/stream*.go).
func TestReasonixTextFailure(t *testing.T) {
	t.Parallel()
	observedAt := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		text string
		want providerkit.TurnFailureKind
	}{
		{"a 500 is transient", `deepseek: status 500: {"error":{"message":"Internal server error","type":"server_error"}}`, providerkit.TurnFailureTransient},
		{"a 503 without a body is transient", `deepseek: status 503`, providerkit.TurnFailureTransient},
		{"a 408 is transient", `deepseek: status 408: request timeout`, providerkit.TurnFailureTransient},
		{"a 429 is a rate limit", `deepseek: status 429: {"error":{"message":"slow down please"}}`, providerkit.TurnFailureRateLimited},
		{"a vendor's monthly limit behind a 429 does not retry", `kimi: status 429: You've reached your monthly usage limit for this billing cycle.`, providerkit.TurnFailureUsageLimited},
		{"a 402 is a billing block", `deepseek: status 402: {"error":{"message":"Insufficient Balance"}}`, providerkit.TurnFailureBillingBlocked},
		{"a 400 is not retryable", `deepseek: status 400: {"error":{"message":"bad request"}}`, providerkit.TurnFailureNotRetryable},
		{"a dropped OpenAI stream is transient", `drop: stream ended before completion: unexpected EOF`, providerkit.TurnFailureTransient},
		{"a dropped Anthropic stream is transient", `anthropic: stream ended before message_stop: unexpected EOF`, providerkit.TurnFailureTransient},
		{"a refused connection is transient", `deepseek: Post "http://127.0.0.1:1/v1/chat/completions": dial tcp 127.0.0.1:1: connect: connection refused`, providerkit.TurnFailureTransient},
		{"a definitive vendor verdict decides first", `zai: status 500: {"error":{"code":"1113","message":"Insufficient balance or no resource package. Please recharge."}}`, providerkit.TurnFailureBillingBlocked},
		{"an empty text is not retryable", ``, providerkit.TurnFailureNotRetryable},
		{"a refusal of the request is not retryable", `the request was refused`, providerkit.TurnFailureNotRetryable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, reasonixTextFailure(tc.text, observedAt).Kind)
		})
	}
}

// A status update that an earlier sequence number marks as stale restates an
// older status. Its failure records nothing, so the prompt's own error decides.
func TestReasonixStaleStatusUpdateRecordsNoFailure(t *testing.T) {
	t.Parallel()
	a, sink := newReasonixRetryAgent(t, agenttest.RPCReply{Result: json.RawMessage(`{}`)})
	a.SetSessionIDForTest("reasonix-2")
	a.SetPromptActiveForTest(true)
	current := `{"schemaVersion":1,"sequence":9,"sessionId":"reasonix-2","event":"phase","status":{"phase":"implementing"}}`
	stale := `{"schemaVersion":1,"sequence":7,"sessionId":"reasonix-2","event":"error","status":{"turnOutcome":{"kind":"error","reason":"deepseek: status 503: upstream unavailable"}}}`

	require.True(t, a.handleExtraMethod(&providerkit.ParsedLine{Method: reasonixMethodStatusUpdate, Params: json.RawMessage(current)}))
	require.True(t, a.handleExtraMethod(&providerkit.ParsedLine{Method: reasonixMethodStatusUpdate, Params: json.RawMessage(stale)}))
	a.FinishPromptRequestForTest("reasonix-2", nil, reasonixUnclassifiedPromptError)

	assert.Equal(t, []string{"leapmux_notification", "cancel_server_error"}, reasonixTimelineOps(sink.Timeline()))
}

// A Reasonix 2.53.1 status update states the outcome with no diagnostic. Its
// reason decides, and the status update is the retry's source.
func TestReasonix2StatusUpdateDecidesByItsReason(t *testing.T) {
	t.Parallel()
	a, sink := newReasonixRetryAgent(t, agenttest.RPCReply{Result: json.RawMessage(`{}`)})
	a.SetSessionIDForTest("reasonix-2")
	a.SetPromptActiveForTest(true)
	params := `{"schemaVersion":1,"sequence":7,"sessionId":"reasonix-2","event":"error","status":{"turnOutcome":{"kind":"error","reason":"deepseek: status 503: upstream unavailable"}}}`

	require.True(t, a.handleExtraMethod(&providerkit.ParsedLine{Method: reasonixMethodStatusUpdate, Params: json.RawMessage(params)}))
	a.FinishPromptRequestForTest("reasonix-2", nil, reasonixUnclassifiedPromptError)

	assert.Equal(t, []string{"leapmux_notification", "schedule_server_error"}, reasonixTimelineOps(sink.Timeline()))
	assert.JSONEq(t, params, string(sink.LastAutoSchedule().SourcePayload))
}
