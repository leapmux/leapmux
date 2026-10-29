package muse

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMuseSessionOwnershipRequiresTheCompleteCurrentAncestry(t *testing.T) {
	for _, change := range []string{"current", "replaced root", "retired root", "retired parent", "missing parent", "empty parent", "self cycle", "parent cycle", "missing child identity", "replaced state"} {
		t.Run(change, func(t *testing.T) {
			a, _ := testAgent(t)
			parent := &sessionState{childID: "parent-agent", parentSessionID: "session"}
			child := &sessionState{childID: "child-agent", parentSessionID: "parent"}
			a.sessions["parent"], a.sessions["child"] = parent, child
			switch change {
			case "replaced root":
				a.sessionID = "replacement"
				a.sessions["replacement"] = &sessionState{}
			case "retired root":
				a.sessions["session"].retired = true
			case "retired parent":
				parent.retired = true
			case "missing parent":
				delete(a.sessions, "parent")
			case "empty parent":
				parent.parentSessionID = ""
			case "self cycle":
				child.parentSessionID = "child"
			case "parent cycle":
				parent.parentSessionID = "child"
			case "missing child identity":
				parent.childID = ""
			case "replaced state":
				a.sessions["child"] = &sessionState{childID: "new-child", parentSessionID: "session"}
			}
			a.stateMu.Lock()
			current := a.currentSessionState("child", child)
			a.stateMu.Unlock()
			assert.Equal(t, change == "current", current)
		})
	}
}

