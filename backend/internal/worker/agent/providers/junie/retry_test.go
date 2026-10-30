package junie

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The turns of Junie 3419.7 behind a LiteLLM proxy (R/junie.md), verbatim:
// the sessionFailure that Junie states, then the response of the prompt.
const (
	junie503Failure = `{"type":"com.agentclientprotocol.rpc.JsonRpcNotification","method":"session/update","params":{"sessionId":"session-261028-101920-ad1r","update":{"sessionUpdate":"session_info_update","_meta":{"jetbrains":{"air":{"version":1,"sessionFailure":{"id":"turn-1:error","revision":1,"category":"connection","severity":"error","title":"LiteLLM: probe status 503","actions":["retry"]}}}}}},"jsonrpc":"2.0"}`

	junie429Retrying = `{"type":"com.agentclientprotocol.rpc.JsonRpcNotification","method":"session/update","params":{"sessionId":"session-261028-102144-9v6z","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"The model provider is not available right now: it keeps rejecting requests with 429 Too Many Requests.  \nRetrying for 30s so far; giving up after around 5m.\n\n"},"messageId":"416c8aa5-7637-4e82-a92c-a6c4fa9d90a3","_meta":{"codex":{"phase":"commentary"},"junie":{"version":1,"phase":"commentary"}}}},"jsonrpc":"2.0"}`
	junie429Failure  = `{"type":"com.agentclientprotocol.rpc.JsonRpcNotification","method":"session/update","params":{"sessionId":"session-261028-102144-9v6z","update":{"sessionUpdate":"session_info_update","_meta":{"jetbrains":{"air":{"version":1,"sessionFailure":{"id":"turn-1:error","revision":1,"category":"service","severity":"error","title":"The model provider is not available right now: it keeps rejecting requests with 429 Too Many Requests.","details":"Try again in a few minutes or switch to another model.\n\nprobe status 429","actions":[]}}}}}},"jsonrpc":"2.0"}`

	junie500Failure = `{"type":"com.agentclientprotocol.rpc.JsonRpcNotification","method":"session/update","params":{"sessionId":"session-261028-101537-1hdh","update":{"sessionUpdate":"session_info_update","_meta":{"jetbrains":{"air":{"version":1,"sessionFailure":{"id":"turn-1:error","revision":1,"category":"unknown","severity":"error","title":"LiteLLM: probe status 500","actions":["retry"]}}}}}},"jsonrpc":"2.0"}`

	junieBillingFailure = `{"type":"com.agentclientprotocol.rpc.JsonRpcNotification","method":"session/update","params":{"sessionId":"session-261028-104611-1095","update":{"sessionUpdate":"session_info_update","_meta":{"jetbrains":{"air":{"version":1,"sessionFailure":{"id":"turn-1:error","revision":1,"category":"unknown","severity":"error","title":"LiteLLM: Insufficient account balance. All tokens in your account have been spent.","actions":[]}}}}}},"jsonrpc":"2.0"}`

	junieSuccessAnswer = `{"type":"com.agentclientprotocol.rpc.JsonRpcNotification","method":"session/update","params":{"sessionId":"session-261028-104431-1u2j","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"pong\n\n"},"messageId":"7a9ba64f-095d-4cbd-b2e8-3b890e650b3e","_meta":{"codex":{"phase":"final_answer"},"junie":{"version":1,"phase":"final_answer"}}}},"jsonrpc":"2.0"}`

	// junieFormatFailure is a failed review of the model's own answer
	// (.tmp/probe/junie/run/t-chat.log), which no wait fixes.
	junieFormatFailure = `{"type":"com.agentclientprotocol.rpc.JsonRpcNotification","method":"session/update","params":{"sessionId":"session-261028-104303-o6kr","update":{"sessionUpdate":"session_info_update","_meta":{"jetbrains":{"air":{"version":1,"sessionFailure":{"id":"turn-1:error","revision":1,"category":"service","severity":"error","title":"Failed to pass user review after 6 attempts","actions":["retry"]}}}}}},"jsonrpc":"2.0"}`

	junieCancelledResult = `{"stopReason":"cancelled","usage":{"inputTokens":0,"outputTokens":0,"totalTokens":0,"cachedReadTokens":0,"cachedWriteTokens":0}}`
	junieEndTurnResult   = `{"stopReason":"end_turn","usage":{"inputTokens":30,"outputTokens":3,"totalTokens":33,"cachedReadTokens":0,"cachedWriteTokens":0}}`
)

