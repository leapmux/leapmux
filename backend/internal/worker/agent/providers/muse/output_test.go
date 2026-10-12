package muse

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testAgent(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	sink := &agenttest.Sink{}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	a := &Agent{connection: &connection{JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "muse-test", ProviderName: "Muse Code", Ctx: ctx, Cancel: cancel, Stdin: &agenttest.Stdin{}})}}, sink: agent.NewProviderServices(sink), sessionID: "session", settings: optionmap.Map{agent.OptionIDModel: "native"}, sessions: make(map[string]*sessionState)}
	a.sessions["session"] = &sessionState{sink: a.sink, items: make(map[string]*itemState), completed: make(map[string]bool), log: newNativeLog("session")}
	return a, sink
}
func feed(t *testing.T, a *Agent, method string, params any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	require.NoError(t, err)
	a.HandleOutput(raw)
	return raw
}
func TestMuseTurnFrames(t *testing.T) {
	agenttest.AssertTurnFrames(t, []agenttest.TurnFrameCase{
		{Name: "starts", Line: `{"method":"turn/started","params":{"sessionId":"session","turnId":"turn"}}`, Moves: true},
		{Name: "completes", Line: `{"method":"turn/completed","params":{"sessionId":"session","turnId":"turn","terminal":"completed"}}`, Moves: true},
		{Name: "foreign session", Line: `{"method":"turn/started","params":{"sessionId":"foreign","turnId":"turn"}}`},
		{Name: "unknown notification", Line: `{"method":"future/event","params":{"sessionId":"session"}}`},
	}, func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		a, sink := testAgent(t)
		if tc.Name == "completes" {
			a.sessions["session"].turnID = "turn"
		}
		a.HandleOutput([]byte(tc.Line))
		return sink.TurnActiveCalls
	})
}
func TestMuseRejectsMissingAndReplacedSessions(t *testing.T) {
	a, _ := testAgent(t)
	agenttest.AssertRejectsMissingAndReplacedSessions(t, a)
}
func TestMuseBusyRefusalRepublishesTheTurn(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("session", "turn")
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, a, a.SendInput("later", nil))
}
func TestMuseRisingTurnTokens(t *testing.T) {
	a, sink := testAgent(t)
	agenttest.AssertRisingTurnTokens(t, sink, a)
}
func TestMusePreservesDistinctItemsWithARepeatedCallID(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("session", "turn")
	for _, id := range []string{"first", "second"} {
		item := resultItem(id, "same", "origin")
		item.Item.Revision = 1
		item.Item.Status = "inProgress"
		feed(t, a, "item/started", item)
		item.Item.Revision = 2
		item.Item.Status = "completed"
		item.Item.VisibleOutput = id
		raw := feed(t, a, "item/completed", item)
		a.HandleOutput(raw)
	}
	messages := sink.Messages()
	require.Len(t, messages, 4)
	assert.Equal(t, "first", messages[0].SpanID)
	assert.Equal(t, "second", messages[2].SpanID)
	feed(t, a, "turn/completed", map[string]any{"sessionId": "session", "turnId": "turn", "terminal": "completed"})
	assert.Equal(t, []string{"turn_active:true", "turn_end", "turn_active:false"}, sink.TurnLifecycle())
}

func TestMuseChildStatusUsesTheGenericFinalState(t *testing.T) {
	assert.Equal(t, bgtask.StatusFailed, childStatusResult("failed", "closing"))
	assert.Equal(t, bgtask.StatusSucceeded, childStatusResult("completed", "recoveryPending"))
	assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, childStatusResult("futureFinal", "completed"))
}

func TestRetiredRootFramesDoNotPublishReplacementActivity(t *testing.T) {
	a, sink := testAgent(t)
	retired := a.sessions["session"]
	a.sessionID = "replacement"
	a.sessions["replacement"] = &sessionState{sink: a.sink, turnID: "replacement-turn", items: make(map[string]*itemState), completed: make(map[string]bool), log: newNativeLog("replacement")}

	feed(t, a, methodTurnStarted, map[string]any{"sessionId": "session", "turnId": "retired-turn"})
	retired.turnID = "retired-turn"
	feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "retired-turn", "terminal": contracts.MuseTurnOutcomeCompleted})

	assert.Equal(t, []string{"turn_end"}, sink.TurnLifecycle())
	assert.Equal(t, "replacement-turn", a.sessions["replacement"].turnID)
}

