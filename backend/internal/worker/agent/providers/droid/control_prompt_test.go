package droid

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

// droidTestRequestID is the stored id of the request that each test answers.
const droidTestRequestID = "droid-request"

func droidJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}

func droidPlan(text string) *leapmuxv1.ControlPrompt {
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{Text: text}}}
}

func TestReadControlRequest_DroidExitSpecModeIsAPlanWithThePlanItCarries(t *testing.T) {
	agenttest.RequireProto(t, droidPlan("# Native plan"), agenttest.ControlPromptOf(droidProvider{}, droidJSON(t, map[string]any{
		"type":             "permission_request",
		"toolUse":          map[string]any{"type": "tool_use", "id": "exit-1", "name": "ExitSpecMode", "input": map[string]any{"plan": "# Native plan"}},
		"confirmationType": "exit_spec_mode",
		"details":          map[string]any{"type": "exit_spec_mode", "plan": "# Native plan"},
	})))
}

// Droid groups a mission proposal with the spec-mode exit by the
// confirmation type, and the proposal rides `details.proposal`.
func TestReadControlRequest_DroidMissionProposalIsAPlanWithTheProposal(t *testing.T) {
	agenttest.RequireProto(t, droidPlan("# Mission\n\n- Ship the feature."), agenttest.ControlPromptOf(droidProvider{}, droidJSON(t, map[string]any{
		"type":             "permission_request",
		"toolUse":          map[string]any{"type": "tool_use", "id": "mission-1", "name": "ProposeMission", "input": map[string]any{}},
		"confirmationType": "propose_mission",
		"details":          map[string]any{"type": "propose_mission", "proposal": "# Mission\n\n- Ship the feature.", "title": "Feature"},
	})))
}

// The confirmation type, not the tool's name, makes a request a review.
func TestReadControlRequest_DroidReviewWithNoDocumentIsABarePlan(t *testing.T) {
	agenttest.RequireProto(t, droidPlan(""), agenttest.ControlPromptOf(droidProvider{}, droidJSON(t, map[string]any{
		"type":             "permission_request",
		"toolUse":          map[string]any{"name": "ExitSpecMode", "input": map[string]any{}},
		"confirmationType": "exit_spec_mode",
		"details":          map[string]any{"type": "exit_spec_mode", "plan": "  "},
	})))
	agenttest.RequireProto(t, droidPlan(""), agenttest.ControlPromptOf(droidProvider{}, droidJSON(t, map[string]any{
		"type": "permission_request", "toolUse": map[string]any{"name": "Edit"}, "confirmationType": "propose_mission",
	})))
}

// A Droid Shield refusal is a permission for an Execute call whose details
// state why Droid blocked the call.
func TestReadControlRequest_DroidStatesTheReasonOfADroidShieldRefusal(t *testing.T) {
	got := agenttest.ControlPromptOf(droidProvider{}, droidJSON(t, map[string]any{
		"type":             "permission_request",
		"toolUse":          map[string]any{"type": "tool_use", "id": "shield-1", "name": "Execute", "input": map[string]any{"command": "git push"}},
		"confirmationType": "droid_shield_violation",
		"details":          map[string]any{"type": "droid_shield_violation", "command": "git push", "reason": "the diff may hold a secret"},
	}))
	require.NotNil(t, got.GetPermission())
	assert.JSONEq(t, `{"command":"git push"}`, got.GetPermission().GetInputJson())
	got.GetPermission().InputJson = ""
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
		Title:   "Execute",
		Command: "git push",
		Reason:  "the diff may hold a secret",
		Options: []*leapmuxv1.PermissionOption{{OptionId: contracts.ControlAnswerOptionDeny, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE}},
	}}}, got)
}

func TestReadControlRequest_DroidKeepsAnOrdinaryEditAsAPermission(t *testing.T) {
	permission := agenttest.ControlPromptOf(droidProvider{}, droidJSON(t, map[string]any{
		"type":    "permission_request",
		"toolUse": map[string]any{"name": "Edit", "input": map[string]any{"file_path": "/work/note.txt"}},
	})).GetPermission()
	require.NotNil(t, permission)
	assert.Equal(t, "Edit", permission.GetTitle())
	assert.JSONEq(t, `{"file_path":"/work/note.txt"}`, permission.GetInputJson())
	assert.Empty(t, permission.GetCommand(), "only an Execute call states a command")
	assert.Empty(t, permission.GetReason(), "only a Droid Shield refusal states a reason")
}