// junieWarning is a warning in the shape of the probed failures. No probe
// provoked one; Junie gives a warning the id `turn-N:warning-M` and the
// fields of an error (R/junie.md).
const junieWarning = `{"type":"com.agentclientprotocol.rpc.JsonRpcNotification","method":"session/update","params":{"sessionId":"session-261028-101920-ad1r","update":{"sessionUpdate":"session_info_update","_meta":{"jetbrains":{"air":{"version":1,"sessionFailure":{"id":"turn-1:warning-1","revision":1,"category":"request","severity":"warning","title":"The terminal is not available.","details":"Commands run without a terminal.","actions":[]}}}}}},"jsonrpc":"2.0"}`

// junieLineSession is the session that one Junie line states.
func junieLineSession(t *testing.T, line string) string {
	t.Helper()
	var frame struct {
		Params struct {
			SessionID string `json:"sessionId"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal([]byte(line), &frame))
	require.NotEmpty(t, frame.Params.SessionID)
	return frame.Params.SessionID
}

// junieLineUpdate is the update object of one Junie session/update line.
func junieLineUpdate(t *testing.T, line string) json.RawMessage {
	t.Helper()
	var frame struct {
		Params struct {
			Update json.RawMessage `json:"update"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal([]byte(line), &frame))
	return frame.Params.Update
}

// junieLineFailure is the record that one sessionFailure line leaves.
func junieLineFailure(t *testing.T, line string) *junieFailureRecord {
	t.Helper()
	update := junieLineUpdate(t, line)
	var header struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	require.NoError(t, json.Unmarshal(update, &header))
	failure, ok := readJunieSessionFailure(header.Meta)
	require.True(t, ok)
	return &junieFailureRecord{failure: failure, frame: update}
}

func TestJunieTurnFailure(t *testing.T) {
	t.Parallel()
	observedAt := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		failure  string
		wantKind providerkit.TurnFailureKind
	}{
		{"a connection failure is transient", junie503Failure, providerkit.TurnFailureTransient},
		{"a service failure with no action is a rate limit", junie429Failure, providerkit.TurnFailureRateLimited},
		{"an unknown failure that offers a retry is transient", junie500Failure, providerkit.TurnFailureTransient},
		{"an unknown failure with no action is a billing block", junieBillingFailure, providerkit.TurnFailureBillingBlocked},
		{"a failed review of the answer is not retryable", junieFormatFailure, providerkit.TurnFailureNotRetryable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			record := junieLineFailure(t, tc.failure)
			verdict := junieTurnVerdict(nil, false, record, observedAt)
			assert.Equal(t, tc.wantKind, verdict.Failure.Kind)
			if tc.wantKind != providerkit.TurnFailureNotRetryable {
				assert.NotEmpty(t, verdict.Failure.Cause)
			}
			assert.JSONEq(t, string(record.frame), string(verdict.Payload), "the update that states the failure is the retry's source")
		})
	}

	record := junieLineFailure(t, junie503Failure)
	rpcError := func(code int, message, data string) error {
		err := &providerkit.JSONRPCResponseError{Code: code, Message: message}
		if data != "" {
			err.Data = json.RawMessage(data)
		}
		return err
	}
	for _, tc := range []struct {
		name     string
		err      error
		stopped  bool
		record   *junieFailureRecord
		wantKind providerkit.TurnFailureKind
	}{
		{name: "a stop is not retryable", stopped: true, record: record, wantKind: providerkit.TurnFailureNotRetryable},
		{name: "a turn with no failure is not retryable", wantKind: providerkit.TurnFailureNotRetryable},
		// Junie 3419.7 declares these errors; no probe provoked them.
		{name: "the rate-limit code is a rate limit", err: rpcError(-32011, "Rate limit exceeded", ""), wantKind: providerkit.TurnFailureRateLimited},
		{name: "the rate-limit reason is a rate limit", err: rpcError(-32000, "Request failed", `{"reason":"rate_limit_exceeded"}`), wantKind: providerkit.TurnFailureRateLimited},
		{name: "the balance code is a billing block", err: rpcError(-32010, "Insufficient account balance", ""), wantKind: providerkit.TurnFailureBillingBlocked},
		{name: "the balance reason is a billing block", err: rpcError(-32000, "Request failed", `{"reason":"insufficient_account_balance"}`), wantKind: providerkit.TurnFailureBillingBlocked},
		// A JSON-RPC error decides over a recorded failure.
		{name: "another error is not retryable", err: rpcError(-32000, "Authentication is required before this operation can be performed.", ""), record: record, wantKind: providerkit.TurnFailureNotRetryable},
		{name: "a failure with no JSON-RPC error is not retryable", err: errors.New("agent process exited"), record: record, wantKind: providerkit.TurnFailureNotRetryable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			verdict := junieTurnVerdict(tc.err, tc.stopped, tc.record, observedAt)
			assert.Equal(t, tc.wantKind, verdict.Failure.Kind)
		})
	}

	t.Run("a JSON-RPC error is the retry's source", func(t *testing.T) {
		t.Parallel()
		verdict := junieTurnVerdict(rpcError(-32011, "Rate limit exceeded", `{"reason":"rate_limit_exceeded"}`), false, record, observedAt)
		assert.JSONEq(t, `{"code":-32011,"message":"Rate limit exceeded","data":{"reason":"rate_limit_exceeded"}}`, string(verdict.Payload))
	})
	t.Run("a vendor usage limit with a reset instant waits for the stamp", func(t *testing.T) {
		t.Parallel()
		// GLM behind a LiteLLM proxy, with the text of modelvendor's Pi
		// capture as the details of Junie's 429 give-up.
		failure := junieSessionFailure{
			ID: "turn-1:error", Category: "service", Severity: "error",
			Title:   "The model provider is not available right now: it keeps rejecting requests with 429 Too Many Requests.",
			Details: `Try again in a few minutes or switch to another model.` + "\n\n" + `429: {"code":"1308","message":"Usage limit reached for 5 hour. Your limit will reset at 2026-07-14 19:06:13"}`,
		}
		verdict := junieTurnVerdict(nil, false, &junieFailureRecord{failure: failure}, time.Date(2026, 7, 14, 10, 34, 45, 0, time.UTC))
		assert.Equal(t, providerkit.TurnFailureUsageLimited, verdict.Failure.Kind)
		assert.Equal(t, time.Date(2026, 7, 14, 11, 6, 14, 0, time.UTC), verdict.Failure.ResetAt)
	})
}