func TestMuseContextClearRetiresEveryDescendantAndKeepsReceivedEvidence(t *testing.T) {
	a, sink := testAgent(t)
	a.viewWork.CloseAndWait()
	parent := "session"
	childControls := make(map[string]*museControlPublicationSink)
	for depth := 1; depth <= 3; depth++ {
		id, key := fmt.Sprintf("child-%d", depth), fmt.Sprintf("key-%d", depth)
		childSink := sink.Child(id)
		controls := &museControlPublicationSink{Sink: childSink}
		childControls[id] = controls
		state := &sessionState{sink: agent.NewProviderServices(controls), childID: id, childKey: key, parentSessionID: parent,
			subagentID: "native-" + id, turnID: "turn-" + id, items: make(map[string]*itemState), completed: make(map[string]turnCompletionState), log: newNativeLog(id),
			controls: map[string]controlState{"control": {key: "control-" + id, published: true}}, compactionLive: map[string]bool{"compaction": true}}
		state.log.subscriptionID = int64(depth)
		a.sessions[id] = state
		require.NoError(t, a.sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: key, Kind: bgtask.KindSubagent, Status: bgtask.StatusRunning}))
		parent = id
	}
	var unsubscribed []string
	childWrites := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		if method == methodSessionStart {
			return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"replacement","activeTurnId":null},"viewCursor":"replacement-head"}`)}
		}
		var params struct {
			SessionID string `json:"sessionId"`
		}
		require.NoError(t, json.Unmarshal(raw, &params))
		if method == methodViewUnsubscribe {
			unsubscribed = append(unsubscribed, params.SessionID)
		}
		if method == methodChildSend || method == methodChildFollowup || method == methodChildInterrupt {
			childWrites++
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	}})
	_, err := a.ClearContext()
	require.NoError(t, err)
	for depth := 1; depth <= 3; depth++ {
		id, key := fmt.Sprintf("child-%d", depth), fmt.Sprintf("key-%d", depth)
		state, childSink := a.sessions[id], sink.Child(id)
		assert.True(t, state.retired, id)
		assert.Empty(t, state.turnID, id)
		assert.Empty(t, state.controls, id)
		assert.Empty(t, state.compactionLive, id)
		assert.Contains(t, unsubscribed, id)
		assert.Contains(t, childControls[id].canceled, "control-"+id)
		assert.Equal(t, agent.TurnState{}, a.ActiveChildTurnState(key))
		require.ErrorIs(t, a.SendChildInput(key, "late input", nil), agent.ErrChildRouteNotReady)
		require.ErrorIs(t, a.SteerChildInput(key, "late input", nil), agent.ErrChildRouteNotReady)
		require.ErrorIs(t, a.InterruptChild(key, agent.StopContext{}), agent.ErrChildRouteNotReady)
		beforeTurns, beforeProgress, beforeInfo := len(childSink.TurnActiveCalls), len(childSink.ProgressUpdates()), childSink.SessionInfoCount()
		feed(t, a, methodTurnStarted, map[string]any{"sessionId": id, "turnId": "late-turn"})
		feed(t, a, contracts.MuseMethodContextUsage, map[string]any{"sessionId": id, "usedTokens": 12})
		feed(t, a, contracts.MuseMethodItemStarted, itemParams{SessionID: id, Item: nativeItem{ID: "late-text", Kind: contracts.MuseItemKindAgentMessage, Revision: 1, Status: contracts.MuseItemStatusInProgress}})
		feed(t, a, contracts.MuseMethodItemDelta, map[string]any{"sessionId": id, "itemId": "late-text", "delta": "Retained native text.", "viewCursor": "late-delta"})
		retained := feed(t, a, contracts.MuseMethodItemCompleted, itemParams{SessionID: id, Item: nativeItem{ID: "late-text", Kind: contracts.MuseItemKindAgentMessage, Revision: 2, Status: contracts.MuseItemStatusCompleted, Text: "Retained native text."}})
		assert.Len(t, childSink.TurnActiveCalls, beforeTurns)
		assert.Len(t, childSink.ProgressUpdates(), beforeProgress)
		assert.Equal(t, beforeInfo, childSink.SessionInfoCount())
		assert.Empty(t, state.turnID)
		require.NotEmpty(t, sink.Messages())
		assert.Equal(t, retained, sink.Messages()[len(sink.Messages())-1].Content)
		assert.Equal(t, id, sink.Messages()[len(sink.Messages())-1].AgentSessionID)
	}
	assert.Zero(t, childWrites)
	assert.Contains(t, unsubscribed, "session")
	assert.False(t, a.sessions["replacement"].retired)
}

func TestMuseClearContextRefusesABusyNativeSessionBeforeWriting(t *testing.T) {
	a, sink := testAgent(t)
	a.sessions["session"].turnID = "busy-turn"
	writes := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(string, json.RawMessage) agenttest.RPCReply {
		writes++
		return agenttest.RPCReply{}
	}})
	id, err := a.ClearContext()
	require.ErrorIs(t, err, agent.ErrAgentBusy)
	assert.Empty(t, id)
	assert.Zero(t, writes)
	assert.Equal(t, "session", a.sessionID)
	assert.Zero(t, sink.ResetSpanCount())
	assert.Empty(t, sink.SessionIDs())
	assert.Zero(t, sink.GoalClears())
}

func TestMuseClearContextPreservesTheOldSessionWhenNativeStartFails(t *testing.T) {
	a, sink := testAgent(t)
	old := a.sessions["session"]
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodSessionStart, method)
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"The native start failed"}`)}
	}})
	id, err := a.ClearContext()
	require.ErrorContains(t, err, "The native start failed")
	assert.Empty(t, id)
	assert.Equal(t, "session", a.sessionID)
	assert.Same(t, old, a.sessions["session"])
	assert.False(t, old.retired)
	assert.Zero(t, sink.ResetSpanCount())
	assert.Empty(t, sink.SessionIDs())
	assert.Zero(t, sink.GoalClears())
}

