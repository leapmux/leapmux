package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexCompactContextUsesNativeCompaction(t *testing.T) {
	t.Parallel()

	agent, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	agent.threadID = "thread-1"
	agent.mainTurn.id = "turn-1"

	require.NoError(t, agent.CompactContext())
	require.Len(t, requests(), 1)
	assert.Equal(t, "thread/compact/start", requests()[0].Method)
	assert.Equal(t, "thread-1", requests()[0].Params["threadId"])
}

// SendInput must not read a command out of the message text. The queue
// classifies "/compact" and "/summarize" before dispatch and calls
// CompactContext instead, so CompactContext is the single entry point to a
// native compaction. The second check that lived here disagreed with that one:
// the queue classifies only a plain user message, so a control response whose
// text is "/compact" reached SendInput and started a compaction in place of the
// answer that the agent waited for.
func TestCodexSendInputDoesNotInterceptCompactCommand(t *testing.T) {
	t.Parallel()

	agent, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	agent.threadID = "thread-1"

	result := make(chan error, 1)
	go func() { result <- agent.SendInput(" /compact ", nil) }()
	require.Eventually(t, func() bool { return len(requests()) == 1 }, time.Second, time.Millisecond)

	assert.Equal(t, "turn/start", requests()[0].Method,
		"the text must start a turn, not a native compaction")
	inputJSON, err := json.Marshal(requests()[0].Params["input"])
	require.NoError(t, err)
	assert.Contains(t, string(inputJSON), "/compact",
		"the composer text must reach the agent unchanged")

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"thread-1","turn":{"id":"turn-1"}}}`)))
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("turn/started did not confirm delivery")
	}
}

func TestCodexTurnStartProcessExitIsDeliveryUncertain(t *testing.T) {
	t.Parallel()

	a, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	a.threadID = "thread-1"
	a.SimulateExitForTest()

	assert.ErrorIs(t, a.SendInput("hello", nil), agent.ErrDeliveryUncertain)
}

func TestCodexTurnStartExplicitRejectionIsKnownFailure(t *testing.T) {
	t.Parallel()

	a, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32600,"message":"thread is active"}`)}
	})
	a.threadID = "thread-1"

	err := a.SendInput("hello", nil)
	require.Error(t, err)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.Contains(t, err.Error(), "thread is active")
}

func TestCodexCompactionResponseTimeoutIsDeliveryUncertain(t *testing.T) {
	t.Parallel()

	err := classifyCodexCompactionRequestError(errors.New("timeout waiting for thread/compact/start response"))

	assert.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	known := classifyCodexCompactionRequestError(&providerkit.JSONRPCResponseError{Code: -32600, Message: "thread is active"})
	assert.NotErrorIs(t, known, agent.ErrDeliveryUncertain)
}

func TestCodexCompactionStartNotificationConfirmsDelivery(t *testing.T) {
	t.Parallel()

	releaseResponse := make(chan struct{})
	defer close(releaseResponse)
	agent, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		<-releaseResponse
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	agent.threadID = "thread-1"
	result := make(chan error, 1)
	go func() {
		result <- agent.CompactContext()
	}()
	require.Eventually(t, func() bool { return len(requests()) == 1 }, time.Second, time.Millisecond)

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"thread-1","turnId":"turn-1","item":{"type":"contextCompaction","id":"compact-1"}}}`)))

	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("contextCompaction start did not confirm delivery")
	}
}

func TestHandleCodexOutput_ContextCompactionCompletionPersistsBoundary(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	ag := newCodexAgentWithSink(agent.NewProviderServices(sink))

	completion := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"contextCompaction","id":"compact-1"}}}`
	handleCodexOutput(ag, providerkit.ParseLine([]byte(completion)))
	assert.Contains(t, sink.SessionInfos(), map[string]interface{}{"compaction": nil})

	// The native completion stays a notification boundary. The live start has no stored row.
	assert.Equal(t, 0, sink.MessageCount(),
		"the compaction boundary must not reach the transcript as a chat row")
	require.Equal(t, 1, sink.NotificationCount())
	last := sink.LastNotification()
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source)
	assert.JSONEq(t, completion, string(last.Content),
		"the raw JSON-RPC envelope must reach the frontend unchanged")
	assert.Equal(t, agent.CompactionBoundaryNotification(), codexProvider{}.Classify(last.Content),
		"the consolidator drops the compacting status only for a compaction boundary")

	assert.Empty(t, sink.TurnActives(), "the item boundary must not end its enclosing turn")

	handleCodexOutput(ag, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed"}}}`)))
	assert.Equal(t, []bool{false}, sink.TurnActives(),
		"the turn's own completion releases the Worker's input queue")
}

