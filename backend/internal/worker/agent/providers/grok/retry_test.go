package grok

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// grokTurnFixture is one probed prompt of Grok Build 1.0.70
// (.tmp/probe/grok/run1, run2 and run3 acp.jsonl): the last retry_state frame
// of the turn, null for a turn that sent none, and the session/prompt response
// that followed it, both verbatim.
type grokTurnFixture struct {
	Name       string          `json:"name"`
	RetryState json.RawMessage `json:"retryState"`
	Response   json.RawMessage `json:"response"`
}

// sessionID is the session that the fixture's retry_state frame states.
func (f grokTurnFixture) sessionID(t *testing.T) string {
	t.Helper()
	var line struct {
		Params struct {
			SessionID string `json:"sessionId"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal(f.RetryState, &line))
	require.NotEmpty(t, line.Params.SessionID)
	return line.Params.SessionID
}

// responseError is the error of the fixture's response, as the base hands it
// to Hooks.PromptEnded, and nil for a result.
func (f grokTurnFixture) responseError(t *testing.T) error {
	t.Helper()
	return grokResponseError(t, f.Response)
}

// errorObject is the error member of the fixture's response, verbatim.
func (f grokTurnFixture) errorObject(t *testing.T) json.RawMessage {
	t.Helper()
	var line struct {
		Error json.RawMessage `json:"error"`
	}
	require.NoError(t, json.Unmarshal(f.Response, &line))
	return line.Error
}

// failureType is the error_type of the fixture's retry_state frame, as
// reportRetry records it.
func (f grokTurnFixture) failureType(t *testing.T) string {
	t.Helper()
	if string(f.RetryState) == "null" {
		return ""
	}
	var line struct {
		Params struct {
			Update struct {
				ErrorType string `json:"error_type"`
			} `json:"update"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal(f.RetryState, &line))
	return line.Params.Update.ErrorType
}

func grokResponseError(t *testing.T, response json.RawMessage) error {
	t.Helper()
	var line struct {
		Error *struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(response, &line))
	if line.Error == nil {
		return nil
	}
	return &providerkit.JSONRPCResponseError{Code: line.Error.Code, Message: line.Error.Message, Data: line.Error.Data}
}

// grokTurnFixtures reads testdata/turn_failure.jsonl by fixture name.
func grokTurnFixtures(t *testing.T) map[string]grokTurnFixture {
	t.Helper()
	file, err := os.Open("testdata/turn_failure.jsonl")
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	fixtures := make(map[string]grokTurnFixture)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var fixture grokTurnFixture
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &fixture))
		fixtures[fixture.Name] = fixture
	}
	require.NoError(t, scanner.Err())
	return fixtures
}