func TestMuseClearContextRetiresTheOldSessionAndBothNativeSubscriptions(t *testing.T) {
	a, sink := testAgent(t)
	old := a.sessions["session"]
	old.log.subscriptionID = 7
	var methods []string
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		methods = append(methods, method)
		if method == methodSessionStart {
			return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"replacement","modelId":null,"activeTurnId":null},"viewCursor":"replacement-cursor"}`)}
		}
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		if method == methodViewUnsubscribe {
			assert.Equal(t, "session", params["sessionId"])
		}
		if method == methodLogUnsubscribe {
			assert.Equal(t, float64(7), params["subscriptionId"])
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	}})
	id, err := a.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, "replacement", id)
	assert.Equal(t, []string{"replacement"}, sink.SessionIDs())
	assert.Equal(t, 1, sink.ResetSpanCount())
	assert.True(t, old.retired)
	assert.Contains(t, methods, methodViewUnsubscribe)
	assert.Contains(t, methods, methodLogUnsubscribe)
	feed(t, a, methodTurnStarted, map[string]any{"sessionId": "session", "turnId": "late-old-turn"})
	assert.Empty(t, old.turnID)
	assert.Empty(t, sink.TurnActiveCalls)
}

func TestMuseClearContextKeepsTheCommittedReplacementAfterCleanupRefusal(t *testing.T) {
	a, sink := testAgent(t)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		if method == methodSessionStart {
			return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"replacement","modelId":null,"activeTurnId":null},"viewCursor":"replacement-cursor"}`)}
		}
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"The old native subscription is unavailable"}`)}
	}})
	id, err := a.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, "replacement", id)
	assert.Equal(t, "replacement", a.sessionID)
	assert.Equal(t, []string{"replacement"}, sink.SessionIDs())
	assert.True(t, a.sessions["session"].retired)
}

func TestMuseResumeUsesTheExactNativeSessionAndRejectsInvalidHandlesBeforeWriting(t *testing.T) {
	a, _ := testAgent(t)
	const id = "01a11719-71f4-76d1-b7d0-2dd11166e831"
	calls := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		calls++
		require.Equal(t, methodSessionResume, method)
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		assert.Equal(t, id, params["sessionId"])
		assert.Equal(t, false, params["excludeItems"])
		assert.Equal(t, "inline", params["history"])
		assert.NotContains(t, params, "workspaceRoot")
		assert.NotEmpty(t, params["commandId"])
		return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"` + id + `","modelId":null,"activeTurnId":null},"viewCursor":"native-cursor","history":{"mode":"inline","items":[],"snapshot":null}}`)}
	}})
	require.Error(t, a.openSession("../invalid", time.Second))
	assert.Zero(t, calls)
	require.NoError(t, a.openSession(id, time.Second))
	assert.Equal(t, 1, calls)
	assert.Equal(t, id, a.sessionID)
}

func TestMuseCompactionRequiresIdleStateAndPreservesNativeNoop(t *testing.T) {
	a, _ := testAgent(t)
	calls := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		calls++
		require.Equal(t, methodSessionCompact, method)
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		assert.Equal(t, "session", params["sessionId"])
		result, err := json.Marshal(map[string]any{"commandId": params["commandId"], "status": contracts.MuseCompactionOutcomeNoop})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: result}
	}})
	a.sessions["session"].turnID = "busy"
	require.ErrorIs(t, a.CompactContext(), agent.ErrAgentBusy)
	assert.Zero(t, calls)
	a.sessions["session"].turnID = ""
	require.NoError(t, a.CompactContext())
	assert.Equal(t, 1, calls)
}