type codexCompactionEndSink struct {
	agenttest.Sink
	endedBeforeTurn bool
}

func (s *codexCompactionEndSink) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	s.endedBeforeTurn = len(s.LeapMuxNotifications()) == 1
	return s.Sink.PersistTurnEnd(content, span)
}

func TestHandleCodexOutput_AFailedCompactionPersistsAFailedEnd(t *testing.T) {
	t.Parallel()
	sink := &codexCompactionEndSink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	// Codex run1-frames.log records retry errors followed by a failed turn with no completed compaction item.
	feedCodexLines(a, `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"contextCompaction","id":"compact-1"}}}`)
	for range 3 {
		feedCodexLines(a, `{"method":"error","params":{"threadId":"main-thread","willRetry":true,"error":{"message":"retry"}}}`)
	}
	feedCodexLines(a,
		`{"method":"error","params":{"threadId":"main-thread","willRetry":false,"error":{"message":"failed"}}}`,
		`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"failed","error":{"message":"failed"}}}}`,
	)
	assert.Contains(t, sink.SessionInfos(), map[string]interface{}{"compaction": map[string]interface{}{}})
	assert.Contains(t, sink.SessionInfos(), map[string]interface{}{"compaction": nil})
	assert.Equal(t, []map[string]interface{}{{"type": "compaction_ended", "outcome": "failed"}}, sink.LeapMuxNotifications())
	assert.True(t, sink.endedBeforeTurn)
}

func TestHandleCodexOutput_AnInterruptedCompactionPersistsAnAbortedEnd(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a,
		`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"contextCompaction","id":"compact-1"}}}`,
		`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"interrupted"}}}`,
	)
	assert.Equal(t, []map[string]interface{}{{"type": "compaction_ended", "outcome": "aborted"}}, sink.LeapMuxNotifications())
	assert.Contains(t, sink.SessionInfos(), map[string]interface{}{"compaction": nil})
}

func TestHandleCodexOutput_AChildCompactionPublishesAndEndsOnTheChildSink(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a,
		`{"method":"item/started","params":{"threadId":"child-1","turnId":"turn-1","item":{"type":"contextCompaction","id":"compact-1"}}}`,
		`{"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"turn-1","status":"failed"}}}`,
	)
	assert.Empty(t, sink.SessionInfos())
	feedCodexLines(a, `{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-1","agentPath":"/root/child"}}}`)
	child := sink.Child("child-of-spawn-1")
	require.NotNil(t, child)
	assert.Contains(t, child.SessionInfos(), map[string]interface{}{"compaction": map[string]interface{}{}})
	assert.Contains(t, child.SessionInfos(), map[string]interface{}{"compaction": nil})
	assert.Equal(t, []map[string]interface{}{{"type": "compaction_ended", "outcome": "failed"}}, child.LeapMuxNotifications())
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestHandleCodexOutput_ASuccessfulCompactionPersistsNoExtraEnd(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a,
		`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"contextCompaction","id":"compact-1"}}}`,
		`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"contextCompaction","id":"compact-1"}}}`,
		`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed"}}}`,
	)
	assert.Empty(t, sink.LeapMuxNotifications())
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.Contains(t, string(sink.PersistedNotifications()[0].Content), `"method":"item/completed"`)
	assert.Contains(t, sink.SessionInfos(), map[string]interface{}{"compaction": nil})
}

