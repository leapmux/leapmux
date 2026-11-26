package droid

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
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

// decision is the neutral control response around the fields of one decision,
// as AnswerControlRequest builds it.
func decision(t *testing.T, requestID string, fields map[string]any) []byte {
	t.Helper()
	content, err := json.Marshal(map[string]any{
		"response": map[string]any{"request_id": requestID, "response": fields},
	})
	require.NoError(t, err)
	return content
}

// resolve resolves the decision of fields to the request stored as requestID,
// as the Worker does. The decision addresses that request, as each answer of
// AnswerControlRequest does, and it carries no plan settings.
func resolve(t *testing.T, requestPayload []byte, requestID string, fields map[string]any) agent.ControlResponseResolution {
	t.Helper()
	return agenttest.ResolveAnswer(droidProvider{}, requestPayload, requestID, agent.ControlAnswer{Content: decision(t, requestID, fields)})
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
	resolution := resolve(t, request, "droid-perm-1", map[string]any{"behavior": "allow"})
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
	answer, err := agenttest.AnswerControl(droidProvider{}, request, "droid-perm-exit-1", agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}))
	require.NoError(t, err)
	resolution := agenttest.ResolveAnswer(droidProvider{}, request, "droid-perm-exit-1", answer)
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

// A plan decision reaches Droid as the approval option of its permission
// mode. The Worker resolves the plan answer of AnswerControlRequest, which
// carries the settings of an approval and none for a refusal.
func TestResolvePlanApprovalSelectsNativeAutonomy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, mode, want     string
		deny, clear, noOffer bool
		mission              bool
		refusal              string
	}{
		{name: "unchanged", want: "proceed_once"},
		{name: "default", mode: "default", want: "proceed_once"},
		{name: "low", mode: "auto-low", want: "proceed_auto_run_low"},
		{name: "medium", mode: "auto-medium", want: "proceed_auto_run_medium"},
		{name: "high", mode: "auto-high", want: "proceed_auto_run_high"},
		{name: "reject keeps cancellation", deny: true, noOffer: true, want: "cancel"},
		{name: "clear context uses the service path", mode: "auto-medium", clear: true, want: "proceed_once"},
		{name: "unoffered policy", mode: "auto-medium", noOffer: true, refusal: "Factory Droid did not offer proceed_auto_run_medium"},
		{name: "unknown policy", mode: "other", refusal: "the Factory Droid session cannot approve a plan with permission mode \"other\""},
		{name: "spec is not an approval policy", mode: "spec", refusal: "the Factory Droid session cannot approve a plan with permission mode \"spec\""},
		{name: "a mission proposal takes no permission mode", mode: "auto-medium", mission: true, refusal: "this Factory Droid confirmation cannot apply the selected plan permission mode"},
		{name: "a mission proposal approves without a permission mode", mission: true, want: "proceed_once"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tool, confirmation := contracts.DroidToolExitSpecMode, contracts.DroidConfirmationTypeExitSpecMode
			if tc.mission {
				tool, confirmation = contracts.DroidToolProposeMission, contracts.DroidConfirmationTypeProposeMission
			}
			offered := []string{"proceed_once", "proceed_auto_run_low", "proceed_auto_run_medium", "proceed_auto_run_high", "cancel"}
			if tc.noOffer {
				offered = []string{"proceed_once"}
			}
			plan := &leapmuxv1.PlanDecision{Approve: !tc.deny, PermissionMode: tc.mode, ClearContext: tc.clear}
			payload := permissionRequestPayload(t, tool, confirmation, offered...)
			answer, err := agenttest.AnswerControl(droidProvider{}, payload, "droid-perm-1", agenttest.PlanDecision(plan))
			require.NoError(t, err)
			resolution := agenttest.ResolveAnswer(droidProvider{}, payload, "droid-perm-1", answer)
			if tc.refusal != "" {
				require.True(t, resolution.Withhold)
				assert.EqualError(t, resolution.Refusal(), tc.refusal)
				assert.Empty(t, resolution.Content)
				return
			}
			id, raw := replyResult(t, resolution)
			assert.Equal(t, "rpc-42", id)
			var reply contracts.DroidPermissionResult
			require.NoError(t, json.Unmarshal(raw, &reply))
			assert.Equal(t, tc.want, reply.SelectedOption)
		})
	}
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
			resolution := resolve(t, request, "droid-perm-1", map[string]any{
				"behavior": "deny", "message": "  Use a dry run first.  ",
			})
			_, result := replyResult(t, resolution)
			assert.JSONEq(t, `{"selectedOption":"cancel"}`, string(result),
				"the reply is Droid's own cancel; Droid discards a comment beside it")
			assert.Equal(t, "Use a dry run first.", resolution.FollowUp.Text)
			assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK, resolution.FollowUp.Kind)
			assert.Equal(t, tc.planModeControl, resolution.PlanModeControl)
		})
	}
}