func TestMuseClearContextClearsTheGoal(t *testing.T) {
	for _, replacement := range []string{"replacement", "session"} {
		t.Run(replacement, func(t *testing.T) {
			a, sink := goalTestAgent(t)
			sink.UpsertGoal(agent.GoalUpdate{Objective: "Finish the old session", Status: agent.GoalStatusActive})
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
				if method == methodSessionStart {
					return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"` + replacement + `","modelId":null,"activeTurnId":null}}`)}
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			}})
			id, err := a.ClearContext()
			require.NoError(t, err)
			assert.Equal(t, replacement, id)
			if replacement == "session" {
				assert.Zero(t, sink.GoalClears())
			} else {
				assert.Equal(t, 1, sink.GoalClears())
			}
		})
	}
}

func TestMuseClearContextEndsTheOldLiveCompaction(t *testing.T) {
	a, sink := testAgent(t)
	feed(t, a, contracts.MuseMethodItemStarted, itemParams{SessionID: "session", Item: nativeItem{ID: "compact", Kind: "compaction", Revision: 1, Status: "inProgress"}})
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		if method == methodSessionStart {
			return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"replacement","modelId":null,"activeTurnId":null}}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	}})
	_, err := a.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, map[string]interface{}{contracts.SessionInfoKeyCompaction: nil}, sink.LastSessionInfo())
	assert.Empty(t, a.sessions["session"].compactionLive)
}

func TestMuseResumeRestoresChildRoutesWithoutReplayingParentConversation(t *testing.T) {
	const items = `[{"itemId":"workflow","kind":"workflow","revision":3,"status":"completed","workflowRunId":"run","entryId":"Native code","children":[{"childId":"sub","attempt":1,"status":"terminal","terminal":"completed"}]},{"itemId":"spawn","kind":"subagent","revision":4,"status":"inProgress","controlStatus":"resultReady","subagentId":"sub","childSessionId":"native-child","workflowRunId":"run","objective":"Original child assignment","result":{"summary":"Done","evidenceRefs":[],"artifactRefs":[]}},{"itemId":"old-parent-answer","kind":"agentMessage","revision":1,"status":"completed","text":"Never replay this parent answer"}]`
	for _, mode := range []string{"inline", "snapshot", "anchoredSnapshot", "none"} {
		t.Run(mode, func(t *testing.T) {
			a, sink := testAgent(t)
			a.viewWork.CloseAndWait()
			history := `{"mode":"inline","items":` + items + `,"snapshot":null}`
			if mode == "snapshot" || mode == "anchoredSnapshot" {
				history = `{"mode":"` + mode + `","items":null,"snapshot":{"schemaVersion":1,"viewCursor":"head","state":{"items":` + items + `}}}`
			}
			if mode == "none" {
				history = `{"mode":"none","items":null,"snapshot":null,"noneReason":"overBudget"}`
			}
			sends := 0
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
				var params map[string]any
				require.NoError(t, json.Unmarshal(raw, &params))
				switch method {
				case methodSessionResume:
					assert.Equal(t, false, params["excludeItems"])
					assert.Equal(t, "inline", params["history"])
					return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"session","modelId":"native-model"},"viewCursor":"head","history":` + history + `}`)}
				case methodViewPage:
					var nativeItems []json.RawMessage
					require.NoError(t, json.Unmarshal([]byte(items), &nativeItems))
					var events []map[string]any
					for _, item := range nativeItems {
						events = append(events, map[string]any{"method": contracts.MuseMethodItemUpdated, "params": map[string]any{"sessionId": "session", "viewCursor": "head", "item": item}})
					}
					reply, err := json.Marshal(map[string]any{"events": events, "nextCursor": nil})
					require.NoError(t, err)
					return agenttest.RPCReply{Result: reply}
				case methodChildFollowup:
					sends++
					assert.Equal(t, "session", params["sessionId"])
					assert.Equal(t, "sub", params["subagentId"])
					assert.Equal(t, "A note after restart", params["body"])
					reply, err := json.Marshal(map[string]any{"commandId": params["commandId"], "status": "accepted"})
					require.NoError(t, err)
					return agenttest.RPCReply{Result: reply}
				default:
					t.Errorf("unexpected native method %s", method)
					return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
				}
			}})
			require.NoError(t, a.openSession("session", time.Second))
			child := a.sessions["native-child"]
			require.NotNil(t, child, "the actual native child route must survive owner-process replacement")
			assert.Equal(t, "session", child.parentSessionID)
			assert.Equal(t, "sub", child.subagentID)
			assert.Equal(t, "native-model", a.settings[agent.OptionIDModel])
			child.childViewPrefix = childViewPrefixReady // View bootstrap is covered independently.
			child.viewRecovery = nil
			require.NoError(t, a.SendChildInput(museChildRowKey("session", "sub"), "A note after restart", nil))
			assert.Equal(t, 1, sends)
			for _, message := range sink.Messages() {
				assert.NotContains(t, string(message.Content), "Never replay this parent answer")
			}
		})
	}
}

