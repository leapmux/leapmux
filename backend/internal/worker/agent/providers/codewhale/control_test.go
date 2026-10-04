package codewhale

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// approvalEvent is an `approval.required` as 0.9.13 sends it: no arguments, and
// the tool's static description.
func approvalEvent(seq uint64, approvalID, callID, tool string) []byte {
	return runtimeEvent(seq, contracts.CodewhaleEventApprovalRequired, testTurnID, "", map[string]any{
		"id": approvalID, "approval_id": approvalID, "tool_call_id": callID, "tool_name": tool,
		"description": "Execute a shell command in the workspace.", "intent_summary": nil,
	})
}

var testQuestions = []map[string]any{
	{"header": "Color", "id": "color", "question": "Which color?", "options": []map[string]any{{"label": "Red"}, {"label": "Blue"}}, "allow_free_text": true},
	{"header": "Sizes", "id": "sizes", "question": "Which sizes?", "options": []map[string]any{{"label": "S"}, {"label": "M"}}, "multi_select": true},
}

// userInputEvent is a `user_input.required` for testQuestions.
func userInputEvent(seq uint64, inputID string) []byte {
	return runtimeEvent(seq, contracts.CodewhaleEventUserInputRequired, testTurnID, "", map[string]any{
		"id": inputID, "request": map[string]any{"questions": testQuestions},
	})
}

// neutralResponse is the shared control response the browser sends.
func neutralResponse(t *testing.T, requestID string, response map[string]any) []byte {
	t.Helper()
	return mustJSON(t, map[string]any{
		"type":     "control_response",
		"response": map[string]any{"subtype": "success", "request_id": requestID, "response": response},
	})
}

func TestApprovalPublishesTheCallsOwnArguments(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(toolStartEvent(1, "item_1", "call_1", contracts.CodewhaleToolBash, map[string]any{"command": "touch x"}))
	a.HandleOutput(approvalEvent(2, "ap1", "call_1", contracts.CodewhaleToolBash))

	require.Equal(t, 1, sink.PublishedControlCount())
	record := sink.LastPublishedControl()
	assert.Equal(t, "approval:ap1", record.RequestID)
	payload := decodeJSON(t, record.Payload)
	assert.Equal(t, "control_request", payload["type"])
	assert.Equal(t, "approval:ap1", payload["request_id"])
	assert.Equal(t, map[string]any{"tool_name": "bash", "tool_use_id": "call_1", "input": map[string]any{"command": "touch x"}}, payload["request"])
	event := payload[contracts.CodewhaleControlPayloadEvent].(map[string]any)
	assert.Equal(t, contracts.CodewhaleEventApprovalRequired, event["event"])

	// A stream that delivers the event twice keeps one card.
	a.HandleOutput(approvalEvent(2, "ap1", "call_1", contracts.CodewhaleToolBash))
	assert.Equal(t, 1, sink.PublishedControlCount())
}

func TestApprovalOfAnUnseenCallTakesTheEventsToolName(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(approvalEvent(2, "ap1", "call_unseen", contracts.CodewhaleToolApplyPatch))

	payload := decodeJSON(t, sink.LastPublishedControl().Payload)
	assert.Equal(t, map[string]any{"tool_name": "apply_patch", "tool_use_id": "call_unseen", "input": map[string]any{}}, payload["request"])
}

func TestApprovalWithNoIDPublishesNothing(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(runtimeEvent(2, contracts.CodewhaleEventApprovalRequired, testTurnID, "", map[string]any{"tool_name": "bash"}))
	assert.Equal(t, 0, sink.PublishedControlCount())
}