func TestReadControlRequest_DroidReadsTheToolNameCommandAndInputDefensively(t *testing.T) {
	permission := agenttest.ControlPromptOf(droidProvider{}, droidJSON(t, map[string]any{
		"type": "permission_request", "toolUse": map[string]any{"toolName": "Execute", "input": map[string]any{"cmd": "make"}},
	})).GetPermission()
	assert.Equal(t, "Execute", permission.GetTitle(), "the tool name falls back to `toolName`")
	assert.Equal(t, "make", permission.GetCommand(), "the command falls back to `cmd`")

	permission = agenttest.ControlPromptOf(droidProvider{}, droidJSON(t, map[string]any{"type": "permission_request", "toolUse": map[string]any{"name": 7, "input": "x"}})).GetPermission()
	assert.Equal(t, "Tool", permission.GetTitle())
	assert.Equal(t, "{}", permission.GetInputJson(), "an input that is no object states an empty object")
}

// Droid raises no other request, and the resolution refuses an answer to
// one, so a payload outside the permission channel reads as unrecognized.
func TestReadControlRequest_DroidPayloadOutsideThePermissionChannelIsUnrecognized(t *testing.T) {
	for _, payload := range []string{`{"type":"settings_updated"}`, `{}`, `not json`, `{"type":7}`} {
		agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(droidProvider{}, []byte(payload)), payload)
	}
	// A permission decision drawn for such a payload anyway answers nothing.
	_, err := droidProvider{}.AnswerControlRequest(agent.ControlAnswerInput{
		Request: agent.NewControlRequestInput([]byte(`{"type":"settings_updated"}`)), RequestID: droidTestRequestID,
		Prompt: agent.GenericPermissionPrompt(), Decision: agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""),
	})
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

func TestReadControlRequest_DroidStatesTheOfferedOptionsWithTheirKinds(t *testing.T) {
	permission := agenttest.ControlPromptOf(droidProvider{}, droidJSON(t, map[string]any{
		"type":    "permission_request",
		"toolUse": map[string]any{"name": "Edit"},
		"options": []any{"proceed_once", "proceed_always_file", "proceed_auto_run_high", 7, "", "cancel"},
	})).GetPermission()
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{Options: []*leapmuxv1.PermissionOption{
		{OptionId: "proceed_once", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE},
		{OptionId: "proceed_always_file", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "Allow always for this file"},
		{OptionId: "proceed_auto_run_high", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_OTHER, Name: "Allow and set the autonomy level to High"},
		{OptionId: "cancel", Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE},
	}}}}, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{Options: permission.GetOptions()}}})

	for _, value := range []string{"proceed_always", "proceed_always_tools", "proceed_always_server"} {
		assert.Equal(t, leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, droidOptionKind(value), value)
	}
}

// Droid states each option as a wire value and no label, so the prompt labels
// every option in words: a button and a saved answer read the words, and
// never a token such as proceed_auto_run_low. The once pair carries no name,
// because a client words it as its Allow and Deny buttons. A value that this
// build does not know keeps the value, which is the only word Droid states
// for it.
func TestReadControlRequest_DroidLabelsEachOptionInWords(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]string{
		contracts.DroidPermissionOptionProceedOnce:                "",
		contracts.DroidPermissionOptionCancel:                     "",
		contracts.DroidPermissionOptionProceedAlways:              "Allow always",
		contracts.DroidPermissionOptionProceedAlwaysFile:          "Allow always for this file",
		contracts.DroidPermissionOptionProceedAlwaysTools:         "Allow always for these MCP tools",
		contracts.DroidPermissionOptionProceedAlwaysServer:        "Allow always for this MCP server",
		contracts.DroidPermissionOptionProceedAutoRun:             "Allow and raise the autonomy level",
		contracts.DroidPermissionOptionProceedAutoRunLow:          "Allow and set the autonomy level to Low",
		contracts.DroidPermissionOptionProceedAutoRunMedium:       "Allow and set the autonomy level to Medium",
		contracts.DroidPermissionOptionProceedAutoRunHigh:         "Allow and set the autonomy level to High",
		contracts.DroidPermissionOptionProceedNewSession:          "Allow in a new session",
		contracts.DroidPermissionOptionProceedNewSessionLow:       "Allow in a new session at autonomy level Low",
		contracts.DroidPermissionOptionProceedNewSessionMedium:    "Allow in a new session at autonomy level Medium",
		contracts.DroidPermissionOptionProceedNewSessionHigh:      "Allow in a new session at autonomy level High",
		contracts.DroidPermissionOptionProceedReportFalsePositive: "Allow and report a false positive",
		"proceed_later_tier":                                      "proceed_later_tier",
	} {
		options := agenttest.ControlPromptOf(droidProvider{}, droidJSON(t, map[string]any{
			"type":    "permission_request",
			"toolUse": map[string]any{"name": "Edit"},
			"options": []any{value, contracts.DroidPermissionOptionCancel},
		})).GetPermission().GetOptions()
		require.NotEmpty(t, options, value)
		assert.Equal(t, value, options[0].GetOptionId())
		assert.Equal(t, want, options[0].GetName(), value)
	}
}