// newJunieRetryAgent builds an agent with the hooks that Start configures, over
// a peer that answers through respond, and a transcript that records every row
// and retry operation in order.
func newJunieRetryAgent(t *testing.T, respond func(method string) agenttest.RPCReply) (*Agent, *agenttest.Sink) {
	t.Helper()
	if respond == nil {
		respond = func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} }
	}
	a, _ := acptest.NewAgentForRPCWithResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		respond)
	sink := &agenttest.Sink{}
	clock := testutil.NewQuartzMock(t)
	clock.Set(time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC))
	hooks := a.configure(context.Background(), agent.Options{HomeDir: t.TempDir(), WorkingDir: t.TempDir()}, agent.NewProviderServices(sink), clock)
	// The start keeps the sink of the hooks as the agent's own.
	a.SetSinkForTest(hooks.Sink)
	hooks.Sink = nil
	*a.HooksForTest() = hooks
	return a, sink
}

func junieTimelineOps(timeline []agenttest.TimelineEntry) []string {
	ops := make([]string, 0, len(timeline))
	for _, entry := range timeline {
		ops = append(ops, entry.Op)
	}
	return ops
}

func TestJunieAdvertisesTheSessionFailureCapability(t *testing.T) {
	t.Parallel()
	a, _ := newJunieRetryAgent(t, nil)
	meta := a.HooksForTest().ClientCapabilityMeta
	assert.Equal(t, map[string]any{"jetbrains": map[string]any{"air": map[string]any{
		"version": 1, "capabilities": []string{"nativeSubagentSessions", "sessionFailure"},
	}}}, meta)
}