// A question has no field for a reason either: Droid's reply is
// `{cancelled, answers}`. The reason follows as Feedback.
func TestResolveControlResponseSendsAQuestionRefusalReasonAsFeedback(t *testing.T) {
	t.Parallel()
	resolution := resolve(t, askUserRequestPayload(t, colorAndSizeQuestions()...), "droid-ask-1", map[string]any{
		"behavior": "deny", "message": "Ask about the shape instead.",
	})
	id, result := replyResult(t, resolution)
	assert.Equal(t, "rpc-43", id)
	assert.JSONEq(t, `{"cancelled":true,"answers":[]}`, string(result), "Droid's own cancel carries no answer")
	assert.Equal(t, "Ask about the shape instead.", resolution.FollowUp.Text)
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK, resolution.FollowUp.Kind)
}

// DenyResponse fills the reason of a bare rejection with a placeholder. That is
// no reason, so it must not reach the model as the user's words.
func TestResolveControlResponseSendsNoFeedbackForABareRejection(t *testing.T) {
	t.Parallel()
	for _, message := range []string{contracts.ControlAnswerMessageRejectedByUser, "", "   "} {
		permission := resolve(t, permissionRequestPayload(t, "Edit", contracts.DroidConfirmationTypeEdit, contracts.DroidPermissionOptionCancel), "droid-perm-1", map[string]any{"behavior": "deny", "message": message})
		_, result := replyResult(t, permission)
		assert.JSONEq(t, `{"selectedOption":"cancel"}`, string(result))
		assert.Emptyf(t, permission.FollowUp, "a permission rejection with the message %q has no reason", message)

		question := resolve(t, askUserRequestPayload(t, colorAndSizeQuestions()...), "droid-ask-1", map[string]any{"behavior": "deny", "message": message})
		_, result = replyResult(t, question)
		assert.JSONEq(t, `{"cancelled":true,"answers":[]}`, string(result))
		assert.Emptyf(t, question.FollowUp, "a question refusal with the message %q has no reason", message)
	}
}

// An approval has no reason to send, so a stray message beside it does not reach
// the model.
func TestResolveControlResponseSendsNoFeedbackForAnApproval(t *testing.T) {
	t.Parallel()
	resolution := resolve(t, permissionRequestPayload(t, "Edit", contracts.DroidConfirmationTypeEdit, contracts.DroidPermissionOptionProceedOnce), "droid-perm-1", map[string]any{"behavior": "allow", "message": "Go ahead."})
	_, result := replyResult(t, resolution)
	assert.JSONEq(t, `{"selectedOption":"proceed_once"}`, string(result))
	assert.Empty(t, resolution.FollowUp)
}