func TestMalformedCompletionDoesNotConsumeTheTurn(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("session", "turn")
	feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "turn", "terminal": true})
	assert.Equal(t, "turn", a.sessions["session"].turnID)
	feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "turn", "terminal": contracts.MuseTurnOutcomeCompleted})
	assert.Equal(t, []string{"turn_active:true", "turn_end", "turn_active:false"}, sink.TurnLifecycle())
}

func TestFinalItemReplayRetriesAFailedPersistence(t *testing.T) {
	a, sink := testAgent(t)
	sink.PersistErr = errors.New("store unavailable")
	item := resultItem("item", "call", "origin")
	item.Item.Revision = 1
	item.Item.Status = contracts.MuseItemStatusCompleted
	raw := feed(t, a, contracts.MuseMethodItemCompleted, item)
	assert.Zero(t, a.sessions["session"].items["item"].persistedRevision)

	sink.PersistErr = nil
	a.HandleOutput(raw)
	assert.Equal(t, int64(1), a.sessions["session"].items["item"].persistedRevision)
}

func childStatusResult(status, controlStatus string) bgtask.Status {
	return resolvedChildStatus(nativeItem{Status: status, ControlStatus: controlStatus})
}

type finalizationSink struct {
	*agenttest.Sink
	failMessage int
	failEnd     int
	successful  []agenttest.Message
}

func (s *finalizationSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if s.failMessage > 0 {
		s.failMessage--
		return errors.New("message store unavailable")
	}
	s.successful = append(s.successful, agenttest.Message{Content: append([]byte(nil), content.Original...), AgentSessionID: content.AgentSessionID, Completion: content.Completion, SpanID: span.SpanID})
	return s.Sink.PersistMessage(source, content, span)
}

func (s *finalizationSink) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	if s.failEnd > 0 {
		s.failEnd--
		return errors.New("turn store unavailable")
	}
	s.successful = append(s.successful, agenttest.Message{Content: append([]byte(nil), content.Original...), AgentSessionID: content.AgentSessionID, Completion: content.Completion, TurnEnd: true})
	return s.Sink.PersistTurnEnd(content, span)
}

func testFinalizationAgent(t *testing.T) (*Agent, *finalizationSink) {
	t.Helper()
	a, recording := testAgent(t)
	sink := &finalizationSink{Sink: recording}
	a.sink = agent.NewProviderServices(sink)
	a.sessions["session"].sink = a.sink
	return a, sink
}

func TestUnknownTurnOutcomeRetainsContentWithoutInventingAnError(t *testing.T) {
	a, sink := testFinalizationAgent(t)
	a.startTurn("session", "turn")
	a.sessions["session"].generation.Append("text", agent.AssembledMessageKindText, "native partial text", providerkit.JoinVerbatim)
	item := resultItem("unfinished-tool", "call", "origin")
	item.Item.Revision = 1
	item.Item.Status = contracts.MuseItemStatusInProgress
	tool := feed(t, a, contracts.MuseMethodItemStarted, item)
	divider := feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "turn", "terminal": "futureFinal"})
	retained := 0
	for _, message := range sink.successful {
		if message.Completion == "" {
			continue
		}
		retained++
		assert.Equal(t, agent.MessageCompletion("finished"), message.Completion)
		assert.Equal(t, "session", message.AgentSessionID)
		if message.TurnEnd {
			assert.Equal(t, divider, message.Content)
		} else if message.SpanID == "unfinished-tool" {
			assert.Equal(t, tool, message.Content)
		} else {
			var text map[string]any
			require.NoError(t, json.Unmarshal(message.Content, &text))
			assert.Equal(t, "native partial text", text["text"])
			assert.Equal(t, "finished", text["completion"])
		}
	}
	assert.Equal(t, 3, retained)
	assert.Empty(t, a.sessions["session"].turnID)
}