// The composer can still refuse a request whose options include no cancel,
// so the prompt offers LeapMux's own refusal after Droid's options.
func TestReadControlRequest_DroidAddsARefusalWhenNoOptionCancels(t *testing.T) {
	options := agenttest.ControlPromptOf(droidProvider{}, droidJSON(t, map[string]any{
		"type": "permission_request", "toolUse": map[string]any{"name": "Edit"}, "options": []any{"proceed_once", "proceed_always"},
	})).GetPermission().GetOptions()
	require.Len(t, options, 3)
	assert.Equal(t, contracts.ControlAnswerOptionDeny, options[2].GetOptionId())
	assert.Equal(t, leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, options[2].GetKind())
}

func TestReadControlRequest_DroidOnlyUnsupportedInvalidOrNonArrayOptionsOffersSingleDeny(t *testing.T) {
	t.Parallel()
	cases := map[string]any{
		"only proceed_edit":        []any{contracts.DroidPermissionOptionProceedEdit},
		"only invalid and dropped": []any{"", 7, nil, true, contracts.DroidPermissionOptionProceedEdit},
		"non-array string":         contracts.DroidPermissionOptionProceedOnce,
		"non-array number":         42,
		"non-array object":         map[string]any{"value": contracts.DroidPermissionOptionProceedOnce},
	}
	for name, rawOptions := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			permission := agenttest.ControlPromptOf(droidProvider{}, droidJSON(t, map[string]any{
				"type":    contracts.DroidRequestTypePermission,
				"toolUse": map[string]any{"name": "Edit"},
				"options": rawOptions,
			})).GetPermission()
			require.NotNil(t, permission)
			require.Len(t, permission.GetOptions(), 1)
			assert.Equal(t, contracts.ControlAnswerOptionDeny, permission.GetOptions()[0].GetOptionId())
			assert.Equal(t, leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, permission.GetOptions()[0].GetKind())
		})
	}
}

func droidQuestionPrompt(questions ...*leapmuxv1.ControlQuestion) *leapmuxv1.ControlPrompt {
	if questions == nil {
		questions = []*leapmuxv1.ControlQuestion{}
	}
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: questions}}}
}

func droidOptions(labels ...string) []*leapmuxv1.QuestionOption {
	options := []*leapmuxv1.QuestionOption{}
	for _, label := range labels {
		options = append(options, &leapmuxv1.QuestionOption{Value: &label, Label: label})
	}
	return options
}

// colorAndSizes are two questions that Droid numbers from 1, in order.
func colorAndSizes() []map[string]any {
	return []map[string]any{
		{"index": 1, "question": "Which color?", "options": []string{"Blue", "Red"}, "multiSelect": false},
		{"index": 2, "question": "Which sizes?", "options": []string{"Small", "Medium", "Large"}, "multiSelect": true},
	}
}

func TestReadControlRequest_DroidReadsEachQuestionWithItsOptionsAndItsKind(t *testing.T) {
	agenttest.RequireProto(t, droidQuestionPrompt(
		&leapmuxv1.ControlQuestion{Question: "Which color?", Options: droidOptions("Blue", "Red")},
		&leapmuxv1.ControlQuestion{Question: "Which sizes?", Options: droidOptions("Small", "Medium", "Large"), MultiSelect: true},
	), agenttest.ControlPromptOf(droidProvider{}, askUserRequestPayload(t, colorAndSizes()...)))
}

