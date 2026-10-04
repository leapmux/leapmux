package kiro

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestKiroControlRequestsKeepNumericAndStringIdentitiesSeparate(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	// The shared frames state the session "test-session". The agent serves that
	// session, so each dialog passes the session guard and reaches the reader.
	a.SetSessionIDForTest("test-session")
	agenttest.AssertControlIdentitiesStaySeparate(t, sink, a.HandleOutput, contracts.KiroMethodUserInput)
}

// userInputRequest is Kiro's question of the main session, as the probe
// recorded it.
func userInputRequest(t *testing.T, id int, toolCallID string) []byte {
	t.Helper()
	return userInputRequestOf(t, kiroTestSession, id, toolCallID)
}

// userInputRequestOf is Kiro's question of the session sessionID.
func userInputRequestOf(t *testing.T, sessionID string, id int, toolCallID string) []byte {
	t.Helper()
	return frame(t, map[string]any{
		"id": id, "method": contracts.KiroMethodUserInput,
		"params": map[string]any{
			"sessionId": sessionID, "toolCallId": toolCallID, "question": "Which DB?",
			"options": []any{
				map[string]any{"title": "Postgres", "description": "pg", "recommended": true},
				map[string]any{"title": "SQLite", "recommended": false},
			},
		},
	})
}

// elicitationRequest is an MCP server's form in the main session, as the probe
// recorded it.
func elicitationRequest(t *testing.T, id int, toolCallID string) []byte {
	t.Helper()
	return elicitationRequestOf(t, kiroTestSession, id, toolCallID)
}

// elicitationRequestOf is an MCP server's form in the session sessionID.
func elicitationRequestOf(t *testing.T, sessionID string, id int, toolCallID string) []byte {
	t.Helper()
	return frame(t, map[string]any{
		"id": id, "method": contracts.KiroMethodMcpElicitation,
		"params": map[string]any{
			"sessionId": sessionID, "toolCallId": toolCallID,
			"elicitation": map[string]any{
				"mode": "form", "message": "Pick a color",
				"requestedSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"choice": map[string]any{"type": "string", "enum": []any{"red", "blue"}}},
					"required":   []any{"choice"},
				},
			},
		},
	})
}

// interactionResolved is Kiro's report that the interaction of one tool call
// resolved.
func interactionResolved(t *testing.T, toolCallID, outcome string) []byte {
	t.Helper()
	return infoUpdate(t, map[string]any{
		"kind":                kiroKindInteractionResolved,
		"interactionResolved": map[string]any{"toolCallId": toolCallID, "outcome": outcome},
		"toolCallId":          toolCallID,
		"outcome":             outcome,
	})
}

func TestKiroPublishesEachOfItsOwnDialogs(t *testing.T) {
	t.Parallel()
	for name, line := range map[string]func(*testing.T) []byte{
		"question":    func(t *testing.T) []byte { return userInputRequest(t, 3, "t_q") },
		"elicitation": func(t *testing.T) []byte { return elicitationRequest(t, 3, "m_ask") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
			raw := line(t)

			a.HandleOutput(raw)

			require.Equal(t, 1, sink.PublishedControlCount())
			assert.JSONEq(t, string(raw), string(sink.LastPublishedControl().Payload), "the browser reads Kiro's own bytes")
		})
	}
}

// Kiro sends every session of its process down one connection. A dialog of a
// session that the agent does not serve -- a retired session, or a workflow
// step whose route a context clear removed -- never reaches the reader, and
// Kiro takes the cancel answer at once, because its turn waits on the answer.
func TestKiroRefusesADialogOfASessionThatItDoesNotServe(t *testing.T) {
	t.Parallel()
	a, sink, requests := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(userInputRequestOf(t, "session-retired", 41, "t_q"))
	a.HandleOutput(elicitationRequestOf(t, "session-retired", 42, "m_ask"))
	syncPeer(t, a)

	assert.Zero(t, sink.PublishedControlCount(), "no card of another session reaches the reader")
	answers := agenttest.JSONRPCResultsByID(t, rawLines(requests()))
	assert.JSONEq(t, `{"action":"dismissed"}`, answers["41"])
	assert.JSONEq(t, `{"action":"cancel"}`, answers["42"])
	assert.Zero(t, a.OutstandingControlCountForTest(), "a refused request leaves no record for a later stop")
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	assert.Empty(t, a.controls.byToolCall, "a refused request records no tool call")
}

// A workflow step runs in a session of its own, which a registry row routes to
// a child transcript. Its dialogs reach the reader.
func TestKiroPublishesADialogOfARoutedStepSession(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.AttachChildSession("session-step", "session-step")

	a.HandleOutput(userInputRequestOf(t, "session-step", 43, "t_q"))

	assert.Equal(t, 1, sink.PublishedControlCount())
}

func TestKiroDialogNotificationWithoutAnIDPublishesNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(frame(t, map[string]any{
		"method": contracts.KiroMethodUserInput,
		"params": map[string]any{"sessionId": kiroTestSession, "toolCallId": "t_q", "question": "Which DB?"},
	}))

	assert.Zero(t, sink.PublishedControlCount(), "a notification has no id that an answer could reach")
}

