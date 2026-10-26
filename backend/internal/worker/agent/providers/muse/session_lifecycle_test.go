package muse

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	const items = `[{"itemId":"workflow","kind":"workflow","revision":3,"status":"completed","workflowRunId":"run","entryId":"Native code","children":[{"childId":"sub","status":"terminal","terminal":"completed"}]},{"itemId":"spawn","kind":"subagent","revision":4,"status":"inProgress","controlStatus":"resultReady","subagentId":"sub","childSessionId":"native-child","workflowRunId":"run","objective":"Original child assignment","result":{"summary":"Done","evidenceRefs":[],"artifactRefs":[]}},{"itemId":"old-parent-answer","kind":"agentMessage","revision":1,"status":"completed","text":"Never replay this parent answer"}]`
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

func (s *childPromptOutcomeSink) PersistChildPrompt(string, string) error {
	s.calls++
	return s.failure
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