func TestReadControlRequest_DroidSkipsAQuestionThatIsNoObjectAndAnOptionThatIsNoText(t *testing.T) {
	payload := droidJSON(t, map[string]any{
		"type":      "ask_user_request",
		"questions": []any{"not a question", nil, map[string]any{"index": 1, "question": "Q", "options": []any{"A", 7, nil}}},
	})
	agenttest.RequireProto(t, droidQuestionPrompt(&leapmuxv1.ControlQuestion{Question: "Q", Options: droidOptions("A")}), agenttest.ControlPromptOf(droidProvider{}, payload))
	agenttest.RequireProto(t, droidQuestionPrompt(), agenttest.ControlPromptOf(droidProvider{}, []byte(`{"type":"ask_user_request","questions":"Q"}`)))
}

func TestReadControlRequest_DroidRecognizesAQuestionRequest(t *testing.T) {
	assert.NotNil(t, agenttest.ControlPromptOf(droidProvider{}, askUserRequestPayload(t, colorAndSizes()...)).GetQuestion())
	assert.Nil(t, agenttest.ControlPromptOf(droidProvider{}, []byte(`{"type":"permission_request"}`)).GetQuestion())
	assert.Nil(t, agenttest.ControlPromptOf(droidProvider{}, []byte(`{}`)).GetQuestion())
}

