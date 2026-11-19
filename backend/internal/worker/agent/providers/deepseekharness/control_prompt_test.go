package deepseekharness

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// deepseekHarnessTestRequestID is the stored id of the request that
// deepseekHarnessAnswer answers.
const deepseekHarnessTestRequestID = "native-event"

func deepseekHarnessJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}

func deepseekHarnessPrompt(t *testing.T, payload any) *leapmuxv1.ControlPrompt {
	t.Helper()
	return agenttest.ControlPromptOf(deepseekHarnessProvider{}, deepseekHarnessJSON(t, payload))
}

func deepseekHarnessApproval(request any) map[string]any {
	return map[string]any{"event": contracts.DeepseekHarnessControlEventApproval, "request": request}
}

func deepseekHarnessUserQuestions(questions any) map[string]any {
	return map[string]any{"event": contracts.DeepseekHarnessControlEventUserQuestions, "request": map[string]any{"questions": questions}}
}

// deepseekHarnessPlanReview is a user-questions request whose one question
// reviews a plan.
func deepseekHarnessPlanReview(options []any) map[string]any {
	return deepseekHarnessUserQuestions([]any{map[string]any{
		"id": "plan-review", "question": "Approve?", "detail": "# Native plan",
		"intent":  map[string]any{"kind": "plan-review", "approve": "Approve", "callId": "native-plan"},
		"options": options,
	}})
}

func deepseekHarnessPlanOptions() []any {
	return []any{map[string]any{"label": "Approve"}, map[string]any{"label": "Keep planning"}}
}

func TestReadControlRequest_DeepseekHarnessApprovalOffersOnlyTheNativeChoices(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Title:  "bash",
		Reason: "Approve wider file access.",
		Options: []*leapmuxv1.PermissionOption{
			{OptionId: "allow", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow once"},
			{OptionId: "deny", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Deny"},
		},
	}}}, deepseekHarnessPrompt(t, deepseekHarnessApproval(map[string]any{"toolName": "bash", "callId": "native-call", "reason": "Approve wider file access."})))
}

// An Auto review denial states its English display text beside its audit
// record, and the native client shows the display text.
func TestReadControlRequest_DeepseekHarnessPrefersTheDisplayReason(t *testing.T) {
	permission := deepseekHarnessPrompt(t, deepseekHarnessApproval(map[string]any{
		"toolName": "bash", "callId": "native-call",
		"reason":        `Auto review denied tool "bash": The removal is not authorized.`,
		"displayReason": map[string]any{"en": "Auto review denied this call: The removal is not authorized.", "zh": "Auto review 拒绝了此调用：The removal is not authorized."},
	})).GetPermission()
	assert.Equal(t, "bash", permission.GetTitle())
	assert.Equal(t, "Auto review denied this call: The removal is not authorized.", permission.GetReason())
}

func TestReadControlRequest_DeepseekHarnessFallsBackToTheAuditReason(t *testing.T) {
	for name, displayReason := range map[string]any{
		"no English display text":            map[string]any{"zh": "允许本次操作"},
		"an empty English display text":      map[string]any{"en": ""},
		"a display reason that is no object": "Allow this operation.",
	} {
		permission := deepseekHarnessPrompt(t, deepseekHarnessApproval(map[string]any{
			"toolName": "bash", "callId": "native-call",
			"reason":        "escalate sandbox to danger-full-access: wider access",
			"displayReason": displayReason,
		})).GetPermission()
		assert.Equal(t, "escalate sandbox to danger-full-access: wider access", permission.GetReason(), name)
	}
}

func TestReadControlRequest_DeepseekHarnessStatesNoReasonWhenTheRequestHoldsNone(t *testing.T) {
	permission := deepseekHarnessPrompt(t, deepseekHarnessApproval(map[string]any{
		"toolName": "bash", "callId": "native-call", "displayReason": map[string]any{"en": ""},
	})).GetPermission()
	assert.Equal(t, "bash", permission.GetTitle())
	assert.Empty(t, permission.GetReason())
	assert.Empty(t, permission.GetInputJson(), "the approval states no arguments")
}

