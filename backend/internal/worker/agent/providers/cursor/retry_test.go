package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The chunk texts of .tmp/retry-research/cursor-kiro-grok.md and
// .tmp/probe/cursor/fake/runs/<mode>/acp.log, cursor-agent
// 2026.09.28-64d2043, copied verbatim. Every failure ends the prompt with
// {"stopReason":"end_turn"} after one agent message chunk.
const (
	cursorUnavailableText       = "\n\nError: RetriableError: [unavailable] upstream connect error or disconnect/reset before headers"
	cursorRefusedText           = "\n\nError: RetriableError: [unavailable] connect ECONNREFUSED 127.0.0.1:47399"
	cursorProviderErrorText     = "\n\nError: RetriableError: [unavailable] fake provider error"
	cursorInternalText          = "\n\nError: RetriableError: [internal] fake internal server error"
	cursorDeadlineText          = "\n\nError: RetriableError: [deadline_exceeded] fake deadline exceeded"
	cursorResourceExhaustedText = "\n\nError: RetriableError: [resource_exhausted] fake resource exhausted"
	cursorConversationText      = "\n\nError: NonRetriableError: Conversation too long Start a new chat."
	cursorPaymentText           = "\n\nAdd a payment method to continue"
	cursorUpgradeText           = "\n\nUpgrade your plan to continue"
)

// Constructed from the research: Cursor wraps a plain Error (such as "Failed
// to compute request context") in a RetriableError with no connect code, and
// its connection watchdog raises "Connection stalled repeatedly" and
// "Connection failed repeatedly" (pretty/main.js). No probe run captured
// these three.
const (
	cursorUnbracketedText       = "\n\nError: RetriableError: Failed to compute request context"
	cursorConnectionStalledText = "\n\nError: RetriableError: Connection stalled repeatedly"
	cursorConnectionFailedText  = "\n\nError: RetriableError: Connection failed repeatedly"
)

const cursorRetrySession = "31f5950b-e04f-4368-ac69-eed4938e7162"

func TestCursorTurnFailureClassifiesEveryCapture(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		text string
		want providerkit.TurnFailure
	}{
		"an unavailable upstream":     {cursorUnavailableText, providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}},
		"a refused connection":        {cursorRefusedText, providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}},
		"a provider error":            {cursorProviderErrorText, providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}},
		"an internal error":           {cursorInternalText, providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}},
		"an exceeded deadline":        {cursorDeadlineText, providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}},
		"a stalled connection":        {cursorConnectionStalledText, providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}},
		"a failed connection":         {cursorConnectionFailedText, providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient}},
		"an exhausted resource":       {cursorResourceExhaustedText, providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited}},
		"a payment block":             {cursorPaymentText, providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked}},
		"a plan limit":                {cursorUpgradeText, providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowUnstated}},
		"a conversation that is long": {cursorConversationText, providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}},
		"a RetriableError with no connect code": {
			cursorUnbracketedText, providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable},
		},
		"an ordinary answer":   {"Hello from Cursor.", providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}},
		"no chunk at all":      {"", providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}},
		"a quoted error code":  {"The log says Error: RetriableError: [unavailable] once.", providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}},
		"a quoted plan notice": {"Cursor answers \"Upgrade your plan to continue\" when the plan runs out.", providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := cursorTurnFailure(tc.text)
			assert.Equal(t, tc.want.Kind, got.Kind)
			assert.Equal(t, tc.want.Window, got.Window)
		})
	}
}

// newCursorRetryAgent builds a Cursor agent with the hooks that Start
// installs, in a running prompt.
func newCursorRetryAgent(t *testing.T, sink *agenttest.Sink) *Agent {
	t.Helper()
	a := &Agent{}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "cursor-1", ProviderName: "cursor"})
	applyCursorStartHooks(t, a, sink)
	a.SetPromptActiveForTest(true)
	return a
}

// newCursorPromptRetryAgent builds a Cursor agent with the hooks that Start
// installs, over a peer that answers every session/prompt with end_turn.
func newCursorPromptRetryAgent(t *testing.T, sink *agenttest.Sink) *Agent {
	t.Helper()
	a, _ := acptest.NewAgentForRPCWithResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		func(method string) agenttest.RPCReply {
			if method == acp.MethodSessionPrompt {
				return agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		})
	applyCursorStartHooks(t, a, sink)
	return a
}