func TestApprovalAnswers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		response map[string]any
		decision string
		feedback string
	}{
		{"allow", map[string]any{"behavior": "allow", "updatedInput": map[string]any{}}, "allow", ""},
		{"bare deny", map[string]any{"behavior": "deny", "message": "Rejected by user."}, "deny", ""},
		{"deny with a reason", map[string]any{"behavior": "deny", "message": "  Use a dry run.  "}, "deny", "  Use a dry run.  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rt := newFakeRuntime(t)
			rt.respondJSON(http.MethodPost, routeApprovals+"/ap1", http.StatusOK, map[string]any{"ok": true, "delivered": true})
			a, sink := newTestAgent(t, rt)
			a.HandleOutput(approvalEvent(2, "ap1", "call_1", contracts.CodewhaleToolBash))
			stored := sink.LastPublishedControl().Payload

			res := codewhaleProvider{}.ResolveControlResponse(agent.ControlResponseContext{
				RequestPayload:  stored,
				ResponseContent: neutralResponse(t, "approval:ap1", tc.response),
			})
			require.False(t, res.Withhold)
			assert.Equal(t, tc.feedback, res.Feedback, "a reason rides as the next message, never in the decision")
			frame := decodeJSON(t, res.Content)
			assert.Equal(t, map[string]any{"frame": "approval", "approval_id": "ap1", "decision": tc.decision}, frame)

			require.NoError(t, a.SendRawInput(res.Content))
			body := rt.lastBody(t, http.MethodPost, routeApprovals+"/ap1")
			assert.Equal(t, map[string]any{"decision": tc.decision, "remember": false}, body, "remember is never sent")
			// The answer retired the card, so a second answer is refused.
			assert.ErrorIs(t, a.SendRawInput(res.Content), errControlNotPending)
		})
	}
}

func TestApprovalAnswerToASettledApprovalWithdrawsTheCard(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondStatus(http.MethodPost, routeApprovals+"/ap1", http.StatusNotFound, "no pending approval")
	a, sink := newTestAgent(t, rt)
	a.HandleOutput(approvalEvent(2, "ap1", "call_1", contracts.CodewhaleToolBash))

	err := a.SendRawInput([]byte(`{"frame":"approval","approval_id":"ap1","decision":"allow"}`))
	assert.ErrorIs(t, err, errControlNotPending)
	assert.Equal(t, []string{"approval:ap1"}, sink.CanceledControls())
}

func TestApprovalDecidedElsewhereRetiresTheCard(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(approvalEvent(2, "ap1", "call_1", contracts.CodewhaleToolBash))
	a.HandleOutput(runtimeEvent(3, "approval.decided", testTurnID, "", map[string]any{"approval_id": "ap1", "decision": "deny", "timeout": true}))
	assert.Equal(t, []string{"approval:ap1"}, sink.CanceledControls())
	// A decision for an approval this agent never published retires nothing.
	a.HandleOutput(runtimeEvent(4, "approval.decided", testTurnID, "", map[string]any{"approval_id": "ap_other"}))
	assert.Equal(t, []string{"approval:ap1"}, sink.CanceledControls())
}

func TestQuestionPublishesTheRuntimesRequest(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(userInputEvent(5, "q1"))

	record := sink.LastPublishedControl()
	assert.Equal(t, "user_input:q1", record.RequestID)
	payload := decodeJSON(t, record.Payload)
	request := payload["request"].(map[string]any)
	assert.Equal(t, contracts.CodewhaleToolRequestUserInput, request["tool_name"])
	assert.Equal(t, "q1", request["tool_use_id"])
	assert.Len(t, request["input"].(map[string]any)["questions"], 2)
}