func TestReadControlRequest_DeepseekHarnessReadsTheNativePlanAndItsExactChoices(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{
		Text: "# Native plan",
		Choices: []*leapmuxv1.PlanChoice{
			{Id: "Approve", Label: "Approve", Effect: leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_APPROVE},
			{Id: "Keep planning", Label: "Keep planning", Effect: leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_REFUSE},
		},
	}}}, deepseekHarnessPrompt(t, deepseekHarnessPlanReview(deepseekHarnessPlanOptions())))
}

// A plan review is a plan, never a question, even when it offers no choice.
func TestReadControlRequest_DeepseekHarnessRoutesAPlanReviewToThePlan(t *testing.T) {
	got := deepseekHarnessPrompt(t, deepseekHarnessUserQuestions([]any{map[string]any{
		"id": "plan-review", "question": "Approve the plan?",
		"intent":  map[string]any{"kind": "plan-review", "approve": "Approve", "callId": "native-plan"},
		"options": []any{},
	}}))
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{}}}, got)

	plan, ok := deepseekHarnessPlanPrompt([]map[string]json.RawMessage{{
		"intent":  json.RawMessage(`{"kind":"plan-review","approve":"Go"}`),
		"options": json.RawMessage(`[null,"Go",{"label":""},{"label":"Go"},{"label":"Stop"}]`),
	}})
	require.True(t, ok)
	assert.Equal(t, []string{"Go", "Stop"}, []string{plan.GetChoices()[0].GetId(), plan.GetChoices()[1].GetId()}, "an option with no label offers no choice")
	assert.Equal(t, leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_APPROVE, plan.GetChoices()[0].GetEffect())
}

// A plan review beside another question is neither a question nor a plan,
// and the reader falls back to the generic prompt.
func TestReadControlRequest_DeepseekHarnessPlanReviewBesideAnotherQuestionIsGeneric(t *testing.T) {
	agenttest.RequireProto(t, agent.GenericPermissionPrompt(), deepseekHarnessPrompt(t, deepseekHarnessUserQuestions([]any{
		map[string]any{"id": "plan-review", "question": "Approve?", "intent": map[string]any{"kind": "plan-review"}},
		map[string]any{"id": "other", "question": "Other?"},
	})))
}

func TestReadControlRequest_DeepseekHarnessRefusesAControlThatItCannotRead(t *testing.T) {
	for _, payload := range []any{
		map[string]any{},
		deepseekHarnessApproval(nil),
		deepseekHarnessApproval(map[string]any{"toolName": ""}),
		deepseekHarnessApproval(map[string]any{"toolName": 7}),
		map[string]any{"event": "other/request", "request": map[string]any{"toolName": "bash"}},
	} {
		agenttest.RequireProto(t, agent.GenericPermissionPrompt(), deepseekHarnessPrompt(t, payload))
	}
	agenttest.RequireProto(t, agent.GenericPermissionPrompt(), agenttest.ControlPromptOf(deepseekHarnessProvider{}, []byte("not json")))

	// The question recognizer claims a user-questions request first, so one
	// whose every record is unreadable is a question with nothing to answer.
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{}, PreservesSelectionNotes: true,
	}}}, deepseekHarnessPrompt(t, deepseekHarnessUserQuestions([]any{nil})))
	_, ok := deepseekHarnessPlanPrompt(nil)
	assert.False(t, ok, "a request with no readable question is no plan")
}

func deepseekHarnessOption(label string) *leapmuxv1.QuestionOption {
	return &leapmuxv1.QuestionOption{Value: &label, Label: label}
}

