package droid

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// permissionRequestPayload is the control request that onRequestPermission
// publishes for one tool call, with the option values that Droid offered.
func permissionRequestPayload(t *testing.T, toolName, confirmationType string, options ...string) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"type":             "permission_request",
		"requestId":        "droid-perm-1",
		"rpcId":            "rpc-42",
		"toolUse":          map[string]any{"type": "tool_use", "id": "c1", "name": toolName, "input": map[string]any{"file_path": "x"}},
		"confirmationType": confirmationType,
		"options":          options,
	})
	require.NoError(t, err)
	return payload
}

// askUserRequestPayload is the control request that onAskUser publishes, with
// the questions in Droid's own order and with Droid's own numbers.
func askUserRequestPayload(t *testing.T, questions ...map[string]any) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"type":       "ask_user_request",
		"requestId":  "droid-ask-1",
		"rpcId":      "rpc-43",
		"toolCallId": "c2",
		"questions":  questions,
	})
	require.NoError(t, err)
	return payload
}

// colorAndSizeQuestions are two questions as Droid numbers them: from 1, in order.
func colorAndSizeQuestions() []map[string]any {
	return []map[string]any{
		{"index": 1, "question": "Which color?", "options": []string{"Blue", "Red"}, "multiSelect": false},
		{"index": 2, "question": "Which sizes?", "options": []string{"Small", "Large"}, "multiSelect": true},
	}
}

// decision is the browser's neutral control response around the fields of one
// decision.
func decision(t *testing.T, requestID string, fields map[string]any) []byte {
	t.Helper()
	content, err := json.Marshal(map[string]any{
		"response": map[string]any{"request_id": requestID, "response": fields},
	})
	require.NoError(t, err)
	return content
}

// resolve runs the provider's resolution for one stored request and one decision.
func resolve(requestPayload, content []byte) agent.ControlResponseResolution {
	return droidProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestID:       "droid-request",
		RequestPayload:  requestPayload,
		ResponseContent: content,
	})
}

// replyResult reads the id and the `result` of the JSON-RPC response that a
// resolution writes to Droid's stdin.
func replyResult(t *testing.T, resolution agent.ControlResponseResolution) (string, json.RawMessage) {
	t.Helper()
	require.False(t, resolution.Withhold)
	var envelope struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(resolution.Content, &envelope))
	return envelope.ID, envelope.Result
}

// The service forwards the resolved bytes to the agent's stdin. Droid reads a
// JSON-RPC RESPONSE envelope there, keyed by the id of the request it answers.
// A bare result body, or an envelope with the wrong id, leaves the call hanging.
func TestResolveControlResponseWritesAResponseEnvelope(t *testing.T) {
	t.Parallel()
	request := permissionRequestPayload(t, "Edit", contracts.DroidConfirmationTypeEdit,
		contracts.DroidPermissionOptionProceedOnce, contracts.DroidPermissionOptionCancel)
	response, _ := json.Marshal(map[string]any{
		"response": map[string]any{
			"request_id": "droid-perm-1",
			"response":   map[string]any{"behavior": "allow"},
		},
	})
	resolution := droidProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestID:       "droid-perm-1",
		RequestPayload:  request,
		ResponseContent: response,
	})
	require.False(t, resolution.Withhold)
	require.NotEmpty(t, resolution.Content)

	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		Type    string          `json:"type"`
		ID      string          `json:"id"`
		Result  json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(resolution.Content, &envelope))
	assert.Equal(t, "2.0", envelope.JSONRPC, "the reply is a JSON-RPC envelope")
	assert.Equal(t, "response", envelope.Type)
	assert.Equal(t, "rpc-42", envelope.ID, "the reply answers the request's own id")

	var result map[string]string
	require.NoError(t, json.Unmarshal(envelope.Result, &result))
	assert.Equal(t, "proceed_once", result["selectedOption"])
}