func TestQuestionAnswers(t *testing.T) {
	t.Parallel()
	nativeAnswers := []map[string]any{
		{"id": "color", "label": "Other", "value": "Green"},
		{"id": "sizes", "label": "S", "value": "S"},
		{"id": "sizes", "label": "M", "value": "M"},
	}
	for _, tc := range []struct {
		name     string
		response map[string]any
		want     []any
		declined bool
	}{
		{
			name:     "the runtime's own list",
			response: map[string]any{"behavior": "allow", "updatedInput": map[string]any{"answers": nativeAnswers}},
			want: []any{
				map[string]any{"id": "color", "label": "Other", "value": "Green"},
				map[string]any{"id": "sizes", "label": "S", "value": "S"},
				map[string]any{"id": "sizes", "label": "M", "value": "M"},
			},
		},
		{
			name:     "the shared control's map by question text",
			response: map[string]any{"behavior": "allow", "updatedInput": map[string]any{"answers": map[string]any{"Which color?": "Red", "Sizes": "S, M"}}},
			want: []any{
				map[string]any{"id": "color", "label": "Red", "value": "Red"},
				map[string]any{"id": "sizes", "label": "Other", "value": "S, M"},
			},
		},
		{
			name:     "a bare decline",
			response: map[string]any{"behavior": "deny", "message": "Rejected by user."},
			want: []any{
				map[string]any{"id": "color", "label": "Other", "value": contracts.CodewhaleAnswerTextDeclined},
				map[string]any{"id": "sizes", "label": "Other", "value": contracts.CodewhaleAnswerTextDeclined},
			},
			declined: true,
		},
		{
			name:     "a decline with a reason",
			response: map[string]any{"behavior": "deny", "message": "User stopped"},
			want: []any{
				map[string]any{"id": "color", "label": "Other", "value": "User stopped"},
				map[string]any{"id": "sizes", "label": "Other", "value": "User stopped"},
			},
			declined: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rt := newFakeRuntime(t)
			route := routeUserInput + "/" + testThreadID + "/q1"
			rt.respondJSON(http.MethodPost, route, http.StatusOK, map[string]any{"ok": true})
			a, sink := newTestAgent(t, rt)
			a.HandleOutput(userInputEvent(5, "q1"))

			res := codewhaleProvider{}.ResolveControlResponse(agent.ControlResponseContext{
				RequestPayload:  sink.LastPublishedControl().Payload,
				ResponseContent: neutralResponse(t, "user_input:q1", tc.response),
			})
			require.False(t, res.Withhold)
			assert.Empty(t, res.Feedback, "a question's answer reaches the runtime whole")
			frame := decodeJSON(t, res.Content)
			assert.Equal(t, "user_input", frame["frame"])
			assert.Equal(t, testThreadID, frame["thread_id"])
			assert.Equal(t, "q1", frame["input_id"])
			assert.Equal(t, tc.want, frame["answers"])
			if tc.declined {
				assert.Equal(t, true, frame["declined"])
			} else {
				assert.NotContains(t, frame, "declined")
			}

			require.NoError(t, a.SendRawInput(res.Content))
			assert.Equal(t, map[string]any{"answers": tc.want}, rt.lastBody(t, http.MethodPost, route))
		})
	}
}

func TestQuestionAnswerWithAnUnaddressedAnswerIsWithheld(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(userInputEvent(5, "q1"))
	stored := sink.LastPublishedControl().Payload
	for name, response := range map[string]map[string]any{
		"an answer with no id": {"behavior": "allow", "updatedInput": map[string]any{"answers": []map[string]any{{"label": "Red", "value": "Red"}}}},
		"no answers at all":    {"behavior": "allow", "updatedInput": map[string]any{}},
		"answers of no shape":  {"behavior": "allow", "updatedInput": map[string]any{"answers": 7}},
	} {
		res := codewhaleProvider{}.ResolveControlResponse(agent.ControlResponseContext{RequestPayload: stored, ResponseContent: neutralResponse(t, "user_input:q1", response)})
		assert.True(t, res.Withhold, name)
	}
}

func TestControlResponseForAnotherRequestIsWithheld(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(approvalEvent(2, "ap1", "call_1", contracts.CodewhaleToolBash))
	stored := sink.LastPublishedControl().Payload
	for name, content := range map[string][]byte{
		"another request's id": neutralResponse(t, "approval:other", map[string]any{"behavior": "allow"}),
		"an unknown behavior":  neutralResponse(t, "approval:ap1", map[string]any{"behavior": "maybe"}),
		"not an envelope":      []byte(`{"decision":"allow"}`),
	} {
		res := codewhaleProvider{}.ResolveControlResponse(agent.ControlResponseContext{RequestPayload: stored, ResponseContent: content})
		assert.True(t, res.Withhold, name)
		assert.Equal(t, content, res.Content, name)
	}
	// A stored payload that holds no runtime event names no route.
	res := codewhaleProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestPayload:  []byte(`{"request_id":"approval:ap1","request":{"tool_name":"bash"}}`),
		ResponseContent: neutralResponse(t, "approval:ap1", map[string]any{"behavior": "allow"}),
	})
	assert.True(t, res.Withhold)
}