func TestMuseResumeRejectsAForeignNativeSessionBeforeChangingState(t *testing.T) {
	a, _ := testAgent(t)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodSessionResume, method)
		return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"foreign"},"viewCursor":"head","history":{"mode":"inline","items":[],"snapshot":null}}`)}
	}})
	require.Error(t, a.openSession("session", time.Second))
	assert.Equal(t, "session", a.sessionID)
	assert.NotContains(t, a.sessions, "foreign")
}

func TestMuseResumeKeepsNewerLiveChildMetadata(t *testing.T) {
	a, sink := testAgent(t)
	a.viewWork.CloseAndWait()
	live := nativeItem{ID: "spawn", Kind: museItemSubagent, Revision: 5, Status: "inProgress", ControlStatus: "running", SubagentID: "sub", ChildSessionID: "native-child", Objective: "The newer live assignment"}
	feed(t, a, contracts.MuseMethodItemUpdated, itemParams{SessionID: "session", Item: live})
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodSessionResume, method)
		return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"session"},"viewCursor":"head","history":{"mode":"inline","snapshot":null,"items":[{"itemId":"spawn","kind":"subagent","revision":4,"status":"completed","subagentId":"sub","childSessionId":"native-child","objective":"An older stored assignment"}]}}`)}
	}})
	require.NoError(t, a.openSession("session", time.Second))
	assert.Equal(t, int64(5), a.sessions["session"].items["spawn"].params.Item.Revision)
	row, ok := sink.BackgroundTask(museChildRowKey("session", "sub"))
	require.True(t, ok)
	assert.Equal(t, "The newer live assignment", row.Title)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
}

func TestMuseResumeReportsAnUnavailableChildHistoryPage(t *testing.T) {
	a, _ := testAgent(t)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		if method == methodSessionResume {
			return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"session"},"viewCursor":"head","history":{"mode":"none","items":null,"snapshot":null}}`)}
		}
		require.Equal(t, methodViewPage, method)
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"the native child history is unavailable"}`)}
	}})
	require.ErrorContains(t, a.openSession("session", time.Second), "the native child history is unavailable")
	assert.Len(t, a.sessions, 1)
}

type childCreationFailureSink struct {
	*agenttest.Sink
	failure error
}

func (s childCreationFailureSink) EnsureChildAgent(agent.ChildAgentSpec) (string, error) {
	return "", s.failure
}

