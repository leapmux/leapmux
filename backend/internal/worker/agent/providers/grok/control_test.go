package grok

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func TestGrokControlRequestsKeepNumericAndStringIdentitiesSeparate(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	// The shared frames state a session of their own. An agent that has no
	// session yet reads every session as its own, as the bare agents of the
	// other providers' identity tests do, so each dialog reaches the reader.
	a.SetSessionIDForTest("")
	agenttest.AssertControlIdentitiesStaySeparate(t, sink, a.HandleOutput, contracts.GrokMethodAskUserQuestion)
	agenttest.AssertPublishedControlKeys(t, grokProvider{}, sink)
}

func TestGrokPublishesEachOfItsOwnDialogs(t *testing.T) {
	t.Parallel()
	for _, method := range []string{
		contracts.GrokMethodAskUserQuestion,
		contracts.GrokMethodExitPlanMode,
		contracts.GrokMethodMcpElicit,
		contracts.GrokMethodFolderTrust,
	} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
			line := frame(t, map[string]any{
				"id": 3, "method": method,
				"params": map[string]any{"sessionId": grokTestSession, "toolCallId": "call_1"},
			})

			a.HandleOutput(line)

			require.Equal(t, 1, sink.PublishedControlCount())
			assert.JSONEq(t, string(line), string(sink.LastPublishedControl().Payload))
		})
	}
}

func TestGrokPlanApprovalStoresThePlan(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)

	a.HandleOutput(frame(t, map[string]any{
		"id": 4, "method": contracts.GrokMethodExitPlanMode,
		"params": map[string]any{"sessionId": grokTestSession, "toolCallId": "call_6_0", "planContent": "# Ship the release\n\n1. Tag it."},
	}))

	require.Equal(t, 1, sink.PlanUpdateCount())
	update := sink.LastPlanUpdate()
	assert.Equal(t, "Ship the release", update.Title)
	plan, err := msgcodec.Decompress(update.Content, update.Compression)
	require.NoError(t, err)
	assert.Equal(t, "# Ship the release\n\n1. Tag it.", string(plan), "the whole plan is stored, not only its title")
	require.Equal(t, 1, sink.PublishedControlCount(), "the plan approval still reaches the reader")
}

// A plan that is empty, or that holds only whitespace, states no plan. Stored,
// it would replace the plan of an earlier approval with a blank one, and a
// clear-context approval would hand the new session nothing to implement.
func TestGrokPlanApprovalWithAnEmptyPlanStoresNothing(t *testing.T) {
	t.Parallel()
	for _, plan := range []any{nil, "", " \n\t "} {
		a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
		a.HandleOutput(frame(t, map[string]any{
			"id": 4, "method": contracts.GrokMethodExitPlanMode,
			"params": map[string]any{"sessionId": grokTestSession, "toolCallId": "call_6_0", "planContent": plan},
		}))
		assert.Zero(t, sink.PlanUpdateCount(), "plan %v", plan)
		assert.Equal(t, 1, sink.PublishedControlCount())
	}
}

func TestGrokResolvedInteractionRetiresAnOpenCard(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(frame(t, map[string]any{
		"id": 9, "method": contracts.GrokMethodAskUserQuestion,
		"params": map[string]any{"sessionId": grokTestSession, "toolCallId": "call_2_0", "questions": []any{}},
	}))
	requestID := sink.LastPublishedControl().RequestID

	// A cancelled turn drops the question, and Grok states only that the
	// interaction of the tool call resolved.
	a.HandleOutput(notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "interaction_resolved", "tool_call_id": "call_2_0",
	}))

	assert.Equal(t, []string{requestID}, sink.CanceledControls())
}