func TestCodexClearContext_EndsTheLiveCompactionOfTheRetiredThread(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a, `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"contextCompaction","id":"compact-1"}}}`)
	_, err := a.replaceMainThread(codexThreadResult{ID: "fresh-thread"})
	require.NoError(t, err)
	assert.Contains(t, sink.LeapMuxNotifications(), map[string]interface{}{"type": "compaction_ended", "outcome": "aborted"})
	assert.Contains(t, sink.SessionInfos(), map[string]interface{}{"compaction": nil})
}

func TestCodexNewTurnEndsAnUnfinishedCompaction(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a,
		`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-1"}}}`,
		`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"contextCompaction","id":"compact-1"}}}`,
		`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-2"}}}`,
	)
	assert.Equal(t, []map[string]interface{}{{"type": "compaction_ended", "outcome": "aborted"}}, sink.LeapMuxNotifications())
}

func TestCodexAnEarlierCompactionEndKeepsTheCurrentCompactionLive(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a,
		`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"contextCompaction","id":"first"}}}`,
		`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"contextCompaction","id":"second"}}}`,
		`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"contextCompaction","id":"first"}}}`,
	)
	assert.NotContains(t, sink.SessionInfos(), map[string]interface{}{"compaction": nil})
	feedCodexLines(a, `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"contextCompaction","id":"second"}}}`)
	assert.Contains(t, sink.SessionInfos(), map[string]interface{}{"compaction": nil})
}

func TestCodexClearContextEndsTheChildCompactionBeforeItRetiresTheRoute(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a,
		`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-1","agentPath":"/root/child"}}}`,
		`{"method":"item/started","params":{"threadId":"child-1","turnId":"child-turn","item":{"type":"contextCompaction","id":"compact-1"}}}`,
	)
	child := sink.Child("child-of-spawn-1")
	require.NotNil(t, child)
	_, err := a.replaceMainThread(codexThreadResult{ID: "fresh-thread"})
	require.NoError(t, err)
	assert.Equal(t, []map[string]interface{}{{"type": "compaction_ended", "outcome": "aborted"}}, child.LeapMuxNotifications())
	assert.Contains(t, child.SessionInfos(), map[string]interface{}{"compaction": nil})
}

// blockingTurnFlagSink blocks inside SetTurnState until the test releases it,
// which is what tells a synchronous publish apart from one a goroutine carries.
// Asserting the recorded order alone cannot: a spawned goroutine usually wins
// the race to the slice, so the same sequence appears either way.
type blockingTurnFlagSink struct {
	agenttest.Sink
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingTurnFlagSink) SetTurnState(state agent.TurnState, seq uint64) {
	if !state.Active {
		s.once.Do(func() {
			close(s.entered)
			<-s.release
		})
	}
	s.Sink.SetTurnState(state, seq)
}

