package antigravity

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func antigravityJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}

// antigravityInteractionPayload is the control request that the Worker
// publishes for one interaction of a cascade's step (newInteractionControl).
func antigravityInteractionPayload(t *testing.T, interaction any) []byte {
	t.Helper()
	return antigravityJSON(t, map[string]any{
		"name":                 contracts.AntigravityFrameInteraction,
		"request":              map[string]any{"tool_name": "run_command", "tool_use_id": "tool-1", "input": interaction},
		"cascadeId":            "cascade",
		"trajectoryId":         "trajectory",
		"stepIndex":            4,
		"requestedInteraction": interaction,
		"toolCall":             map[string]any{"id": "tool-1", "name": "run_command"},
	})
}

// antigravityPlanReviewPayload is the plan review that the Worker raises.
// antigravityPlanReviewKey is the key of the plan review that
// antigravityPlanReviewPayload stores.
const antigravityPlanReviewKey = "antigravity-plan:review-1"

func antigravityPlanReviewPayload(t *testing.T, plan string) []byte {
	t.Helper()
	return antigravityJSON(t, map[string]any{
		"name":       contracts.AntigravityFrameInteraction,
		"request_id": antigravityPlanReviewKey,
		"request": map[string]any{
			"tool_name": contracts.AntigravityControlToolPlanReview,
			"input":     map[string]any{contracts.AntigravityControlPayloadPlanContent: plan},
		},
	})
}

func TestReadControlRequest_AntigravityDistinguishesPermissionsAndPlansFromQuestions(t *testing.T) {
	assert.Nil(t, agenttest.ControlPromptOf(antigravityProvider{}, antigravityJSON(t, map[string]any{
		"requestedInteraction": map[string]any{"permission": map[string]any{"resource": map[string]any{"action": "command"}}},
	})).GetQuestion())
	assert.Nil(t, agenttest.ControlPromptOf(antigravityProvider{}, antigravityJSON(t, map[string]any{
		"request": map[string]any{"tool_name": contracts.AntigravityControlToolPlanReview},
	})).GetQuestion())
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{},
	}}}, agenttest.ControlPromptOf(antigravityProvider{}, antigravityJSON(t, map[string]any{"requestedInteraction": map[string]any{"askQuestion": map[string]any{"questions": []any{}}}})))
}

func TestReadControlRequest_AntigravityRoutesAPlanReviewToThePlan(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{
		Text: "# Complete native plan\n\n1. Inspect the source.",
	}}}, agenttest.ControlPromptOf(antigravityProvider{}, antigravityPlanReviewPayload(t, "# Complete native plan\n\n1. Inspect the source.")))
	for _, payload := range []string{
		`{"request":{"tool_name":"AntigravityPlanReview"}}`,
		`{"request":{"tool_name":"AntigravityPlanReview","input":"not-an-object"}}`,
		`{"request":{"tool_name":"AntigravityPlanReview","input":[]}}`,
		`{"request":{"tool_name":"AntigravityPlanReview","input":null}}`,
		`{"request":{"tool_name":"AntigravityPlanReview","input":{"planContent":123}}}`,
		`{"request":{"tool_name":"AntigravityPlanReview","input":{"planContent":{}}}}`,
		`{"request":{"tool_name":"AntigravityPlanReview","input":{"planContent":null}}}`,
	} {
		agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{}}},
			agenttest.ControlPromptOf(antigravityProvider{}, []byte(payload)), payload)
	}
}

func TestReadControlRequest_AntigravityReadsAPermissionForItsResource(t *testing.T) {
	got := agenttest.ControlPromptOf(antigravityProvider{}, antigravityInteractionPayload(t, map[string]any{"permission": map[string]any{
		"resource": map[string]any{"action": "command", "target": "go test ./..."},
	}}))
	require.NotNil(t, got.GetPermission())
	assert.JSONEq(t, `{"action":"command","target":"go test ./..."}`, got.GetPermission().GetInputJson())
	got.GetPermission().InputJson = ""
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Title:   "Run command",
		Command: "go test ./...",
		Options: []*leapmuxv1.PermissionOption{
			{OptionId: "allow", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow once"},
			{OptionId: "reject", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Reject"},
		},
	}}}, got)
}