func TestGrokInterruptCancelsBeforeDismissingAQuestion(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)
	original := a.StdinForTest()
	stdin := &agenttest.Stdin{}
	a.SetStdinForTest(stdin)
	defer a.SetStdinForTest(original)
	a.HandleOutput(frame(t, map[string]any{
		"id": 77, "method": contracts.GrokMethodAskUserQuestion,
		"params": map[string]any{"sessionId": grokTestSession, "toolCallId": "waiting-question", "questions": []any{}},
	}))
	require.Equal(t, 1, sink.PublishedControlCount())
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.NoError(t, a.SendNotification("test/barrier", nil))

	cancelAt, replyAt := -1, -1
	for index, raw := range strings.Split(strings.TrimSpace(stdin.String()), "\n") {
		var message struct {
			Method string `json:"method"`
			ID     int    `json:"id"`
		}
		require.NoError(t, json.Unmarshal([]byte(raw), &message))
		if message.Method == acp.MethodSessionCancel {
			cancelAt = index
		}
		if message.Method == "" && message.ID == 77 {
			replyAt = index
		}
	}
	require.NotEqual(t, -1, cancelAt)
	require.NotEqual(t, -1, replyAt)
	assert.Less(t, cancelAt, replyAt, "the dismissed question must not start another model request")
}

func TestGrokResolvedInteractionLeavesAnAnsweredCardAlone(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(frame(t, map[string]any{
		"id": 9, "method": contracts.GrokMethodAskUserQuestion,
		"params": map[string]any{"sessionId": grokTestSession, "toolCallId": "call_2_0", "questions": []any{}},
	}))
	requestID := sink.LastPublishedControl().RequestID
	// The reader answers. The answer retires the record of the request.
	answer, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 9, "result": map[string]any{"outcome": "accepted", "answers": map[string]any{}}})
	require.NoError(t, err)
	require.NoError(t, a.SendRawInput(sink.Answer(requestID, answer)))
	syncPeer(t, a)

	// Grok echoes the resolution for the reader's own answer too.
	a.HandleOutput(notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "interaction_resolved", "tool_call_id": "call_2_0",
	}))

	assert.NotContains(t, sink.CanceledControls(), requestID, "an answered card never reads as cancelled")
}

func TestGrokResolvedInteractionRetiresAToolPermission(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	// The base publishes the standard permission request, and the observer hook
	// records its tool call for Grok.
	a.HandleOutput(frame(t, map[string]any{
		"id": 11, "method": "session/request_permission",
		"params": map[string]any{
			"sessionId": grokTestSession,
			"toolCall":  map[string]any{"toolCallId": "call_3_0", "title": "Run Command"},
			"options":   []any{map[string]any{"optionId": "allow-once", "name": "Yes", "kind": "allow_once"}},
		},
	}))
	require.Equal(t, 1, sink.PublishedControlCount())
	requestID := sink.LastPublishedControl().RequestID

	a.HandleOutput(notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "interaction_resolved", "tool_call_id": "call_3_0",
	}))

	assert.Equal(t, []string{requestID}, sink.CanceledControls())
}

func TestGrokForwardedPermissionUsesTheChildNotice(t *testing.T) {
	t.Parallel()
	for _, ambiguous := range []bool{false, true} {
		t.Run(fmt.Sprintf("ambiguous=%t", ambiguous), func(t *testing.T) {
			t.Parallel()
			a, parent, requests := newGrokAgent(t, agent.Options{}, nil)
			const childSession = "child-session"
			const toolID = "tool-call"
			child := recordGrokChildPermissions(t, a, childSession)
			pending := map[string]any{"sessionUpdate": "pending_interaction", "tool_call_id": toolID, "kind": "permission"}
			a.HandleOutput(notification(t, childSession, pending))
			// A root turn can finish while the background child still waits.
			a.clearPermissionChecks(grokTestSession)
			if ambiguous {
				a.HandleOutput(notification(t, grokTestSession, pending))
			}
			request := frame(t, map[string]any{
				"id": 11, "method": "session/request_permission",
				"params": map[string]any{"sessionId": grokTestSession, "toolCall": map[string]any{"toolCallId": toolID}},
			})
			a.HandleOutput(request)
			assert.Empty(t, parent.PublishedControls())
			if ambiguous {
				assert.Empty(t, child.PublishedControls())
				syncPeer(t, a)
				results := agenttest.JSONRPCResultsByID(t, rawLines(requests()))
				assert.JSONEq(t, `{"outcome":{"outcome":"cancelled"}}`, results["11"])
				return
			}
			require.Equal(t, 1, child.PublishedControlCount())
			published := child.LastPublishedControl()
			assert.Equal(t, childSession, published.AgentSessionID)
			assert.JSONEq(t, string(request), string(published.Payload))
			a.HandleOutput(notification(t, childSession, map[string]any{"sessionUpdate": "interaction_resolved", "tool_call_id": toolID}))
			assert.Equal(t, []string{published.RequestID}, child.CanceledControls())
			assert.Empty(t, parent.CanceledControls())
		})
	}
}