func TestHandleCodexOutput_TurnCompletionPublishesTheClearBeforeItReturns(t *testing.T) {
	t.Parallel()

	// The Worker's input queue takes its turn state from this publish, and the
	// state carries no turn identity -- so ORDER is the only thing that keeps
	// the clear attached to the turn it belongs to. A clear that a goroutine
	// carries can land after the NEXT turn already opened, and the queue then
	// dispatches the next message into that running turn.
	//
	// This site once did carry it that way, to keep the reader free for the
	// response to a compaction the app-server submits before it answers. The
	// publish is safe inline instead, because Manager.drain never holds the
	// coordinator lock across dispatcher.Dispatch.
	sink := &blockingTurnFlagSink{entered: make(chan struct{}), release: make(chan struct{})}
	ag := newCodexAgentWithSink(agent.NewProviderServices(sink))
	// Bypass the thinking-token wrapper so this test isolates the output-reader
	// callback order. A separate assertion verifies wrapper forwarding.
	ag.sink = agent.NewProviderServices(sink)

	handleCodexOutput(ag, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-1"}}}`)))
	returned := make(chan struct{})
	go func() {
		handleCodexOutput(ag, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed"}}}`)))
		close(returned)
	}()

	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("the turn end published no clear")
	}
	select {
	case <-returned:
		t.Fatal("the handler returned before the clear reached the sink, so a new goroutine carried it")
	case <-time.After(50 * time.Millisecond):
	}
	close(sink.release)
	<-returned

	assert.Equal(t, []bool{true, false}, sink.TurnActives(),
		"the handler reports the end before it returns, so the reader's next line cannot overtake it")
}

// --- The automatic-retry verdict of a compaction turn ---

// codexCompactionEnded is the timeline entry of the verdict at the end of a
// compaction turn. It cancels the server-error retry and schedules nothing.
var codexCompactionEnded = agenttest.TimelineEntry{
	Op: "cancel_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
	Reason: providerkit.TurnEndedOnCompaction,
}

// The frames of one compaction turn of the main thread.
const (
	codexCompactionTurnStarted = `{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"compact-turn"}}}`
	codexCompactionItemStarted = `{"method":"item/started","params":{"threadId":"main-thread","turnId":"compact-turn","item":{"type":"contextCompaction","id":"compact-1"}}}`
)

// codexFailedTurnCompleted returns the turn/completed of a main-thread turn
// that failed with the codexErrorInfo info and the message message.
func codexFailedTurnCompleted(turnID, info, message string) string {
	return fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":%q,"status":"failed","items":[],"error":{"message":%q,"codexErrorInfo":%s,"additionalDetails":null}}}}`, turnID, message, info)
}

// newCodexCompactionAgent builds an agent on the main thread whose Codex
// answers every request at once with an empty result.
func newCodexCompactionAgent(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	a, sink, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.threadID = "main-thread"
	return a, sink
}

// A compaction that fails on an error that the classifier reads as retryable
// schedules no retry. The clock stands at the capture of the usage-limit
// message, so its reset lies ahead.
func TestCodexCompactContext_AFailedCompactionSchedulesNoRetry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		info    string
		message string
	}{
		{"an internal server error", `"internalServerError"`, "Internal server error"},
		{"an overload", `"serverOverloaded"`, "Selected model is at capacity. Please try a different model."},
		{"a stream that disconnected with a 502", `{"responseStreamDisconnected":{"httpStatusCode":502}}`, "stream disconnected before completion"},
		{"a rate limit", `"rateLimitExceeded"`, "exceeded retry limit, last status: 429 Too Many Requests"},
		{"a usage limit that states its reset", `"usageLimitExceeded"`, "You've hit your usage limit. Try again at Nov 2nd, 2026 3:00 PM."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			clock.Set(codexRealUsageLimitAt).MustWait(testutil.DeadlineContext(t))
			a, sink, _ := newCodexAgentForRPCWithClock(t, clock, func(string) agenttest.RPCReply {
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			})
			a.threadID = "main-thread"
			require.NoError(t, a.CompactContext())

			feedCodexLines(a,
				codexCompactionTurnStarted,
				codexCompactionItemStarted,
				codexFailedTurnCompleted("compact-turn", tc.info, tc.message),
			)

			assert.Equal(t, []agenttest.TimelineEntry{{Op: "leapmux_notification"}, codexTurnEndRow, codexCompactionEnded}, sink.Timeline())
		})
	}
}

// Codex can start the compaction's turn before it answers the request. The
// mark stands before the request goes out, so the turn that starts first still
// carries it.
func TestCodexCompactContext_ACompactionTurnThatStartsBeforeTheReplySchedulesNoRetry(t *testing.T) {
	t.Parallel()
	releaseResponse := make(chan struct{})
	a, sink, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		<-releaseResponse
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.threadID = "main-thread"
	result := make(chan error, 1)
	go func() { result <- a.CompactContext() }()
	require.Eventually(t, func() bool { return len(requests()) == 1 }, 30*time.Second, time.Millisecond)

	feedCodexLines(a, codexCompactionTurnStarted, codexCompactionItemStarted)
	require.NoError(t, <-result, "the contextCompaction start confirms delivery")
	feedCodexLines(a, codexFailedTurnCompleted("compact-turn", `"internalServerError"`, "Internal server error"))
	close(releaseResponse)

	assert.Equal(t, []agenttest.TimelineEntry{{Op: "leapmux_notification"}, codexTurnEndRow, codexCompactionEnded}, sink.Timeline())
}