func TestReadControlRequest_AntigravityTitlesAPermission(t *testing.T) {
	for action, title := range map[string]string{
		"read_file": "Read file", "write_file": "Write file", "mcp": "MCP tool", "future_action": "Permission", "": "Permission",
	} {
		permission := agenttest.ControlPromptOf(antigravityProvider{}, antigravityInteractionPayload(t, map[string]any{"permission": map[string]any{
			"resource": map[string]any{"action": action, "target": "/w/a.go"},
		}})).GetPermission()
		assert.Equal(t, title, permission.GetTitle(), action)
		assert.Empty(t, permission.GetCommand(), "only a command states a command line")
	}
	permission := agenttest.ControlPromptOf(antigravityProvider{}, antigravityInteractionPayload(t, map[string]any{"permission": map[string]any{
		"actionDescription": "Delete the build directory", "resource": map[string]any{"action": "command", "target": "rm -rf build"},
	}})).GetPermission()
	assert.Equal(t, "Delete the build directory", permission.GetTitle(), "the interaction's own description wins")

	permission = agenttest.ControlPromptOf(antigravityProvider{}, antigravityInteractionPayload(t, map[string]any{"permission": map[string]any{}})).GetPermission()
	assert.JSONEq(t, `{"action":"","target":""}`, permission.GetInputJson(), "a permission with no resource states empty arguments")
}

// An interaction that is neither a question nor a permission is answered as
// a whole, with the interaction as its arguments.
func TestReadControlRequest_AntigravityReadsAnotherInteractionAsAPermission(t *testing.T) {
	got := agenttest.ControlPromptOf(antigravityProvider{}, antigravityInteractionPayload(t, map[string]any{"browserAction": map[string]any{"url": "https://example.com"}}))
	require.NotNil(t, got.GetPermission())
	assert.JSONEq(t, `{"interaction":{"browserAction":{"url":"https://example.com"}}}`, got.GetPermission().GetInputJson())
	assert.Equal(t, "Antigravity interaction", got.GetPermission().GetTitle())
	assert.Equal(t, []string{"allow", "reject"}, []string{got.GetPermission().GetOptions()[0].GetOptionId(), got.GetPermission().GetOptions()[1].GetOptionId()})
}

// A payload that states no interaction is no request that the Worker
// publishes, and an answer would guess what its step asks, so it reads as
// unrecognized.
func TestReadControlRequest_AntigravityAnotherPayloadIsUnrecognized(t *testing.T) {
	for _, payload := range []string{
		`{}`, `not json`, `"text"`, `[1]`, `null`, `42`,
		`{"requestedInteraction":"permission"}`, `{"requestedInteraction":null}`,
		`{"request":"plan"}`, `{"request":[]}`, `{"request":null}`,
		`{"request":{"tool_name":"run_command"}}`,
	} {
		agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(antigravityProvider{}, []byte(payload)), payload)
	}
	for name, payload := range antigravityPayloadsWithNoInteraction(t) {
		agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(antigravityProvider{}, payload), name)
	}
}

// twoAntigravityQuestions are a single and a multiple choice, as the
// language server states them.
func twoAntigravityQuestions() []any {
	return []any{
		map[string]any{"question": "First?", "options": []any{map[string]any{"id": "a", "text": "A"}, map[string]any{"id": "b", "text": "B"}}, "isMultiSelect": false},
		map[string]any{"question": "Second?", "options": []any{map[string]any{"id": "x", "text": "X"}, map[string]any{"id": "y", "text": "Y"}}, "isMultiSelect": true},
	}
}

func antigravityQuestionPayload(t *testing.T, questions any) []byte {
	t.Helper()
	return antigravityInteractionPayload(t, map[string]any{"askQuestion": map[string]any{"questions": questions}})
}

func TestReadControlRequest_AntigravityReadsEachQuestionWithItsOptionIds(t *testing.T) {
	a, b, x, y := "a", "b", "x", "y"
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{
			{Question: "First?", Options: []*leapmuxv1.QuestionOption{{Value: &a, Label: "A"}, {Value: &b, Label: "B"}}},
			{Question: "Second?", Options: []*leapmuxv1.QuestionOption{{Value: &x, Label: "X"}, {Value: &y, Label: "Y"}}, MultiSelect: true},
		},
	}}}, agenttest.ControlPromptOf(antigravityProvider{}, antigravityQuestionPayload(t, twoAntigravityQuestions())))

	// Only questions and options with display text reach the neutral prompt.
	invalidAndValid := []any{
		nil,
		map[string]any{"question": 7},
		map[string]any{"question": "\uFEFF "},
		map[string]any{
			"id":            "typed-question",
			"question":      "Valid?",
			"isMultiSelect": "yes",
			"options": []any{
				"x",
				map[string]any{"id": 3, "text": false},
				map[string]any{"text": "\uFEFF "},
				map[string]any{"text": "Valid option"},
				map[string]any{"text": "\u0085"},
			},
		},
	}
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{{Id: "typed-question", Question: "Valid?", Options: []*leapmuxv1.QuestionOption{{Label: "Valid option"}, {Label: "\u0085"}}}},
	}}}, agenttest.ControlPromptOf(antigravityProvider{}, antigravityQuestionPayload(t, invalidAndValid)))
}