func TestSendRawInputRefusesAFrameItCannotRoute(t *testing.T) {
	t.Parallel()
	a, _ := newTestAgent(t, nil)
	for raw, want := range map[string]string{
		`not json`:                                 "decode the Codewhale reply frame",
		`{"frame":"unknown"}`:                      `takes no raw input of the frame "unknown"`,
		`{}`:                                       `takes no raw input of the frame ""`,
		`{"frame":"approval"}`:                     "identifies no approval",
		`{"frame":"user_input","thread_id":"t"}`:   "identifies no question",
		`{"frame":"user_input","input_id":"q9"}`:   "identifies no question",
		`{"frame":"approval","approval_id":"ap9"}`: errControlNotPending.Error(),
	} {
		assert.ErrorContains(t, a.SendRawInput([]byte(raw)), want, raw)
	}
	assert.ErrorIs(t, a.SendRawInput([]byte(`{"frame":"user_input","thread_id":"t","input_id":"q9"}`)), errControlNotPending)

	a.SetStoppedForTest(true)
	assert.ErrorContains(t, a.SendRawInput([]byte(`{"frame":"interrupt"}`)), "stopped")
}

func TestSendRawInputRunsTheInterruptFrame(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	route := turnPath(testThreadID, testTurnID, turnRouteInterrupt)
	rt.respondJSON(http.MethodPost, route, http.StatusOK, map[string]any{"id": testTurnID})
	a, _ := newTestAgent(t, rt)
	a.turnID = testTurnID

	require.NoError(t, a.SendRawInput([]byte(`{"frame":"interrupt"}`)))
	assert.Len(t, rt.requestsTo(http.MethodPost, route), 1)
}

func TestTurnEndRetiresItsControls(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.HandleOutput(approvalEvent(2, "ap1", "call_1", contracts.CodewhaleToolBash))
	a.HandleOutput(userInputEvent(3, "q1"))
	a.HandleOutput(turnCompletedEvent(4, testTurnID, contracts.CodewhaleTurnStatusInterrupted))
	assert.ElementsMatch(t, []string{"approval:ap1", "user_input:q1"}, sink.CanceledControls())
}

func TestUserInputSettledRetiresTheCard(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(userInputEvent(3, "q1"))
	a.HandleOutput(runtimeEvent(4, contracts.CodewhaleEventUserInputCanceled, testTurnID, "", map[string]any{"id": "q1", "input_id": "q1", "terminal": true}))
	assert.Equal(t, []string{"user_input:q1"}, sink.CanceledControls())
}

func TestUnpublishedControlsAreAnsweredSoTheTurnDoesNotWait(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	approvals := make(chan map[string]any, 1)
	rt.handle(http.MethodPost, routeApprovals+"/ap1", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		approvals <- body
		w.WriteHeader(http.StatusOK)
	})
	questions := make(chan map[string]any, 1)
	rt.handle(http.MethodPost, routeUserInput+"/"+testThreadID+"/q1", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		questions <- body
		w.WriteHeader(http.StatusOK)
	})
	a, sink := newTestAgent(t, rt)
	sink.PublicationError = assert.AnError
	a.HandleOutput(approvalEvent(2, "ap1", "call_1", contracts.CodewhaleToolBash))
	a.HandleOutput(userInputEvent(3, "q1"))

	select {
	case body := <-approvals:
		assert.Equal(t, map[string]any{"decision": "deny", "remember": false}, body)
	case <-time.After(30 * time.Second):
		t.Fatal("the unpublished approval was never denied")
	}
	select {
	case body := <-questions:
		answers := body["answers"].([]any)
		require.Len(t, answers, 2)
		assert.Equal(t, "Other", answers[0].(map[string]any)["label"])
	case <-time.After(30 * time.Second):
		t.Fatal("the unpublished question was never declined")
	}
}