func TestResolveControlResponseClassifiesNativeSpecApproval(t *testing.T) {
	t.Parallel()
	request := []byte(`{"type":"permission_request","requestId":"droid-perm-exit-1","rpcId":"rpc-exit-1","toolUse":{"type":"tool_use","id":"exit-1","name":"ExitSpecMode","input":{"plan":"# Native plan"}},"confirmationType":"exit_spec_mode","options":["proceed_once","proceed_auto_run_low","cancel"]}`)
	response := []byte(`{"response":{"request_id":"droid-perm-exit-1","response":{"behavior":"allow"}}}`)
	resolution := droidProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestID: "droid-perm-exit-1", RequestPayload: request, ResponseContent: response,
	})
	require.False(t, resolution.Withhold)
	assert.Equal(t, agent.PlanModeControlExit, resolution.PlanModeControl)
	var envelope struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(resolution.Content, &envelope))
	assert.Equal(t, "rpc-exit-1", envelope.ID)
	assert.JSONEq(t, `{"selectedOption":"proceed_once"}`, string(envelope.Result))
}

// Droid's reply schema accepts a `comment` beside every selectedOption, but Droid
// reads it only beside an APPROVAL of exit_spec_mode or propose_mission. A cancel
// discards it for every confirmation type, and Droid's own TUI sends the reason
// for a rejection as the next user message. So the typed reason must follow as
// Feedback, which the service queues as the next user input.
func TestResolveControlResponseSendsTheDenyReasonAsFeedback(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name             string
		toolName         string
		confirmationType string
		planModeControl  agent.PlanModeControlKind
	}{
		{"a tool permission", "Edit", contracts.DroidConfirmationTypeEdit, agent.PlanModeControlNone},
		{"a spec review", "ExitSpecMode", contracts.DroidConfirmationTypeExitSpecMode, agent.PlanModeControlExit},
		{"a mission proposal", "ProposeMission", contracts.DroidConfirmationTypeProposeMission, agent.PlanModeControlExit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			request := permissionRequestPayload(t, tc.toolName, tc.confirmationType,
				contracts.DroidPermissionOptionProceedOnce, contracts.DroidPermissionOptionCancel)
			resolution := resolve(request, decision(t, "droid-perm-1", map[string]any{
				"behavior": "deny", "message": "  Use a dry run first.  ",
			}))
			_, result := replyResult(t, resolution)
			assert.JSONEq(t, `{"selectedOption":"cancel"}`, string(result),
				"the reply is Droid's own cancel; Droid discards a comment beside it")
			assert.Equal(t, "Use a dry run first.", resolution.Feedback)
			assert.Equal(t, tc.planModeControl, resolution.PlanModeControl)
		})
	}
}

// A question has no field for a reason either: Droid's reply is
// `{cancelled, answers}`. The reason follows as Feedback.
func TestResolveControlResponseSendsAQuestionRefusalReasonAsFeedback(t *testing.T) {
	t.Parallel()
	resolution := resolve(askUserRequestPayload(t, colorAndSizeQuestions()...), decision(t, "droid-ask-1", map[string]any{
		"behavior": "deny", "message": "Ask about the shape instead.",
	}))
	id, result := replyResult(t, resolution)
	assert.Equal(t, "rpc-43", id)
	assert.JSONEq(t, `{"cancelled":true,"answers":[]}`, string(result), "Droid's own cancel carries no answer")
	assert.Equal(t, "Ask about the shape instead.", resolution.Feedback)
}