func TestReadControlRequest_DeepseekHarnessPreservesNativeQuestionIdsAndOptionLabels(t *testing.T) {
	first := deepseekHarnessOption("First")
	first.Description = "Use the first result."
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{{
			Id: "native-question", Question: "Choose a result.", Header: "Result", MultiSelect: true,
			Options: []*leapmuxv1.QuestionOption{first, deepseekHarnessOption("Second")},
		}},
		PreservesSelectionNotes: true,
	}}}, deepseekHarnessPrompt(t, deepseekHarnessUserQuestions([]any{map[string]any{
		"id": "native-question", "question": "Choose a result.", "header": "Result", "multiSelect": true,
		"options": []any{map[string]any{"label": "First", "description": "Use the first result."}, map[string]any{"label": "Second"}},
	}})))
}

// A question needs its id and its text. A payload that states no `request`
// object reads its own `questions`.
func TestReadControlRequest_DeepseekHarnessKeepsAFreeTextQuestionAndDropsMalformedRecords(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions:               []*leapmuxv1.ControlQuestion{{Id: "empty-options", Question: "Enter text.", Options: []*leapmuxv1.QuestionOption{}}},
		PreservesSelectionNotes: true,
	}}}, deepseekHarnessPrompt(t, map[string]any{"event": contracts.DeepseekHarnessControlEventUserQuestions, "questions": []any{
		nil,
		"invalid",
		map[string]any{"id": "empty-options", "question": "Enter text."},
		map[string]any{"question": "Missing ID"},
		map[string]any{"id": "missing-question"},
	}}))
	assert.Empty(t, deepseekHarnessPrompt(t, map[string]any{"event": contracts.DeepseekHarnessControlEventUserQuestions, "questions": nil}).GetQuestion().GetQuestions())
}

func deepseekHarnessAnswer(t *testing.T, payload any, decision *leapmuxv1.ControlDecision) (agent.ControlAnswer, error) {
	t.Helper()
	return agenttest.AnswerControl(deepseekHarnessProvider{}, deepseekHarnessJSON(t, payload), deepseekHarnessTestRequestID, decision)
}

func TestAnswerControlRequest_DeepseekHarnessApprovalSendsTheNativeDecision(t *testing.T) {
	approval := deepseekHarnessApproval(map[string]any{"toolName": "bash", "callId": "native-call"})
	got, err := deepseekHarnessAnswer(t, approval, agenttest.PermissionDecision("allow", ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"native-event","response":{"behavior":"allow"}}}`, string(got.Content),
		"the approval's allow is bare: the runtime reads it as allowed-once")
	assert.Empty(t, got.FollowUp)

	got, err = deepseekHarnessAnswer(t, approval, agenttest.PermissionDecision("deny", ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"native-event","response":{"behavior":"deny","message":"Rejected by user."}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp, "a refusal with no words sends no message")
}

// The runtime's outcome cannot carry the reason, so the composer's typed
// refusal also sends it as the user's next message, exactly as typed.
func TestAnswerControlRequest_DeepseekHarnessRefusalFeedbackAlsoFollowsAsAMessage(t *testing.T) {
	approval := deepseekHarnessApproval(map[string]any{"toolName": "bash", "callId": "native-call"})
	got, err := deepseekHarnessAnswer(t, approval, agenttest.PermissionDecision("deny", "  Use the read-only tool.\n"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"native-event","response":{"behavior":"deny","message":"  Use the read-only tool.\n"}}}`, string(got.Content))
	assert.Equal(t, "  Use the read-only tool.\n", got.FollowUp)

	got, err = deepseekHarnessAnswer(t, approval, agenttest.PermissionDecision("deny", " \t\n"))
	require.NoError(t, err)
	assert.Contains(t, string(got.Content), `"message":" \t\n"`)
	assert.Empty(t, got.FollowUp, "feedback with no words sends no message")

	_, err = deepseekHarnessAnswer(t, approval, agenttest.PermissionDecision("allow", "a note"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an approval carries no feedback")
}

// A payload that the reader does not recognize takes the shared Allow and
// Deny. Its allow sends the request's input back, and a typed refusal still
// follows as a message.
func TestAnswerControlRequest_DeepseekHarnessGenericPromptTakesTheSharedAnswers(t *testing.T) {
	payload := map[string]any{"event": "other/request", "request": map[string]any{"input": map[string]any{"a": 1}}}
	got, err := deepseekHarnessAnswer(t, payload, agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"native-event","response":{"behavior":"allow","updatedInput":{"a":1}}}}`, string(got.Content))

	got, err = deepseekHarnessAnswer(t, payload, agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, "No."))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"native-event","response":{"behavior":"deny","message":"No."}}}`, string(got.Content))
	assert.Equal(t, "No.", got.FollowUp)
}