func TestKiroResolvedInteractionRetiresAnOpenQuestion(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(userInputRequest(t, 5, "t_q"))
	require.Equal(t, 1, sink.PublishedControlCount())
	requestID := sink.LastPublishedControl().RequestID

	// Kiro dropped the question: the turn that asked it was cancelled.
	a.HandleOutput(interactionResolved(t, "t_q", "cancelled"))

	assert.Equal(t, []string{requestID}, sink.CanceledControls())
}

func TestKiroResolvedInteractionAfterTheAnswerCancelsNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(userInputRequest(t, 6, "t_q"))
	requestID := sink.LastPublishedControl().RequestID
	answer, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 6,
		"result": map[string]any{"action": contracts.KiroUserInputActionAnswered, "answer": "Postgres"},
	})
	require.NoError(t, err)
	require.NoError(t, a.SendRawInput(answer))
	syncPeer(t, a)

	// Kiro reports every resolution, the reader's own answer included.
	a.HandleOutput(interactionResolved(t, "t_q", "answered"))

	assert.NotContains(t, sink.CanceledControls(), requestID, "an answered card never reads as cancelled")
}

func TestKiroResolvedInteractionRetiresAToolPermission(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	// The base publishes the standard permission request, and the observer hook
	// records its tool call for Kiro.
	a.HandleOutput(frame(t, map[string]any{
		"id": 11, "method": "session/request_permission",
		"params": map[string]any{
			"sessionId": kiroTestSession,
			"toolCall":  map[string]any{"toolCallId": "run_command_t_sh", "title": "Running: ls"},
			"options": []any{
				map[string]any{"optionId": "accept", "name": "Yes", "kind": "allow_once"},
				map[string]any{"optionId": "reject", "name": "No", "kind": "reject_once"},
			},
		},
	}))
	require.Equal(t, 1, sink.PublishedControlCount())
	requestID := sink.LastPublishedControl().RequestID

	a.HandleOutput(interactionResolved(t, "run_command_t_sh", "cancelled"))

	assert.Equal(t, []string{requestID}, sink.CanceledControls())
}

func TestKiroResolvedInteractionOfAnUnknownCallChangesNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(interactionResolved(t, "never-raised", "cancelled"))
	a.HandleOutput(infoUpdate(t, map[string]any{"kind": kiroKindInteractionResolved}))
	a.HandleOutput(infoUpdate(t, map[string]any{"kind": kiroKindInteractionResolved, "interactionResolved": map[string]any{"toolCallId": 7}}))

	assert.Empty(t, sink.CanceledControls())
}

// A dialog that states no tool call still reaches the reader. It records no
// tool call, so no resolution of another call can retire its card.
func TestKiroDialogWithoutAToolCallIsPublishedAndRecordsNoToolCall(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(frame(t, map[string]any{
		"id": 9, "method": contracts.KiroMethodUserInput,
		"params": map[string]any{"sessionId": kiroTestSession, "question": "Which DB?"},
	}))
	assert.Equal(t, 1, sink.PublishedControlCount())
	// Params that are no object state no tool call either. The session guard of
	// the base decides whether such a dialog reaches the reader.
	a.HandleOutput(frame(t, map[string]any{"id": 10, "method": contracts.KiroMethodUserInput, "params": []any{"Which DB?"}}))
	a.HandleOutput(interactionResolved(t, "", "cancelled"))

	assert.Empty(t, sink.CanceledControls())
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	assert.Empty(t, a.controls.byToolCall)
}

// A resolution retires its card once. The record goes with the first
// resolution, so a second report of the same call withdraws nothing more.
func TestKiroResolvedInteractionRetiresItsCardOnce(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(userInputRequest(t, 5, "t_q"))

	a.HandleOutput(interactionResolved(t, "t_q", "cancelled"))
	a.HandleOutput(interactionResolved(t, "t_q", "cancelled"))

	assert.Len(t, sink.CanceledControls(), 1)
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	assert.Empty(t, a.controls.byToolCall, "the resolution forgets the call")
}

func TestKiroControlCancelAnswers(t *testing.T) {
	t.Parallel()
	assert.Equal(t, map[string]any{"action": contracts.KiroUserInputActionDismissed}, kiroControlCancelAnswer(contracts.KiroMethodUserInput))
	assert.Equal(t, map[string]any{"action": "cancel"}, kiroControlCancelAnswer(contracts.KiroMethodMcpElicitation))
	assert.Nil(t, kiroControlCancelAnswer("_kiro/unknown"))
}

// rawLines joins the frames that the agent wrote, one per line.
func rawLines(requests []agenttest.RecordedRequest) string {
	lines := make([]string, 0, len(requests))
	for _, request := range requests {
		lines = append(lines, request.Raw)
	}
	return strings.Join(lines, "\n")
}

func TestKiroWithdrawalAnswersEachOpenDialog(t *testing.T) {
	t.Parallel()
	a, sink, requests := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(userInputRequest(t, 21, "t_q"))
	a.HandleOutput(elicitationRequest(t, 22, "m_ask"))
	require.Equal(t, 2, sink.PublishedControlCount())

	a.WithdrawAllControlRequests(a.Sink())
	syncPeer(t, a)

	answers := agenttest.JSONRPCResultsByID(t, rawLines(requests()))
	assert.JSONEq(t, `{"action":"dismissed"}`, answers["21"], "Kiro reads a dismissed question as no answer")
	assert.JSONEq(t, `{"action":"cancel"}`, answers["22"])
	assert.Len(t, sink.CanceledControls(), 2, "every card retires")
}