// The browser fills the reason of a bare rejection with a placeholder. That is
// no reason, so it must not reach the model as the user's words.
func TestResolveControlResponseSendsNoFeedbackForABareRejection(t *testing.T) {
	t.Parallel()
	for _, message := range []string{agent.ControlRejectedByUserMessage, "", "   "} {
		permission := resolve(permissionRequestPayload(t, "Edit", contracts.DroidConfirmationTypeEdit, contracts.DroidPermissionOptionCancel),
			decision(t, "droid-perm-1", map[string]any{"behavior": "deny", "message": message}))
		_, result := replyResult(t, permission)
		assert.JSONEq(t, `{"selectedOption":"cancel"}`, string(result))
		assert.Emptyf(t, permission.Feedback, "a permission rejection with the message %q has no reason", message)

		question := resolve(askUserRequestPayload(t, colorAndSizeQuestions()...),
			decision(t, "droid-ask-1", map[string]any{"behavior": "deny", "message": message}))
		_, result = replyResult(t, question)
		assert.JSONEq(t, `{"cancelled":true,"answers":[]}`, string(result))
		assert.Emptyf(t, question.Feedback, "a question refusal with the message %q has no reason", message)
	}
}

// An approval has no reason to send, so a stray message beside it does not reach
// the model.
func TestResolveControlResponseSendsNoFeedbackForAnApproval(t *testing.T) {
	t.Parallel()
	resolution := resolve(permissionRequestPayload(t, "Edit", contracts.DroidConfirmationTypeEdit, contracts.DroidPermissionOptionProceedOnce),
		decision(t, "droid-perm-1", map[string]any{"behavior": "allow", "message": "Go ahead."}))
	_, result := replyResult(t, resolution)
	assert.JSONEq(t, `{"selectedOption":"proceed_once"}`, string(result))
	assert.Empty(t, resolution.Feedback)
}

// Droid identifies each answer by the `index` of its question. The reply lists
// the answers in Droid's own question order, with Droid's own numbers and
// question words, whatever order the browser lists them in.
func TestResolveControlResponseAnswersEachQuestionByItsNativeIndex(t *testing.T) {
	t.Parallel()
	resolution := resolve(askUserRequestPayload(t, colorAndSizeQuestions()...), decision(t, "droid-ask-1", map[string]any{
		"behavior": "allow",
		"answers": []map[string]any{
			{"index": 2, "answer": "Small, Large"},
			{"index": 1, "answer": "Blue"},
		},
	}))
	id, result := replyResult(t, resolution)
	assert.Equal(t, "rpc-43", id)
	assert.JSONEq(t, `{"cancelled":false,"answers":[
		{"index":1,"question":"Which color?","answer":"Blue"},
		{"index":2,"question":"Which sizes?","answer":"Small, Large"}
	]}`, string(result))
	assert.Empty(t, resolution.Feedback)
}

// The numbers are Droid's own: the reply copies them, and never renumbers the
// questions from 0 or from 1.
func TestResolveControlResponseKeepsANativeIndexThatDoesNotStartAtOne(t *testing.T) {
	t.Parallel()
	request := askUserRequestPayload(t,
		map[string]any{"index": 7, "question": "Seventh?", "options": []string{"Yes", "No"}},
		map[string]any{"index": 3, "question": "Third?", "options": []string{"Yes", "No"}},
	)
	resolution := resolve(request, decision(t, "droid-ask-1", map[string]any{
		"behavior": "allow",
		"answers":  []map[string]any{{"index": 3, "answer": "No"}, {"index": 7, "answer": "Yes"}},
	}))
	_, result := replyResult(t, resolution)
	assert.JSONEq(t, `{"cancelled":false,"answers":[
		{"index":7,"question":"Seventh?","answer":"Yes"},
		{"index":3,"question":"Third?","answer":"No"}
	]}`, string(result))
}

// An empty answer is an answer: Droid's own TUI sends "" for a question that
// holds no choice.
func TestResolveControlResponseForwardsAnEmptyAnswer(t *testing.T) {
	t.Parallel()
	resolution := resolve(askUserRequestPayload(t, colorAndSizeQuestions()...), decision(t, "droid-ask-1", map[string]any{
		"behavior": "allow",
		"answers":  []map[string]any{{"index": 1, "answer": ""}, {"index": 2, "answer": "Large"}},
	}))
	_, result := replyResult(t, resolution)
	assert.JSONEq(t, `{"cancelled":false,"answers":[
		{"index":1,"question":"Which color?","answer":""},
		{"index":2,"question":"Which sizes?","answer":"Large"}
	]}`, string(result))
}

