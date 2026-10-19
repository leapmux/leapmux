package muse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	done := make(chan struct{})
	a := &Agent{connection: &connection{JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "muse-test", ProviderName: "Muse Code", Ctx: ctx, Cancel: cancel, Stdin: &agenttest.Stdin{}, ProcessDone: done})}}, sink: agent.NewProviderServices(sink), sessionID: "session", settings: optionmap.Map{agent.OptionIDModel: "native"}, sessions: make(map[string]*sessionState)}
	t.Cleanup(func() { close(done); a.Process.Stop() })
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

func TestMuseHookRunPassingStoresNothing(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("session", "turn")
	for revision, status := range []string{"inProgress", "completed"} {
		feed(t, a, contracts.MuseMethodItemUpdated, map[string]any{
			"sessionId": "session", "item": map[string]any{
				"itemId": "hook", "revision": revision + 1, "kind": "hookRun", "status": status,
				"event": "Stop", "label": "post-turn", "runStatus": status, "turnId": "turn",
			},
		})
	}
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
	feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "turn", "terminal": "completed"})
	require.Len(t, sink.Messages(), 1)
	assert.True(t, sink.Messages()[0].TurnEnd)
}

func TestMuseHookRunFailedPersists(t *testing.T) {
	for _, status := range []string{"failed", "timedOut", "rejected", "cancelled", "futureStatus"} {
		t.Run(status, func(t *testing.T) {
			a, sink := testAgent(t)
			// Muse run11 sends the hook event, label, and reason in the completed item.
			raw := feed(t, a, contracts.MuseMethodItemCompleted, map[string]any{
				"sessionId": "session", "item": map[string]any{
					"itemId": "hook", "revision": 2, "kind": "hookRun", "status": status,
					"event": "Stop", "label": "post-turn", "reason": "The hook refused the action.",
				},
			})
			a.HandleOutput(raw)
			require.Len(t, sink.Messages(), 1)
			assert.Equal(t, raw, sink.Messages()[0].Content)
		})
	}
}

func TestMuseRetryResolvesAtTheFirstDelta(t *testing.T) {
	for _, child := range []bool{false, true} {
		for _, kind := range []string{"agentMessage", "reasoning"} {
			t.Run(fmt.Sprintf("child=%t/kind=%s", child, kind), func(t *testing.T) {
				a, root := testAgent(t)
				id, sink := "session", root
				if child {
					id, sink = "child-session", &agenttest.Sink{}
					a.sessions[id] = &sessionState{sink: agent.NewProviderServices(sink), childID: "child", items: make(map[string]*itemState), completed: make(map[string]bool)}
				}
				a.startTurn(id, "turn")
				// Muse run6 reports nextAttempt and retryDelayMs, not a generic delayMs.
				feed(t, a, contracts.MuseMethodTurnRetryScheduled, map[string]any{"sessionId": id, "turnId": "turn", "attempt": 1, "nextAttempt": 2, "maxAttempts": 10, "retryDelayMs": 1000, "reason": "The response ended early."})
				feed(t, a, contracts.MuseMethodItemStarted, map[string]any{"sessionId": id, "item": map[string]any{"itemId": "answer", "kind": kind, "status": "inProgress", "revision": 1, "turnId": "turn"}})
				assert.Empty(t, sink.LeapMuxNotifications())
				field := "text"
				if kind == "reasoning" {
					field = "summary.0"
				}
				for _, cursor := range []string{"first", "second"} {
					feed(t, a, contracts.MuseMethodItemDelta, map[string]any{"sessionId": id, "itemId": "answer", "field": field, "delta": "new text", "viewCursor": cursor})
					require.Len(t, sink.LeapMuxNotifications(), 1)
					assert.Equal(t, contracts.NotificationTypeApiRetryResolved, sink.LeapMuxNotifications()[0][contracts.NotificationFieldType])
				}
				if child {
					assert.Empty(t, root.LeapMuxNotifications())
				}
			})
		}
	}
}