// applyCursorStartHooks installs the hooks of configure as the start applies
// them, over sink, in the session of the retry fixtures.
func applyCursorStartHooks(t *testing.T, a *Agent, sink *agenttest.Sink) {
	t.Helper()
	hooks := a.configure(context.Background(), agent.Options{HomeDir: t.TempDir(), WorkingDir: t.TempDir()}, agent.NewProviderServices(sink))
	// The start keeps the sink of the hooks as the agent's own.
	a.SetSinkForTest(hooks.Sink)
	hooks.Sink = nil
	*a.HooksForTest() = hooks
	a.SetSessionIDForTest(cursorRetrySession)
}

// cursorUpdateLine builds one session/update line.
func cursorUpdateLine(t *testing.T, sessionID string, update map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "session/update",
		"params": map[string]any{"sessionId": sessionID, "update": update},
	})
	require.NoError(t, err)
	return raw
}

func cursorChunk(kind, text string) map[string]any {
	return map[string]any{"sessionUpdate": kind, "content": map[string]any{"type": "text", "text": text}}
}

// cursorRetryTimeline splits the timeline at its last row. It returns that
// row's operation and every retry operation after it, and fails when a retry
// operation precedes the row.
func cursorRetryTimeline(t *testing.T, timeline []agenttest.TimelineEntry) (lastRow string, retries []string) {
	t.Helper()
	for _, entry := range timeline {
		switch entry.Op {
		case "message", "turn_end", "notification", "leapmux_notification":
			require.Empty(t, retries, "a retry operation precedes the row %q", entry.Op)
			lastRow = entry.Op
		default:
			retries = append(retries, entry.Op)
		}
	}
	return lastRow, retries
}

func TestCursorPromptEndAppliesOneVerdictAfterTheTurnEndRow(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		text    string
		retries []string
	}{
		"an unavailable upstream schedules a server-error retry": {cursorUnavailableText, []string{"schedule_server_error"}},
		"an exhausted resource schedules a server-error retry":   {cursorResourceExhaustedText, []string{"schedule_server_error"}},
		"a payment block cancels both retries":                   {cursorPaymentText, []string{"cancel_usage_limit", "cancel_server_error"}},
		"a plan limit cancels the server-error retry":            {cursorUpgradeText, []string{"cancel_server_error"}},
		"a long conversation cancels the server-error retry":     {cursorConversationText, []string{"cancel_server_error"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCursorRetryAgent(t, sink)
			chunk := cursorUpdateLine(t, cursorRetrySession, cursorChunk("agent_message_chunk", tc.text))
			a.HandleOutput(chunk)
			a.FinishPromptRequestForTest(cursorRetrySession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)

			lastRow, retries := cursorRetryTimeline(t, sink.Timeline())
			assert.Equal(t, "turn_end", lastRow)
			require.Equal(t, tc.retries, retries)
			if tc.retries[0] == "schedule_server_error" {
				var frame struct {
					Params struct {
						Update json.RawMessage `json:"update"`
					} `json:"params"`
				}
				require.NoError(t, json.Unmarshal(chunk, &frame))
				assert.JSONEq(t, string(frame.Params.Update), string(sink.LastAutoSchedule().SourcePayload), "the payload is the raw chunk update")
			}
		})
	}
}

// Only the LAST root update decides. A thought, a tool call, a tool update,
// or a later answer chunk after the error chunk each clears it, because the
// turn went on after the error.
func TestCursorLaterRootUpdateClearsTheErrorChunk(t *testing.T) {
	t.Parallel()
	for name, later := range map[string]map[string]any{
		"a later answer chunk": cursorChunk("agent_message_chunk", "Recovered."),
		"a thought":            cursorChunk("agent_thought_chunk", "Thinking."),
		"a tool call":          {"sessionUpdate": "tool_call", "toolCallId": "tool-1", "title": "Read file", "kind": "read", "status": "pending"},
		"a tool update":        {"sessionUpdate": "tool_call_update", "toolCallId": "tool-1", "status": "completed"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCursorRetryAgent(t, sink)
			a.HandleOutput(cursorUpdateLine(t, cursorRetrySession, cursorChunk("agent_message_chunk", cursorUnavailableText)))
			a.HandleOutput(cursorUpdateLine(t, cursorRetrySession, later))
			a.FinishPromptRequestForTest(cursorRetrySession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)

			_, retries := cursorRetryTimeline(t, sink.Timeline())
			assert.Equal(t, []string{"cancel_server_error"}, retries)
		})
	}
}