// Droid reports each answer to the model by its number, and its AskUser tool
// fails when the count of answers differs from the count of questions. An answer
// set that does not match the questions one to one is refused, so the reader can
// answer again, rather than sent to fail in Droid.
func TestResolveControlResponseRefusesAnswersThatDoNotMatchTheQuestions(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		answers []map[string]any
		reason  string
	}{
		"an unknown index":        {[]map[string]any{{"index": 1, "answer": "Blue"}, {"index": 3, "answer": "Large"}}, "the question with the index 2 has no answer"},
		"an extra index":          {[]map[string]any{{"index": 1, "answer": "Blue"}, {"index": 2, "answer": "Large"}, {"index": 3, "answer": "Huge"}}, "an answer states an index that no question states"},
		"a duplicate index":       {[]map[string]any{{"index": 1, "answer": "Blue"}, {"index": 1, "answer": "Red"}, {"index": 2, "answer": "Large"}}, "two answers state the index 1"},
		"a missing question":      {[]map[string]any{{"index": 1, "answer": "Blue"}}, "the question with the index 2 has no answer"},
		"no answer at all":        {[]map[string]any{}, "the question with the index 1 has no answer"},
		"an answer with no index": {[]map[string]any{{"answer": "Blue"}, {"index": 2, "answer": "Large"}}, "an answer states no index or no text"},
		"an index with no answer": {[]map[string]any{{"index": 1}, {"index": 2, "answer": "Large"}}, "an answer states no index or no text"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resolution := resolve(askUserRequestPayload(t, colorAndSizeQuestions()...), decision(t, "droid-ask-1", map[string]any{
				"behavior": "allow", "answers": tc.answers,
			}))
			require.True(t, resolution.Withhold)
			assert.EqualError(t, resolution.Refusal(), tc.reason)
		})
	}
	// An answer set that does not decode, or that is no list, states the shared
	// unreadable-answer reason; an absent list states its own.
	for name, tc := range map[string]struct {
		fields map[string]any
		reason string
	}{
		"an answer that is not text": {map[string]any{"behavior": "allow", "answers": []map[string]any{{"index": 1, "answer": 7}, {"index": 2, "answer": "Large"}}}, agent.RefusalUnreadableAnswer},
		"answers by question":        {map[string]any{"behavior": "allow", "answers": map[string]string{"Which color?": "Blue"}}, agent.RefusalUnreadableAnswer},
		"answers not a list":         {map[string]any{"behavior": "allow", "answers": "Blue"}, agent.RefusalUnreadableAnswer},
		"absent answers field":       {map[string]any{"behavior": "allow"}, "the decision holds no answer list"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resolution := resolve(askUserRequestPayload(t, colorAndSizeQuestions()...), decision(t, "droid-ask-1", tc.fields))
			require.True(t, resolution.Withhold)
			assert.EqualError(t, resolution.Refusal(), tc.reason)
		})
	}
}

// A request whose questions repeat a number cannot pair an answer with one
// question, so an approval of it is refused. A refusal still answers, because
// Droid's own cancel carries no answer.
func TestResolveControlResponseRefusesAnswersToQuestionsThatRepeatAnIndex(t *testing.T) {
	t.Parallel()
	request := askUserRequestPayload(t,
		map[string]any{"index": 1, "question": "Which color?", "options": []string{"Blue", "Red"}},
		map[string]any{"index": 1, "question": "Which size?", "options": []string{"Small", "Large"}},
	)
	approval := resolve(request, decision(t, "droid-ask-1", map[string]any{
		"behavior": "allow", "answers": []map[string]any{{"index": 1, "answer": "Blue"}},
	}))
	require.True(t, approval.Withhold)
	assert.EqualError(t, approval.Refusal(), "two questions state the index 1")

	refusal := resolve(request, decision(t, "droid-ask-1", map[string]any{"behavior": "deny"}))
	_, result := replyResult(t, refusal)
	assert.JSONEq(t, `{"cancelled":true,"answers":[]}`, string(result))
}