func TestCompletionReplayRetriesOnlyUnfinishedWrites(t *testing.T) {
	a, sink := testFinalizationAgent(t)
	a.startTurn("session", "turn")
	a.sessions["session"].generation.Append("text", agent.AssembledMessageKindText, "partial", providerkit.JoinVerbatim)
	sink.failEnd = 1
	raw := feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "turn", "terminal": contracts.MuseTurnOutcomeCancelled})
	a.HandleOutput(raw)
	require.Len(t, sink.successful, 2)
	assert.False(t, sink.successful[0].TurnEnd)
	assert.True(t, sink.successful[1].TurnEnd)
	assert.Equal(t, raw, sink.successful[1].Content)
	assert.Equal(t, agent.MessageCompletionInterrupted, sink.successful[0].Completion)
	assert.Equal(t, agent.MessageCompletionInterrupted, sink.successful[1].Completion)
	a.HandleOutput(raw)
	assert.Len(t, sink.successful, 2)
}

func TestLaterTurnKeepsTheOriginalFailedCompletion(t *testing.T) {
	a, sink := testFinalizationAgent(t)
	a.startTurn("session", "first")
	a.sessions["session"].generation.Append("first-text", agent.AssembledMessageKindText, "first partial", providerkit.JoinVerbatim)
	sink.failMessage = 1
	first := feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "first", "terminal": contracts.MuseTurnOutcomeCancelled})
	a.startTurn("session", "second")
	a.sessions["session"].generation.Append("second-text", agent.AssembledMessageKindText, "second partial", providerkit.JoinVerbatim)
	feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "second", "terminal": contracts.MuseTurnOutcomeCompleted})
	require.Len(t, sink.successful, 4)
	_, text, ended, ok := agenttest.DecodeAssembledMessage(sink.successful[0].Content)
	require.True(t, ok)
	assert.Equal(t, "first partial", text)
	assert.Equal(t, agent.MessageCompletionInterrupted, ended)
	assert.Equal(t, first, sink.successful[1].Content)
	assert.True(t, sink.successful[1].TurnEnd)
	_, text, ended, ok = agenttest.DecodeAssembledMessage(sink.successful[2].Content)
	require.True(t, ok)
	assert.Equal(t, "second partial", text)
	assert.Equal(t, agent.MessageCompletionComplete, ended)
}

func TestCompletionRetriesAFailedFinalNativeItem(t *testing.T) {
	a, sink := testFinalizationAgent(t)
	a.startTurn("session", "turn")
	item := resultItem("item", "call", "origin")
	item.Item.Revision = 1
	item.Item.Status = contracts.MuseItemStatusCompleted
	sink.failMessage = 1
	raw := feed(t, a, contracts.MuseMethodItemCompleted, item)
	feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "turn", "terminal": contracts.MuseTurnOutcomeCompleted})
	require.Len(t, sink.successful, 2)
	assert.Equal(t, raw, sink.successful[0].Content)
	assert.Equal(t, "session", sink.successful[0].AgentSessionID)
	assert.Equal(t, "item", sink.successful[0].SpanID)
	assert.True(t, sink.successful[1].TurnEnd)
}

func TestCompletedTurnsReleaseTheirCapturedWrites(t *testing.T) {
	a, sink := testFinalizationAgent(t)
	for index := range 100 {
		id := "turn-" + jsonNumber(index)
		a.startTurn("session", id)
		feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": id, "terminal": contracts.MuseTurnOutcomeCompleted})
		assert.Empty(t, a.sessions["session"].finalizations)
	}
	assert.Len(t, sink.successful, 100)
}