func TestGrokTurnFailure(t *testing.T) {
	t.Parallel()
	fixtures := grokTurnFixtures(t)
	observedAt := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		fixture    string
		wantKind   providerkit.TurnFailureKind
		wantWindow providerkit.UsageWindow
	}{
		{fixture: "success", wantKind: providerkit.TurnFailureNotRetryable},
		{fixture: "e503", wantKind: providerkit.TurnFailureTransient},
		{fixture: "e529", wantKind: providerkit.TurnFailureTransient},
		{fixture: "streamdrop", wantKind: providerkit.TurnFailureTransient},
		{fixture: "e429", wantKind: providerkit.TurnFailureRateLimited},
		{fixture: "freeusage", wantKind: providerkit.TurnFailureUsageLimited, wantWindow: providerkit.UsageWindowUnstated},
		{fixture: "e402", wantKind: providerkit.TurnFailureBillingBlocked},
		{fixture: "e401", wantKind: providerkit.TurnFailureNotRetryable},
		{fixture: "e400context", wantKind: providerkit.TurnFailureNotRetryable},
		{fixture: "e403policy", wantKind: providerkit.TurnFailureNotRetryable},
	} {
		t.Run("the "+tc.fixture+" prompt", func(t *testing.T) {
			t.Parallel()
			fixture := fixtures[tc.fixture]
			failure, payload := grokTurnFailure(fixture.responseError(t), false, fixture.failureType(t), observedAt)
			assert.Equal(t, tc.wantKind, failure.Kind)
			assert.Equal(t, tc.wantWindow, failure.Window)
			assert.True(t, failure.ResetAt.IsZero(), "Grok states no reset instant")
			if failure.Kind != providerkit.TurnFailureNotRetryable {
				assert.NotEmpty(t, failure.Cause)
			}
			if fixture.responseError(t) != nil {
				assert.JSONEq(t, string(fixture.errorObject(t)), string(payload), "the payload is the wire's error")
			} else {
				assert.Nil(t, payload)
			}
		})
	}

	internalError := func(data string) error {
		return &providerkit.JSONRPCResponseError{Code: -32603, Message: "Internal error", Data: json.RawMessage(data)}
	}
	for _, tc := range []struct {
		name        string
		err         error
		stopped     bool
		failureType string
		wantKind    providerkit.TurnFailureKind
	}{
		{name: "a stop is not retryable", err: fixtures["e503"].responseError(t), stopped: true, failureType: "api", wantKind: providerkit.TurnFailureNotRetryable},
		{name: "a failure with no JSON-RPC error is not retryable", err: errors.New("agent process exited"), wantKind: providerkit.TurnFailureNotRetryable},
		// A 5xx that a failed frame of another class preceded is that class's.
		{name: "a 5xx after an authentication failure is not retryable", err: fixtures["e503"].responseError(t), failureType: "auth", wantKind: providerkit.TurnFailureNotRetryable},
		{name: "a 5xx with no failed frame is transient", err: fixtures["e503"].responseError(t), wantKind: providerkit.TurnFailureTransient},
		// A context overflow can arrive inside a 500 (R/cursor-kiro-grok.md).
		{name: "a 500 after a context overflow is not retryable", err: internalError(`{"message":"API error (status 500 Internal Server Error): context length exceeded","http_status":500}`), failureType: "context_length", wantKind: providerkit.TurnFailureNotRetryable},
		{name: "a 525 is not retryable", err: internalError(`{"message":"API error (status 525 <unknown status code>): SSL handshake failed","http_status":525}`), failureType: "api", wantKind: providerkit.TurnFailureNotRetryable},
		{name: "a 526 is not retryable", err: internalError(`{"message":"API error (status 526 <unknown status code>): Invalid SSL certificate","http_status":526}`), failureType: "api", wantKind: providerkit.TurnFailureNotRetryable},
		{name: "a request error is transient", err: internalError(`"request error: error sending request for url (https://api.x.ai/v1/responses)"`), failureType: "http", wantKind: providerkit.TurnFailureTransient},
		{name: "an overloaded stream error is transient", err: internalError(`"stream error (overloaded_error): Overloaded"`), failureType: "api", wantKind: providerkit.TurnFailureTransient},
		{name: "an idle timeout is transient", err: internalError(`{"message":"the model stream went idle","error_kind":"idle_timeout"}`), failureType: "idle_timeout", wantKind: providerkit.TurnFailureTransient},
		{name: "a 403 that states run-out credits is a billing block", err: internalError(`{"message":"API error (status 403 Forbidden): invalid_request_error: Your team has run out of credits.","http_status":403}`), failureType: "api", wantKind: providerkit.TurnFailureBillingBlocked},
		{name: "an internal error with no data is not retryable", err: internalError(`null`), wantKind: providerkit.TurnFailureNotRetryable},
		{name: "an encrypted-content mismatch is not retryable", err: &providerkit.JSONRPCResponseError{Code: -32602, Message: "Invalid params", Data: json.RawMessage(`"encrypted content does not match"`)}, wantKind: providerkit.TurnFailureNotRetryable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			failure, _ := grokTurnFailure(tc.err, tc.stopped, tc.failureType, observedAt)
			assert.Equal(t, tc.wantKind, failure.Kind)
		})
	}

	t.Run("a vendor usage limit with a reset instant waits for the stamp", func(t *testing.T) {
		t.Parallel()
		// GLM behind Grok's model gateway, with the text of modelvendor's Pi
		// capture inside Grok's 429 wording.
		err := &providerkit.JSONRPCResponseError{Code: -32003, Message: "Rate limited", Data: json.RawMessage(
			`"API error (status 429 Too Many Requests): {\"code\":\"1308\",\"message\":\"Usage limit reached for 5 hour. Your limit will reset at 2026-07-14 19:06:13\"}"`)}
		failure, _ := grokTurnFailure(err, false, "", time.Date(2026, 7, 14, 10, 34, 45, 0, time.UTC))
		assert.Equal(t, providerkit.TurnFailureUsageLimited, failure.Kind)
		assert.Equal(t, time.Date(2026, 7, 14, 11, 6, 14, 0, time.UTC), failure.ResetAt)
	})
}

// newGrokRetryAgent builds a Grok agent with its own hooks, whose transcript is
// a recording sink that keeps the order of every row and retry operation.
func newGrokRetryAgent(t *testing.T, respond func(agenttest.RecordedRequest) agenttest.RPCReply) (*Agent, *agenttest.Sink) {
	t.Helper()
	a, _, _ := newGrokAgent(t, agent.Options{}, respond)
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	return a, sink
}

func grokTimelineOps(timeline []agenttest.TimelineEntry) []string {
	ops := make([]string, 0, len(timeline))
	for _, entry := range timeline {
		ops = append(ops, entry.Op)
	}
	return ops
}