func TestAnswerControlRequest_DeepseekHarnessAnswersAPlanReview(t *testing.T) {
	review := deepseekHarnessPlanReview(deepseekHarnessPlanOptions())
	got, err := deepseekHarnessAnswer(t, review, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, PermissionMode: contracts.DeepseekHarnessModeAct}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"native-event","response":{"behavior":"allow","updatedInput":{}}}}`, string(got.Content))
	assert.True(t, proto.Equal(&leapmuxv1.PlanApprovalSettings{PermissionMode: contracts.DeepseekHarnessModeAct}, got.PlanApproval))
	assert.Empty(t, got.FollowUp)

	got, err = deepseekHarnessAnswer(t, review, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "Approve"}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"native-event","response":{"behavior":"allow","updatedInput":{},"choice":"Approve"}}}`, string(got.Content))
	assert.NotNil(t, got.PlanApproval, "an approving choice carries the plan settings")

	got, err = deepseekHarnessAnswer(t, review, agenttest.PlanDecision(&leapmuxv1.PlanDecision{ChoiceId: "Keep planning"}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"native-event","response":{"behavior":"deny","message":"Rejected by user.","choice":"Keep planning"}}}`, string(got.Content))
	assert.Nil(t, got.PlanApproval)
	assert.Empty(t, got.FollowUp)

	got, err = deepseekHarnessAnswer(t, review, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Feedback: "Split step 1."}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"native-event","response":{"behavior":"deny","message":"Split step 1."}}}`, string(got.Content))
	assert.Equal(t, "Split step 1.", got.FollowUp, "the runtime's outcome cannot carry the feedback")

	_, err = deepseekHarnessAnswer(t, review, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "Keep planning"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the choice refuses the plan")
	_, err = deepseekHarnessAnswer(t, review, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "Later"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the review offers no such choice")
}

func deepseekHarnessTwoQuestions() map[string]any {
	return deepseekHarnessUserQuestions([]any{
		map[string]any{"id": "single", "question": "Choose one.", "options": []any{map[string]any{"label": "First"}, map[string]any{"label": "Second"}}},
		map[string]any{"id": "multi", "question": "Choose several.", "multiSelect": true, "options": []any{map[string]any{"label": "First"}, map[string]any{"label": "Second"}}},
	})
}

func TestAnswerControlRequest_DeepseekHarnessSendsNativeSelectionsAndCustomText(t *testing.T) {
	got, err := deepseekHarnessAnswer(t, deepseekHarnessTwoQuestions(), agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"First"}, CustomText: "  Native note.  "},
		&leapmuxv1.QuestionAnswer{Selected: []string{"First", "Second"}},
	))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"native-event","response":{"behavior":"allow","answers":[
		{"id":"single","selected":["First"],"custom":"Native note."},
		{"id":"multi","selected":["First","Second"]}
	]}}}`, string(got.Content))

	got, err = deepseekHarnessAnswer(t, deepseekHarnessTwoQuestions(), agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{CustomText: "Only text."},
		&leapmuxv1.QuestionAnswer{Selected: []string{"Second"}},
	))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"native-event","response":{"behavior":"allow","answers":[
		{"id":"single","selected":[],"custom":"Only text."},
		{"id":"multi","selected":["Second"]}
	]}}}`, string(got.Content), "typed text alone states an empty selection")

	_, err = deepseekHarnessAnswer(t, deepseekHarnessTwoQuestions(), agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{CustomText: "Only text."},
	))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an answer that leaves a question open")
}