// A successful compaction ends its turn on the compaction verdict too.
func TestCodexCompactContext_ASuccessfulCompactionAppliesTheCompactionVerdict(t *testing.T) {
	t.Parallel()
	a, sink := newCodexCompactionAgent(t)
	require.NoError(t, a.CompactContext())

	feedCodexLines(a,
		codexCompactionTurnStarted,
		codexCompactionItemStarted,
		`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"compact-turn","item":{"type":"contextCompaction","id":"compact-1"}}}`,
		`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"compact-turn","status":"completed"}}}`,
	)

	assert.Equal(t, []agenttest.TimelineEntry{{Op: "notification"}, codexTurnEndRow, codexCompactionEnded}, sink.Timeline())
}

// An interrupted compaction ends its turn on the compaction verdict, not on
// the verdict of an interrupted conversation turn.
func TestCodexCompactContext_AnInterruptedCompactionAppliesTheCompactionVerdict(t *testing.T) {
	t.Parallel()
	a, sink := newCodexCompactionAgent(t)
	require.NoError(t, a.CompactContext())

	feedCodexLines(a,
		codexCompactionTurnStarted,
		codexCompactionItemStarted,
		`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"compact-turn","status":"interrupted"}}}`,
	)

	assert.Equal(t, []agenttest.TimelineEntry{{Op: "leapmux_notification"}, codexTurnEndRow, codexCompactionEnded}, sink.Timeline())
}

// The compaction's turn end spends the mark, so the next conversation turn
// applies its own verdict.
func TestCodexCompactContext_TheTurnAfterACompactionAppliesItsOwnVerdict(t *testing.T) {
	t.Parallel()
	a, sink := newCodexCompactionAgent(t)
	require.NoError(t, a.CompactContext())
	feedCodexLines(a,
		codexCompactionTurnStarted,
		`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"compact-turn","status":"completed"}}}`,
	)

	feedCodexLines(a,
		`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-2"}}}`,
		codexFailedTurnCompleted("turn-2", `"internalServerError"`, "Internal server error"),
	)

	assert.Equal(t, []agenttest.TimelineEntry{
		codexTurnEndRow, codexCompactionEnded,
		codexTurnEndRow, codexServerErrorScheduled,
	}, sink.Timeline())
}

// An automatic compaction inside a conversation turn ends no turn. A failed
// conversation turn keeps its own verdict, also when the compaction failed it.
func TestCodexAnAutomaticCompactionLeavesTheConversationTurnItsOwnVerdict(t *testing.T) {
	t.Parallel()
	a, sink := newCodexCompactionAgent(t)

	feedCodexLines(a,
		`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-1"}}}`,
		`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"contextCompaction","id":"auto-1"}}}`,
		codexFailedTurnCompleted("turn-1", `"internalServerError"`, "Internal server error"),
	)

	assert.Equal(t, []agenttest.TimelineEntry{{Op: "leapmux_notification"}, codexTurnEndRow, codexServerErrorScheduled}, sink.Timeline())
}

// A compaction request that Codex refuses starts no compaction, so the next
// turn keeps its own verdict.
func TestCodexCompactContext_ARefusedRequestLeavesTheNextTurnItsOwnVerdict(t *testing.T) {
	t.Parallel()
	a, sink, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32600,"message":"thread is active"}`)}
	})
	a.threadID = "main-thread"
	require.Error(t, a.CompactContext())

	feedCodexLines(a,
		`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-1"}}}`,
		codexFailedTurnCompleted("turn-1", `"internalServerError"`, "Internal server error"),
	)

	assert.Equal(t, []agenttest.TimelineEntry{codexTurnEndRow, codexServerErrorScheduled}, sink.Timeline())
}