// Factory's SDK refuses to send an option that the request did not offer, and
// cancels the request instead. The worker refuses such an option the same way,
// so the reader can answer again, rather than forward a choice Droid never gave.
func TestResolveControlResponseRefusesAnUnofferedOption(t *testing.T) {
	t.Parallel()
	request := permissionRequestPayload(t, "Edit", contracts.DroidConfirmationTypeEdit,
		contracts.DroidPermissionOptionProceedOnce, contracts.DroidPermissionOptionCancel)
	cases := map[string]string{
		"an option of the contract": contracts.DroidPermissionOptionProceedAlways,
		"an option of no contract":  "proceed_sometimes",
	}
	for name, option := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resolution := resolve(request, decision(t, "droid-perm-1", map[string]any{"behavior": "allow", "selectedOption": option}))
			require.True(t, resolution.Withhold)
			assert.EqualError(t, resolution.Refusal(), agent.RefusalUnofferedOption(leapmuxv1.AgentProvider_AGENT_PROVIDER_DROID, option))
		})
	}
}

// Allow becomes proceed_once, which a request can leave out of its options. The
// worker refuses the approval then, rather than send an option Droid never gave.
func TestResolveControlResponseRefusesAnApprovalThatTheRequestDoesNotOffer(t *testing.T) {
	t.Parallel()
	request := permissionRequestPayload(t, "Edit", contracts.DroidConfirmationTypeEdit,
		contracts.DroidPermissionOptionProceedAlways, contracts.DroidPermissionOptionCancel)
	resolution := resolve(request, decision(t, "droid-perm-1", map[string]any{"behavior": "allow"}))
	require.True(t, resolution.Withhold)
	assert.EqualError(t, resolution.Refusal(), agent.RefusalUnofferedOption(leapmuxv1.AgentProvider_AGENT_PROVIDER_DROID, contracts.DroidPermissionOptionProceedOnce))

	none := resolve(permissionRequestPayload(t, "Edit", contracts.DroidConfirmationTypeEdit),
		decision(t, "droid-perm-1", map[string]any{"behavior": "allow"}))
	require.True(t, none.Withhold, "a request that offers no option offers no approval")
	assert.EqualError(t, none.Refusal(), agent.RefusalUnofferedOption(leapmuxv1.AgentProvider_AGENT_PROVIDER_DROID, contracts.DroidPermissionOptionProceedOnce))
}

// Droid's reply schema requires `editedSpecContent` beside proceed_edit, and
// fails the reply without it. The worker sends no edited spec, so it refuses the
// option even when the spec review offers it.
func TestResolveControlResponseRefusesAnEditedSpecApproval(t *testing.T) {
	t.Parallel()
	request := permissionRequestPayload(t, "ExitSpecMode", contracts.DroidConfirmationTypeExitSpecMode,
		contracts.DroidPermissionOptionProceedOnce, contracts.DroidPermissionOptionProceedEdit, contracts.DroidPermissionOptionCancel)
	resolution := resolve(request, decision(t, "droid-perm-1", map[string]any{
		"behavior": "allow", "selectedOption": contracts.DroidPermissionOptionProceedEdit,
	}))
	require.True(t, resolution.Withhold)
	assert.EqualError(t, resolution.Refusal(), "the option proceed_edit requires an edited spec, which LeapMux does not send")
}

func TestResolveControlResponseForwardsAnOfferedOption(t *testing.T) {
	t.Parallel()
	request := permissionRequestPayload(t, "Edit", contracts.DroidConfirmationTypeEdit,
		contracts.DroidPermissionOptionProceedOnce, contracts.DroidPermissionOptionProceedAlways, contracts.DroidPermissionOptionCancel)
	resolution := resolve(request, decision(t, "droid-perm-1", map[string]any{
		"behavior": "allow", "selectedOption": contracts.DroidPermissionOptionProceedAlways,
	}))
	_, result := replyResult(t, resolution)
	assert.JSONEq(t, `{"selectedOption":"proceed_always"}`, string(result))
}

