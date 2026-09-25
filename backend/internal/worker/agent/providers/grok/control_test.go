package grok

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

func TestGrokControlRequestsKeepNumericAndStringIdentitiesSeparate(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	// The shared frames state a session of their own. An agent that has no
	// session yet reads every session as its own, as the bare agents of the
	// other providers' identity tests do, so each dialog reaches the reader.
	a.SetSessionIDForTest("")
	agenttest.AssertControlIdentitiesStaySeparate(t, sink, a.HandleOutput, contracts.GrokMethodAskUserQuestion)
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
	require.NoError(t, a.SendRawInput(answer))
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
	require.NoError(t, a.Interrupt())
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
	require.NoError(t, a.Interrupt())
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
	a.AttachChildSession("child-session", "call_spawn")
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

	a.HandleOutput(notification(t, "child-session", map[string]any{"sessionUpdate": "interaction_resolved", "tool_call_id": "call_0"}))
	assert.Empty(t, sink.CanceledControls(), "the child's resolution is not the parent's")

	// The child's own request with the same tool-call id resolves in the child.
	a.HandleOutput(permission(12, "child-session"))
	a.HandleOutput(notification(t, "child-session", map[string]any{"sessionUpdate": "interaction_resolved", "tool_call_id": "call_0"}))
	assert.Equal(t, []string{"jsonrpc:12"}, sink.CanceledControls())

	a.SetPromptActiveForTest(true)
	require.NoError(t, a.Interrupt())
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