// The browser dropped an option that a question does not offer and kept the
// first pick of a single choice. A decision states its answer exactly, so
// the Worker refuses both.
func TestAnswerControlRequest_DeepseekHarnessRefusesAnUnofferedOption(t *testing.T) {
	_, err := deepseekHarnessAnswer(t, deepseekHarnessTwoQuestions(), agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"unknown"}},
	))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
	_, err = deepseekHarnessAnswer(t, deepseekHarnessTwoQuestions(), agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"First", "Second"}},
	))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

func TestAnswerControlRequest_DeepseekHarnessCancelledQuestionDeniesWithItsFeedback(t *testing.T) {
	got, err := deepseekHarnessAnswer(t, deepseekHarnessTwoQuestions(), agenttest.CancelQuestions("User stopped"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"native-event","response":{"behavior":"deny","message":"User stopped"}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp, "the question's stop sends no message")
}

func TestAnswerControlRequest_DeepseekHarnessRefusesADecisionOfAnotherKind(t *testing.T) {
	_, err := deepseekHarnessAnswer(t, deepseekHarnessTwoQuestions(), agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
	_, err = deepseekHarnessAnswer(t, deepseekHarnessPlanReview(deepseekHarnessPlanOptions()), agenttest.QuestionDecision())
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
	_, err = deepseekHarnessAnswer(t, deepseekHarnessApproval(map[string]any{"toolName": "bash"}), agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

// The agent turns each answer into the runtime's waterfall outcome, so the
// answers here are ones that the agent reads.
func TestAnswerControlRequest_DeepseekHarnessAnswersReachTheRuntimeAsItsOutcome(t *testing.T) {
	outcomeOf := func(t *testing.T, event string, request map[string]any, decision *leapmuxv1.ControlDecision) map[string]any {
		t.Helper()
		sink := &agenttest.ControlSink{}
		a := newOfflineAgentServing(t, agent.NewProviderServices(sink), nil)
		var outcome map[string]any
		serveControlReplies(t, a, func(w http.ResponseWriter, call map[string]any) {
			payload, _ := call["payload"].(map[string]any)
			args, _ := payload["args"].(map[string]any)
			outcome, _ = args["outcome"].(map[string]any)
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": call["rpcId"], "result": map[string]any{"ok": true}}))
		})
		rawRequest := deepseekHarnessJSON(t, request)
		raw := deepseekHarnessJSON(t, map[string]any{"type": "waterfall", "event": event, "eventId": "native-event", "agentId": "native-root", "request": json.RawMessage(rawRequest)})
		require.NoError(t, a.publishNativeControl(event, "native-event", "native-root", rawRequest, raw))
		published := sink.LastPublishedControl()
		answer, err := agenttest.AnswerControl(deepseekHarnessProvider{}, published.Payload, published.RequestID, decision)
		require.NoError(t, err)
		require.NoError(t, agenttest.ResolveAnswer(deepseekHarnessProvider{}, published.Payload, published.RequestID, answer).Refusal())
		require.NoError(t, a.answerControl(answer.Content))
		return outcome
	}

	approval := map[string]any{"toolName": "bash", "callId": "call"}
	assert.Equal(t, map[string]any{"kind": "result", "value": "allowed-once"},
		outcomeOf(t, contracts.DeepseekHarnessControlEventApproval, approval, agenttest.PermissionDecision("allow", "")))
	assert.Equal(t, map[string]any{"kind": "result", "value": "rejected"},
		outcomeOf(t, contracts.DeepseekHarnessControlEventApproval, approval, agenttest.PermissionDecision("deny", "No.")))

	review := map[string]any{"questions": []any{map[string]any{
		"id": "plan-review", "question": "Review the plan", "detail": "# Plan",
		"options": []any{map[string]any{"label": "Approve"}, map[string]any{"label": "Keep planning"}},
		"intent":  map[string]any{"kind": "plan-review", "approve": "Approve", "callId": "native-plan"},
	}}}
	assert.Equal(t, map[string]any{"kind": "result", "value": map[string]any{"answers": []any{map[string]any{"id": "plan-review", "selected": []any{"Approve"}}}}},
		outcomeOf(t, contracts.DeepseekHarnessControlEventUserQuestions, review, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true})))
	assert.Equal(t, map[string]any{"kind": "result", "value": map[string]any{"answers": []any{map[string]any{"id": "plan-review", "selected": []any{"Keep planning"}}}}},
		outcomeOf(t, contracts.DeepseekHarnessControlEventUserQuestions, review, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Feedback: "Split step 1."})))

	// A review with several refusals sends the one that the user chose.
	threeWays := map[string]any{"questions": []any{map[string]any{
		"id": "plan-review", "question": "Review the plan", "detail": "# Plan",
		"options": []any{map[string]any{"label": "Approve"}, map[string]any{"label": "Keep planning"}, map[string]any{"label": "Reject and exit"}},
		"intent":  map[string]any{"kind": "plan-review", "approve": "Approve", "callId": "native-plan"},
	}}}
	assert.Equal(t, map[string]any{"kind": "result", "value": map[string]any{"answers": []any{map[string]any{"id": "plan-review", "selected": []any{"Reject and exit"}}}}},
		outcomeOf(t, contracts.DeepseekHarnessControlEventUserQuestions, threeWays, agenttest.PlanDecision(&leapmuxv1.PlanDecision{ChoiceId: "Reject and exit"})))
	assert.Equal(t, map[string]any{"kind": "result", "value": map[string]any{"answers": []any{map[string]any{"id": "plan-review", "selected": []any{"Keep planning"}}}}},
		outcomeOf(t, contracts.DeepseekHarnessControlEventUserQuestions, threeWays, agenttest.PlanDecision(&leapmuxv1.PlanDecision{ChoiceId: "Keep planning"})))
	assert.Equal(t, map[string]any{"kind": "result", "value": map[string]any{"answers": []any{map[string]any{"id": "plan-review", "selected": []any{"Approve"}}}}},
		outcomeOf(t, contracts.DeepseekHarnessControlEventUserQuestions, threeWays, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "Approve"})))

	questions := map[string]any{"questions": []any{map[string]any{"id": "q", "question": "Pick.", "options": []any{map[string]any{"label": "A"}}}}}
	assert.Equal(t, map[string]any{"kind": "result", "value": map[string]any{"answers": []any{map[string]any{"id": "q", "selected": []any{"A"}, "custom": "why"}}}},
		outcomeOf(t, contracts.DeepseekHarnessControlEventUserQuestions, questions, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"A"}, CustomText: " why "})))
}