func recordGrokChildPermissions(t *testing.T, a *Agent, childSession string) *agenttest.ControlSink {
	t.Helper()
	const rowKey = "child-row"
	a.ApplySubagentObservation(&acp.SubagentObservation{
		RowKey: rowKey, ChildAgentKey: rowKey, ChildAgentSessionID: childSession,
		Title: "Native child", Status: bgtask.StatusRunning,
	})
	require.True(t, a.AttachChildSession(acp.ChildSessionTarget{RootSessionID: grokTestSession, SessionID: childSession, RowKey: rowKey}))
	child := &agenttest.ControlSink{}
	baseOwner := a.HooksForTest().ControlOwner
	a.HooksForTest().ControlOwner = func(sessionID string, line *providerkit.ParsedLine, needsTurn bool) (acp.ControlOwnerResolution, error) {
		res, err := baseOwner(sessionID, line, needsTurn)
		if err != nil {
			return res, err
		}
		resolved := res.SessionID
		if resolved == "" {
			resolved = sessionID
		}
		if resolved != "" && !a.IsCurrentSession(resolved) {
			assert.False(t, res.Refuse)
			assert.NotNil(t, res.Sink)
			assert.Equal(t, childSession, resolved)
			res.Sink = child
		}
		return res, nil
	}
	return child
}

func TestGrokResolvedInteractionOfAnUnknownCallChangesNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	for _, update := range []map[string]any{
		{"sessionUpdate": "interaction_resolved", "tool_call_id": "never-raised"},
		{"sessionUpdate": "interaction_resolved"},
		{"sessionUpdate": "interaction_resolved", "tool_call_id": 7},
	} {
		a.HandleOutput(notification(t, grokTestSession, update))
	}
	assert.Empty(t, sink.CanceledControls())
}

func TestGrokControlCancelAnswers(t *testing.T) {
	t.Parallel()
	assert.Equal(t, map[string]any{"outcome": "cancelled"}, grokControlCancelAnswer(contracts.GrokMethodAskUserQuestion))
	assert.Equal(t, map[string]any{"outcome": "cancelled"}, grokControlCancelAnswer(contracts.GrokMethodExitPlanMode))
	assert.Equal(t, map[string]any{"outcome": "cancel"}, grokControlCancelAnswer(contracts.GrokMethodMcpElicit))
	assert.Nil(t, grokControlCancelAnswer(contracts.GrokMethodFolderTrust), "a cancel would record a rejection that Grok keeps for the process")
	assert.Nil(t, grokControlCancelAnswer("_x.ai/unknown"))
}

// A stop answers each dialog that the turn owns, and leaves the folder-trust
// question open. No turn owns that question, and Grok keeps waiting for its
// answer for the life of the process, so a stop that retired the card left
// the repository untrusted until the agent restarted.
func TestGrokInterruptAnswersEachDialogOfTheTurn(t *testing.T) {
	t.Parallel()
	a, sink, requests := newGrokAgent(t, agent.Options{}, nil)
	for id, method := range map[int]string{
		21: contracts.GrokMethodAskUserQuestion,
		22: contracts.GrokMethodExitPlanMode,
		23: contracts.GrokMethodMcpElicit,
		24: contracts.GrokMethodFolderTrust,
	} {
		a.HandleOutput(frame(t, map[string]any{
			"id": id, "method": method, "params": map[string]any{"sessionId": grokTestSession, "toolCallId": method},
		}))
	}
	require.Equal(t, 4, sink.PublishedControlCount())

	a.SetPromptActiveForTest(true)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	syncPeer(t, a)

	answers := agenttest.JSONRPCResultsByID(t, rawLines(requests()))
	assert.JSONEq(t, `{"outcome":"cancelled"}`, answers["21"])
	assert.JSONEq(t, `{"outcome":"cancelled"}`, answers["22"])
	assert.JSONEq(t, `{"outcome":"cancel"}`, answers["23"])
	assert.NotContains(t, answers, "24", "a stop decides nothing about folder trust")
	assert.ElementsMatch(t, []string{"jsonrpc:21", "jsonrpc:22", "jsonrpc:23"}, sink.CanceledControls(), "the folder-trust card stays")
	assert.True(t, a.OutstandingControlForTest("jsonrpc:24"), "the reader can still answer the folder-trust question")
}