func TestMuseRetryDoesNotResolveAfterTheTurnEnds(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("session", "turn")
	feed(t, a, contracts.MuseMethodTurnRetryScheduled, map[string]any{"sessionId": "session", "turnId": "turn", "nextAttempt": 2})
	feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "turn", "terminal": "failed"})
	a.startTurn("session", "next")
	feed(t, a, contracts.MuseMethodItemStarted, map[string]any{"sessionId": "session", "item": map[string]any{"itemId": "answer", "kind": "agentMessage", "status": "inProgress", "revision": 1, "turnId": "next"}})
	feed(t, a, contracts.MuseMethodItemDelta, map[string]any{"sessionId": "session", "itemId": "answer", "field": "text", "delta": "new text", "viewCursor": "first"})
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestMuseFailedAttemptCarriesItsNativeCompletion(t *testing.T) {
	for _, kind := range []string{"agentMessage", "reasoning", "toolCall"} {
		for _, scenario := range []struct {
			status string
			want   agent.MessageCompletion
		}{
			{"failed", agent.MessageCompletionError},
			{"timedOut", agent.MessageCompletionError},
			{"cancelled", agent.MessageCompletionInterrupted},
			{"completed", ""},
		} {
			for _, failedWrite := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/failed-write=%t", kind, scenario.status, failedWrite), func(t *testing.T) {
					a, sink := testAgent(t)
					a.startTurn("session", "turn")
					if failedWrite {
						sink.PersistErr = errors.New("The store is unavailable.")
					}
					// Muse run6 retains the failed attempt as a completed native item.
					raw := feed(t, a, contracts.MuseMethodItemCompleted, map[string]any{"sessionId": "session", "item": map[string]any{
						"itemId": "attempt", "kind": kind, "revision": 1, "status": scenario.status,
						"turnId": "turn", "text": "PARTIAL-ATTEMPT-ONE", "tool": "bash",
					}})
					sink.PersistErr = nil
					if failedWrite {
						feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "turn", "terminal": "completed"})
					}
					require.NotEmpty(t, sink.Messages())
					message := sink.Messages()[0]
					assert.Equal(t, raw, message.Content)
					if kind == "toolCall" {
						assert.Empty(t, message.Completion)
					} else {
						assert.Equal(t, scenario.want, message.Completion)
					}
				})
			}
		}
	}
}

func TestMuseCompactionIsLiveUntilItsItemCompletes(t *testing.T) {
	for _, child := range []bool{false, true} {
		for _, outcome := range []string{"compacted", "noop", "failed", "cancelled"} {
			t.Run(fmt.Sprintf("child=%t/outcome=%s", child, outcome), func(t *testing.T) {
				a, root := testAgent(t)
				id, sink := "session", root
				if child {
					id, sink = "child-session", &agenttest.Sink{}
					a.sessions[id] = &sessionState{sink: agent.NewProviderServices(sink), childID: "child", items: make(map[string]*itemState), completed: make(map[string]bool)}
				}
				item := itemParams{SessionID: id, Item: nativeItem{ID: "compact", Kind: "compaction", Revision: 1, Status: "inProgress", Trigger: "manual"}}
				raw := feed(t, a, contracts.MuseMethodItemStarted, item)
				a.HandleOutput(raw)
				assert.Equal(t, []map[string]interface{}{{contracts.SessionInfoKeyCompaction: map[string]interface{}{contracts.CompactionFieldTrigger: "manual"}}}, sink.SessionInfos())
				assert.Empty(t, sink.Messages())
				assert.Empty(t, sink.PersistedNotifications())
				item.Item.Revision, item.Item.Status, item.Item.Outcome = 2, "completed", outcome
				raw = feed(t, a, contracts.MuseMethodItemCompleted, item)
				a.HandleOutput(raw)
				assert.Equal(t, map[string]interface{}{contracts.SessionInfoKeyCompaction: nil}, sink.LastSessionInfo())
				require.Len(t, sink.PersistedNotifications(), 1)
				assert.Equal(t, raw, sink.PersistedNotifications()[0].Content)
				if child {
					assert.Empty(t, root.SessionInfos())
				}
			})
		}
	}
}