// requireAntigravityAccepted checks that the Worker finds the request and
// takes the answer: ControlResponseRequestID reads the stored id back out of
// the answer, and ResolveControlResponse forwards it.
func requireAntigravityAccepted(t *testing.T, payload []byte, requestID string, answer agent.ControlAnswer) {
	t.Helper()
	assert.Equal(t, requestID, antigravityProvider{}.ControlResponseRequestID(answer.Content))
	require.NoError(t, agenttest.ResolveAnswer(antigravityProvider{}, payload, requestID, answer).Refusal())
}

func TestAnswerControlRequest_AntigravityAnswersAPermissionNatively(t *testing.T) {
	payload := antigravityInteractionPayload(t, map[string]any{"permission": map[string]any{"resource": map[string]any{"action": "command", "target": "ls"}}})
	const requestID = "antigravity:cascade:trajectory:4"
	for option, allow := range map[string]string{"allow": "true", "reject": "false"} {
		got, err := agenttest.AnswerControl(antigravityProvider{}, payload, requestID, agenttest.PermissionDecision(option, ""))
		require.NoError(t, err, option)
		assert.JSONEq(t, `{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":4,"permission":{"allow":`+allow+`,"scope":"PERMISSION_SCOPE_ONCE"}}}`, string(got.Content), option)
		assert.Empty(t, got.FollowUp, option)
		requireAntigravityAccepted(t, payload, requestID, got)
	}

	got, err := agenttest.AnswerControl(antigravityProvider{}, payload, requestID, agenttest.PermissionDecision("reject", "Use another approach"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":4,"permission":{"allow":false,"scope":"PERMISSION_SCOPE_ONCE","userDenyInstruction":"Use another approach"}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp, "the instruction rides the answer")

	_, err = agenttest.AnswerControl(antigravityProvider{}, payload, requestID, agenttest.PermissionDecision("allow", "a note"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an approval carries no instruction")
}

// antigravityPayloadsWithNoInteraction are stored requests of a cascade's
// step that state no interaction object, so each reads as unrecognized. The
// Worker publishes no such request, because
// newInteractionControl refuses a step that requests no interaction LeapMux
// can answer. The first has the shape that such a request would have:
// `requestedInteraction` is null.
func antigravityPayloadsWithNoInteraction(t *testing.T) map[string][]byte {
	t.Helper()
	return map[string][]byte{
		"a null interaction":               antigravityInteractionPayload(t, nil),
		"an interaction that is no object": antigravityInteractionPayload(t, "permission"),
		"no interaction":                   antigravityJSON(t, map[string]any{"cascadeId": "cascade", "trajectoryId": "trajectory", "stepIndex": 4}),
	}
}

// A stored request that states no interaction reads as unrecognized, so no
// decision answers it: an answer would tell the language server that its
// step asked for a permission, which nothing states.
func TestAnswerControlRequest_AntigravityAnswersNoRequestWithNoInteraction(t *testing.T) {
	const requestID = "antigravity:cascade:trajectory:4"
	for name, payload := range antigravityPayloadsWithNoInteraction(t) {
		for _, decision := range []*leapmuxv1.ControlDecision{
			agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""),
			agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, "Use another approach"),
		} {
			_, err := agenttest.AnswerControl(antigravityProvider{}, payload, requestID, decision)
			assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
		}
	}
}

// Each answer that a permission prompt offers resolves, whatever the stored
// step states: the Worker never offers an answer that ResolveControlResponse
// refuses.
func TestAnswerControlRequest_AntigravityEveryOfferedPermissionAnswerResolves(t *testing.T) {
	const requestID = "antigravity:cascade:trajectory:4"
	payloads := map[string][]byte{
		"a permission":         antigravityInteractionPayload(t, map[string]any{"permission": map[string]any{"resource": map[string]any{"action": "command", "target": "ls"}}}),
		"another interaction":  antigravityInteractionPayload(t, map[string]any{"browserAction": map[string]any{"url": "https://example.com"}}),
		"an empty interaction": antigravityInteractionPayload(t, map[string]any{}),
	}
	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			options := agenttest.ControlPromptOf(antigravityProvider{}, payload).GetPermission().GetOptions()
			require.NotEmpty(t, options, "the stored step reads as a permission")
			for _, option := range options {
				feedbacks := []string{""}
				if agent.IsRefusalOption(option) {
					feedbacks = append(feedbacks, "Use another approach")
				}
				for _, feedback := range feedbacks {
					got, err := agenttest.AnswerControl(antigravityProvider{}, payload, requestID, agenttest.PermissionDecision(option.GetOptionId(), feedback))
					require.NoError(t, err, option.GetOptionId())
					requireAntigravityAccepted(t, payload, requestID, got)
				}
			}
		})
	}
}