// droidSentAnswers reads the answers of a question answer, after checking
// its envelope.
func droidSentAnswers(t *testing.T, payload []byte, decision *leapmuxv1.ControlDecision) []map[string]any {
	t.Helper()
	got, err := agenttest.AnswerControl(droidProvider{}, payload, droidTestRequestID, decision)
	require.NoError(t, err)
	var envelope struct {
		Response struct {
			RequestID string `json:"request_id"`
			Response  struct {
				Behavior string           `json:"behavior"`
				Answers  []map[string]any `json:"answers"`
			} `json:"response"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal(got.Content, &envelope))
	assert.Equal(t, "droid-request", envelope.Response.RequestID)
	assert.Equal(t, contracts.ControlAnswerBehaviorAllow, envelope.Response.Response.Behavior)
	require.NotNil(t, envelope.Response.Response.Answers, "the answer list is never null")
	return envelope.Response.Response.Answers
}

// Droid identifies each answer by the index of its question.
func TestAnswerControlRequest_DroidAnswersEachQuestionByItsNativeIndex(t *testing.T) {
	assert.Equal(t, []map[string]any{{"index": 1.0, "answer": "Red"}, {"index": 2.0, "answer": "Large"}},
		droidSentAnswers(t, askUserRequestPayload(t, colorAndSizes()...), agenttest.QuestionDecision(
			&leapmuxv1.QuestionAnswer{Selected: []string{"Red"}},
			&leapmuxv1.QuestionAnswer{Selected: []string{"Large"}},
		)))

	payload := askUserRequestPayload(t,
		map[string]any{"index": 7, "question": "Seventh?", "options": []string{"Yes", "No"}},
		map[string]any{"index": 3, "question": "Third?", "options": []string{"Yes", "No"}},
	)
	assert.Equal(t, []map[string]any{{"index": 7.0, "answer": "Yes"}, {"index": 3.0, "answer": "No"}},
		droidSentAnswers(t, payload, agenttest.QuestionDecision(
			&leapmuxv1.QuestionAnswer{Selected: []string{"Yes"}},
			&leapmuxv1.QuestionAnswer{Selected: []string{"No"}},
		)), "an index that does not start at one stays")
}

// Droid's answer is one string. Its own client joins the chosen options in
// the order of the options, then the typed text, with ", ".
func TestAnswerControlRequest_DroidJoinsAMultipleChoiceInTheOrderOfTheOptions(t *testing.T) {
	assert.Equal(t, []map[string]any{{"index": 1.0, "answer": "Blue"}, {"index": 2.0, "answer": "Small, Large"}},
		droidSentAnswers(t, askUserRequestPayload(t, colorAndSizes()...), agenttest.QuestionDecision(
			&leapmuxv1.QuestionAnswer{Selected: []string{"Blue"}},
			&leapmuxv1.QuestionAnswer{Selected: []string{"Large", "Small"}},
		)))
}

// A multiple choice takes its options and its typed text together.
func TestAnswerControlRequest_DroidSendsTheTypedTextAfterThePicksOfAMultipleChoice(t *testing.T) {
	assert.Equal(t, []map[string]any{{"index": 1.0, "answer": "Blue"}, {"index": 2.0, "answer": "Small, Large, Extra large"}},
		droidSentAnswers(t, askUserRequestPayload(t, colorAndSizes()...), agenttest.QuestionDecision(
			&leapmuxv1.QuestionAnswer{Selected: []string{"Blue"}},
			&leapmuxv1.QuestionAnswer{Selected: []string{"Large", "Small"}, CustomText: "  Extra large  "},
		)))
}

func TestAnswerControlRequest_DroidSendsTheTypedTextWhenNothingIsPicked(t *testing.T) {
	assert.Equal(t, []map[string]any{{"index": 1.0, "answer": "Green"}, {"index": 2.0, "answer": "Huge"}},
		droidSentAnswers(t, askUserRequestPayload(t, colorAndSizes()...), agenttest.QuestionDecision(
			&leapmuxv1.QuestionAnswer{CustomText: " Green "},
			&leapmuxv1.QuestionAnswer{CustomText: "Huge"},
		)))
}

// A single choice takes its choice or its text, as Droid's own client does,
// and never both. A pick that the question does not offer is no answer.
func TestAnswerControlRequest_DroidRefusesAnAnswerThatTheQuestionCannotTake(t *testing.T) {
	payload := askUserRequestPayload(t, colorAndSizes()...)
	for name, answers := range map[string][]*leapmuxv1.QuestionAnswer{
		"a choice and text for a single choice":  {{Selected: []string{"Red"}, CustomText: "Green"}},
		"an unoffered pick of a single choice":   {{Selected: []string{"Purple"}}},
		"an unoffered pick of a multiple choice": {{Selected: []string{"Blue"}}, {Selected: []string{"Huge", "Medium"}}},
		"two picks of a single choice":           {{Selected: []string{"Blue", "Red"}}},
	} {
		_, err := agenttest.AnswerControl(droidProvider{}, payload, droidTestRequestID, agenttest.QuestionDecision(answers...))
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, name)
	}
}

// Droid's own client sends "" for a question that holds no answer.
// The browser sends an answer only when every question has one, so the
// Worker refuses an answer that leaves a question blank, and one to a
// request of no question.
func TestAnswerControlRequest_DroidRefusesAQuestionLeftOpen(t *testing.T) {
	_, err := agenttest.AnswerControl(droidProvider{}, askUserRequestPayload(t, colorAndSizes()...), droidTestRequestID, agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"Blue"}},
		&leapmuxv1.QuestionAnswer{CustomText: "   "},
	))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

// A question with no whole-number index cannot identify its answer. The
// answer goes without one, and the Worker refuses the set.
func TestAnswerControlRequest_DroidSendsNoIndexForAQuestionWhoseIndexIsNotWhole(t *testing.T) {
	payload := droidJSON(t, map[string]any{"type": "ask_user_request", "questions": []any{
		map[string]any{"question": "No number?", "options": []string{"Yes"}},
		map[string]any{"index": "2", "question": "Text number?", "options": []string{"Yes"}},
		map[string]any{"index": 1.5, "question": "Fraction?", "options": []string{"Yes"}},
		map[string]any{"index": json.RawMessage(`4.0`), "question": "Whole?", "options": []string{"Yes"}},
	}})
	yes := &leapmuxv1.QuestionAnswer{Selected: []string{"Yes"}}
	assert.Equal(t, []map[string]any{{"answer": "Yes"}, {"answer": "Yes"}, {"answer": "Yes"}, {"index": 4.0, "answer": "Yes"}},
		droidSentAnswers(t, payload, agenttest.QuestionDecision(yes, yes, yes, yes)))
}

func TestAnswerControlRequest_DroidRefusesAnAnswerToARequestWithNoQuestion(t *testing.T) {
	_, err := agenttest.AnswerControl(droidProvider{}, askUserRequestPayload(t), droidTestRequestID, agenttest.QuestionDecision())
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "only a cancel answers a request of no question")
}

// A refusal is Droid's own cancel, which has no field for a reason. The
// Worker sends the reason as the user's next message.
func TestAnswerControlRequest_DroidCancelledQuestionDeniesWithItsReason(t *testing.T) {
	payload := askUserRequestPayload(t, colorAndSizes()...)
	got, err := agenttest.AnswerControl(droidProvider{}, payload, "droid-ask-1", agenttest.CancelQuestions("User stopped"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"droid-ask-1","response":{"behavior":"deny","message":"User stopped"}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp, "droidResolveControlResponse queues the reason itself")
	resolution := agenttest.ResolveAnswer(droidProvider{}, payload, "droid-ask-1", got)
	require.NoError(t, resolution.Refusal())
	assert.JSONEq(t, `{"cancelled":true,"answers":[]}`, string(droidResult(t, resolution.Content)))
	assert.Equal(t, agent.FeedbackFollowUp("User stopped"), resolution.FollowUp)
}

// droidResult reads the result of Droid's JSON-RPC reply.
func droidResult(t *testing.T, content []byte) json.RawMessage {
	t.Helper()
	var reply droidEnvelope
	require.NoError(t, json.Unmarshal(content, &reply))
	return reply.Result
}

func TestAnswerControlRequest_DroidSelectsTheOfferedOption(t *testing.T) {
	payload := permissionRequestPayload(t, "Edit", "edit", "proceed_once", "proceed_always", "proceed_auto_run_high", "cancel")
	for option, behavior := range map[string]string{
		"proceed_once": "allow", "proceed_always": "allow", "proceed_auto_run_high": "allow", "cancel": "deny",
	} {
		got, err := agenttest.AnswerControl(droidProvider{}, payload, "droid-perm-1", agenttest.PermissionDecision(option, ""))
		require.NoError(t, err, option)
		assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"droid-perm-1","response":{"behavior":"`+behavior+`","selectedOption":"`+option+`"}}}`, string(got.Content), option)
		resolution := agenttest.ResolveAnswer(droidProvider{}, payload, "droid-perm-1", got)
		require.NoError(t, resolution.Refusal(), option)
		assert.JSONEq(t, `{"selectedOption":"`+option+`"}`, string(droidResult(t, resolution.Content)), option)
		assert.Empty(t, got.FollowUp, option)
	}
}