// answerCount returns how many responses the agent wrote for request id.
func answerCount(t *testing.T, requests []agenttest.RecordedRequest, id int) int {
	t.Helper()
	count := 0
	for _, request := range requests {
		if request.Method != "" {
			continue
		}
		var response struct {
			ID json.RawMessage `json:"id"`
		}
		require.NoError(t, json.Unmarshal([]byte(request.Raw), &response))
		if string(response.ID) == fmt.Sprint(id) {
			count++
		}
	}
	return count
}

// A context clear with an open plan approval releases the approval of the
// outgoing session. Grok blocks the turn on the answer and reads `cancelled`
// as "keep planning", so the clear answers it, cancels that turn and closes
// the session, which ends its subagents and background commands too. Without
// that, a later stop of the NEW session answered the old approval, and the old
// turn went on where nobody could see it.
func TestGrokClearContextReleasesThePlanApprovalOfTheOutgoingSession(t *testing.T) {
	t.Parallel()
	a, sink, requests := newGrokAgent(t, agent.Options{}, openingSession("session-2"))
	a.SetClosesSessionsForTest(true)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(frame(t, map[string]any{
		"id": 30, "method": contracts.GrokMethodExitPlanMode,
		"params": map[string]any{"sessionId": grokTestSession, "toolCallId": "call_plan", "planContent": "# Plan"},
	}))
	planCard := sink.LastPublishedControl().RequestID

	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	require.Equal(t, "session-2", sessionID)
	syncPeer(t, a)

	answers := agenttest.JSONRPCResultsByID(t, rawLines(requests()))
	assert.JSONEq(t, `{"outcome":"cancelled"}`, answers["30"])
	assert.Equal(t, []string{planCard}, sink.CanceledControls())
	assert.Equal(t, []string{grokTestSession}, sessionIDs(requestsFor(requests(), acp.MethodSessionCancel)))
	assert.Equal(t, []string{grokTestSession}, sessionIDs(requestsFor(requests(), acp.MethodSessionClose)))

	a.SetPromptActiveForTest(true)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	syncPeer(t, a)
	assert.Equal(t, []string{grokTestSession, "session-2"}, sessionIDs(requestsFor(requests(), acp.MethodSessionCancel)))
	assert.Equal(t, 1, answerCount(t, requests(), 30), "the later stop answers nothing of the old session")
}

// A dialog that the retired session raises after the clear never reaches the
// reader, and its plan does not replace the plan of the new session.
func TestGrokRefusesADialogOfARetiredSession(t *testing.T) {
	t.Parallel()
	a, sink, requests := newGrokAgent(t, agent.Options{}, openingSession("session-2"))
	_, err := a.ClearContext()
	require.NoError(t, err)

	a.HandleOutput(frame(t, map[string]any{
		"id": 31, "method": contracts.GrokMethodAskUserQuestion,
		"params": map[string]any{"sessionId": grokTestSession, "toolCallId": "call_q", "questions": []any{}},
	}))
	a.HandleOutput(frame(t, map[string]any{
		"id": 32, "method": contracts.GrokMethodExitPlanMode,
		"params": map[string]any{"sessionId": grokTestSession, "toolCallId": "call_plan", "planContent": "# Old plan"},
	}))
	syncPeer(t, a)

	assert.Zero(t, sink.PublishedControlCount())
	assert.Zero(t, sink.PlanUpdateCount())
	answers := agenttest.JSONRPCResultsByID(t, rawLines(requests()))
	assert.JSONEq(t, `{"outcome":"cancelled"}`, answers["31"])
	assert.JSONEq(t, `{"outcome":"cancelled"}`, answers["32"])
}