func TestMuseResumeReportsAChildCreationFailure(t *testing.T) {
	a, sink := testAgent(t)
	failure := errors.New("the worker could not create the child")
	a.sink = agent.NewProviderServices(childCreationFailureSink{Sink: sink, failure: failure})
	a.sessions["session"].sink = a.sink
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodSessionResume, method)
		return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"session"},"viewCursor":"head","history":{"mode":"inline","snapshot":null,"items":[{"itemId":"spawn","kind":"subagent","revision":1,"status":"completed","subagentId":"sub","childSessionId":"native-child","objective":"A child"}]}}`)}
	}})
	require.ErrorIs(t, a.openSession("session", time.Second), failure)
	assert.NotContains(t, a.sessions, "native-child")
}

func TestChildRouteHistoryRequiresAnOwnedSupportedSnapshot(t *testing.T) {
	for _, source := range []string{
		`null`, `false`, `{}`, `{"mode":"inline","items":null}`, `{"mode":"inline","items":"invalid"}`,
		`{"mode":"snapshot","snapshot":null}`,
		`{"mode":"snapshot","snapshot":{"schemaVersion":0,"viewCursor":"head","state":{"items":[]}}}`,
		`{"mode":"snapshot","snapshot":{"schemaVersion":-1,"viewCursor":"head","state":{"items":[]}}}`,
		`{"mode":"snapshot","snapshot":{"schemaVersion":2,"viewCursor":"head","state":{"items":[]}}}`,
		`{"mode":"snapshot","snapshot":{"schemaVersion":1,"viewCursor":"foreign","state":{"items":[]}}}`,
		`{"mode":"snapshot","snapshot":{"schemaVersion":1,"viewCursor":"head","state":{"items":null}}}`,
	} {
		t.Run(source, func(t *testing.T) {
			items, served := childRouteHistoryItems(json.RawMessage(source), "head")
			assert.False(t, served)
			assert.Nil(t, items)
		})
	}
	items, served := childRouteHistoryItems(json.RawMessage(`{"mode":"inline","items":[],"snapshot":null}`), "head")
	assert.True(t, served)
	assert.Empty(t, items)
}

type childPromptOutcomeSink struct {
	*agenttest.Sink
	failure error
	calls   int
}

func (s *childPromptOutcomeSink) PersistChildPrompt(id, text string) error {
	s.calls++
	if s.failure != nil {
		return s.failure
	}
	return s.Sink.PersistChildPrompt(id, text)
}

func TestMuseResumeKeepsAQueuedPromptAndReportsAHardPromptFailure(t *testing.T) {
	failure := errors.New("the child prompt write failed")
	for _, retained := range []bool{false, true} {
		t.Run(map[bool]string{false: "unretained", true: "retained"}[retained], func(t *testing.T) {
			a, base := testAgent(t)
			outcome := &childPromptOutcomeSink{Sink: base, failure: failure}
			if retained {
				outcome.failure = &agent.PendingOutputError{Err: failure}
			}
			a.sink = agent.NewProviderServices(outcome)
			a.sessions["session"].sink = a.sink
			a.viewWork.CloseAndWait()
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
				require.Equal(t, methodSessionResume, method)
				return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"session"},"viewCursor":"head","history":{"mode":"inline","snapshot":null,"items":[{"itemId":"spawn","kind":"subagent","revision":1,"status":"completed","subagentId":"sub","childSessionId":"native-child","objective":"A retained child assignment"}]}}`)}
			}})
			err := a.openSession("session", time.Second)
			if retained {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, failure)
			}
			assert.Equal(t, 1, outcome.calls, "a retained write must never be submitted twice")
			require.NotNil(t, a.sessions["native-child"])
			assert.Equal(t, "sub", a.sessions["native-child"].subagentID)
		})
	}
}