// The composer's typed refusal is a deny with the text, which Droid reads as
// `cancel` and which the Worker sends as the next message.
func TestAnswerControlRequest_DroidRefusalWithFeedbackDeniesWithIt(t *testing.T) {
	payload := permissionRequestPayload(t, "Edit", "edit", "proceed_once", "cancel")
	got, err := agenttest.AnswerControl(droidProvider{}, payload, "droid-perm-1", agenttest.PermissionDecision("cancel", "Use the other file."))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"droid-perm-1","response":{"behavior":"deny","message":"Use the other file."}}}`, string(got.Content))
	assert.Empty(t, got.FollowUp)
	resolution := agenttest.ResolveAnswer(droidProvider{}, payload, "droid-perm-1", got)
	require.NoError(t, resolution.Refusal())
	assert.JSONEq(t, `{"selectedOption":"cancel"}`, string(droidResult(t, resolution.Content)))
	assert.Equal(t, agent.FeedbackFollowUp("Use the other file."), resolution.FollowUp)
}

func TestAnswerControlRequest_DroidLeapMuxRefusalDeniesARequestWithNoCancel(t *testing.T) {
	payload := permissionRequestPayload(t, "Edit", "edit", "proceed_once")
	for _, feedback := range []string{"", "No."} {
		got, err := agenttest.AnswerControl(droidProvider{}, payload, "droid-perm-1", agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, feedback))
		require.NoError(t, err)
		resolution := agenttest.ResolveAnswer(droidProvider{}, payload, "droid-perm-1", got)
		require.NoError(t, resolution.Refusal())
		assert.JSONEq(t, `{"selectedOption":"cancel"}`, string(droidResult(t, resolution.Content)), "a cancel needs no offer")
	}
}

func TestAnswerControlRequest_DroidRefusesFeedbackBesideAnOptionThatDoesNotRefuse(t *testing.T) {
	payload := permissionRequestPayload(t, "Edit", "edit", "proceed_once", "proceed_auto_run_high", "cancel")
	for _, option := range []string{"proceed_once", "proceed_auto_run_high"} {
		_, err := agenttest.AnswerControl(droidProvider{}, payload, droidTestRequestID, agenttest.PermissionDecision(option, "a note"))
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, option)
	}
	_, err := agenttest.AnswerControl(droidProvider{}, payload, droidTestRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a request that offers cancel gets no LeapMux refusal")
	_, err = agenttest.AnswerControl(droidProvider{}, payload, droidTestRequestID, agenttest.PermissionDecision("proceed_always", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an option that Droid did not offer")
}

// A request that offers no option takes only Deny, because Droid refuses an
// unoffered approval while cancel needs no offer.
func TestAnswerControlRequest_DroidRequestWithNoOptionsTakesOnlyDeny(t *testing.T) {
	payload := permissionRequestPayload(t, "Edit", "edit")
	_, err := agenttest.AnswerControl(droidProvider{}, payload, droidTestRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)

	got, err := agenttest.AnswerControl(droidProvider{}, payload, droidTestRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, "No."))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"droid-request","response":{"behavior":"deny","message":"No."}}}`, string(got.Content))

	// A payload outside the permission channel reads as unrecognized, so no
	// decision answers it.
	_, err = agenttest.AnswerControl(droidProvider{}, []byte(`{"type":"settings_updated"}`), droidTestRequestID, agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

// A plan takes the shared plan answer, and the Worker turns its permission
// mode into Droid's own approval option.
func TestAnswerControlRequest_DroidAnswersAPlan(t *testing.T) {
	payload := droidJSON(t, map[string]any{
		"type": "permission_request", "requestId": droidTestRequestID, "rpcId": "rpc-9",
		"toolUse":          map[string]any{"name": contracts.DroidToolExitSpecMode, "input": map[string]any{"plan": "# Plan"}},
		"confirmationType": contracts.DroidConfirmationTypeExitSpecMode,
		"details":          map[string]any{"plan": "# Plan"},
		"options":          []string{"proceed_once", "proceed_auto_run_high", "cancel"},
	})
	got, err := agenttest.AnswerControl(droidProvider{}, payload, droidTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, PermissionMode: contracts.DroidModeAutoHigh}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"droid-request","response":{"behavior":"allow","updatedInput":{}}}}`, string(got.Content))
	require.True(t, proto.Equal(&leapmuxv1.PlanApprovalSettings{PermissionMode: contracts.DroidModeAutoHigh}, got.PlanApproval))
	resolution := agenttest.ResolveAnswer(droidProvider{}, payload, droidTestRequestID, got)
	require.NoError(t, resolution.Refusal())
	assert.JSONEq(t, `{"selectedOption":"proceed_auto_run_high"}`, string(droidResult(t, resolution.Content)))

	got, err = agenttest.AnswerControl(droidProvider{}, payload, droidTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Feedback: "Split step 1."}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"droid-request","response":{"behavior":"deny","message":"Split step 1."}}}`, string(got.Content))
	assert.Nil(t, got.PlanApproval)

	_, err = agenttest.AnswerControl(droidProvider{}, payload, droidTestRequestID, agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true, ChoiceId: "proceed_auto_run_high"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "Droid's plan offers no choices")
	_, err = agenttest.AnswerControl(droidProvider{}, payload, droidTestRequestID, agenttest.PermissionDecision("proceed_once", ""))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "a plan takes no permission decision")
}

// Each question answer reaches Droid as its own reply, which pairs each
// answer with its question.
func TestAnswerControlRequest_DroidQuestionAnswerResolvesIntoDroidsReply(t *testing.T) {
	payload := askUserRequestPayload(t, colorAndSizes()...)
	got, err := agenttest.AnswerControl(droidProvider{}, payload, "droid-ask-1", agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"Red"}},
		&leapmuxv1.QuestionAnswer{Selected: []string{"Small"}, CustomText: "XL"},
	))
	require.NoError(t, err)
	resolution := agenttest.ResolveAnswer(droidProvider{}, payload, "droid-ask-1", got)
	require.NoError(t, resolution.Refusal())
	assert.JSONEq(t, `{"cancelled":false,"answers":[{"index":1,"question":"Which color?","answer":"Red"},{"index":2,"question":"Which sizes?","answer":"Small, XL"}]}`,
		string(droidResult(t, resolution.Content)))
}