// Junie states a failure of a prompt in a sessionFailure, and the prompt then
// ends with a plain `cancelled`. The failure note states the title and the
// details, and the verdict follows the turn-end row.
func TestJuniePromptEndAppliesTheVerdictOfTheSessionFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		lines     []string
		result    string
		wantOps   []string
		wantNotes []map[string]any
	}{
		{
			name: "a connection failure schedules a server-error retry", lines: []string{junie503Failure}, result: junieCancelledResult,
			wantOps:   []string{"leapmux_notification", "turn_end", "schedule_server_error"},
			wantNotes: []map[string]any{{"type": "agent_error", "error": "LiteLLM: probe status 503"}},
		},
		{
			name: "a rate limit that Junie gave up on schedules a server-error retry", lines: []string{junie429Retrying, junie429Failure}, result: junieCancelledResult,
			wantOps: []string{"message", "leapmux_notification", "turn_end", "schedule_server_error"},
			wantNotes: []map[string]any{{
				"type":  "agent_error",
				"error": "The model provider is not available right now: it keeps rejecting requests with 429 Too Many Requests.\n\nTry again in a few minutes or switch to another model.\n\nprobe status 429",
			}},
		},
		{
			name: "a provider failure that offers a retry schedules a server-error retry", lines: []string{junie500Failure}, result: junieCancelledResult,
			wantOps:   []string{"leapmux_notification", "turn_end", "schedule_server_error"},
			wantNotes: []map[string]any{{"type": "agent_error", "error": "LiteLLM: probe status 500"}},
		},
		{
			name: "a spent balance cancels both retries", lines: []string{junieBillingFailure}, result: junieCancelledResult,
			wantOps:   []string{"leapmux_notification", "turn_end", "cancel_usage_limit", "cancel_server_error"},
			wantNotes: []map[string]any{{"type": "agent_error", "error": "LiteLLM: Insufficient account balance. All tokens in your account have been spent."}},
		},
		{
			name: "a failed review of the answer cancels the server-error retry", lines: []string{junieFormatFailure}, result: junieCancelledResult,
			wantOps:   []string{"leapmux_notification", "turn_end", "cancel_server_error"},
			wantNotes: []map[string]any{{"type": "agent_error", "error": "Failed to pass user review after 6 attempts"}},
		},
		{
			name: "a warning is no failure", lines: []string{junieWarning}, result: junieEndTurnResult,
			wantOps:   []string{"leapmux_notification", "turn_end", "cancel_server_error"},
			wantNotes: []map[string]any{{"type": "agent_status", "text": "The terminal is not available.\n\nCommands run without a terminal."}},
		},
		{
			name: "an answer cancels the server-error retry", lines: []string{junieSuccessAnswer}, result: junieEndTurnResult,
			wantOps: []string{"message", "turn_end", "cancel_server_error"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newJunieRetryAgent(t, nil)
			sessionID := junieLineSession(t, tc.lines[0])
			a.SetSessionIDForTest(sessionID)
			a.SetPromptActiveForTest(true)

			for _, line := range tc.lines {
				a.HandleOutput([]byte(line))
			}
			a.FinishPromptRequestForTest(sessionID, json.RawMessage(tc.result), nil)

			require.Equal(t, tc.wantOps, junieTimelineOps(sink.Timeline()))
			assert.Equal(t, tc.wantNotes, sink.LeapMuxNotifications())
			if tc.wantOps[len(tc.wantOps)-1] == "schedule_server_error" {
				assert.JSONEq(t, string(junieLineUpdate(t, tc.lines[len(tc.lines)-1])), string(sink.LastAutoSchedule().SourcePayload),
					"the update that states the failure is the retry's source")
			}
		})
	}
}