func TestMuseCompactionOutlivesTheModelTurn(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("session", "turn")
	feed(t, a, contracts.MuseMethodItemStarted, itemParams{SessionID: "session", Item: nativeItem{ID: "compact", Kind: "compaction", Revision: 1, Status: "inProgress"}})
	feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "turn", "terminal": "completed"})
	assert.Equal(t, map[string]interface{}{contracts.SessionInfoKeyCompaction: map[string]interface{}{}}, sink.LastSessionInfo())
	assert.Empty(t, sink.LeapMuxNotifications())
	a.retireHost(agent.MessageCompletionError)
	assert.Equal(t, map[string]interface{}{contracts.SessionInfoKeyCompaction: nil}, sink.LastSessionInfo())
}

func TestMuseCompactionEndWithoutStartClearsLiveState(t *testing.T) {
	a, sink := testAgent(t)
	feed(t, a, contracts.MuseMethodItemCompleted, itemParams{SessionID: "session", Item: nativeItem{ID: "compact", Kind: "compaction", Revision: 1, Status: "completed", Outcome: "failed"}})
	assert.Equal(t, map[string]interface{}{contracts.SessionInfoKeyCompaction: nil}, sink.LastSessionInfo())
	require.Len(t, sink.PersistedNotifications(), 1)
}

type museCompactionSessionSwitchSink struct {
	agent.ProviderServices
	a    *Agent
	done chan struct{}
}

func (s *museCompactionSessionSwitchSink) PersistNotification(_ leapmuxv1.MessageSource, _ agent.MessageContent) (bool, error) {
	locked := make(chan struct{})
	go func() {
		s.a.stateMu.Lock()
		close(locked)
		s.a.sessionID = "replacement"
		s.a.stateMu.Unlock()
		close(s.done)
	}()
	<-locked
	return false, errors.New("The compaction write failed.")
}

func TestMuseCompactionDoesNotRaceASessionReplacement(t *testing.T) {
	a, _ := testAgent(t)
	a.sessions["replacement"] = &sessionState{sink: a.sink, items: make(map[string]*itemState), completed: make(map[string]bool)}
	done := make(chan struct{})
	a.sessions["session"].sink = &museCompactionSessionSwitchSink{ProviderServices: a.sink, a: a, done: done}
	feed(t, a, contracts.MuseMethodItemCompleted, itemParams{SessionID: "session", Item: nativeItem{ID: "compact", Kind: "compaction", Revision: 1, Status: "completed", Outcome: "compacted"}})
	<-done
}

func TestMuseSubagentItemStoresNoRow(t *testing.T) {
	for _, kind := range []string{"subagent", "reminderChild"} {
		t.Run(kind, func(t *testing.T) {
			a, sink := testAgent(t)
			a.startTurn("session", "turn")
			a.sessions["child"] = &sessionState{sink: a.sink.ChildSink("child-agent"), childID: "child-agent", items: make(map[string]*itemState), completed: make(map[string]bool)}
			turnID := "turn"
			item := itemParams{SessionID: "session", Item: nativeItem{ID: "child-item", Kind: kind, Revision: 1, Status: "completed", TurnID: &turnID, ChildSessionID: "child", SubagentID: "native-child", Objective: "Read the code"}}
			feed(t, a, contracts.MuseMethodItemCompleted, item)
			assert.Empty(t, sink.Messages())
			if kind == "subagent" {
				row, ok := sink.BackgroundTask(bgtask.NormalizeRowKey("session:child:native-child"))
				require.True(t, ok)
				assert.Equal(t, bgtask.StatusSucceeded, row.Status)
				assert.Equal(t, "child-agent", row.ChildAgentID)
			}
			feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "turn", "terminal": "completed"})
			require.Len(t, sink.Messages(), 1)
			assert.True(t, sink.Messages()[0].TurnEnd)
		})
	}
}

type museRetryOrderSink struct {
	agent.ProviderServices
	rows []string
}

func (s *museRetryOrderSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	s.rows = append(s.rows, "message")
	return s.ProviderServices.PersistMessage(source, content, span)
}

func (s *museRetryOrderSink) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	s.rows = append(s.rows, "retry")
	return s.ProviderServices.PersistNotification(source, content)
}

func (s *museRetryOrderSink) PersistLeapMuxNotification(content map[string]interface{}) {
	s.rows = append(s.rows, "resolution")
	s.ProviderServices.PersistLeapMuxNotification(content)
}

func (s *museRetryOrderSink) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	s.rows = append(s.rows, "end")
	return s.ProviderServices.PersistTurnEnd(content, span)
}