// An answer echoes the address of its request id, and the Worker finds the
// request again by the id that ControlResponseRequestID reads back out of
// the answer. So an id that states no cascade, no trajectory, or a step in
// any form but its canonical one has no answer that reaches the request.
func TestAnswerControlRequest_AntigravityRefusesAMalformedRequestID(t *testing.T) {
	for name, tc := range map[string]struct {
		payload  []byte
		decision *leapmuxv1.ControlDecision
	}{
		"a permission":         {antigravityInteractionPayload(t, map[string]any{"permission": map[string]any{}}), agenttest.PermissionDecision("allow", "")},
		"a refused permission": {antigravityInteractionPayload(t, map[string]any{"permission": map[string]any{}}), agenttest.PermissionDecision("reject", "")},
		"a question":           {antigravityQuestionPayload(t, twoAntigravityQuestions()), agenttest.CancelQuestions("")},
	} {
		for _, requestID := range []string{
			"antigravity:cascade:trajectory", "other:cascade:trajectory:4", "antigravity:cascade:trajectory:four",
			"antigravity:cascade:trajectory:-1", "antigravity:a:b:c:4", "antigravity::trajectory:4", "antigravity:cascade::4",
			"antigravity:cascade:trajectory:", "antigravity:cascade:trajectory:04", "antigravity:cascade:trajectory:+4",
		} {
			_, err := agenttest.AnswerControl(antigravityProvider{}, tc.payload, requestID, tc.decision)
			assert.ErrorContains(t, err, "malformed", "%s: %s", name, requestID)
			assert.NotErrorIs(t, err, agent.ErrInvalidControlDecision, "the stored request is at fault, not the decision")
		}
	}
}

func TestAnswerControlRequest_AntigravityPlanReviewTakesTheSharedPlanAnswer(t *testing.T) {
	payload := antigravityPlanReviewPayload(t, "# Plan")
	got, err := agenttest.AnswerControl(antigravityProvider{}, payload, "antigravity-plan:review-1", agenttest.PlanDecision(&leapmuxv1.PlanDecision{Feedback: "Revise the plan"}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"antigravity-plan:review-1","response":{"behavior":"deny","message":"Revise the plan"}}}`, string(got.Content))

	got, err = agenttest.AnswerControl(antigravityProvider{}, payload, "antigravity-plan:review-1", agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, PermissionMode: contracts.AntigravityModeBypass}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"antigravity-plan:review-1","response":{"behavior":"allow","updatedInput":{"planContent":"# Plan"}}}}`, string(got.Content))
	assert.True(t, proto.Equal(&leapmuxv1.PlanApprovalSettings{PermissionMode: contracts.AntigravityModeBypass}, got.PlanApproval))
	assert.Equal(t, "antigravity-plan:review-1", antigravityProvider{}.ControlResponseRequestID(got.Content))
}

func TestAnswerControlRequest_AntigravityAnswersSingleAndMultipleChoiceQuestionsNatively(t *testing.T) {
	payload := antigravityQuestionPayload(t, twoAntigravityQuestions())
	const requestID = "antigravity:child:trajectory-child:8"
	got, err := agenttest.AnswerControl(antigravityProvider{}, payload, requestID, agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"a"}},
		&leapmuxv1.QuestionAnswer{Selected: []string{"x", "y"}},
	))
	require.NoError(t, err)
	assert.JSONEq(t, `{"cascadeId":"child","interaction":{"trajectoryId":"trajectory-child","stepIndex":8,"askQuestion":{"responses":[
		{"question":"First?","options":[{"id":"a","text":"A"},{"id":"b","text":"B"}],"isMultiSelect":false,"selectedOptionIds":["a"]},
		{"question":"Second?","options":[{"id":"x","text":"X"},{"id":"y","text":"Y"}],"isMultiSelect":true,"selectedOptionIds":["x","y"]}
	]}}}`, string(got.Content))
	assert.Equal(t, requestID, antigravityProvider{}.ControlResponseRequestID(got.Content))
}