// The runtime waits `[tools] user_input_timeout_seconds` (300 s by default) for
// an answer. At the end of that wait it fails the question's call and states
// why in a status item. It keeps the question's registration until the turn
// ends, and a late answer then returns 200 and reaches no model. So the card
// goes when its call ends, and a late answer is refused with no request.
func TestAQuestionWhoseCallEndedRetiresItsCard(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	route := routeUserInput + "/" + testThreadID + "/q1"
	rt.respondJSON(http.MethodPost, route, http.StatusOK, map[string]any{"ok": true, "delivered": true})
	a, sink := newTestAgent(t, rt)
	input := map[string]any{"questions": testQuestions}
	a.HandleOutput(toolStartEvent(2, "item_q1", "q1", contracts.CodewhaleToolRequestUserInput, input))
	a.HandleOutput(userInputEvent(3, "q1"))
	require.Equal(t, "user_input:q1", sink.LastPublishedControl().RequestID)

	a.HandleOutput(itemEvent(4, contracts.CodewhaleEventItemCompleted, "item_status", contracts.CodewhaleItemKindStatus, "User input timed out after 300s", nil))
	a.HandleOutput(toolEndEvent(5, contracts.CodewhaleEventItemFailed, "item_q1", "q1", contracts.CodewhaleToolRequestUserInput, "User input timed out after 300s", input, nil))

	assert.Equal(t, []string{"user_input:q1"}, sink.CanceledControls())
	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 1, "the runtime's own words state why the card went")
	assert.Contains(t, string(notifications[0].Content), "User input timed out after 300s")

	err := a.SendRawInput([]byte(`{"frame":"user_input","thread_id":"` + testThreadID + `","input_id":"q1","answers":[{"id":"color","label":"Red","value":"Red"}]}`))
	assert.ErrorIs(t, err, errControlNotPending)
	assert.Empty(t, rt.requestsTo(http.MethodPost, route), "a late answer reaches no route")
	// The turn end that follows finds nothing left to retire.
	a.HandleOutput(turnCompletedEvent(6, testTurnID, contracts.CodewhaleTurnStatusCompleted))
	assert.Equal(t, []string{"user_input:q1"}, sink.CanceledControls())
}

// An answered question's call ends too, after the answer retired the card.
func TestAnAnsweredQuestionRetiresNothingTwice(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondJSON(http.MethodPost, routeUserInput+"/"+testThreadID+"/q1", http.StatusOK, map[string]any{"ok": true, "delivered": true})
	a, sink := newTestAgent(t, rt)
	input := map[string]any{"questions": testQuestions}
	a.HandleOutput(toolStartEvent(2, "item_q1", "q1", contracts.CodewhaleToolRequestUserInput, input))
	a.HandleOutput(userInputEvent(3, "q1"))
	require.NoError(t, a.SendRawInput([]byte(`{"frame":"user_input","thread_id":"`+testThreadID+`","input_id":"q1","answers":[{"id":"color","label":"Red","value":"Red"}]}`)))

	a.HandleOutput(toolEndEvent(4, contracts.CodewhaleEventItemCompleted, "item_q1", "q1", contracts.CodewhaleToolRequestUserInput, "User input submitted", input, map[string]any{"response_redacted": true}))
	assert.Empty(t, sink.CanceledControls(), "the answer retired the card, and nothing withdraws it again")
}

// At the end of the same wait the runtime denies an approval. It states the
// timeout, and then the decision that retires the card.
func TestAnApprovalThatTimedOutStatesWhyItsCardWentAway(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	a, sink := newTestAgent(t, rt)
	a.HandleOutput(toolStartEvent(1, "item_1", "call_1", contracts.CodewhaleToolBash, map[string]any{"command": "touch x"}))
	a.HandleOutput(approvalEvent(2, "ap1", "call_1", contracts.CodewhaleToolBash))

	a.HandleOutput(runtimeEvent(3, contracts.CodewhaleEventApprovalTimeout, testTurnID, "", map[string]any{"approval_id": "ap1", "tool_call_id": "call_1", "timeout_secs": 300}))
	a.HandleOutput(runtimeEvent(4, "approval.decided", testTurnID, "", map[string]any{"approval_id": "ap1", "tool_call_id": "call_1", "decision": "deny", "timeout": true}))

	assert.Equal(t, []string{"approval:ap1"}, sink.CanceledControls())
	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 1)
	assert.Equal(t, contracts.CodewhaleEventApprovalTimeout, decodeJSON(t, notifications[0].Content)["event"])

	err := a.SendRawInput([]byte(`{"frame":"approval","approval_id":"ap1","decision":"allow"}`))
	assert.ErrorIs(t, err, errControlNotPending)
	assert.Empty(t, rt.requestsTo(http.MethodPost, routeApprovals+"/ap1"), "a late answer reaches no route")
}