// Droid identifies each answer by the `index` of its question. The reply lists
// the answers in Droid's own question order, with Droid's own numbers and
// question words, whatever order the answer lists them in.
func TestResolveControlResponseAnswersEachQuestionByItsNativeIndex(t *testing.T) {
	t.Parallel()
	resolution := resolve(t, askUserRequestPayload(t, colorAndSizeQuestions()...), "droid-ask-1", map[string]any{
		"behavior": "allow",
		"answers": []map[string]any{
			{"index": 2, "answer": "Small, Large"},
			{"index": 1, "answer": "Blue"},
		},
	})
	id, result := replyResult(t, resolution)
	assert.Equal(t, "rpc-43", id)
	assert.JSONEq(t, `{"cancelled":false,"answers":[
		{"index":1,"question":"Which color?","answer":"Blue"},
		{"index":2,"question":"Which sizes?","answer":"Small, Large"}
	]}`, string(result))
	assert.Empty(t, resolution.FollowUp)
}

// The numbers are Droid's own: the reply copies them, and never renumbers the
// questions from 0 or from 1.
func TestResolveControlResponseKeepsANativeIndexThatDoesNotStartAtOne(t *testing.T) {
	t.Parallel()
	request := askUserRequestPayload(t,
		map[string]any{"index": 7, "question": "Seventh?", "options": []string{"Yes", "No"}},
		map[string]any{"index": 3, "question": "Third?", "options": []string{"Yes", "No"}},
	)
	resolution := resolve(t, request, "droid-ask-1", map[string]any{
		"behavior": "allow",
		"answers":  []map[string]any{{"index": 3, "answer": "No"}, {"index": 7, "answer": "Yes"}},
	})
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
	resolution := resolve(t, askUserRequestPayload(t, colorAndSizeQuestions()...), "droid-ask-1", map[string]any{
		"behavior": "allow",
		"answers":  []map[string]any{{"index": 1, "answer": ""}, {"index": 2, "answer": "Large"}},
	})
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
			resolution := resolve(t, askUserRequestPayload(t, colorAndSizeQuestions()...), "droid-ask-1", map[string]any{
				"behavior": "allow", "answers": tc.answers,
			})
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
		"null answers field":         {map[string]any{"behavior": "allow", "answers": nil}, "the decision holds no answer list"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resolution := resolve(t, askUserRequestPayload(t, colorAndSizeQuestions()...), "droid-ask-1", tc.fields)
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
	approval := resolve(t, request, "droid-ask-1", map[string]any{
		"behavior": "allow", "answers": []map[string]any{{"index": 1, "answer": "Blue"}},
	})
	require.True(t, approval.Withhold)
	assert.EqualError(t, approval.Refusal(), "two questions state the index 1")

	refusal := resolve(t, request, "droid-ask-1", map[string]any{"behavior": "deny"})
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
			resolution := resolve(t, request, "droid-perm-1", map[string]any{"behavior": "allow", "selectedOption": option})
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
	resolution := resolve(t, request, "droid-perm-1", map[string]any{"behavior": "allow"})
	require.True(t, resolution.Withhold)
	assert.EqualError(t, resolution.Refusal(), agent.RefusalUnofferedOption(leapmuxv1.AgentProvider_AGENT_PROVIDER_DROID, contracts.DroidPermissionOptionProceedOnce))

	none := resolve(t, permissionRequestPayload(t, "Edit", contracts.DroidConfirmationTypeEdit), "droid-perm-1", map[string]any{"behavior": "allow"})
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
	resolution := resolve(t, request, "droid-perm-1", map[string]any{
		"behavior": "allow", "selectedOption": contracts.DroidPermissionOptionProceedEdit,
	})
	require.True(t, resolution.Withhold)
	assert.EqualError(t, resolution.Refusal(), "the option proceed_edit requires an edited spec, which LeapMux does not send")
}

func TestResolveControlResponseForwardsAnOfferedOption(t *testing.T) {
	t.Parallel()
	request := permissionRequestPayload(t, "Edit", contracts.DroidConfirmationTypeEdit,
		contracts.DroidPermissionOptionProceedOnce, contracts.DroidPermissionOptionProceedAlways, contracts.DroidPermissionOptionCancel)
	resolution := resolve(t, request, "droid-perm-1", map[string]any{
		"behavior": "allow", "selectedOption": contracts.DroidPermissionOptionProceedAlways,
	})
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
			resolution := resolve(t, request, "droid-perm-1", fields)
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
			resolution := resolve(t, request, "droid-perm-1", fields)
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
		permission := resolve(t, permissionRequestPayload(t, "Edit", contracts.DroidConfirmationTypeEdit,
			contracts.DroidPermissionOptionProceedOnce, contracts.DroidPermissionOptionCancel), "droid-perm-1", map[string]any{"behavior": behavior})
		require.Truef(t, permission.Withhold, "a permission decision with the behavior %q", behavior)
		assert.EqualError(t, permission.Refusal(), agent.RefusalNoDecision)

		question := resolve(t, askUserRequestPayload(t, colorAndSizeQuestions()...), "droid-ask-1", map[string]any{"behavior": behavior})
		require.Truef(t, question.Withhold, "a question decision with the behavior %q", behavior)
		assert.EqualError(t, question.Refusal(), agent.RefusalNoDecision)
	}
}