func TestHardChildOpeningPromptFailureBlocksViewUntilOneExplicitRetry(t *testing.T) {
	a, base := testAgent(t)
	failure := errors.New("the opening prompt writer rejected the row")
	outcome := &childPromptOutcomeSink{Sink: base, failure: failure}
	a.sink = agent.NewProviderServices(outcome)
	a.sessions["session"].sink = a.sink
	a.viewWork.CloseAndWait()
	feed(t, a, contracts.MuseMethodItemUpdated, itemParams{SessionID: "session", Item: nativeItem{ID: "spawn", Kind: museItemSubagent, Revision: 1, Status: contracts.MuseItemStatusInProgress, SubagentID: "sub", ChildSessionID: "child", Objective: "Original assignment"}})
	state := a.sessions["child"]
	require.NotNil(t, state)
	assert.Equal(t, childViewPrefixFailed, state.childViewPrefix)
	assert.False(t, state.childViewRequested, "unretained opening input cannot admit a child history read")
	assert.Empty(t, base.Child(state.childID).Messages())
	assert.Len(t, base.LeapMuxNotifications(), 1)
	feed(t, a, contracts.MuseMethodItemUpdated, itemParams{SessionID: "session", Item: nativeItem{ID: "spawn", Kind: museItemSubagent, Revision: 2, Status: contracts.MuseItemStatusInProgress, SubagentID: "sub", ChildSessionID: "child", Objective: "Changed assignment"}})
	assert.Equal(t, 1, outcome.calls, "later metadata cannot replace or retry the opening assignment")
	methods := []string{}
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		methods = append(methods, method)
		switch method {
		case methodChildInterrupt:
			var params map[string]any
			require.NoError(t, json.Unmarshal(raw, &params))
			reply, err := json.Marshal(map[string]any{"commandId": params["commandId"], "status": "accepted"})
			require.NoError(t, err)
			return agenttest.RPCReply{Result: reply}
		case methodSessionRead:
			assert.Equal(t, 1, len(base.Child(state.childID).Messages()), "the original assignment must be retained before the native view is read")
			return agenttest.RPCReply{Result: json.RawMessage(`{"session":{"sessionId":"child","status":"idle","activeTurnId":null},"viewCursor":"","history":{"mode":"inline","items":[]}}`)}
		case methodViewPage:
			var params map[string]any
			require.NoError(t, json.Unmarshal(raw, &params))
			assert.Equal(t, "child", params["sessionId"])
			assert.NotContains(t, params, "cursor", "the child bootstrap reads the origin")
			rows := base.Child(state.childID).Messages()
			require.Len(t, rows, 1, "the opening assignment precedes the native origin read")
			assert.JSONEq(t, `{"content":"Original assignment"}`, string(rows[0].Content))
			return agenttest.RPCReply{Result: json.RawMessage(`{"events":[],"nextCursor":null}`)}
		case methodViewSubscribe:
			return agenttest.RPCReply{Result: json.RawMessage(`{"viewCursor":""}`)}
		default:
			t.Errorf("unexpected opening-prompt method %s", method)
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
	}})
	require.NoError(t, a.InterruptChild(state.childKey, agent.StopContext{}))
	require.ErrorIs(t, a.SendChildInput(state.childKey, "later", nil), agent.ErrChildRouteNotReady)
	assert.Equal(t, []string{methodChildInterrupt}, methods)
	assert.Equal(t, 1, outcome.calls)
	outcome.failure = nil
	a.requestChildView("child")
	a.syncView("child")
	assert.Equal(t, 2, outcome.calls)
	assert.Equal(t, childViewPrefixReady, state.childViewPrefix)
	assert.Equal(t, []string{methodChildInterrupt, methodSessionRead, methodViewPage, methodViewSubscribe}, methods)
	rows := base.Child(state.childID).Messages()
	require.Len(t, rows, 1)
	assert.JSONEq(t, `{"content":"Original assignment"}`, string(rows[0].Content))
}

// compactionVerdictOps is the retry operation that the end of one manual
// compaction applies: CompactionEnd's cancellation of the server-error retry.
func compactionVerdictOps() []agenttest.TimelineEntry {
	reference := &agenttest.Sink{}
	providerkit.ApplyTurnFailure(reference, providerkit.CompactionEnd(), nil)
	return reference.Timeline()
}

// museCompactPeer answers session/compact with status. It runs during before
// the answer, as a host that emits frames before its acknowledgement does.
func museCompactPeer(t *testing.T, a *Agent, status string, during func()) *museTestPeer {
	t.Helper()
	return &museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodSessionCompact, method)
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		if during != nil {
			during()
		}
		result, err := json.Marshal(map[string]any{"commandId": params["commandId"], "status": status})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: result}
	}}
}

// feedCompactionItem feeds the start of the root compaction item, and then its
// end with outcome when outcome is set.
func feedCompactionItem(t *testing.T, a *Agent, outcome string) {
	t.Helper()
	feed(t, a, contracts.MuseMethodItemStarted, itemParams{SessionID: "session", Item: nativeItem{ID: "compact", Kind: contracts.MuseItemKindCompaction, Revision: 1, Status: contracts.MuseItemStatusInProgress, Trigger: "manual"}})
	if outcome != "" {
		feed(t, a, contracts.MuseMethodItemCompleted, itemParams{SessionID: "session", Item: nativeItem{ID: "compact", Kind: contracts.MuseItemKindCompaction, Revision: 2, Status: contracts.MuseItemStatusCompleted, Outcome: outcome}})
	}
}