// cancel is the answer that Factory's SDK sends for every failure, offered or
// not, so a rejection always reaches Droid.
func TestResolveControlResponseAlwaysSendsACancel(t *testing.T) {
	t.Parallel()
	for name, options := range map[string][]string{
		"offered":     {contracts.DroidPermissionOptionProceedOnce, contracts.DroidPermissionOptionCancel},
		"not offered": {contracts.DroidPermissionOptionProceedOnce},
		"no option":   nil,
	} {
		request := permissionRequestPayload(t, "Edit", contracts.DroidConfirmationTypeEdit, options...)
		for _, fields := range []map[string]any{
			{"behavior": "deny"},
			{"behavior": "deny", "selectedOption": contracts.DroidPermissionOptionCancel},
		} {
			resolution := resolve(request, decision(t, "droid-perm-1", fields))
			require.Falsef(t, resolution.Withhold, "cancel %s, decision %v", name, fields)
			_, result := replyResult(t, resolution)
			assert.JSONEqf(t, `{"selectedOption":"cancel"}`, string(result), "cancel %s, decision %v", name, fields)
		}
	}
}

// A decision whose option contradicts its behavior states no one choice, so the
// worker refuses it rather than guess which half the reader meant.
func TestResolveControlResponseRefusesAnOptionThatContradictsTheBehavior(t *testing.T) {
	t.Parallel()
	request := permissionRequestPayload(t, "Edit", contracts.DroidConfirmationTypeEdit,
		contracts.DroidPermissionOptionProceedOnce, contracts.DroidPermissionOptionCancel)
	for name, fields := range map[string]map[string]any{
		"a deny that selects an approval":  {"behavior": "deny", "selectedOption": contracts.DroidPermissionOptionProceedOnce},
		"an allow that selects the cancel": {"behavior": "allow", "selectedOption": contracts.DroidPermissionOptionCancel},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resolution := resolve(request, decision(t, "droid-perm-1", fields))
			require.True(t, resolution.Withhold)
			option, _ := fields["selectedOption"].(string)
			assert.EqualError(t, resolution.Refusal(), "the option "+option+" contradicts the decision")
		})
	}
}

// Only allow and deny are decisions. Anything else is refused, rather than read
// as a rejection that the reader did not give.
func TestResolveControlResponseRefusesAnUnknownBehavior(t *testing.T) {
	t.Parallel()
	for _, behavior := range []string{"", "maybe"} {
		permission := resolve(permissionRequestPayload(t, "Edit", contracts.DroidConfirmationTypeEdit,
			contracts.DroidPermissionOptionProceedOnce, contracts.DroidPermissionOptionCancel),
			decision(t, "droid-perm-1", map[string]any{"behavior": behavior}))
		require.Truef(t, permission.Withhold, "a permission decision with the behavior %q", behavior)
		assert.EqualError(t, permission.Refusal(), agent.RefusalNoDecision)

		question := resolve(askUserRequestPayload(t, colorAndSizeQuestions()...),
			decision(t, "droid-ask-1", map[string]any{"behavior": behavior}))
		require.Truef(t, question.Withhold, "a question decision with the behavior %q", behavior)
		assert.EqualError(t, question.Refusal(), agent.RefusalNoDecision)
	}
}

// The worker publishes two kinds of request. A stored request of any other kind
// is not one the worker can answer.
func TestResolveControlResponseRefusesAnUnknownRequestType(t *testing.T) {
	t.Parallel()
	request := []byte(`{"type":"pick_options_request","requestId":"droid-pick-1","rpcId":"rpc-44","options":["proceed_once","cancel"]}`)
	resolution := resolve(request, decision(t, "droid-pick-1", map[string]any{"behavior": "allow"}))
	require.True(t, resolution.Withhold)
	assert.EqualError(t, resolution.Refusal(), agent.RefusalUnreadableRequest)
}
