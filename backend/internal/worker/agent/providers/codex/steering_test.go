package codex

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexSteerCarriesAClientUserMessageID(t *testing.T) {
	t.Parallel()
	a, sink, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	a.threadID, a.mainTurn.id = "main-thread", "turn-1"
	require.NoError(t, a.SteerInput("guide", nil))
	policy, supports := interface{}(a).(agent.SteerEchoReporter)
	require.True(t, supports)
	assert.True(t, policy.SteerRowAtEcho())
	require.Len(t, requests(), 1)
	clientID, ok := requests()[0].Params["clientUserMessageId"].(string)
	require.True(t, ok)
	require.NotEmpty(t, clientID)
	assert.Empty(t, sink.SteerEchoReports())
	feedCodexLines(a, fmt.Sprintf(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"userMessage","id":"native-steer","clientId":%q,"content":[]}}}`, clientID))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}

func TestHandleCodexOutput_ASteerEchoReportsAfterTheAnswerItPrecedes(t *testing.T) {
	t.Parallel()
	a, sink, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	a.threadID, a.mainTurn.id = "main-thread", "turn-1"
	require.NoError(t, a.SteerInput("guide", nil))
	clientID, ok := requests()[0].Params["clientUserMessageId"].(string)
	require.True(t, ok)
	feedCodexLines(a, `{"method":"item/completed","params":{"threadId":"main-thread","item":{"type":"agentMessage","id":"answer","text":"first answer"}}}`)
	require.Len(t, sink.Messages(), 1)
	assert.Empty(t, sink.SteerEchoReports())
	feedCodexLines(a, fmt.Sprintf(`{"method":"item/started","params":{"threadId":"main-thread","item":{"type":"userMessage","id":"native-steer","clientId":%q,"content":[]}}}`, clientID))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}

func TestHandleCodexOutput_ATurnEndSettlesItsUnechoedSteerFirst(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"completed", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
			a.threadID, a.mainTurn.id = "main-thread", "turn-1"
			require.NoError(t, a.SteerInput("guide", nil))
			feedCodexLines(a, fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":%q}}}`, status))
			expected := agent.SteerOutcomeDropped
			if status == "completed" {
				expected = agent.SteerOutcomeEchoed
			}
			assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: expected, Count: 1}}, sink.SteerEchoReports())
			lifecycle := sink.TurnLifecycle()
			require.NotEmpty(t, lifecycle)
			if status == "completed" {
				assert.Equal(t, "steer-echoed:1", lifecycle[0])
			} else {
				assert.Equal(t, "steer-dropped:1", lifecycle[0])
			}
		})
	}
}

func TestHandleCodexOutput_ATurnStartInputIsNotAnEcho(t *testing.T) {
	t.Parallel()
	a, sink, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	a.threadID, a.mainTurn.id = "main-thread", "turn-1"
	require.NoError(t, a.SteerInput("guide", nil))
	feedCodexLines(a, `{"method":"item/started","params":{"threadId":"main-thread","item":{"type":"userMessage","id":"initial","clientId":null,"content":[]}}}`)
	assert.Empty(t, sink.SteerEchoReports())
}

func TestCodexSteerFailureForgetsItsTicket(t *testing.T) {
	t.Parallel()
	refuse := true
	a, sink, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		if refuse {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"refused"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.threadID, a.mainTurn.id = "main-thread", "turn-1"
	require.ErrorIs(t, a.SteerInput("refused", nil), agent.ErrNoActiveTurn)
	refuse = false
	require.NoError(t, a.SteerInput("accepted", nil))
	clientID, ok := requests()[1].Params["clientUserMessageId"].(string)
	require.True(t, ok)
	feedCodexLines(a, fmt.Sprintf(`{"method":"item/started","params":{"threadId":"main-thread","item":{"type":"userMessage","id":"native-steer","clientId":%q,"content":[]}}}`, clientID))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}

func TestCodexARejectedSteerReplyAfterTurnEndCannotReportAnEcho(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	a, sink, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		close(entered)
		<-release
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"turn ended"}`)}
	})
	a.threadID, a.mainTurn.id = "main-thread", "turn-1"
	finished := make(chan error, 1)
	go func() { finished <- a.SteerInput("guide", nil) }()
	<-entered
	feedCodexLines(a, `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed"}}}`)
	assert.Empty(t, sink.SteerEchoReports(), "a pending RPC does not prove native acceptance")
	assert.NotContains(t, sink.TurnLifecycle(), "turn_end", "the final divider must wait for the reply")
	assert.NotContains(t, sink.TurnActives(), false, "the queue release must wait for the reply")
	assert.True(t, codexTurnIsActiveForTest(a))
	close(release)
	require.ErrorIs(t, <-finished, agent.ErrNoActiveTurn)
	assert.Empty(t, sink.SteerEchoReports())
}