// The last retry_state frame of the main session records its error_type, and
// the prompt's end applies one verdict after the failure note. A failed or
// exhausted frame writes no row of its own.
func TestGrokPromptEndAppliesOneVerdictAfterTheFailureNote(t *testing.T) {
	t.Parallel()
	fixtures := grokTurnFixtures(t)
	for _, tc := range []struct {
		fixture string
		wantOps []string
	}{
		{"e503", []string{"leapmux_notification", "schedule_server_error"}},
		{"streamdrop", []string{"leapmux_notification", "schedule_server_error"}},
		{"e529", []string{"leapmux_notification", "schedule_server_error"}},
		{"e429", []string{"leapmux_notification", "schedule_server_error"}},
		{"freeusage", []string{"leapmux_notification", "cancel_server_error"}},
		{"e402", []string{"leapmux_notification", "cancel_usage_limit", "cancel_server_error"}},
		{"e401", []string{"leapmux_notification", "cancel_server_error"}},
		{"e400context", []string{"leapmux_notification", "cancel_server_error"}},
	} {
		t.Run("the "+tc.fixture+" prompt", func(t *testing.T) {
			t.Parallel()
			fixture := fixtures[tc.fixture]
			a, sink := newGrokRetryAgent(t, nil)
			sessionID := fixture.sessionID(t)
			a.SetSessionIDForTest(sessionID)
			a.SetPromptActiveForTest(true)

			a.HandleOutput(fixture.RetryState)
			a.FinishPromptRequestForTest(sessionID, nil, fixture.responseError(t))

			require.Equal(t, tc.wantOps, grokTimelineOps(sink.Timeline()))
			if tc.wantOps[len(tc.wantOps)-1] == "schedule_server_error" {
				assert.JSONEq(t, string(fixture.errorObject(t)), string(sink.LastAutoSchedule().SourcePayload))
			}
		})
	}
}

// grokFailedFrame is a retry_state frame that ends a failed request of
// sessionID, in the shape of the probed frames.
func grokFailedFrame(t *testing.T, sessionID, errorType string) []byte {
	t.Helper()
	return notification(t, sessionID, map[string]any{
		"sessionUpdate": "retry_state", "type": "failed", "error_type": errorType,
		"message": "Unauthorized (401) from https://api.x.ai/v1/responses: invalid_request_error: Incorrect API key provided.",
	})
}

// A subagent's own failed request is not the main session's failure.
func TestGrokSubagentFailedFrameLeavesTheRecordUnchanged(t *testing.T) {
	t.Parallel()
	fixture := grokTurnFixtures(t)["e503"]
	a, sink := newGrokRetryAgent(t, nil)
	a.SetSessionIDForTest(grokTestSession)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(spawnCall(t, "call_2_0", "List files", "FGCHILD: list the working directory.", false))
	a.HandleOutput(spawned(t, "sub-1", "List files", nil))

	a.HandleOutput(grokFailedFrame(t, grokTestSession, "api"))
	a.HandleOutput(grokFailedFrame(t, "sub-1", "auth"))
	a.FinishPromptRequestForTest(grokTestSession, nil, fixture.responseError(t))

	ops := grokTimelineOps(sink.Timeline())
	require.NotEmpty(t, ops)
	assert.Equal(t, "schedule_server_error", ops[len(ops)-1], "the main session's own error_type decides")
}

// The start of the next prompt forgets the error_type of a failed request
// that no prompt end took, as one of a turn that Grok started by itself.
func TestGrokPromptStartForgetsAStaleFailureType(t *testing.T) {
	t.Parallel()
	fixture := grokTurnFixtures(t)["e503"]
	a, sink := newGrokRetryAgent(t, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method == acp.MethodSessionPrompt {
			return agenttest.RPCReply{Error: fixture.errorObject(t)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetSessionIDForTest(grokTestSession)
	a.HandleOutput(grokFailedFrame(t, grokTestSession, "auth"))

	require.NoError(t, a.SendInput("Say hello.", nil))
	testutil.RequireEventually(t, func() bool { return !a.PromptActive() })

	ops := grokTimelineOps(sink.Timeline())
	require.NotEmpty(t, ops)
	assert.Equal(t, "schedule_server_error", ops[len(ops)-1], "a 5xx with no failed frame of its own is transient")
}

// The end of the prompt takes the error_type, so the next prompt's failure
// with no frame of its own reads none.
func TestGrokPromptEndTakesTheFailureType(t *testing.T) {
	t.Parallel()
	fixture := grokTurnFixtures(t)["e503"]
	a, sink := newGrokRetryAgent(t, nil)
	a.SetSessionIDForTest(grokTestSession)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(grokFailedFrame(t, grokTestSession, "auth"))
	a.FinishPromptRequestForTest(grokTestSession, nil, fixture.responseError(t))

	a.SetPromptActiveForTest(true)
	a.FinishPromptRequestForTest(grokTestSession, nil, fixture.responseError(t))

	assert.Equal(t, []string{"leapmux_notification", "cancel_server_error", "leapmux_notification", "schedule_server_error"},
		grokTimelineOps(sink.Timeline()))
}

// A turn that Grok started by itself ends with its own frame, and the default
// verdict cancels the server-error retry after that row.
func TestGrokAgentTurnEndCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()
	a, sink := newGrokRetryAgent(t, nil)
	a.SetSessionIDForTest(grokTestSession)
	a.HandleOutput(queueChanged(t, grokTestSession, "subagent-completed-bg-1"))
	require.True(t, a.AgentTurnActive())

	a.HandleOutput(turnCompleted(t, "subagent-completed-bg-1"))

	assert.Equal(t, []string{"turn_end", "cancel_server_error"}, grokTimelineOps(sink.Timeline()))
}