// A subagent's chunk in its own session never becomes the root record, and it
// does not clear the root's error chunk either.
func TestCursorSubagentChunkDoesNotTouchTheRootRecord(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCursorRetryAgent(t, sink)
	require.True(t, a.AttachChildSession(acp.ChildSessionTarget{RootSessionID: cursorRetrySession, SessionID: "child-session", RowKey: "child-row"}))
	a.HandleOutput(cursorUpdateLine(t, cursorRetrySession, cursorChunk("agent_message_chunk", cursorUnavailableText)))
	a.HandleOutput(cursorUpdateLine(t, "child-session", cursorChunk("agent_message_chunk", "child answer")))
	a.FinishPromptRequestForTest(cursorRetrySession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	_, retries := cursorRetryTimeline(t, sink.Timeline())
	assert.Equal(t, []string{"schedule_server_error"}, retries)

	// A child's error chunk alone decides nothing for the root turn.
	child := &agenttest.Sink{}
	b := newCursorRetryAgent(t, child)
	require.True(t, b.AttachChildSession(acp.ChildSessionTarget{RootSessionID: cursorRetrySession, SessionID: "child-session", RowKey: "child-row"}))
	b.HandleOutput(cursorUpdateLine(t, "child-session", cursorChunk("agent_message_chunk", cursorUnavailableText)))
	b.FinishPromptRequestForTest(cursorRetrySession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	_, retries = cursorRetryTimeline(t, child.Timeline())
	assert.Equal(t, []string{"cancel_server_error"}, retries)
}

// The recording hook keeps the return value of the lifecycle handler: it
// claims a native subagent lifecycle update and no other update.
func TestCursorSessionUpdateHandlerKeepsItsReturnValue(t *testing.T) {
	t.Parallel()
	a := newCursorRetryAgent(t, &agenttest.Sink{})
	assert.False(t, a.handleCursorSessionUpdate(cursorRetrySession, a.Sink(), json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}`)))
	assert.False(t, a.handleCursorSessionUpdate(cursorRetrySession, a.Sink(), json.RawMessage(`not json`)))
	assert.True(t, a.handleCursorSessionUpdate(cursorRetrySession, a.Sink(), json.RawMessage(`{"sessionUpdate":"subagent_state_update","state":"running"}`)))
}

func TestCursorStopAndFailedPromptGiveNotRetryable(t *testing.T) {
	t.Parallel()
	for name, end := range map[string]func(*Agent){
		"a stop": func(a *Agent) {
			a.NoteInterruptRequestedForTest()
			a.FinishPromptRequestForTest(cursorRetrySession, json.RawMessage(`{"stopReason":"cancelled"}`), nil)
		},
		"a failed prompt request": func(a *Agent) {
			a.FinishPromptRequestForTest(cursorRetrySession, nil, errors.New("write failed"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCursorRetryAgent(t, sink)
			a.HandleOutput(cursorUpdateLine(t, cursorRetrySession, cursorChunk("agent_message_chunk", cursorUnavailableText)))
			end(a)
			_, retries := cursorRetryTimeline(t, sink.Timeline())
			assert.Equal(t, []string{"cancel_server_error"}, retries)
		})
	}
}

// An error chunk that arrived before the prompt started, from an earlier
// turn, does not decide the prompt's end: PromptPrepared clears it.
func TestCursorErrorChunkBeforeThePromptDecidesNothing(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCursorPromptRetryAgent(t, sink)
	a.HandleOutput(cursorUpdateLine(t, cursorRetrySession, cursorChunk("agent_message_chunk", cursorUnavailableText)))

	require.NoError(t, a.SendInput("Say hello.", nil))
	testutil.RequireEventually(t, func() bool { return !a.PromptActive() })

	lastRow, retries := cursorRetryTimeline(t, sink.Timeline())
	assert.Equal(t, "turn_end", lastRow)
	assert.Equal(t, []string{"cancel_server_error"}, retries)
	assert.Zero(t, sink.AutoScheduleCount(), "the earlier chunk must not schedule")
}

// A context clear drops the record of the session that it replaced.
func TestCursorContextClearDropsTheRecord(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCursorRetryAgent(t, sink)
	a.HandleOutput(cursorUpdateLine(t, cursorRetrySession, cursorChunk("agent_message_chunk", cursorUnavailableText)))
	a.HooksForTest().ClearProviderState()
	a.FinishPromptRequestForTest(cursorRetrySession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	_, retries := cursorRetryTimeline(t, sink.Timeline())
	assert.Equal(t, []string{"cancel_server_error"}, retries)
}