func TestCodexPendingSteerDefersTheTurnEndUntilItsReply(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"completed", "interrupted"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			entered, release := make(chan struct{}), make(chan struct{})
			a, sink, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
				close(entered)
				<-release
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			})
			a.threadID, a.mainTurn.id = "main-thread", "turn-1"
			finished := make(chan error, 1)
			go func() { finished <- a.SteerInput("guide", nil) }()
			<-entered
			feedCodexLines(a, fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":%q}}}`, outcome))
			assert.NotContains(t, sink.TurnLifecycle(), "turn_end")
			assert.NotContains(t, sink.TurnActives(), false)
			assert.True(t, codexTurnIsActiveForTest(a))
			close(release)
			require.NoError(t, <-finished)
			assert.False(t, codexTurnIsActiveForTest(a))
			expected := agent.SteerOutcomeDropped
			if outcome == "completed" {
				expected = agent.SteerOutcomeEchoed
			}
			assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: expected, Count: 1}}, sink.SteerEchoReports())
			lifecycle := sink.TurnLifecycle()
			require.Len(t, lifecycle, 5)
			assert.Equal(t, "turn_active:true", lifecycle[0])
			lifecycle = lifecycle[1:]
			assert.True(t, strings.HasPrefix(lifecycle[0], "steer-"))
			assert.Equal(t, []string{"turn_end", "reset_spans", "turn_active:false"}, lifecycle[1:])
		})
	}
}

func TestCodexPendingSteerKeepsLaterTurnOutputInOrder(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	a, sink, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		close(entered)
		<-release
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.threadID, a.mainTurn.id = "main-thread", "turn-1"
	finished := make(chan error, 1)
	go func() { finished <- a.SteerInput("guide", nil) }()
	<-entered
	feedCodexLines(a,
		`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed"}}}`,
		`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-2"}}}`,
		`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-2","item":{"type":"agentMessage","id":"next-answer","text":"next answer"}}}`,
	)
	assert.Empty(t, sink.Messages())
	close(release)
	require.NoError(t, <-finished)
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.Contains(t, string(rows[0].Content), `"status":"completed"`)
	assert.Contains(t, string(rows[1].Content), "next answer")
	assert.True(t, codexTurnIsActiveForTest(a))
	assert.NotContains(t, sink.TurnActives(), false, "a queued native turn must keep the input queue active")
}

func TestCodexNativeSteerEchoBeforeTheReplyReportsOnce(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	a, sink, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		close(entered)
		<-release
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.threadID, a.mainTurn.id = "main-thread", "turn-1"
	finished := make(chan error, 1)
	go func() { finished <- a.SteerInput("guide", nil) }()
	<-entered
	clientID, _ := requests()[0].Params["clientUserMessageId"].(string)
	feedCodexLines(a,
		`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed"}}}`,
		fmt.Sprintf(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"userMessage","id":"echo","clientId":%q,"content":[]}}}`, clientID),
	)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
	assert.False(t, slices.Contains(sink.TurnLifecycle(), "turn_end"))
	close(release)
	require.NoError(t, <-finished)
	assert.Len(t, sink.SteerEchoReports(), 1)
	assert.Equal(t, []string{"turn_active:true", "steer-echoed:1", "turn_end", "reset_spans", "turn_active:false"}, sink.TurnLifecycle())
}

func TestCodexSteerUsesExpectedActiveTurn(t *testing.T) {
	t.Parallel()

	agent, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	agent.threadID = "thread-1"
	agent.mainTurn.id = "turn-1"
	require.NoError(t, agent.SteerInput("guide", nil))
	require.Len(t, requests(), 1)
	assert.Equal(t, "turn/steer", requests()[0].Method)
	assert.Equal(t, "turn-1", requests()[0].Params["expectedTurnId"])
}

func TestCodexSteerMapsEndedTurnResponse(t *testing.T) {
	t.Parallel()

	a, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"turn is no longer active"}`)}
	})
	a.threadID = "thread-1"
	a.mainTurn.id = "turn-1"
	assert.ErrorIs(t, a.SteerInput("guide", nil), agent.ErrNoActiveTurn)
}