// Grok states each resolution under the session that raised the interaction,
// and a subagent session can resolve a tool call whose id equals the id of an
// open request of the parent: a model backend reuses ids across conversations.
// That resolution leaves the parent's card and record alone, so a later stop
// still releases the parent turn.
func TestGrokResolutionInAnotherSessionLeavesTheCardOpen(t *testing.T) {
	t.Parallel()
	a, sink, requests := newGrokAgent(t, agent.Options{}, nil)
	const childSession = "child-session"
	child := recordGrokChildPermissions(t, a, childSession)
	permission := func(id int, sessionID string) []byte {
		return frame(t, map[string]any{
			"id": id, "method": "session/request_permission",
			"params": map[string]any{
				"sessionId": sessionID,
				"toolCall":  map[string]any{"toolCallId": "call_0", "title": "Run Command"},
				"options":   []any{map[string]any{"optionId": "allow-once", "name": "Yes", "kind": "allow_once"}},
			},
		})
	}
	a.HandleOutput(permission(11, grokTestSession))

	a.HandleOutput(notification(t, childSession, map[string]any{"sessionUpdate": "interaction_resolved", "tool_call_id": "call_0"}))
	assert.Empty(t, sink.CanceledControls(), "the child's resolution is not the parent's")

	// The child's own request with the same tool-call id resolves in the child.
	a.HandleOutput(permission(12, childSession))
	require.Equal(t, 1, child.PublishedControlCount())
	a.HandleOutput(notification(t, childSession, map[string]any{"sessionUpdate": "interaction_resolved", "tool_call_id": "call_0"}))
	assert.Equal(t, []string{"jsonrpc:12"}, child.CanceledControls())
	assert.Empty(t, sink.CanceledControls())

	a.SetPromptActiveForTest(true)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	syncPeer(t, a)
	answers := agenttest.JSONRPCResultsByID(t, rawLines(requests()))
	assert.JSONEq(t, `{"outcome":{"outcome":"cancelled"}}`, answers["11"], "the stop still releases the parent turn")
}

// rawLines joins the frames that the agent wrote, one per line.
func rawLines(requests []agenttest.RecordedRequest) string {
	var out string
	for _, request := range requests {
		out += request.Raw + "\n"
	}
	return out
}

func TestGrokControlIndexRemembersOnlyAKeyedRequest(t *testing.T) {
	t.Parallel()
	var index controlIndex
	_, ok := index.take(controlKey{sessionID: grokTestSession, toolCallID: "call_1"})
	assert.False(t, ok, "an empty index holds nothing")

	index.remember(controlKey{sessionID: grokTestSession}, "jsonrpc:1")
	index.remember(controlKey{sessionID: grokTestSession, toolCallID: "call_1"}, "")
	assert.Empty(t, index.byToolCall, "a request with no tool call, or no id, is not recorded")

	key := controlKey{sessionID: grokTestSession, toolCallID: "call_1"}
	index.remember(key, "jsonrpc:1")
	index.remember(key, "jsonrpc:2")
	requestID, ok := index.take(key)
	require.True(t, ok)
	assert.Equal(t, "jsonrpc:2", requestID, "a later request of the same tool call replaces the earlier one")
	_, ok = index.take(key)
	assert.False(t, ok, "a take forgets the request")
}

// A dialog that states no tool call still reaches the reader, and no
// resolution can retire it by accident.
func TestGrokDialogWithNoToolCallIsPublishedAndNotIndexed(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(frame(t, map[string]any{
		"id": 9, "method": contracts.GrokMethodAskUserQuestion,
		"params": map[string]any{"sessionId": grokTestSession, "questions": []any{}},
	}))
	require.Equal(t, 1, sink.PublishedControlCount())

	a.HandleOutput(notification(t, grokTestSession, map[string]any{"sessionUpdate": "interaction_resolved", "tool_call_id": ""}))

	assert.Empty(t, sink.CanceledControls())
	a.stateMu.Lock()
	assert.Empty(t, a.controls.byToolCall)
	a.stateMu.Unlock()
}