// A manual compaction applies CompactionEnd once when it ends, whatever its
// outcome: the end of its item, or the noop acknowledgement that emits no item.
// A replayed end applies no second verdict.
func TestMuseManualCompactionEndAppliesTheCompactionVerdictOnce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status string
		// during runs before the acknowledgement, after runs after it.
		during, after func(*testing.T, *Agent)
	}{
		{name: "a compacted compaction", status: "accepted", after: func(t *testing.T, a *Agent) {
			feedCompactionItem(t, a, contracts.MuseCompactionOutcomeCompacted)
			feedCompactionItem(t, a, contracts.MuseCompactionOutcomeCompacted)
		}},
		{name: "a failed compaction", status: "accepted", after: func(t *testing.T, a *Agent) {
			feedCompactionItem(t, a, contracts.MuseCompactionOutcomeFailed)
		}},
		{name: "a cancelled compaction", status: "accepted", after: func(t *testing.T, a *Agent) {
			feedCompactionItem(t, a, contracts.MuseCompactionOutcomeCancelled)
		}},
		{name: "a noop acknowledgement", status: contracts.MuseCompactionOutcomeNoop},
		{name: "an item that ends before the acknowledgement", status: "accepted", during: func(t *testing.T, a *Agent) {
			feedCompactionItem(t, a, contracts.MuseCompactionOutcomeCompacted)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := testAgent(t)
			var during func()
			if tc.during != nil {
				during = func() { tc.during(t, a) }
			}
			a.SetStdinForTest(museCompactPeer(t, a, tc.status, during))
			require.NoError(t, a.CompactContext())
			if tc.after != nil {
				tc.after(t, a)
			}
			assert.Equal(t, compactionVerdictOps(), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			active, published := sink.LastTurnActive()
			require.True(t, published)
			assert.False(t, active)
		})
	}
}

// An automatic compaction ends no turn of its own, so it applies no verdict.
func TestMuseAutomaticCompactionEndAppliesNoVerdict(t *testing.T) {
	t.Parallel()
	a, sink := testAgent(t)
	feedCompactionItem(t, a, contracts.MuseCompactionOutcomeFailed)
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// A compaction that Muse does not accept leaves no mark, so a later automatic
// compaction applies no verdict.
func TestMuseRefusedCompactionLeavesNoMark(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		reply agenttest.RPCReply
		want  error
	}{
		{name: "an unavailable compaction", reply: agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"unavailable","data":{"code":"compaction_unavailable"}}`)}, want: agent.ErrCompactionUnsupported},
		{name: "a rejected request", reply: agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"rejected"}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := testAgent(t)
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(string, json.RawMessage) agenttest.RPCReply { return tc.reply }})
			err := a.CompactContext()
			require.Error(t, err)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			}
			feedCompactionItem(t, a, contracts.MuseCompactionOutcomeCompacted)
			assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

// The host's retirement ends a manual compaction that is still running, with
// the compaction's verdict, once.
func TestMuseRetirementEndsAPendingManualCompactionOnce(t *testing.T) {
	t.Parallel()
	a, sink := testAgent(t)
	a.SetStdinForTest(museCompactPeer(t, a, "accepted", nil))
	require.NoError(t, a.CompactContext())
	feedCompactionItem(t, a, "")
	a.retireHost(agent.MessageCompletionError)
	a.retireHost(agent.MessageCompletionError)
	assert.Equal(t, compactionVerdictOps(), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// The compaction's verdict belongs to the compaction alone. The next turn
// takes the classifier's verdict again.
func TestMuseTurnAfterManualCompactionTakesItsOwnVerdict(t *testing.T) {
	t.Parallel()
	a, sink := testAgent(t)
	a.SetStdinForTest(museCompactPeer(t, a, "accepted", nil))
	require.NoError(t, a.CompactContext())
	feedCompactionItem(t, a, contracts.MuseCompactionOutcomeFailed)
	a.startTurn("session", "turn")
	a.HandleOutput([]byte(museCompletionFrame(t, museFailedParams(map[string]any{
		"kind": "modelError", "message": "API error 500: Internal Server Error (server_error) (after 10 provider attempts)", "retryable": true,
	}), 1793150211651)))
	want := append(compactionVerdictOps(), providerkittest.TurnEndThen(museTransient("modelError"))...)
	assert.Equal(t, want, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}