// feedMuseRetryBeforeItemEnd uses the event order recorded in Muse run6.
func feedMuseRetryBeforeItemEnd(t *testing.T, a *Agent) itemParams {
	t.Helper()
	a.startTurn("session", "turn")
	turn := "turn"
	item := itemParams{SessionID: "session", Item: nativeItem{ID: "first", Kind: "agentMessage", Revision: 1, Status: "inProgress", TurnID: &turn}}
	feed(t, a, contracts.MuseMethodItemStarted, item)
	feed(t, a, contracts.MuseMethodItemDelta, map[string]any{"sessionId": "session", "itemId": "first", "viewCursor": "first", "delta": "partial "})
	feed(t, a, contracts.MuseMethodTurnRetryScheduled, map[string]any{"sessionId": "session", "turnId": "turn", "nextAttempt": 2})
	return item
}

func TestMuseRetryNoticeFollowsTheFailedAttempt(t *testing.T) {
	a, base := testAgent(t)
	sink := &museRetryOrderSink{ProviderServices: a.sink}
	a.sink, a.sessions["session"].sink = sink, sink
	item := feedMuseRetryBeforeItemEnd(t, a)
	assert.Empty(t, sink.rows)
	item.Item.Status, item.Item.Revision, item.Item.Text = "failed", 2, "partial "
	feed(t, a, contracts.MuseMethodItemCompleted, item)
	assert.Equal(t, []string{"message", "retry"}, sink.rows)
	item.Item.ID, item.Item.Status, item.Item.Text, item.Item.Revision = "second", "inProgress", "", 1
	feed(t, a, contracts.MuseMethodItemStarted, item)
	feed(t, a, contracts.MuseMethodItemDelta, map[string]any{"sessionId": "session", "itemId": "second", "viewCursor": "second", "delta": "answer"})
	assert.Equal(t, []string{"message", "retry", "resolution"}, sink.rows)
	require.Len(t, base.Messages(), 1)
	assert.Equal(t, agent.MessageCompletionError, base.Messages()[0].Completion)
}

func TestMuseRetryNoticeSurvivesATurnEndWithoutAnItemEnd(t *testing.T) {
	a, _ := testAgent(t)
	sink := &museRetryOrderSink{ProviderServices: a.sink}
	a.sink, a.sessions["session"].sink = sink, sink
	feedMuseRetryBeforeItemEnd(t, a)
	feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "turn", "terminal": "failed"})
	assert.Equal(t, []string{"message", "retry", "end"}, sink.rows)
}

func TestMuseQueuedFailureKeepsTheTurnEndBeforeRelease(t *testing.T) {
	a, sink := testFinalizationAgent(t)
	a.sink = agent.NewModelProgressResetSink(a.sink)
	a.sessions["session"].sink = a.sink
	feedMuseRetryBeforeItemEnd(t, a)
	sink.failMessage = 1
	feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "turn", "terminal": "failed"})
	assert.Equal(t, []string{"turn_active:true", "turn_end", "turn_active:false"}, sink.TurnLifecycle())
	require.Len(t, sink.PersistedNotifications(), 1)
	require.Len(t, sink.Messages(), 2)
	assert.True(t, sink.Messages()[1].TurnEnd)
}

func TestMuseRetryResolvesBeforeTheFirstTool(t *testing.T) {
	for _, kind := range []string{"toolCall", "userShell"} {
		t.Run(kind, func(t *testing.T) {
			a, base := testAgent(t)
			sink := &museRetryOrderSink{ProviderServices: a.sink}
			a.sink, a.sessions["session"].sink = sink, sink
			a.startTurn("session", "turn")
			feed(t, a, contracts.MuseMethodTurnRetryScheduled, map[string]any{"sessionId": "session", "turnId": "turn", "nextAttempt": 2})
			turn := "turn"
			item := itemParams{SessionID: "session", Item: nativeItem{ID: "tool", Kind: kind, Tool: "bash", Revision: 1, Status: "inProgress", TurnID: &turn}}
			feed(t, a, contracts.MuseMethodItemStarted, item)
			if kind == "toolCall" {
				assert.Equal(t, []string{"retry", "resolution", "message"}, sink.rows)
				require.Len(t, base.LeapMuxNotifications(), 1)
			} else {
				assert.Equal(t, []string{"retry", "message"}, sink.rows)
				assert.Empty(t, base.LeapMuxNotifications(), "a user shell is not model output")
			}
		})
	}
}