func TestCodexSteerProcessExitIsDeliveryUncertain(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	defer close(release)
	a, _, _ := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		<-release
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.threadID = "thread-1"
	a.mainTurn.id = "turn-1"
	a.SimulateExitForTest()

	assert.ErrorIs(t, a.SteerInput("guide", nil), agent.ErrDeliveryUncertain)
}

func TestCodexSendInputDuringActiveTurnReportsAgentBusy(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	ag, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	ag.sink = agent.NewProviderServices(sink)
	ag.threadID = "thread-1"
	ag.mainTurn.id = "turn-1"
	err := ag.SendInput("later turn", nil)
	assert.Empty(t, requests(), "a refused send must reach no RPC")
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, ag, err)
}

// feedCodexLines handles each line as one frame from the Codex process.
func feedCodexLines(a *Agent, lines ...string) {
	for _, line := range lines {
		handleCodexOutput(a, providerkit.ParseLine([]byte(line)))
	}
}

// messageIndexContaining returns the index of the first message whose content
// contains text, or -1.
func messageIndexContaining(messages []agenttest.Message, text string) int {
	for index, message := range messages {
		if strings.Contains(string(message.Content), text) {
			return index
		}
	}
	return -1
}

// countMessagesContaining counts the messages whose content contains text.
func countMessagesContaining(messages []agenttest.Message, text string) int {
	count := 0
	for _, message := range messages {
		if strings.Contains(string(message.Content), text) {
			count++
		}
	}
	return count
}

// A steer preempts the response that streams (the instant_interrupt feature).
// Codex then drops the partial items of that response: they get item/started
// and deltas, but never an item/completed. The steer's userMessage item/started
// follows every frame of the dropped response, so at that frame the partial
// text is final. It must persist there, as interrupted, and not at the turn
// end, where it would land after the follow-up answer.
func TestHandleCodexOutput_ASteerPersistsTheDroppedPartialBeforeTheFollowUp(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a,
		`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-1"}}}`,
		`{"method":"item/reasoning/summaryTextDelta","params":{"threadId":"main-thread","itemId":"reason-1","summaryIndex":0,"delta":"Reading the launch notes"}}`,
		`{"method":"item/agentMessage/delta","params":{"threadId":"main-thread","itemId":"msg-a","delta":"The launch is "}}`,
	)
	require.NotZero(t, sink.ProgressSnapshot().ThinkingTokens, "the partial response counts its tokens")

	feedCodexLines(a, `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"userMessage","id":"steer-1","content":[{"type":"text","text":"Answer in one word."}]}}}`)

	messages := sink.Messages()
	partial := messageIndexContaining(messages, "The launch is ")
	require.NotEqual(t, -1, partial, "the steer's user message persists the dropped partial text")
	assert.JSONEq(t, `{"type":"assembled_message","kind":"text","text":"The launch is ","completion":"interrupted"}`, string(messages[partial].Content))
	reasoning := messageIndexContaining(messages, "Reading the launch notes")
	require.NotEqual(t, -1, reasoning, "the steer's user message persists the dropped partial reasoning")
	assert.Contains(t, string(messages[reasoning].Content), `"completion":"interrupted"`)
	assert.Less(t, reasoning, partial, "the partial rows keep the order of their first delta")
	assert.Equal(t, agent.ProgressSnapshot{}, sink.ProgressSnapshot(), "the dropped items count no more tokens")

	feedCodexLines(a,
		`{"method":"item/agentMessage/delta","params":{"threadId":"main-thread","itemId":"msg-b","delta":"Soon."}}`,
		`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"agentMessage","id":"msg-b","text":"Soon."}}}`,
		`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed","items":[]}}}`,
	)

	messages = sink.Messages()
	assert.Equal(t, 1, countMessagesContaining(messages, "The launch is "), "the turn end persists the dropped text no second time")
	assert.Equal(t, 1, countMessagesContaining(messages, "Reading the launch notes"))
	followUp := messageIndexContaining(messages, "Soon.")
	require.NotEqual(t, -1, followUp)
	assert.Less(t, messageIndexContaining(messages, "The launch is "), followUp, "the dropped text stays before the follow-up answer")
}