// An answer the runtime failed to take keeps its card: the runtime still waits,
// and the reader can answer again.
func TestAnAnswerThatTheRuntimeFailedToTakeKeepsTheCard(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	approvals := routeApprovals + "/ap1"
	questions := routeUserInput + "/" + testThreadID + "/q1"
	rt.respondStatus(http.MethodPost, approvals, http.StatusInternalServerError, "store busy")
	rt.respondStatus(http.MethodPost, questions, http.StatusInternalServerError, "store busy")
	a, sink := newTestAgent(t, rt)
	a.HandleOutput(approvalEvent(2, "ap1", "call_1", contracts.CodewhaleToolBash))
	a.HandleOutput(userInputEvent(3, "q1"))
	approval := []byte(`{"frame":"approval","approval_id":"ap1","decision":"allow"}`)
	question := []byte(`{"frame":"user_input","thread_id":"` + testThreadID + `","input_id":"q1","answers":[{"id":"color","label":"Red","value":"Red"}]}`)

	for name, frame := range map[string][]byte{"approval": approval, "question": question} {
		err := a.SendRawInput(frame)
		assert.ErrorContains(t, err, "store busy", name)
		assert.NotErrorIs(t, err, errControlNotPending, name)
	}
	assert.Empty(t, sink.CanceledControls(), "no card goes")
	assert.True(t, a.controlPending("approval:ap1"))
	assert.True(t, a.controlPending("user_input:q1"))

	rt.respondJSON(http.MethodPost, approvals, http.StatusOK, map[string]any{"ok": true})
	rt.respondJSON(http.MethodPost, questions, http.StatusOK, map[string]any{"ok": true})
	require.NoError(t, a.SendRawInput(approval), "the second answer reaches the runtime")
	require.NoError(t, a.SendRawInput(question))
	assert.False(t, a.controlPending("approval:ap1"))
	assert.False(t, a.controlPending("user_input:q1"))
	assert.Empty(t, sink.CanceledControls(), "an answer retires its record, and the answer path owns the card")
}

func TestQuestionAnswerToASettledQuestionWithdrawsTheCard(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondStatus(http.MethodPost, routeUserInput+"/"+testThreadID+"/q1", http.StatusNotFound, "no pending user input")
	a, sink := newTestAgent(t, rt)
	a.HandleOutput(userInputEvent(3, "q1"))

	err := a.SendRawInput([]byte(`{"frame":"user_input","thread_id":"` + testThreadID + `","input_id":"q1","answers":[]}`))
	assert.ErrorIs(t, err, errControlNotPending)
	assert.Equal(t, []string{"user_input:q1"}, sink.CanceledControls())
}

// The runtime reads `answers` as a list, so an answer frame that states none
// posts an empty list, never null.
func TestQuestionAnswerWithNoAnswersPostsAnEmptyList(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	route := routeUserInput + "/" + testThreadID + "/q1"
	rt.respondJSON(http.MethodPost, route, http.StatusOK, map[string]any{"ok": true})
	a, _ := newTestAgent(t, rt)
	a.HandleOutput(userInputEvent(3, "q1"))

	require.NoError(t, a.SendRawInput([]byte(`{"frame":"user_input","thread_id":"`+testThreadID+`","input_id":"q1"}`)))
	assert.Equal(t, map[string]any{"answers": []any{}}, rt.lastBody(t, http.MethodPost, route))
}

func TestQuestionWithNoIDPublishesNothing(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(runtimeEvent(3, contracts.CodewhaleEventUserInputRequired, testTurnID, "", map[string]any{"request": map[string]any{"questions": testQuestions}}))
	assert.Zero(t, sink.PublishedControlCount())
	// A settlement with no id retires nothing either.
	a.HandleOutput(userInputEvent(4, "q1"))
	a.HandleOutput(runtimeEvent(5, contracts.CodewhaleEventUserInputCanceled, testTurnID, "", map[string]any{}))
	a.HandleOutput(runtimeEvent(6, "approval.decided", testTurnID, "", map[string]any{}))
	assert.Empty(t, sink.CanceledControls())
}