func TestMuseQueuedFailuresHoldAllFinalRows(t *testing.T) {
	a, sink := testFinalizationAgent(t)
	a.sink = agent.NewModelProgressResetSink(a.sink)
	a.sessions["session"].sink = a.sink
	feedMuseRetryBeforeItemEnd(t, a)
	sink.failMessage = 100
	feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "turn", "terminal": "failed"})
	assert.Equal(t, []string{"turn_active:true"}, sink.TurnLifecycle())
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
	sink.failMessage = 0
	require.NoError(t, agent.FlushPendingOutput(a.sink))
	assert.Equal(t, []string{"turn_active:true", "turn_end", "turn_active:false"}, sink.TurnLifecycle())
	require.Len(t, sink.PersistedNotifications(), 1)
	require.Len(t, sink.Messages(), 2)
	assert.True(t, sink.Messages()[1].TurnEnd)
}

func TestMuseRetiredItemsKeepRowsWithoutLiveEffects(t *testing.T) {
	a, sink := testAgent(t)
	a.sessions["session"].retired = true
	a.sessionID = "replacement"
	a.sessions["replacement"] = &sessionState{sink: a.sink, items: make(map[string]*itemState), completed: make(map[string]bool), log: newNativeLog("replacement")}
	sink.UpdateSessionID("replacement")
	item := resultItem("old-tool", "call", "origin")
	item.Item.Revision, item.Item.Status = 1, "inProgress"
	feed(t, a, contracts.MuseMethodItemStarted, item)
	item.Item.Revision, item.Item.Status = 2, "completed"
	feed(t, a, contracts.MuseMethodItemCompleted, item)
	feed(t, a, contracts.MuseMethodItemCompleted, itemParams{SessionID: "session", Item: nativeItem{ID: "old-workflow", Kind: "workflow", Status: "completed", Revision: 1, WorkflowRunID: "old-run"}})
	require.Len(t, sink.Messages(), 3)
	for _, message := range sink.Messages() {
		assert.Equal(t, "session", message.AgentSessionID)
	}
	assert.Empty(t, sink.OpenSpans())
	assert.Empty(t, sink.ClosedSpans())
	assert.Empty(t, sink.ProgressUpdates())
	assert.Empty(t, sink.BackgroundTasks())
	assert.Empty(t, sink.TurnActiveCalls)
}

func TestMuseQueuedToolStartOpensItsSpanAfterTheWrite(t *testing.T) {
	a, sink := testFinalizationAgent(t)
	a.sink = agent.NewModelProgressResetSink(a.sink)
	a.sessions["session"].sink = a.sink
	sink.failMessage = 1
	item := resultItem("tool", "call", "origin")
	item.Item.Revision, item.Item.Status = 1, "inProgress"
	feed(t, a, contracts.MuseMethodItemStarted, item)
	require.NoError(t, agent.FlushPendingOutput(a.sink))
	require.Len(t, sink.Messages(), 1)
	require.Len(t, sink.OpenSpans(), 1)
	assert.Equal(t, "tool", sink.OpenSpans()[0].SpanID)
}

func TestMuseHistoricalRetryDoesNotResolveTheCurrentTurn(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("session", "old-turn")
	feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "old-turn", "terminal": "failed"})
	a.startTurn("session", "current-turn")
	// A recovered view page can contain a retry notice from an earlier turn.
	feed(t, a, contracts.MuseMethodTurnRetryScheduled, map[string]any{"sessionId": "session", "turnId": "old-turn", "nextAttempt": 2})
	turn := "current-turn"
	feed(t, a, contracts.MuseMethodItemStarted, itemParams{SessionID: "session", Item: nativeItem{ID: "current-answer", Kind: "agentMessage", Revision: 1, Status: "inProgress", TurnID: &turn}})
	feed(t, a, contracts.MuseMethodItemDelta, map[string]any{"sessionId": "session", "itemId": "current-answer", "viewCursor": "current", "delta": "new answer"})
	assert.Empty(t, sink.LeapMuxNotifications())
	require.Len(t, sink.PersistedNotifications(), 1)
}
