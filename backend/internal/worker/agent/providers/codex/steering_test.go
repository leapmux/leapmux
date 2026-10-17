package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