func nativeQuestionOf(t *testing.T, raw string) nativeQuestion {
	t.Helper()
	var question nativeQuestion
	require.NoError(t, json.Unmarshal([]byte(raw), &question))
	return question
}

func TestPlanReviewLabel(t *testing.T) {
	question := nativeQuestionOf(t, `{"options":[{"label":"Approve"},{"label":"Keep planning"},{"label":"Reject and exit"}],"intent":{"kind":"plan-review","approve":"Approve"}}`)
	for _, tc := range []struct {
		approve bool
		choice  string
		want    string
	}{
		{true, "", "Approve"},
		{true, "Approve", "Approve"},
		{false, "", "Keep planning"},
		{false, "Reject and exit", "Reject and exit"},
	} {
		got, err := planReviewLabel(question, tc.approve, tc.choice)
		require.NoError(t, err, tc)
		assert.Equal(t, tc.want, got, tc)
	}
	for _, tc := range []struct {
		approve bool
		choice  string
	}{
		{true, "Keep planning"},
		{false, "Approve"},
		{false, "Ship it"},
	} {
		_, err := planReviewLabel(question, tc.approve, tc.choice)
		assert.Error(t, err, tc)
	}
	_, err := planReviewLabel(nativeQuestionOf(t, `{"options":[{"label":"Approve"}],"intent":{"kind":"plan-review","approve":"Approve"}}`), false, "")
	assert.Error(t, err, "a review that offers no refusal")
}