// The worker publishes two kinds of request. A stored request of any other kind
// is not one the worker can answer.
func TestResolveControlResponseRefusesAnUnknownRequestType(t *testing.T) {
	t.Parallel()
	request := []byte(`{"type":"pick_options_request","requestId":"droid-pick-1","rpcId":"rpc-44","options":["proceed_once","cancel"]}`)
	resolution := resolve(t, request, "droid-pick-1", map[string]any{"behavior": "allow"})
	require.True(t, resolution.Withhold)
	assert.EqualError(t, resolution.Refusal(), agent.RefusalUnreadableRequest)
}

func TestDroidControlPromptAndResolverConsistency(t *testing.T) {
	t.Parallel()
	t.Run("proceed_edit is not offered and is refused", func(t *testing.T) {
		t.Parallel()
		payload := permissionRequestPayload(t, "Edit", contracts.DroidConfirmationTypeEdit,
			contracts.DroidPermissionOptionProceedOnce, contracts.DroidPermissionOptionProceedEdit, contracts.DroidPermissionOptionCancel)
		prompt := agenttest.ControlPromptOf(droidProvider{}, payload)
		require.NotNil(t, prompt.GetPermission())
		var optionIDs []string
		for _, opt := range prompt.GetPermission().GetOptions() {
			optionIDs = append(optionIDs, opt.GetOptionId())
		}
		assert.Equal(t, []string{contracts.DroidPermissionOptionProceedOnce, contracts.DroidPermissionOptionCancel}, optionIDs)
		_, err := agenttest.AnswerControl(droidProvider{}, payload, "droid-perm-1",
			agenttest.PermissionDecision(contracts.DroidPermissionOptionProceedEdit, ""))
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
	})

	t.Run("confirmationType classifies plan in both ReadControlRequest and resolver", func(t *testing.T) {
		t.Parallel()
		payload := permissionRequestPayload(t, "CustomReview", contracts.DroidConfirmationTypeExitSpecMode,
			contracts.DroidPermissionOptionProceedOnce, contracts.DroidPermissionOptionProceedAutoRunHigh, contracts.DroidPermissionOptionCancel)
		prompt := agenttest.ControlPromptOf(droidProvider{}, payload)
		require.NotNil(t, prompt.GetPlan())
		answer, err := agenttest.AnswerControl(droidProvider{}, payload, "droid-perm-1",
			agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, PermissionMode: contracts.DroidModeAutoHigh}))
		require.NoError(t, err)
		resolution := agenttest.ResolveAnswer(droidProvider{}, payload, "droid-perm-1", answer)
		require.NoError(t, resolution.Refusal())
		assert.Equal(t, agent.PlanModeControlExit, resolution.PlanModeControl)
		_, raw := replyResult(t, resolution)
		assert.JSONEq(t, `{"selectedOption":"proceed_auto_run_high"}`, string(raw))
	})

	t.Run("custom tool with propose_mission confirmationType classifies as plan", func(t *testing.T) {
		t.Parallel()
		payload, err := json.Marshal(map[string]any{
			"type":             contracts.DroidRequestTypePermission,
			"requestId":        "droid-perm-mission",
			"rpcId":            "rpc-mission",
			"toolUse":          map[string]any{"type": "tool_use", "id": "m1", "name": "CustomProposal", "input": map[string]any{}},
			"confirmationType": contracts.DroidConfirmationTypeProposeMission,
			"details":          map[string]any{"type": contracts.DroidConfirmationTypeProposeMission, "proposal": "# Proposal"},
			"options":          []string{contracts.DroidPermissionOptionProceedOnce, contracts.DroidPermissionOptionProceedAutoRunHigh, contracts.DroidPermissionOptionCancel},
		})
		require.NoError(t, err)

		prompt := agenttest.ControlPromptOf(droidProvider{}, payload)
		require.NotNil(t, prompt.GetPlan())
		assert.Equal(t, "# Proposal", prompt.GetPlan().GetText())

		withMode, err := agenttest.AnswerControl(droidProvider{}, payload, "droid-perm-mission",
			agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, PermissionMode: contracts.DroidModeAutoHigh}))
		require.NoError(t, err)
		refused := agenttest.ResolveAnswer(droidProvider{}, payload, "droid-perm-mission", withMode)
		assert.True(t, refused.Withhold)
		assert.Equal(t, agent.PlanModeControlExit, refused.PlanModeControl)
		assert.EqualError(t, refused.Refusal(), "this Factory Droid confirmation cannot apply the selected plan permission mode")

		withoutMode, err := agenttest.AnswerControl(droidProvider{}, payload, "droid-perm-mission",
			agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}))
		require.NoError(t, err)
		approved := agenttest.ResolveAnswer(droidProvider{}, payload, "droid-perm-mission", withoutMode)
		require.NoError(t, approved.Refusal())
		assert.Equal(t, agent.PlanModeControlExit, approved.PlanModeControl)
		id, raw := replyResult(t, approved)
		assert.Equal(t, "rpc-mission", id)
		assert.JSONEq(t, `{"selectedOption":"proceed_once"}`, string(raw))
	})

	t.Run("plan tool names with non-plan confirmationType stay permissions", func(t *testing.T) {
		t.Parallel()
		for _, toolName := range []string{contracts.DroidToolExitSpecMode, contracts.DroidToolProposeMission} {
			for _, confirmationType := range []string{contracts.DroidConfirmationTypeEdit, ""} {
				t.Run(toolName+"/"+confirmationType, func(t *testing.T) {
					t.Parallel()
					payload := permissionRequestPayload(t, toolName, confirmationType,
						contracts.DroidPermissionOptionProceedOnce, contracts.DroidPermissionOptionCancel)
					prompt := agenttest.ControlPromptOf(droidProvider{}, payload)
					require.NotNil(t, prompt.GetPermission())
					assert.Nil(t, prompt.GetPlan())
					assert.Equal(t, toolName, prompt.GetPermission().GetTitle())

					answer, err := agenttest.AnswerControl(droidProvider{}, payload, "droid-perm-1",
						agenttest.PermissionDecision(contracts.DroidPermissionOptionProceedOnce, ""))
					require.NoError(t, err)
					resolution := agenttest.ResolveAnswer(droidProvider{}, payload, "droid-perm-1", answer)
					require.NoError(t, resolution.Refusal())
					assert.Equal(t, agent.PlanModeControlNone, resolution.PlanModeControl)
					_, raw := replyResult(t, resolution)
					assert.JSONEq(t, `{"selectedOption":"proceed_once"}`, string(raw))
				})
			}
		}
	})

	t.Run("empty options offers only deny and resolves empty feedback to cancel", func(t *testing.T) {
		t.Parallel()
		payload := permissionRequestPayload(t, "Edit", contracts.DroidConfirmationTypeEdit)
		options := agenttest.ControlPromptOf(droidProvider{}, payload).GetPermission().GetOptions()
		require.Len(t, options, 1)
		assert.Equal(t, contracts.ControlAnswerOptionDeny, options[0].GetOptionId())
		_, err := agenttest.AnswerControl(droidProvider{}, payload, "droid-perm-1",
			agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)

		denyAnswer, err := agenttest.AnswerControl(droidProvider{}, payload, "droid-perm-1",
			agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, ""))
		require.NoError(t, err)
		resolution := agenttest.ResolveAnswer(droidProvider{}, payload, "droid-perm-1", denyAnswer)
		require.NoError(t, resolution.Refusal())
		_, raw := replyResult(t, resolution)
		assert.JSONEq(t, `{"selectedOption":"cancel"}`, string(raw))
		assert.Empty(t, resolution.FollowUp)
	})
}