// The input that starts a turn also arrives as a userMessage item/started.
// At that frame no response streams, so it persists nothing.
func TestHandleCodexOutput_AUserMessageWithNoPartialResponsePersistsNothing(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a,
		`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-1"}}}`,
		`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"userMessage","id":"input-1","content":[{"type":"text","text":"Hello"}]}}}`,
	)

	assert.Zero(t, sink.MessageCount())
}

// A subagent's response drops in the same way when its parent sends it new
// input. The partial text persists in the subagent's transcript, before its
// follow-up answer.
func TestHandleCodexOutput_ASteerOfASubagentPersistsTheDroppedPartialInItsTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a,
		`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","agentsStates":{}}}}`,
		`{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"child-turn"}}}`,
		`{"method":"item/agentMessage/delta","params":{"threadId":"child-1","itemId":"msg-a","delta":"partial child answer"}}`,
		`{"method":"item/started","params":{"threadId":"child-1","turnId":"child-turn","item":{"type":"userMessage","id":"steer-1","content":[{"type":"text","text":"Stop and report."}]}}}`,
		`{"method":"item/completed","params":{"threadId":"child-1","turnId":"child-turn","item":{"type":"agentMessage","id":"msg-b","text":"The report."}}}`,
		`{"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"child-turn","status":"completed","items":[]}}}`,
	)

	child := sink.Child("child-of-call-1")
	messages := child.Messages()
	partial := messageIndexContaining(messages, "partial child answer")
	require.NotEqual(t, -1, partial)
	assert.JSONEq(t, `{"type":"assembled_message","kind":"text","text":"partial child answer","completion":"interrupted"}`, string(messages[partial].Content))
	assert.Equal(t, 1, countMessagesContaining(messages, "partial child answer"))
	followUp := messageIndexContaining(messages, "The report.")
	require.NotEqual(t, -1, followUp)
	assert.Less(t, partial, followUp)
	assert.Equal(t, -1, messageIndexContaining(sink.Messages(), "partial child answer"), "the parent transcript holds no child text")
}

// Before the subagent's route exists, its frames wait in order. The flush that
// the userMessage frame causes must cover only the items that streamed before
// that frame. The follow-up item streams after it, and its own item/completed
// persists it once.
func TestHandleCodexOutput_ASteerOfAnUnroutedSubagentFlushesOnlyTheDroppedItems(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	feedCodexLines(a,
		`{"method":"item/agentMessage/delta","params":{"threadId":"child-1","itemId":"msg-a","delta":"dropped child text"}}`,
		`{"method":"item/started","params":{"threadId":"child-1","turnId":"child-turn","item":{"type":"userMessage","id":"steer-1","content":[{"type":"text","text":"Stop and report."}]}}}`,
		`{"method":"item/agentMessage/delta","params":{"threadId":"child-1","itemId":"msg-b","delta":"The report."}}`,
		`{"method":"item/completed","params":{"threadId":"main-thread","item":{"type":"collabAgentToolCall","id":"spawn-1","tool":"spawnAgent","status":"completed","receiverThreadIds":["child-1"],"prompt":"work","agentsStates":{"child-1":{"status":"running"}}}}}`,
		`{"method":"item/completed","params":{"threadId":"child-1","turnId":"child-turn","item":{"type":"agentMessage","id":"msg-b","text":"The report."}}}`,
		`{"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"child-turn","status":"completed","items":[]}}}`,
	)

	child := sink.Child("child-of-spawn-1")
	messages := child.Messages()
	dropped := messageIndexContaining(messages, "dropped child text")
	require.NotEqual(t, -1, dropped)
	assert.Contains(t, string(messages[dropped].Content), `"completion":"interrupted"`)
	assert.Equal(t, 1, countMessagesContaining(messages, "The report."), "the follow-up persists once, from its item/completed")
	assert.NotContains(t, string(messages[messageIndexContaining(messages, "The report.")].Content), `"completion":"interrupted"`)
	assert.Less(t, dropped, messageIndexContaining(messages, "The report."))
}