func TestFinalizationReleasesSuccessfulWritesBeforeAnEndFailure(t *testing.T) {
	a, sink := testFinalizationAgent(t)
	a.startTurn("session", "turn")
	a.sessions["session"].generation.Append("text", agent.AssembledMessageKindText, "partial", providerkit.JoinVerbatim)
	sink.failEnd = 1
	raw := feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "turn", "terminal": contracts.MuseTurnOutcomeCancelled})
	require.Len(t, a.sessions["session"].finalizations, 1)
	assert.Empty(t, a.sessions["session"].finalizations[0].messages)
	a.HandleOutput(raw)
	assert.Empty(t, a.sessions["session"].finalizations)
	assert.Len(t, sink.successful, 2)
}

func TestInterruptedTextKeepsItsOrderBeforeALaterTool(t *testing.T) {
	a, sink := testFinalizationAgent(t)
	a.startTurn("session", "turn")
	turn := "turn"
	text := itemParams{SessionID: "session", Item: nativeItem{ID: "text", Kind: contracts.MuseItemKindAgentMessage, TurnID: &turn, Revision: 1, Status: contracts.MuseItemStatusInProgress}}
	feed(t, a, contracts.MuseMethodItemStarted, text)
	feed(t, a, contracts.MuseMethodItemDelta, map[string]any{"sessionId": "session", "itemId": "text", "viewCursor": "text-1", "delta": "before tool"})
	tool := resultItem("tool", "call", "origin")
	tool.Item.Revision = 1
	tool.Item.Status = contracts.MuseItemStatusInProgress
	feed(t, a, contracts.MuseMethodItemStarted, tool)
	feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "turn", "terminal": contracts.MuseTurnOutcomeCancelled})
	require.Len(t, sink.successful, 4)
	_, partial, ended, ok := agenttest.DecodeAssembledMessage(sink.successful[1].Content)
	require.True(t, ok)
	assert.Equal(t, "before tool", partial)
	assert.Equal(t, agent.MessageCompletionInterrupted, ended)
	assert.Equal(t, "tool", sink.successful[2].SpanID)
	assert.True(t, sink.successful[3].TurnEnd)
}

func TestReasoningSummaryPartsKeepTheirBoundariesOnInterruption(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("session", "turn")
	turn := "turn"
	feed(t, a, contracts.MuseMethodItemStarted, itemParams{SessionID: "session", Item: nativeItem{ID: "reasoning", Kind: contracts.MuseItemKindReasoning, TurnID: &turn, Revision: 1, Status: contracts.MuseItemStatusInProgress}})
	for index, part := range []struct{ field, text string }{{"summary.0", "first"}, {"summary.1", "second"}, {"summary.0", " part"}} {
		feed(t, a, contracts.MuseMethodItemDelta, map[string]any{"sessionId": "session", "itemId": "reasoning", "viewCursor": "delta-" + jsonNumber(index), "field": part.field, "delta": part.text})
	}
	feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "turn", "terminal": contracts.MuseTurnOutcomeCancelled})
	var texts []string
	for _, message := range sink.Messages() {
		if _, text, _, ok := agenttest.DecodeAssembledMessage(message.Content); ok {
			texts = append(texts, text)
		}
	}
	assert.Equal(t, []string{"first", "second", " part"}, texts)
}

func TestTextItemsRejectDeltaFieldsForOtherKinds(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("session", "turn")
	turn := "turn"
	feed(t, a, contracts.MuseMethodItemStarted, itemParams{SessionID: "session", Item: nativeItem{ID: "text", Kind: contracts.MuseItemKindAgentMessage, TurnID: &turn, Revision: 1, Status: contracts.MuseItemStatusInProgress}})
	for index, field := range []string{"output", "args", "summary.0", "summary.-1"} {
		feed(t, a, contracts.MuseMethodItemDelta, map[string]any{"sessionId": "session", "itemId": "text", "viewCursor": "delta-" + jsonNumber(index), "field": field, "delta": "foreign"})
	}
	feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "turn", "terminal": contracts.MuseTurnOutcomeCancelled})
	for _, message := range sink.Messages() {
		_, _, _, ok := agenttest.DecodeAssembledMessage(message.Content)
		assert.False(t, ok)
	}
}