func TestDecodeControlInnerResponseAndMalformedInnerRefusal(t *testing.T) {
	t.Parallel()
	assert.JSONEq(t, `{}`, string(agent.DecodeControlInnerResponse([]byte(`{"response":{"request_id":"droid-perm-1"}}`))))
	assert.JSONEq(t, `{}`, string(agent.DecodeControlInnerResponse([]byte(`{"response":{"request_id":"droid-perm-1","response":{}}}`))))
	assert.JSONEq(t, `{}`, string(agent.DecodeControlInnerResponse([]byte(`not json`))))
	assert.Equal(t, `"not-an-object"`, string(agent.DecodeControlInnerResponse([]byte(`{"response":{"request_id":"droid-perm-1","response":"not-an-object"}}`))))

	offered := []string{contracts.DroidPermissionOptionProceedOnce, contracts.DroidPermissionOptionCancel}
	allowed, err := droidPermissionReply(offered, true, json.RawMessage(`{}`), "")
	require.NoError(t, err)
	assert.Equal(t, contracts.DroidPermissionOptionProceedOnce, allowed.SelectedOption)

	denied, err := droidPermissionReply(offered, false, json.RawMessage(`{}`), "")
	require.NoError(t, err)
	assert.Equal(t, contracts.DroidPermissionOptionCancel, denied.SelectedOption)

	for _, malformed := range []string{`"not-an-object"`, `[]`, `123`, `{"selectedOption":42}`} {
		_, err := droidPermissionReply(offered, true, json.RawMessage(malformed), "")
		assert.EqualError(t, err, agent.RefusalUnreadableAnswer, malformed)
	}

	questions := []droidStoredQuestion{{Index: 1, Question: "Which color?"}}
	for _, malformed := range []string{`"not-an-object"`, `[]`, `123`} {
		_, err := droidAnswersInQuestionOrder(questions, json.RawMessage(malformed))
		assert.EqualError(t, err, agent.RefusalUnreadableAnswer, malformed)
	}

	// Through droidResolveControlResponse, an unreadable selectedOption inside an
	// allow envelope is refused as unreadable rather than silently defaulted.
	permReq := permissionRequestPayload(t, "Edit", contracts.DroidConfirmationTypeEdit, offered...)
	resolution := droidResolveControlResponse(agent.ControlResponseContext{
		RequestID:       "droid-perm-1",
		RequestPayload:  permReq,
		ResponseContent: []byte(`{"response":{"request_id":"droid-perm-1","response":{"behavior":"allow","selectedOption":7}}}`),
	})
	assert.True(t, resolution.Withhold)
	assert.EqualError(t, resolution.Refusal(), agent.RefusalUnreadableAnswer)
}