func codexTurnIsActiveForTest(a *Agent) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.mainTurn.id != ""
}

type codexSteerEchoSink struct {
	agenttest.Sink
	onEcho func()
}

func (s *codexSteerEchoSink) CaptureSteerReport(count int, outcome agent.SteerOutcome) func() error {
	report := s.Sink.CaptureSteerReport(count, outcome)
	return func() error {
		if outcome == agent.SteerOutcomeEchoed && s.onEcho != nil {
			s.onEcho()
		}
		return report()
	}
}

func (s *codexSteerEchoSink) ReportSteersEchoed(count int) {
	if s.onEcho != nil {
		s.onEcho()
	}
	s.Sink.ReportSteersEchoed(count)
}

func TestCodexSteerFailureSettlementOwnsTheOutputLock(t *testing.T) {
	t.Parallel()
	type reply struct {
		request codexRecordedRequest
		respond func(agenttest.RPCReply)
	}
	replies := make(chan reply, 2)
	a, _, _ := newCodexAgentForAsyncRPCRequests(t, func(request codexRecordedRequest, respond func(agenttest.RPCReply)) {
		replies <- reply{request, respond}
	})
	sink := &codexSteerEchoSink{onEcho: func() {
		if a.outputMu.TryLock() {
			a.outputMu.Unlock()
			t.Error("an echo report must serialize with later output rows")
		}
	}}
	a.sink = agent.NewProviderServices(sink)
	a.threadID, a.mainTurn.id = "main-thread", "turn-1"
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() { firstDone <- a.SteerInput("first", nil) }()
	first := <-replies
	go func() { secondDone <- a.SteerInput("second", nil) }()
	second := <-replies
	second.respond(agenttest.RPCReply{Result: json.RawMessage(`{}`)})
	require.NoError(t, <-secondDone)
	clientID := second.request.Params["clientUserMessageId"].(string)
	feedCodexLines(a, fmt.Sprintf(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"userMessage","id":"second","clientId":%q,"content":[]}}}`, clientID))
	assert.Empty(t, sink.SteerEchoReports(), "the later echo waits for the first call's disposition")
	first.respond(agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"refused"}`)})
	require.ErrorIs(t, <-firstDone, agent.ErrNoActiveTurn)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}

func TestCodexProcessExitFinishesAHeldTurnWithoutInventingASteerEcho(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	a, sink, _ := newCodexAgentForAsyncRPCRequests(t, func(codexRecordedRequest, func(agenttest.RPCReply)) { close(entered) })
	a.threadID, a.mainTurn.id = "main-thread", "turn-1"
	finished := make(chan error, 1)
	go func() { finished <- a.SteerInput("guide", nil) }()
	<-entered
	feedCodexLines(a, `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed"}}}`)
	a.SimulateExitForTest()
	require.NoError(t, a.Wait())
	require.ErrorIs(t, <-finished, agent.ErrDeliveryUncertain)
	assert.Empty(t, sink.SteerEchoReports())
	assert.False(t, codexTurnIsActiveForTest(a))
	assert.Equal(t, 1, countMessagesContaining(sink.Messages(), `"status":"completed"`))
}

func TestCodexHeldTurnEndWithdrawsItsTurnRetryOffer(t *testing.T) {
	t.Parallel()
	replies := make(chan func(agenttest.RPCReply), 1)
	a, sink, _ := newCodexAgentForAsyncRPCRequests(t, func(_ codexRecordedRequest, respond func(agenttest.RPCReply)) { replies <- respond })
	armRetryableTurn(t, a, sink)
	finished := make(chan error, 1)
	go func() { finished <- a.SteerInput("guide", nil) }()
	respond := <-replies
	feedCodexLines(a, `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed"}}}`)
	offer, present := sink.LastSessionInfoValue("turn_retry_offer")
	assert.True(t, present)
	assert.Nil(t, offer, "a completed native turn cannot offer a turn retry during the steer wait")
	respond(agenttest.RPCReply{Result: json.RawMessage(`{}`)})
	require.NoError(t, <-finished)
}