// A revision of the failure that states the same text writes no second note.
func TestJunieRepeatedSessionFailureStatesItsNoteOnce(t *testing.T) {
	t.Parallel()
	a, sink := newJunieRetryAgent(t, nil)
	sessionID := junieLineSession(t, junie503Failure)
	a.SetSessionIDForTest(sessionID)
	a.SetPromptActiveForTest(true)

	a.HandleOutput([]byte(junie503Failure))
	a.HandleOutput([]byte(junie503Failure))
	a.FinishPromptRequestForTest(sessionID, json.RawMessage(junieCancelledResult), nil)

	assert.Equal(t, []string{"leapmux_notification", "turn_end", "schedule_server_error"}, junieTimelineOps(sink.Timeline()))
}

// Junie also sends `_meta.lastRequestComplete` in a turn of LeapMux's. The
// marker then ends no agent turn and makes no retry call, and the prompt's end
// still reads the recorded failure.
func TestJunieTurnEndMarkerOfALeapMuxPromptKeepsTheFailure(t *testing.T) {
	t.Parallel()
	a, sink := newJunieRetryAgent(t, nil)
	sessionID := junieLineSession(t, junie503Failure)
	a.SetSessionIDForTest(sessionID)
	a.SetPromptActiveForTest(true)

	a.HandleOutput([]byte(junie503Failure))
	a.HandleSessionUpdateForTest(junieSessionUpdateParams(sessionID, "agent_message_chunk", "", map[string]any{"lastRequestComplete": true}))
	require.Equal(t, []string{"leapmux_notification"}, junieTimelineOps(sink.Timeline()), "the marker makes no retry call")

	a.FinishPromptRequestForTest(sessionID, json.RawMessage(junieCancelledResult), nil)

	assert.Equal(t, []string{"leapmux_notification", "turn_end", "schedule_server_error"}, junieTimelineOps(sink.Timeline()))
	assert.Equal(t, 1, sink.AutoScheduleCount())
}