// A turn end retires the cards of its own turn, and a card that states no turn.
// A card of another turn waits for that turn.
func TestATurnEndRetiresOnlyTheCardsOfItsTurn(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	require.True(t, a.publishControl("approval:mine", testTurnID, []byte(`{}`)))
	require.True(t, a.publishControl("approval:turnless", "", []byte(`{}`)))
	require.True(t, a.publishControl("approval:theirs", "turn_other", []byte(`{}`)))

	a.withdrawControlsOfTurn(testTurnID)
	assert.ElementsMatch(t, []string{"approval:mine", "approval:turnless"}, sink.CanceledControls())
	assert.True(t, a.controlPending("approval:theirs"))

	// A process exit retires every card.
	a.withdrawAllControls()
	assert.ElementsMatch(t, []string{"approval:mine", "approval:turnless", "approval:theirs"}, sink.CanceledControls())
}

// The provider withholds the answer to a stored request that no route can
// address, so the runtime keeps waiting and the reader can answer again.
func TestAControlResponseToAStoredEventItCannotAddressIsWithheld(t *testing.T) {
	t.Parallel()
	allow := neutralResponse(t, "", map[string]any{"behavior": "allow", "updatedInput": map[string]any{"answers": []any{}}})
	stored := func(event []byte) []byte {
		payload, err := buildControlPayload("", codewhaleControlHeader{ToolName: "bash"}, event)
		require.NoError(t, err)
		return payload
	}
	for name, event := range map[string][]byte{
		"an approval with no id":     runtimeEvent(2, contracts.CodewhaleEventApprovalRequired, testTurnID, "", map[string]any{"tool_name": "bash"}),
		"a question with no id":      runtimeEvent(2, contracts.CodewhaleEventUserInputRequired, testTurnID, "", map[string]any{"request": map[string]any{}}),
		"a question with no thread":  []byte(`{"seq":2,"event":"user_input.required","payload":{"id":"q1"}}`),
		"an event that asks nothing": runtimeEvent(2, contracts.CodewhaleEventSandboxDenied, testTurnID, "", map[string]any{}),
	} {
		res := codewhaleProvider{}.ResolveControlResponse(agent.ControlResponseContext{RequestPayload: stored(event), ResponseContent: allow})
		assert.True(t, res.Withhold, name)
	}
}

func TestDeclinedAnswersSkipAQuestionWithNoID(t *testing.T) {
	t.Parallel()
	request := mustJSON(t, map[string]any{"questions": []map[string]any{{"id": "a", "question": "A?"}, {"question": "No id?"}}})
	assert.Equal(t, []userInputAnswer{{ID: "a", Label: contracts.CodewhaleAnswerLabelOther, Value: "No."}}, declinedAnswers(request, "No."))
	assert.Empty(t, declinedAnswers(nil, "No."), "a request with no questions declines nothing")
	assert.Empty(t, declinedAnswers([]byte(`not json`), "No."))
}

// The shared question control keys each answer by the question's text, or by
// its header. An answer that addresses no question, and a question with no id,
// send nothing.
func TestAnswersFromTheSharedControlsMap(t *testing.T) {
	t.Parallel()
	request := mustJSON(t, map[string]any{"questions": []map[string]any{
		{"id": "color", "header": "Color", "question": "Which color?", "options": []map[string]any{{"label": "Red"}}},
		{"header": "Orphan", "question": "No id?"},
	}})
	response := neutralResponse(t, "user_input:q1", map[string]any{"behavior": "allow", "updatedInput": map[string]any{"answers": map[string]any{
		"Color": "Red", "No id?": "x", "Not asked?": "y",
	}}})
	answers, ok := answersFromResponse(response, request)
	require.True(t, ok)
	assert.Equal(t, []userInputAnswer{{ID: "color", Label: "Red", Value: "Red"}}, answers, "the header finds the question when the text does not")
}