func TestCodexContextReplacementSettlesAHeldTurnBeforeChangingItsSession(t *testing.T) {
	t.Parallel()
	reply := make(chan func(agenttest.RPCReply), 1)
	a, sink, _ := newCodexAgentForAsyncRPCRequests(t, func(_ codexRecordedRequest, respond func(agenttest.RPCReply)) { reply <- respond })
	a.threadID, a.mainTurn.id = "main-thread", "turn-1"
	finished := make(chan error, 1)
	go func() { finished <- a.SteerInput("guide", nil) }()
	respond := <-reply
	feedCodexLines(a, `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed"}}}`)
	_, err := a.replaceMainThread(codexThreadResult{ID: "new-thread"})
	require.NoError(t, err)
	respond(agenttest.RPCReply{Result: json.RawMessage(`{}`)})
	require.ErrorIs(t, <-finished, agent.ErrNoActiveTurn)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeDropped, Count: 1}}, sink.SteerEchoReports())
	assert.Equal(t, 1, countMessagesContaining(sink.Messages(), `"status":"completed"`))
	assert.Equal(t, "new-thread", a.currentThreadID())
}

func TestHandleCodexOutput_ARealWebSocketSteerPersistsTheCutAnswerAsInterrupted(t *testing.T) {
	t.Parallel()
	a, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	sink := &codexSteerEchoSink{}
	var rowsAtEcho []agenttest.Message
	sink.onEcho = func() { rowsAtEcho = sink.Messages() }
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	a.threadID, a.mainTurn.id = "main-thread", "turn-1"
	// Trimmed from codex/real-logs/real1-frames.log, captured on 2026-10-17. The echo uses this request's client ID.
	feedCodexLines(a,
		`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"agentMessage","id":"partial","text":""}}}`,
		`{"method":"item/agentMessage/delta","params":{"threadId":"main-thread","turnId":"turn-1","itemId":"partial","delta":"1 2 3 "}}`,
	)
	require.NoError(t, a.SteerInput("Stop now, say STEERED", nil))
	clientID := requests()[0].Params["clientUserMessageId"].(string)
	feedCodexLines(a, fmt.Sprintf(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"userMessage","id":"steer","clientId":%q,"content":[{"type":"text","text":"Stop now, say STEERED"}]}}}`, clientID))
	require.Len(t, rowsAtEcho, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, rowsAtEcho[0].Source)
	assert.JSONEq(t, `{"type":"assembled_message","kind":"text","text":"1 2 3 ","completion":"interrupted"}`, string(rowsAtEcho[0].Content))
	feedCodexLines(a, `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"agentMessage","id":"answer","text":"STEERED"}}}`)
	require.Len(t, sink.Messages(), 2)
	assert.Contains(t, string(sink.Messages()[1].Content), "STEERED")
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}

func TestCodexSessionSteeringValidatesTheNativeTarget(t *testing.T) {
	t.Parallel()
	for _, expected := range []string{"", "previous", "current"} {
		t.Run(expected, func(t *testing.T) {
			t.Parallel()
			a, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply {
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			})
			a.threadID = "current"
			a.mainTurn.id = "turn-1"

			err := a.SteerInputForSession(expected, "Keep the selected color.", nil)

			if expected != "current" {
				require.ErrorIs(t, err, agent.ErrInputSessionChanged)
				assert.Empty(t, requests(), "a different session must receive no request")
				return
			}
			require.NoError(t, err)
			require.Len(t, requests(), 1)
			assert.Equal(t, "turn/steer", requests()[0].Method)
			assert.Equal(t, "current", requests()[0].Params["threadId"])
			assert.Equal(t, "turn-1", requests()[0].Params["expectedTurnId"])
		})
	}
}