// A multiple choice takes its options and its typed text together: the
// text is the write-in response beside the selected ids.
func TestAnswerControlRequest_AntigravityMultipleChoiceSendsItsOptionsAndItsText(t *testing.T) {
	got, err := agenttest.AnswerControl(antigravityProvider{}, antigravityQuestionPayload(t, twoAntigravityQuestions()), "antigravity:c:t:1", agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{CustomText: "  Neither  "},
		&leapmuxv1.QuestionAnswer{Selected: []string{"x", "y"}, CustomText: " Z "},
	))
	require.NoError(t, err)
	responses := antigravityResponses(t, got.Content)
	assert.Equal(t, "Neither", responses[0]["writeInResponse"], "the typed text is trimmed")
	assert.NotContains(t, responses[0], "selectedOptionIds")
	assert.Equal(t, []any{"x", "y"}, responses[1]["selectedOptionIds"])
	assert.Equal(t, "Z", responses[1]["writeInResponse"])
	assert.NotContains(t, responses[1], "skipped")

	_, err = agenttest.AnswerControl(antigravityProvider{}, antigravityQuestionPayload(t, twoAntigravityQuestions()), "antigravity:c:t:1", agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"a"}, CustomText: "And more"},
	))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a single choice takes an option or text, not both")
}

func antigravityResponses(t *testing.T, content []byte) []map[string]any {
	t.Helper()
	var answer struct {
		Interaction struct {
			AskQuestion struct {
				Responses []map[string]any `json:"responses"`
			} `json:"askQuestion"`
		} `json:"interaction"`
	}
	require.NoError(t, json.Unmarshal(content, &answer))
	return answer.Interaction.AskQuestion.Responses
}

// The browser sends an answer only when every question has one, so the
// Worker refuses an answer that leaves a question empty or blank. A cancel
// is how a user leaves the questions.
func TestAnswerControlRequest_AntigravityRefusesAQuestionWithNoAnswer(t *testing.T) {
	_, err := agenttest.AnswerControl(antigravityProvider{}, antigravityQuestionPayload(t, twoAntigravityQuestions()), "antigravity:c:t:1", agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{CustomText: " \t "},
	))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "text with no words is no answer")
}

func TestAnswerControlRequest_AntigravityKeepsNativeQuestionPositions(t *testing.T) {
	first := map[string]any{"id": "q-first", "question": "First?"}
	blank := map[string]any{"id": "q-blank", "question": "  "}
	last := map[string]any{"id": "q-last", "question": "Last?", "options": []any{map[string]any{"id": "o", "text": "O"}}}
	got, err := agenttest.AnswerControl(antigravityProvider{}, antigravityQuestionPayload(t, []any{first, nil, blank, last}),
		"antigravity:c:t:1", agenttest.QuestionDecision(
			&leapmuxv1.QuestionAnswer{CustomText: "First answer"},
			&leapmuxv1.QuestionAnswer{Selected: []string{"o"}},
		))
	require.NoError(t, err)
	assert.Equal(t, []map[string]any{
		{"id": "q-first", "question": "First?", "writeInResponse": "First answer"},
		{"skipped": true},
		{"id": "q-blank", "question": "  ", "skipped": true},
		{"id": "q-last", "question": "Last?", "options": []any{map[string]any{"id": "o", "text": "O"}}, "selectedOptionIds": []any{"o"}},
	}, antigravityResponses(t, got.Content))
}

// The language server reads no reason for a dismissed question.
func TestAnswerControlRequest_AntigravityCancelledQuestionDismissesTheQuestions(t *testing.T) {
	payload := antigravityQuestionPayload(t, twoAntigravityQuestions())
	got, err := agenttest.AnswerControl(antigravityProvider{}, payload, "antigravity:cascade:trajectory:4", agenttest.CancelQuestions("User stopped"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":4,"askQuestion":{"cancelled":true}}}`, string(got.Content))
	requireAntigravityAccepted(t, payload, "antigravity:cascade:trajectory:4", got)
}

func TestAnswerControlRequest_AntigravityRefusesADecisionOfAnotherKind(t *testing.T) {
	question := antigravityQuestionPayload(t, twoAntigravityQuestions())
	_, err := agenttest.AnswerControl(antigravityProvider{}, question, "antigravity:c:t:1", agenttest.PermissionDecision("allow", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
	_, err = agenttest.AnswerControl(antigravityProvider{}, question, "antigravity:c:t:1", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"A"}}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an option answers with its id, not its text")
	_, err = agenttest.AnswerControl(antigravityProvider{}, antigravityPlanReviewPayload(t, "# Plan"), "antigravity-plan:1", agenttest.PermissionDecision("allow", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}