// A steer that started a turn of Junie's own ends that turn at the marker,
// which applies the turn's verdict once. The prompt's end before it hands the
// busy state over, and applies none.
func TestJunieSteerStartedTurnDecidesOnceAtItsMarker(t *testing.T) {
	t.Parallel()
	a, sink := newJunieRetryAgent(t, func(method string) agenttest.RPCReply {
		if method == junieSteerMethod {
			return agenttest.RPCReply{Result: json.RawMessage(`{"outcome":"startedNewTurn"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	sessionID := junieLineSession(t, junie503Failure)
	a.SetSessionIDForTest(sessionID)
	a.SetPromptActiveForTest(true)
	require.NoError(t, a.SteerInput("Steer text while active.", nil))
	a.FinishPromptRequestForTest(sessionID, json.RawMessage(junieEndTurnResult), nil)
	testutil.RequireEventually(t, func() bool { return a.AgentTurnActive() })
	require.Equal(t, []string{"turn_end"}, junieTimelineOps(sink.Timeline()), "the queued turn's end decides")

	a.HandleOutput([]byte(junie503Failure))
	a.HandleSessionUpdateForTest(junieSessionUpdateParams(sessionID, "agent_message_chunk", "", map[string]any{"lastRequestComplete": true}))

	assert.False(t, a.AgentTurnActive())
	assert.Equal(t, []string{"turn_end", "leapmux_notification", "schedule_server_error"}, junieTimelineOps(sink.Timeline()))
}

// newJunieSteeredAgent builds an agent whose prompt is active, and whose steer
// starts a turn of Junie's own, which waits behind that prompt.
func newJunieSteeredAgent(t *testing.T) (*Agent, *agenttest.Sink, string) {
	t.Helper()
	a, sink := newJunieRetryAgent(t, func(method string) agenttest.RPCReply {
		if method == junieSteerMethod {
			return agenttest.RPCReply{Result: json.RawMessage(`{"outcome":"startedNewTurn"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	sessionID := junieLineSession(t, junie503Failure)
	a.SetSessionIDForTest(sessionID)
	a.SetPromptActiveForTest(true)
	require.NoError(t, a.SteerInput("Steer text while active.", nil))
	return a, sink, sessionID
}

type junieTurnEndBarrier struct {
	*agenttest.Sink
	entered chan struct{}
	release <-chan struct{}
}

func (sink *junieTurnEndBarrier) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	close(sink.entered)
	<-sink.release
	return sink.Sink.PersistTurnEnd(content, span)
}

func TestJunieQueuedFailureDuringPromptPersistenceKeepsItsVerdict(t *testing.T) {
	t.Parallel()
	a, sink, sessionID := newJunieSteeredAgent(t)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	barrier := &junieTurnEndBarrier{Sink: sink, entered: make(chan struct{}), release: release}
	a.SetSinkForTest(agent.NewOrderedProviderServices(&agent.OutputQueue{}, agent.NewProviderServices(barrier)))
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.FinishPromptRequestForTest(sessionID, json.RawMessage(junieCancelledResult), nil)
	}()
	select {
	case <-barrier.entered:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the prompt did not reach its turn-end write")
	}
	a.HandleOutput([]byte(junie503Failure))
	marker := junieSessionUpdateParams(sessionID, "agent_message_chunk", "", map[string]any{"lastRequestComplete": true})
	a.HandleSessionUpdateForTest(marker)
	assert.Zero(t, sink.AutoScheduleCount())
	unblock()
	select {
	case <-done:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the prompt did not finish its queued handoff")
	}

	assert.False(t, a.PromptActive())
	require.Equal(t, 1, sink.AutoScheduleCount())
	assert.Zero(t, sink.AutoCancelCount())
	assert.JSONEq(t, string(junieLineUpdate(t, junie503Failure)), string(sink.LastAutoSchedule().SourcePayload))
	a.HandleSessionUpdateForTest(marker)
	assert.Equal(t, 1, sink.AutoScheduleCount(), "a repeated marker applies no second verdict")
}

// The failure of a steer-started turn can arrive before the response of the
// prompt that the turn waits behind. That failure belongs to the agent turn:
// the prompt's end hands the busy state over without a verdict, and the turn's
// own marker decides from the failure.
func TestJunieSteerStartedTurnKeepsItsFailureThroughThePromptEnd(t *testing.T) {
	t.Parallel()
	a, sink, sessionID := newJunieSteeredAgent(t)
	a.HandleOutput([]byte(junie503Failure))
	a.FinishPromptRequestForTest(sessionID, json.RawMessage(junieCancelledResult), nil)
	testutil.RequireEventually(t, func() bool { return a.AgentTurnActive() })
	require.Equal(t, []string{"leapmux_notification", "turn_end"}, junieTimelineOps(sink.Timeline()), "the prompt's end applies no verdict")

	a.HandleSessionUpdateForTest(junieSessionUpdateParams(sessionID, "agent_message_chunk", "", map[string]any{"lastRequestComplete": true}))

	assert.False(t, a.AgentTurnActive())
	assert.Equal(t, []string{"leapmux_notification", "turn_end", "schedule_server_error"}, junieTimelineOps(sink.Timeline()))
	assert.Equal(t, 1, sink.AutoScheduleCount())
}

// When the steer-started turn ends before the prompt's response, the base
// retains that turn's completion and applies its verdict after the prompt ends.
func TestJunieSteerStartedTurnThatEndsFirstDecidesAtThePromptEnd(t *testing.T) {
	t.Parallel()
	a, sink, sessionID := newJunieSteeredAgent(t)
	a.HandleOutput([]byte(junie503Failure))
	a.HandleSessionUpdateForTest(junieSessionUpdateParams(sessionID, "agent_message_chunk", "", map[string]any{"lastRequestComplete": true}))
	require.Equal(t, []string{"leapmux_notification"}, junieTimelineOps(sink.Timeline()), "the marker of a waiting turn makes no retry call")

	a.FinishPromptRequestForTest(sessionID, json.RawMessage(junieCancelledResult), nil)

	assert.False(t, a.AgentTurnActive())
	assert.Equal(t, []string{"leapmux_notification", "turn_end", "schedule_server_error"}, junieTimelineOps(sink.Timeline()))
	assert.Equal(t, 1, sink.AutoScheduleCount())
}

// A turn that a steer starts runs after the prompt's own work ended, so the
// prompt's failure says nothing about it. A steer-started turn that ends with
// no failure of its own cancels the server-error retry.
func TestJunieSteerStartForgetsThePromptFailure(t *testing.T) {
	t.Parallel()
	a, sink := newJunieRetryAgent(t, func(method string) agenttest.RPCReply {
		if method == junieSteerMethod {
			return agenttest.RPCReply{Result: json.RawMessage(`{"outcome":"startedNewTurn"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	sessionID := junieLineSession(t, junie503Failure)
	a.SetSessionIDForTest(sessionID)
	a.SetPromptActiveForTest(true)
	a.HandleOutput([]byte(junie503Failure))
	require.NoError(t, a.SteerInput("Steer text while active.", nil))
	a.FinishPromptRequestForTest(sessionID, json.RawMessage(junieCancelledResult), nil)
	testutil.RequireEventually(t, func() bool { return a.AgentTurnActive() })

	a.HandleSessionUpdateForTest(junieSessionUpdateParams(sessionID, "agent_message_chunk", "", map[string]any{"lastRequestComplete": true}))

	assert.Equal(t, []string{"leapmux_notification", "turn_end", "cancel_server_error"}, junieTimelineOps(sink.Timeline()))
	assert.Zero(t, sink.AutoScheduleCount())
}

// A reader who stops a steer-started turn never gets a retry, even when Junie
// recorded a retryable failure before the turn ended.
func TestJunieStoppedSteerStartedTurnIsNotRetried(t *testing.T) {
	t.Parallel()
	a, sink := newJunieRetryAgent(t, func(method string) agenttest.RPCReply {
		if method == junieSteerMethod {
			return agenttest.RPCReply{Result: json.RawMessage(`{"outcome":"startedNewTurn"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	sessionID := junieLineSession(t, junie503Failure)
	a.SetSessionIDForTest(sessionID)
	a.SetPromptActiveForTest(true)
	require.NoError(t, a.SteerInput("Steer text while active.", nil))
	a.FinishPromptRequestForTest(sessionID, json.RawMessage(junieEndTurnResult), nil)
	testutil.RequireEventually(t, func() bool { return a.AgentTurnActive() })

	a.HandleOutput([]byte(junie503Failure))
	require.NoError(t, a.Interrupt(agent.NewStopContext(nil)))
	require.True(t, a.InterruptRequested(), "the stop of the agent turn holds until the turn ends")
	a.HandleSessionUpdateForTest(junieSessionUpdateParams(sessionID, "agent_message_chunk", "", map[string]any{"lastRequestComplete": true}))

	assert.False(t, a.AgentTurnActive())
	require.Equal(t, []string{"turn_end", "leapmux_notification", "cancel_server_error"}, junieTimelineOps(sink.Timeline()))
	assert.Equal(t, providerkit.TurnEndedWithoutRetryableFailure, sink.LastAutoCancelReason())
	assert.Zero(t, sink.AutoScheduleCount())
}

// A steer-started turn that ends with no failure cancels the server-error
// retry at its marker.
func TestJunieSteerStartedTurnWithoutAFailureCancels(t *testing.T) {
	t.Parallel()
	a, sink := newJunieRetryAgent(t, func(method string) agenttest.RPCReply {
		if method == junieSteerMethod {
			return agenttest.RPCReply{Result: json.RawMessage(`{"outcome":"startedNewTurn"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetPromptActiveForTest(true)
	require.NoError(t, a.SteerInput("Steer text while active.", nil))
	a.FinishPromptRequestForTest("session-1", json.RawMessage(junieEndTurnResult), nil)
	testutil.RequireEventually(t, func() bool { return a.AgentTurnActive() })

	a.HandleSessionUpdateForTest(junieSessionUpdateParams("session-1", "agent_message_chunk", "", map[string]any{"lastRequestComplete": true}))

	require.Equal(t, []string{"turn_end", "cancel_server_error"}, junieTimelineOps(sink.Timeline()))
	assert.Equal(t, providerkit.TurnEndedWithoutRetryableFailure, sink.LastAutoCancelReason())
}

// The start of the next prompt forgets a failure that no turn end took.
func TestJuniePromptStartForgetsAStaleFailure(t *testing.T) {
	t.Parallel()
	a, sink := newJunieRetryAgent(t, func(method string) agenttest.RPCReply {
		if method == acp.MethodSessionPrompt {
			return agenttest.RPCReply{Result: json.RawMessage(junieCancelledResult)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	sessionID := junieLineSession(t, junie503Failure)
	a.SetSessionIDForTest(sessionID)
	a.HandleOutput([]byte(junie503Failure))

	require.NoError(t, a.SendInput("Say hello.", nil))
	testutil.RequireEventually(t, func() bool { return !a.PromptActive() })

	require.Equal(t, []string{"leapmux_notification", "turn_end", "cancel_server_error"}, junieTimelineOps(sink.Timeline()))
	assert.Equal(t, providerkit.TurnEndedWithoutRetryableFailure, sink.LastAutoCancelReason())
}

// A context clear forgets the failure of the session that it replaced.
func TestJunieContextClearForgetsTheFailure(t *testing.T) {
	t.Parallel()
	a, sink := newJunieRetryAgent(t, nil)
	sessionID := junieLineSession(t, junie503Failure)
	a.SetSessionIDForTest(sessionID)
	a.SetPromptActiveForTest(true)
	a.HandleOutput([]byte(junie503Failure))

	a.HooksForTest().ClearProviderState()
	a.FinishPromptRequestForTest(sessionID, json.RawMessage(junieCancelledResult), nil)

	assert.Equal(t, []string{"leapmux_notification", "turn_end", "cancel_server_error"}, junieTimelineOps(sink.Timeline()))
}

// A failure of a child session reaches the child's own transcript, never the
// record of the main session.
func TestJunieChildSessionFailureIsNotRecorded(t *testing.T) {
	t.Parallel()
	a, sink := newJunieRetryAgent(t, nil)
	a.SetSessionIDForTest("junie-main")
	a.SetPromptActiveForTest(true)
	a.HandleSessionUpdateForTest(junieSessionUpdate(t, "junie-main", map[string]any{
		"sessionUpdate": "subagent_spawned", "subagentSessionId": "junie-child-1",
		"name": "junie-cli-docs", "task": "Read the local file.",
	}))

	childFailure := junieLineUpdate(t, junie503Failure)
	params, err := json.Marshal(map[string]any{"sessionId": "junie-child-1", "update": childFailure})
	require.NoError(t, err)
	a.HandleSessionUpdateForTest(params)
	a.FinishPromptRequestForTest("junie-main", json.RawMessage(junieCancelledResult), nil)

	ops := junieTimelineOps(sink.Timeline())
	require.NotEmpty(t, ops)
	assert.Equal(t, "cancel_server_error", ops[len(ops)-1])
	assert.Empty(t, agenttest.AgentErrorTexts(sink.LeapMuxNotifications()))
}

// A sessionFailure that does not decode leaves the update to the base, which
// drops a session_info_update.
func TestJunieUnreadableSessionFailureRecordsNothing(t *testing.T) {
	t.Parallel()
	a, sink := newJunieRetryAgent(t, nil)
	a.SetPromptActiveForTest(true)
	update := map[string]any{
		"sessionUpdate": contracts.ACPUpdateSessionInfoUpdate,
		"_meta":         map[string]any{"jetbrains": map[string]any{"air": map[string]any{"sessionFailure": "not an object"}}},
	}
	a.HandleSessionUpdateForTest(junieSessionUpdate(t, "session-1", update))
	a.FinishPromptRequestForTest("session-1", json.RawMessage(junieCancelledResult), nil)

	assert.Equal(t, []string{"turn_end", "cancel_server_error"}, junieTimelineOps(sink.Timeline()))
}