// A compaction requested for a thread that a context replacement retired never
// marks a turn of the new thread.
func TestCodexCompactContext_AReplacedThreadDropsThePendingCompaction(t *testing.T) {
	t.Parallel()
	a, sink := newCodexCompactionAgent(t)
	require.NoError(t, a.CompactContext())
	_, err := a.replaceMainThread(codexThreadResult{ID: "fresh-thread"})
	require.NoError(t, err)

	feedCodexLines(a,
		`{"method":"turn/started","params":{"threadId":"fresh-thread","turn":{"id":"turn-1"}}}`,
		`{"method":"turn/completed","params":{"threadId":"fresh-thread","turn":{"id":"turn-1","status":"failed","items":[],"error":{"message":"Internal server error","codexErrorInfo":"internalServerError","additionalDetails":null}}}}`,
	)

	assert.Equal(t, []agenttest.TimelineEntry{codexTurnEndRow, codexServerErrorScheduled}, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// A steer that waits for its reply holds the compaction's turn end. The held
// end replays once, after the reply, and it applies the compaction verdict
// once.
func TestCodexCompactContext_AHeldCompactionTurnEndAppliesTheCompactionVerdictOnce(t *testing.T) {
	t.Parallel()
	steerReplies := make(chan func(agenttest.RPCReply), 1)
	a, sink, _ := newCodexAgentForAsyncRPCRequests(t, func(request codexRecordedRequest, respond func(agenttest.RPCReply)) {
		if request.Method == "turn/steer" {
			steerReplies <- respond
			return
		}
		respond(agenttest.RPCReply{Result: json.RawMessage(`{}`)})
	})
	a.threadID = "main-thread"
	require.NoError(t, a.CompactContext())
	feedCodexLines(a, codexCompactionTurnStarted, codexCompactionItemStarted)

	steered := make(chan error, 1)
	go func() { steered <- a.SteerInput("guide", nil) }()
	respond := <-steerReplies
	feedCodexLines(a, codexFailedTurnCompleted("compact-turn", `"internalServerError"`, "Internal server error"))
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()), "the pending steer holds the turn end")

	respond(agenttest.RPCReply{Error: json.RawMessage(`{"code":-32600,"message":"cannot steer a compact turn","data":{"codexErrorInfo":{"activeTurnNotSteerable":{"turnKind":"compact"}}}}`)})
	require.Error(t, <-steered)

	assert.Equal(t, []agenttest.TimelineEntry{codexTurnEndRow, codexCompactionEnded}, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// Codex replaces a running turn with the compaction's own turn. The replaced
// turn keeps its own verdict, and the mark reaches only the compaction's turn.
func TestCodexCompactContext_TheTurnThatACompactionReplacesKeepsItsOwnVerdict(t *testing.T) {
	t.Parallel()
	a, sink := newCodexCompactionAgent(t)
	feedCodexLines(a, `{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-1"}}}`)
	require.NoError(t, a.CompactContext())

	feedCodexLines(a,
		`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"interrupted"}}}`,
		codexCompactionTurnStarted,
		codexFailedTurnCompleted("compact-turn", `"internalServerError"`, "Internal server error"),
	)

	assert.Equal(t, []agenttest.TimelineEntry{
		codexTurnEndRow, codexServerErrorCancelled,
		codexTurnEndRow, codexCompactionEnded,
	}, sink.Timeline())
}

// A caller that stops waiting leaves the mark, because Codex can still start
// the compaction. The late compaction turn then schedules no retry.
func TestCodexCompactContext_ACompactionTurnAfterTheAckTimeoutSchedulesNoRetry(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	ackTimer := clock.Trap().NewTimer(codexCompactionAckTimerTag)
	t.Cleanup(ackTimer.Close)
	// The deferred release runs before the fixture's cleanup, which waits for
	// the fake peer that this reply blocks.
	releaseResponse := make(chan struct{})
	defer close(releaseResponse)
	a, sink, _ := newCodexAgentForRPCWithClock(t, clock, func(string) agenttest.RPCReply {
		<-releaseResponse
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.threadID = "main-thread"
	result := make(chan error, 1)
	go func() { result <- a.CompactContext() }()
	assert.Equal(t, turnStartAckTimeout, testutil.WaitForTimer(t, ctx, ackTimer))
	clock.Advance(turnStartAckTimeout).MustWait(ctx)
	require.ErrorIs(t, <-result, agent.ErrDeliveryUncertain)

	feedCodexLines(a,
		codexCompactionTurnStarted,
		codexFailedTurnCompleted("compact-turn", `"internalServerError"`, "Internal server error"),
	)

	assert.Equal(t, []agenttest.TimelineEntry{codexTurnEndRow, codexCompactionEnded}, sink.Timeline())
}