// Auto mode holds a tool call in Grok's own classifier before it raises the
// real permission request. The pending_interaction notice is what tells the
// reader the call still runs: frames 30, 34 and 36 of grok/auto2/frames.jsonl.
func TestGrokPendingInteractionSetsThePermissionCheck(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)

	a.HandleOutput(notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "pending_interaction", "tool_call_id": "call_2", "kind": "permission",
	}))

	running, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyRunningTool)
	require.True(t, ok, "the pending check reaches the running tool")
	fields, ok := running.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "call_2", fields[contracts.RunningToolFieldSpanId])
	assert.Equal(t, true, fields[contracts.RunningToolFieldPermissionCheck], "the badge states a check in progress")

	// The real permission request replaces the badge with the card.
	a.HandleOutput(frame(t, map[string]any{
		"id": 9, "method": "session/request_permission",
		"params": map[string]any{"sessionId": grokTestSession, "toolCall": map[string]any{"toolCallId": "call_2"}},
	}))
	running, ok = sink.LastSessionInfoValue(contracts.SessionInfoKeyRunningTool)
	require.True(t, ok)
	fields, ok = running.(map[string]interface{})
	require.True(t, ok)
	assert.Nil(t, fields[contracts.RunningToolFieldPermissionCheck], "the request clears the check")
}

// A kind other than permission states no check, and an unknown session or a
// call without an identity publishes nothing.
func TestGrokPendingInteractionRejectsOtherShapes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		sessionID string
		update    map[string]any
	}{
		{"a kind other than permission", grokTestSession, map[string]any{"sessionUpdate": "pending_interaction", "tool_call_id": "call_2", "kind": "question"}},
		{"a call without an identity", grokTestSession, map[string]any{"sessionUpdate": "pending_interaction", "kind": "permission"}},
		{"a session this agent does not serve", "session-other", map[string]any{"sessionUpdate": "pending_interaction", "tool_call_id": "call_2", "kind": "permission"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
			a.HandleOutput(notification(t, tc.sessionID, tc.update))
			_, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyRunningTool)
			assert.False(t, ok, "no check was published")
		})
	}
}

// Grok resolves an interaction on its own when it drops the request: frame 36.
// The resolution also ends the badge.
func TestGrokInteractionResolvedClearsTheCheck(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "pending_interaction", "tool_call_id": "call_2", "kind": "permission",
	}))
	a.HandleOutput(notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "interaction_resolved", "tool_call_id": "call_2",
	}))
	running, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyRunningTool)
	require.True(t, ok)
	fields, ok := running.(map[string]interface{})
	require.True(t, ok)
	assert.Nil(t, fields[contracts.RunningToolFieldPermissionCheck])
}

// A final tool update ends the call, so its badge ends too.
func TestGrokFinalToolUpdateClearsTheCheck(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "pending_interaction", "tool_call_id": "call_2", "kind": "permission",
	}))
	a.HooksForTest().ToolOutputComplete(grokTestSession, "call_2")
	running, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyRunningTool)
	require.True(t, ok)
	fields, ok := running.(map[string]interface{})
	require.True(t, ok)
	assert.Nil(t, fields[contracts.RunningToolFieldPermissionCheck])
}

func TestGrokChildToolCompletionPreservesTheParentCheck(t *testing.T) {
	t.Parallel()
	a, parent, _ := newGrokAgent(t, agent.Options{}, nil)
	const childSession = "child-session"
	const rowKey = "child-row"
	const toolID = "shared-tool-id"
	a.ApplySubagentObservation(&acp.SubagentObservation{
		RowKey: rowKey, ChildAgentKey: rowKey, ChildAgentSessionID: childSession,
		Title: "Native child", Status: bgtask.StatusRunning,
	})
	require.True(t, a.AttachChildSession(acp.ChildSessionTarget{RootSessionID: grokTestSession, SessionID: childSession, RowKey: rowKey}))
	a.HandleOutput(sessionUpdate(t, childSession, map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": toolID, "kind": "execute", "title": "Child command", "status": "in_progress",
	}))
	a.HandleOutput(notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "pending_interaction", "tool_call_id": toolID, "kind": "permission",
	}))
	a.HandleOutput(sessionUpdate(t, childSession, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": toolID, "status": "completed",
	}))
	running, ok := parent.LastSessionInfoValue(contracts.SessionInfoKeyRunningTool)
	require.True(t, ok)
	fields, ok := running.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, true, fields[contracts.RunningToolFieldPermissionCheck])
}

// A stopped turn clears every permission-check badge. Normal and error turns
// preserve badges for open spans until those calls finish.
func TestGrokTurnEndClearsTheChecksOnlyWhenStopped(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		err       error
		stopped   bool
		wantClear bool
	}{
		{name: "normal turn preserves check", err: nil, stopped: false, wantClear: false},
		{name: "error turn preserves check", err: assert.AnError, stopped: false, wantClear: false},
		{name: "stopped turn clears check", err: nil, stopped: true, wantClear: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
			a.HandleOutput(notification(t, grokTestSession, map[string]any{
				"sessionUpdate": "pending_interaction", "tool_call_id": "call_2", "kind": "permission",
			}))
			a.HooksForTest().PromptEnded(tc.err, tc.stopped)
			running, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyRunningTool)
			require.True(t, ok)
			fields, ok := running.(map[string]interface{})
			require.True(t, ok)
			if tc.wantClear {
				assert.Nil(t, fields[contracts.RunningToolFieldPermissionCheck])
			} else {
				assert.Equal(t, true, fields[contracts.RunningToolFieldPermissionCheck])
			}
		})
	}
}

// Grok completes an external URL elicitation with its own notification: the
// reader opened the URL, finished the flow outside, and the card must close
// with an accept. The elicit request and the completion name the same server
// and elicitation.
func TestGrokElicitCompleteAcceptsTheURLElicitation(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)

	request := frame(t, map[string]any{
		"id": 11, "method": contracts.GrokMethodMcpElicit,
		"params": map[string]any{
			"sessionId": grokTestSession, "serverName": "drive", "elicitationId": "flow-7", "mode": "url",
		},
	})
	a.HandleOutput(request)
	require.Equal(t, 1, sink.PublishedControlCount(), "the URL elicitation published its card")

	a.HandleOutput(frame(t, map[string]any{
		"method": "_x.ai/mcp/elicit_complete",
		"params": map[string]any{"sessionId": grokTestSession, "serverName": "drive", "elicitationId": "flow-7"},
	}))
	require.Len(t, sink.CanceledControls(), 1, "the completion withdrew the card")
}

// A completion that names another server completes nothing.
func TestGrokElicitCompleteOfAnotherServerChangesNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(frame(t, map[string]any{
		"id": 12, "method": contracts.GrokMethodMcpElicit,
		"params": map[string]any{
			"sessionId": grokTestSession, "serverName": "drive", "elicitationId": "flow-7", "mode": "url",
		},
	}))
	require.Equal(t, 1, sink.PublishedControlCount())

	a.HandleOutput(frame(t, map[string]any{
		"method": "_x.ai/mcp/elicit_complete",
		"params": map[string]any{"sessionId": grokTestSession, "serverName": "other", "elicitationId": "flow-7"},
	}))
	assert.Empty(t, sink.CanceledControls(), "a foreign completion changes nothing")
}

func TestGrokCompleteToolOutputWithEmptySessionClearsUnboundCheck(t *testing.T) {
	t.Parallel()
	a, baseSink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.SetSinkForTest(agent.NewProviderServices(&failOnceChildSpawnerSink{ControlSink: baseSink}))

	// Spawn a child whose BindChildSessionID fails once so its child sink stays unbound (MessageSessionID == "").
	a.HandleOutput(spawnCall(t, "spawn-unbound", "Unbound child", "Run task", true))
	a.HandleOutput(spawned(t, "child-unbound", "Unbound child", map[string]any{"agentAddress": "aa1.unbound"}))

	// Record permission checks with the same toolCallID on both bound root and unbound child.
	a.HandleOutput(notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "pending_interaction", "tool_call_id": "call_shared", "kind": "permission",
	}))
	a.HandleOutput(notification(t, "child-unbound", map[string]any{
		"sessionUpdate": "pending_interaction", "tool_call_id": "call_shared", "kind": "permission",
	}))

	// acp.conversation passes sink.MessageSessionID() == "" for the unbound child sink.
	a.HooksForTest().ToolOutputComplete("", "call_shared")

	a.stateMu.Lock()
	_, childPresent := a.controls.checks[controlKey{sessionID: "child-unbound", toolCallID: "call_shared"}]
	_, rootPresent := a.controls.checks[controlKey{sessionID: grokTestSession, toolCallID: "call_shared"}]
	a.stateMu.Unlock()
	assert.False(t, childPresent, "completing tool output on an unbound sink must clear its pending permission check")
	assert.True(t, rootPresent, "bound root session's permission check with the same toolCallID must remain open")
}
