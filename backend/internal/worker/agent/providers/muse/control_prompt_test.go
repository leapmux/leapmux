package muse

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

func museJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}

// museApproval is an approval request that Muse sends with choices.
func museApproval(t *testing.T, choices any) []byte {
	t.Helper()
	params := map[string]any{
		"sessionId":            "session",
		"approvalId":           "approval",
		"currentRequirementId": map[string]any{"approvalId": "approval", "sourceIndex": 0},
		"subject":              map[string]any{"toolName": "bash", "command": "printf native"},
	}
	if choices != nil {
		params["availableChoices"] = choices
	}
	return museJSON(t, map[string]any{"method": "approval/requested", "params": params})
}

// museTestQuestion is one valid question, with fields overlaid.
func museTestQuestion(fields map[string]any) map[string]any {
	question := map[string]any{
		"id":       "native-question",
		"question": "Choose the native answer",
		"header":   "Native question",
		"options": []any{
			map[string]any{"label": "One", "description": "First", "preview": map[string]any{"content": "Native preview"}},
		},
		"selection": map[string]any{"mode": "single"},
	}
	for key, value := range fields {
		if value == nil {
			delete(question, key)
			continue
		}
		question[key] = value
	}
	return question
}

// museUserInput is a user-input request with questions.
func museUserInput(t *testing.T, questions any) []byte {
	t.Helper()
	return museJSON(t, map[string]any{
		"method": "userInput/requested",
		"params": map[string]any{"sessionId": "session", "userInputId": "native-input", "questions": questions},
	})
}

func museChoice(id, decision, scope string) map[string]any {
	return map[string]any{"choiceId": id, "label": "Native choice", "decision": decision, "scope": scope}
}

// museParamsOf reads the params of a frame, as musePermissionAnswer reads
// them.
func museParamsOf(t *testing.T, payload []byte) map[string]json.RawMessage {
	t.Helper()
	_, params, ok := museMethodParams(payload)
	require.True(t, ok)
	return params
}

func TestReadControlRequest_MuseKeepsEachOfferedChoiceWithItsKindAndScope(t *testing.T) {
	for _, test := range []struct {
		decision, scope string
		kind            leapmuxv1.PermissionOptionKind
		mapped          leapmuxv1.PermissionScope
	}{
		{"approved", "once", leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, leapmuxv1.PermissionScope_PERMISSION_SCOPE_UNSPECIFIED},
		{"approvedForSession", "session", leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, leapmuxv1.PermissionScope_PERMISSION_SCOPE_SESSION},
		{"approvedPolicyAmendment", "localPersistent", leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, leapmuxv1.PermissionScope_PERMISSION_SCOPE_WORKSPACE},
		{"denied", "once", leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, leapmuxv1.PermissionScope_PERMISSION_SCOPE_UNSPECIFIED},
		{"deniedPolicyAmendment", "localPersistent", leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ALWAYS, leapmuxv1.PermissionScope_PERMISSION_SCOPE_WORKSPACE},
		{"timedOut", "once", leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, leapmuxv1.PermissionScope_PERMISSION_SCOPE_UNSPECIFIED},
		{"abort", "once", leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, leapmuxv1.PermissionScope_PERMISSION_SCOPE_UNSPECIFIED},
	} {
		agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
			Title:   "bash",
			Command: "printf native",
			Options: []*leapmuxv1.PermissionOption{{OptionId: "choice-0", Name: "Native choice", Kind: test.kind, Scope: test.mapped}},
		}}}, agenttest.ControlPromptOf(museProvider{}, museApproval(t, []any{museChoice("choice-0", test.decision, test.scope)})))
	}
}

func TestReadControlRequest_MuseTakesTheToolNameOfTheParamsFirst(t *testing.T) {
	payload := museJSON(t, map[string]any{"method": "approval/request", "params": map[string]any{
		"toolName": "edit", "subject": map[string]any{"toolName": "bash"}, "availableChoices": []any{museChoice("c", "approved", "once")},
	}})
	assert.Equal(t, "edit", agenttest.ControlPromptOf(museProvider{}, payload).GetPermission().GetTitle())
	payload = museJSON(t, map[string]any{"method": "approval/updated", "params": map[string]any{"availableChoices": []any{museChoice("c", "approved", "once")}}})
	permission := agenttest.ControlPromptOf(museProvider{}, payload).GetPermission()
	assert.Empty(t, permission.GetTitle())
	assert.Empty(t, permission.GetCommand())
	assert.Len(t, permission.GetOptions(), 1, "every approval method states its choices")
}

// LeapMux invents no choice for malformed data: one invalid or unknown
// choice spoils the whole list, and the request reads as unrecognized.
func TestReadControlRequest_MuseReturnsNoInventedChoicesForMalformedData(t *testing.T) {
	for _, choices := range []any{
		nil, false, 0, map[string]any{}, []any{}, []any{nil},
		[]any{museChoice("", "approved", "once")},
		[]any{museChoice(" ", "denied", "once")},
		[]any{museChoice("\t\n", "denied", "once")},
		[]any{map[string]any{"choiceId": "choice", "label": "", "decision": "approved", "scope": "once"}},
		[]any{museChoice("choice", "futureDecision", "once")},
		[]any{museChoice("choice", "approved", "futureScope")},
		[]any{nil, museChoice("choice", "approved", "once")},
		[]any{museChoice("deny", "denied", "once"), museChoice("deny", "denied", "once")},
		[]any{map[string]any{"choiceId": "deny", "label": "Deny", "decision": "denied", "scope": "once", "acceptsFeedback": "true"}},
	} {
		agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(museProvider{}, museApproval(t, choices)))
	}
	agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(museProvider{}, []byte(`{"method":"","params":{"availableChoices":[]}}`)))
	agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(museProvider{}, []byte(`{"method":"item/started","params":{}}`)))
	agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(museProvider{}, []byte(`not json`)))
}

func TestMuseRefusalWithFeedback_SendsTheChosenChoiceWithTheFeedback(t *testing.T) {
	params := museParamsOf(t, museApproval(t, []any{
		map[string]any{"choiceId": "no-feedback", "label": "Deny", "decision": "denied", "scope": "once"},
		map[string]any{"choiceId": "with-feedback", "label": "Deny with feedback", "decision": "denied", "scope": "once", "acceptsFeedback": true},
	}))
	got, err := museRefusalWithFeedback(params, "with-feedback", "  Native feedback\n", "worker-request")
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"worker-request","result":{"choiceId":"with-feedback","feedback":"  Native feedback\n"}}`, string(got.Content))

	_, err = museRefusalWithFeedback(params, "no-feedback", "Native feedback", "worker-request")
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
	assert.ErrorContains(t, err, `the Muse choice "no-feedback" takes no feedback`, "the chosen refusal is never swapped for another")
}

func TestMuseRefusalWithFeedback_RefusesADenialThatMuseCannotTake(t *testing.T) {
	refuses := func(choices any, optionID, reason string) {
		t.Helper()
		_, err := museRefusalWithFeedback(museParamsOf(t, museApproval(t, choices)), optionID, "Feedback", "worker-request")
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
		assert.ErrorContains(t, err, reason)
	}
	const invalid = "the native Muse approval choices are invalid"
	for name, choices := range map[string]any{
		"absent list":           nil,
		"null list":             json.RawMessage(`null`),
		"empty list":            []any{},
		"null entry":            []any{nil},
		"absent ID":             []any{map[string]any{"label": "Deny", "decision": "denied", "scope": "once"}},
		"empty ID":              []any{museChoice("", "denied", "once")},
		"blank ID":              []any{museChoice(" ", "denied", "once")},
		"numeric ID":            []any{map[string]any{"choiceId": 0, "label": "Deny", "decision": "denied", "scope": "once"}},
		"null ID":               []any{map[string]any{"choiceId": nil, "label": "Deny", "decision": "denied", "scope": "once"}},
		"absent label":          []any{map[string]any{"choiceId": "deny", "decision": "denied", "scope": "once"}},
		"invalid feedback flag": []any{map[string]any{"choiceId": "deny", "label": "Deny", "decision": "denied", "scope": "once", "acceptsFeedback": "true"}},
		"partial list":          []any{nil, museChoice("deny", "denied", "once")},
		"duplicate IDs":         []any{museChoice("deny", "denied", "once"), museChoice("deny", "denied", "once")},
	} {
		t.Run(name, func(t *testing.T) { refuses(choices, "deny", invalid) })
	}
	refuses([]any{museChoice("deny", "denied", "once")}, "deny", `the Muse choice "deny" takes no feedback`)
	refuses([]any{map[string]any{"choiceId": "deny", "label": "Deny", "decision": "denied", "scope": "once", "acceptsFeedback": true}}, contracts.ControlAnswerOptionDeny,
		`Muse offers no choice "`+contracts.ControlAnswerOptionDeny+`"`)
}

func TestAnswerControlRequest_MuseSendsTheExactWorkerRequestIDAndNativeChoiceID(t *testing.T) {
	payload := museApproval(t, []any{museChoice("native-choice:0", "approved", "once"), museChoice("native-deny", "denied", "once")})
	got, err := agenttest.AnswerControl(museProvider{}, payload, "worker-request:0", agenttest.PermissionDecision("native-choice:0", ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"worker-request:0","result":{"choiceId":"native-choice:0"}}`, string(got.Content))
	assert.Empty(t, got.FollowUp)

	got, err = agenttest.AnswerControl(museProvider{}, payload, "worker-request:0", agenttest.PermissionDecision("native-deny", ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"worker-request:0","result":{"choiceId":"native-deny"}}`, string(got.Content), "a refusing option is chosen as it is")
}

// A refusal with feedback answers with the refusal that the user chose, so
// a remembered refusal keeps its scope. The browser's composer always sent
// the once-scope denial instead.
func TestAnswerControlRequest_MuseRefusalWithFeedbackKeepsTheChosenRefusal(t *testing.T) {
	payload := museApproval(t, []any{
		museChoice("allow", "approved", "once"),
		map[string]any{"choiceId": "deny-always", "label": "Never", "decision": "deniedPolicyAmendment", "scope": "localPersistent", "acceptsFeedback": true},
		map[string]any{"choiceId": "deny-note", "label": "Deny", "decision": "denied", "scope": "once", "acceptsFeedback": true},
	})
	for _, option := range []string{"deny-always", "deny-note"} {
		got, err := agenttest.AnswerControl(museProvider{}, payload, "worker-request", agenttest.PermissionDecision(option, "Use the dry run."))
		require.NoError(t, err, option)
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":"worker-request","result":{"choiceId":"`+option+`","feedback":"Use the dry run."}}`, string(got.Content), option)
	}
	_, err := agenttest.AnswerControl(museProvider{}, payload, "worker-request", agenttest.PermissionDecision("allow", "a note"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an approval carries no feedback")

	noFeedback := museApproval(t, []any{museChoice("allow", "approved", "once"), museChoice("deny", "denied", "once")})
	_, err = agenttest.AnswerControl(museProvider{}, noFeedback, "worker-request", agenttest.PermissionDecision("deny", "Feedback"))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "the chosen denial accepts no feedback")
}

// A request with unreadable choices or questions reads as unrecognized: the
// resolution refuses LeapMux's own Deny to each of them, and its Allow to all
// but a few, so no decision answers such a request.
func TestAnswerControlRequest_MuseAnswersNoRequestThatAnElementSpoils(t *testing.T) {
	for _, payload := range [][]byte{
		museApproval(t, 0),
		museApproval(t, []any{museChoice("native", "futureDecision", "once")}),
		museApproval(t, []any{museChoice("allow", "approved", "once"), museChoice("native", "futureDecision", "once")}),
		museUserInput(t, 0),
	} {
		agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(museProvider{}, payload), "%s", payload)
		for _, decision := range []*leapmuxv1.ControlDecision{
			agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, ""),
			agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""),
			agenttest.PermissionDecision(contracts.ControlAnswerOptionDeny, "Feedback"),
		} {
			_, err := agenttest.AnswerControl(museProvider{}, payload, "native-pending", decision)
			assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "%s", payload)
			_, err = museProvider{}.AnswerControlRequest(agent.ControlAnswerInput{
				Request: agent.NewControlRequestInput(payload), RequestID: "native-pending",
				Prompt: agent.GenericPermissionPrompt(), Decision: decision,
			})
			assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "%s", payload)
		}
	}
}

func museQuestionPrompt(t *testing.T, payload []byte) *leapmuxv1.ControlQuestion {
	t.Helper()
	question := agenttest.ControlPromptOf(museProvider{}, payload).GetQuestion()
	require.NotNil(t, question)
	assert.True(t, question.GetPreservesSelectionNotes(), "a Muse answer keeps a note beside a selection")
	require.Len(t, question.GetQuestions(), 1)
	return question.GetQuestions()[0]
}

func threeOptions() []any {
	return []any{map[string]any{"label": "One"}, map[string]any{"label": "Two"}, map[string]any{"label": "Three"}}
}

func TestReadControlRequest_MusePreservesTheCountsOfAMultipleSelection(t *testing.T) {
	two, three, zero := int32(2), int32(3), int32(0)
	for name, test := range map[string]struct {
		selection        map[string]any
		minimum, maximum *int32
	}{
		"explicit zero":            {map[string]any{"mode": "multiple", "minSelections": 0, "maxSelections": 0}, &zero, &zero},
		"explicit positive limits": {map[string]any{"mode": "multiple", "minSelections": 2, "maxSelections": 3}, &two, &three},
		"only a minimum":           {map[string]any{"mode": "multiple", "minSelections": 2}, &two, nil},
		"only a maximum":           {map[string]any{"mode": "multiple", "maxSelections": 2}, nil, &two},
		"a whole number as 2.0":    {map[string]any{"mode": "multiple", "minSelections": json.RawMessage(`2.0`)}, &two, nil},
	} {
		question := museQuestionPrompt(t, museUserInput(t, []any{museTestQuestion(map[string]any{"options": threeOptions(), "selection": test.selection})}))
		assert.Equal(t, test.minimum, question.MinimumSelections, name)
		assert.Equal(t, test.maximum, question.MaximumSelections, name)
	}
}

func TestReadControlRequest_MuseKeepsAbsentCountsAbsent(t *testing.T) {
	for _, mode := range []string{"single", "multiple"} {
		question := museQuestionPrompt(t, museUserInput(t, []any{museTestQuestion(map[string]any{"selection": map[string]any{"mode": mode}})}))
		assert.Nil(t, question.MinimumSelections, mode)
		assert.Nil(t, question.MaximumSelections, mode)
		assert.Equal(t, mode == "multiple", question.GetMultiSelect(), mode)
		assert.False(t, question.GetAllowEmpty(), "Muse states no empty answer")
	}
	// A single choice has exactly one answer and states no counts, whatever
	// its form says.
	question := museQuestionPrompt(t, museUserInput(t, []any{museTestQuestion(map[string]any{
		"selection": map[string]any{"mode": "single", "minSelections": 0, "maxSelections": 1},
	})}))
	assert.Nil(t, question.MinimumSelections)
	assert.Nil(t, question.MaximumSelections)
}

func TestReadControlRequest_MuseRefusesTheCompleteFormForOneInvalidQuestion(t *testing.T) {
	for name, fields := range map[string]map[string]any{
		"absent header":          {"header": nil},
		"null header":            {"header": json.RawMessage(`null`)},
		"numeric header":         {"header": 0},
		"blank ID":               {"id": " \t\n"},
		"null selection minimum": {"selection": map[string]any{"mode": "multiple", "minSelections": nil}},
		"null selection maximum": {"selection": map[string]any{"mode": "multiple", "maxSelections": nil}},
		"negative minimum":       {"selection": map[string]any{"mode": "multiple", "minSelections": -1}},
		"negative maximum":       {"selection": map[string]any{"mode": "multiple", "maxSelections": -1}},
		"conflicting counts":     {"selection": map[string]any{"mode": "multiple", "minSelections": 1, "maxSelections": 0}},
		"excessive maximum":      {"selection": map[string]any{"mode": "multiple", "maxSelections": 2}},
		"numeric-string minimum": {"selection": map[string]any{"mode": "multiple", "minSelections": "0"}},
		"fractional maximum":     {"selection": map[string]any{"mode": "multiple", "maxSelections": 0.5}},
	} {
		if _, ok := fields["id"]; !ok {
			fields["id"] = "later-question"
		}
		later := museTestQuestion(fields)
		require.Truef(t, proto.Equal(agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(museProvider{}, museUserInput(t, []any{museTestQuestion(nil), later}))), "%s refuses the form", name)
		assert.Nil(t, museQuestionPrompts(museParamsOf(t, museUserInput(t, []any{museTestQuestion(nil), later}))), name)
	}
}

func TestReadControlRequest_MuseKeepsAnEmptyHeaderAndTheExactNonblankQuestionID(t *testing.T) {
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions:               []*leapmuxv1.ControlQuestion{{Id: " native 界 ", Question: "Choose the native answer", Options: []*leapmuxv1.QuestionOption{}}},
		PreservesSelectionNotes: true,
	}}}, agenttest.ControlPromptOf(museProvider{}, museUserInput(t, []any{museTestQuestion(map[string]any{"id": " native 界 ", "header": "", "options": []any{}})})))
}

func TestReadControlRequest_MuseKeepsTheNativeQuestionIDAndOptionPreview(t *testing.T) {
	one := "One"
	agenttest.RequireProto(t, &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: []*leapmuxv1.ControlQuestion{{
			Id: "native-question", Header: "Native question", Question: "Choose the native answer",
			Options: []*leapmuxv1.QuestionOption{{Value: &one, Label: "One", Description: "First", Preview: "Native preview"}},
		}},
		PreservesSelectionNotes: true,
	}}}, agenttest.ControlPromptOf(museProvider{}, museUserInput(t, []any{museTestQuestion(nil)})))
	assert.Empty(t, museQuestionPrompt(t, museUserInput(t, []any{museTestQuestion(map[string]any{"options": []any{}})})).GetOptions(),
		"a free-text question keeps an empty option list")
}

func TestReadControlRequest_MuseRejectsAMalformedCompleteQuestionList(t *testing.T) {
	for _, questions := range []any{
		nil, false, 0, map[string]any{}, []any{}, []any{nil},
		[]any{museTestQuestion(map[string]any{"id": ""})},
		[]any{museTestQuestion(map[string]any{"question": ""})},
		[]any{museTestQuestion(map[string]any{"options": []any{nil}})},
		[]any{museTestQuestion(map[string]any{"options": []any{map[string]any{"label": "One"}, map[string]any{"label": "One"}}})},
		[]any{museTestQuestion(map[string]any{"options": []any{map[string]any{"label": " "}}})},
		[]any{museTestQuestion(map[string]any{"selection": map[string]any{"mode": "futureMode"}})},
		[]any{museTestQuestion(map[string]any{"selection": []any{}})},
		[]any{museTestQuestion(map[string]any{"options": "One"})},
		[]any{museTestQuestion(nil), nil},
		[]any{museTestQuestion(nil), museTestQuestion(nil)},
	} {
		agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(museProvider{}, museUserInput(t, questions)))
	}
	// Only a user-input request asks.
	agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(museProvider{}, museJSON(t, map[string]any{"method": "userInput/settled", "params": map[string]any{"questions": []any{museTestQuestion(nil)}}})))
}

func TestAnswerControlRequest_MuseKeepsExactSelectionsAndIndependentTypedNotes(t *testing.T) {
	payload := museUserInput(t, []any{
		map[string]any{"id": "single", "question": "Single", "header": "", "options": []any{map[string]any{"label": "One"}}, "selection": map[string]any{"mode": "single"}},
		map[string]any{"id": "multiple", "question": "Multiple", "header": "", "options": []any{map[string]any{"label": "First"}, map[string]any{"label": "Second"}}, "selection": map[string]any{"mode": "multiple"}},
		map[string]any{"id": "text", "question": "Text", "header": "", "options": []any{}, "selection": map[string]any{"mode": "single"}},
	})
	got, err := agenttest.AnswerControl(museProvider{}, payload, "worker-request", agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{Selected: []string{"One"}, CustomText: "  Single note  "},
		&leapmuxv1.QuestionAnswer{Selected: []string{"First", "Second"}, CustomText: "Multiple\nnote"},
		&leapmuxv1.QuestionAnswer{CustomText: "  Free text\n"},
	))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"worker-request","result":{"answers":[
		{"questionId":"single","selectedLabel":"One","note":"  Single note  "},
		{"questionId":"multiple","selectedLabels":["First","Second"],"note":"Multiple\nnote"},
		{"questionId":"text","freeText":"  Free text\n"}
	]}}`, string(got.Content))
	assert.Empty(t, got.FollowUp)
}

// Free text keeps its own white space, and a cancel is Muse's own answer. The
// browser sends an answer only when every question has one, so the Worker
// refuses an answer that leaves a question with no words.
func TestAnswerControlRequest_MuseKeepsFreeTextAndTellsItFromCancellation(t *testing.T) {
	payload := museUserInput(t, []any{museTestQuestion(map[string]any{"options": []any{}}), museTestQuestion(map[string]any{"id": "second", "selection": map[string]any{"mode": "multiple"}})})
	got, err := agenttest.AnswerControl(museProvider{}, payload, "worker-request", agenttest.QuestionDecision(
		&leapmuxv1.QuestionAnswer{CustomText: "  Native text 界\n"},
		&leapmuxv1.QuestionAnswer{Selected: []string{"One"}},
	))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"worker-request","result":{"answers":[
		{"questionId":"native-question","freeText":"  Native text 界\n"},
		{"questionId":"second","selectedLabels":["One"]}
	]}}`, string(got.Content))
	_, err = agenttest.AnswerControl(museProvider{}, payload, "worker-request", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{CustomText: "  Native text 界\n"}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision, "an answer that leaves a question open")

	for _, reason := range []string{"", " \t\n", "The user declined this question.", "  Native reason 界\n", "User stopped"} {
		got, err = agenttest.AnswerControl(museProvider{}, payload, "worker-request", agenttest.CancelQuestions(reason))
		require.NoError(t, err, reason)
		assert.JSONEq(t, string(museJSON(t, map[string]any{"jsonrpc": "2.0", "id": "worker-request", "result": map[string]any{"cancelled": true, "reason": reason}})), string(got.Content), reason)
	}
}

func TestAnswerControlRequest_MusePreservesTheNativeSelectionForm(t *testing.T) {
	limited := museUserInput(t, []any{museTestQuestion(map[string]any{
		"options": threeOptions(), "selection": map[string]any{"mode": "multiple", "minSelections": 2, "maxSelections": 2},
	})})
	got, err := agenttest.AnswerControl(museProvider{}, limited, "native-question-request", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"One", "Two"}}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"native-question-request","result":{"answers":[{"questionId":"native-question","selectedLabels":["One","Two"]}]}}`, string(got.Content))

	single := museUserInput(t, []any{museTestQuestion(map[string]any{"options": threeOptions()})})
	got, err = agenttest.AnswerControl(museProvider{}, single, "native-question-request", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Two"}}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":"native-question-request","result":{"answers":[{"questionId":"native-question","selectedLabel":"Two"}]}}`, string(got.Content))

	_, err = agenttest.AnswerControl(museProvider{}, single, "native-question-request", agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"Four"}}))
	assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
}

// Unreadable questions read as the generic prompt, so a question decision
// does not answer them; the shared Deny does.
func TestAnswerControlRequest_MuseUnreadableQuestionsTakeNoQuestionDecision(t *testing.T) {
	for _, questions := range []any{nil, 0, false, map[string]any{}, []any{}} {
		_, err := agenttest.AnswerControl(museProvider{}, museUserInput(t, questions), "worker-request", agenttest.CancelQuestions("Cancel"))
		assert.ErrorIs(t, err, agent.ErrInvalidControlDecision)
	}
}

// ResolveControlResponse reads each answer into Muse's own reply, so the
// answers here are ones that the Worker forwards.
func TestAnswerControlRequest_MuseAnswersResolveIntoMusesReply(t *testing.T) {
	resolve := func(t *testing.T, payload []byte, decision *leapmuxv1.ControlDecision) map[string]any {
		t.Helper()
		var request struct {
			Params controlParams `json:"params"`
		}
		require.NoError(t, json.Unmarshal(payload, &request))
		key, err := controlID(request.Params)
		require.NoError(t, err)
		answer, err := agenttest.AnswerControl(museProvider{}, payload, key, decision)
		require.NoError(t, err)
		resolution := agenttest.ResolveAnswer(museProvider{}, payload, key, answer)
		require.NoError(t, resolution.Refusal())
		var reply map[string]any
		require.NoError(t, json.Unmarshal(resolution.Content, &reply))
		return reply
	}

	approval := museApproval(t, []any{
		museChoice("allow", "approved", "once"),
		map[string]any{"choiceId": "deny", "label": "Deny", "decision": "denied", "scope": "once", "acceptsFeedback": true},
	})
	reply := resolve(t, approval, agenttest.PermissionDecision("allow", ""))
	assert.Equal(t, "approval/decide", reply["method"])
	assert.Equal(t, "allow", reply["params"].(map[string]any)["choiceId"])
	reply = resolve(t, approval, agenttest.PermissionDecision("deny", "Use the dry run."))
	assert.Equal(t, "deny", reply["params"].(map[string]any)["choiceId"])
	assert.Equal(t, "Use the dry run.", reply["params"].(map[string]any)["feedback"])

	remembered := museApproval(t, []any{
		museChoice("allow", "approved", "once"),
		map[string]any{"choiceId": "deny-always", "label": "Never", "decision": "deniedPolicyAmendment", "scope": "localPersistent", "acceptsFeedback": true},
	})
	reply = resolve(t, remembered, agenttest.PermissionDecision("deny-always", "Never in this project."))
	assert.Equal(t, "deny-always", reply["params"].(map[string]any)["choiceId"], "a remembered refusal with feedback keeps its scope")
	assert.Equal(t, "Never in this project.", reply["params"].(map[string]any)["feedback"])

	question := museUserInput(t, []any{museTestQuestion(map[string]any{"options": threeOptions(), "selection": map[string]any{"mode": "multiple"}})})
	reply = resolve(t, question, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"One", "Three"}, CustomText: "Why"}))
	assert.Equal(t, "userInput/answer", reply["method"])
	assert.Equal(t, []any{map[string]any{"questionId": "native-question", "selectedLabels": []any{"One", "Three"}, "note": "Why"}}, reply["params"].(map[string]any)["answers"])
	reply = resolve(t, question, agenttest.CancelQuestions("User stopped"))
	assert.Equal(t, "User stopped", reply["params"].(map[string]any)["reason"])
}

// The prompt states only questions that the resolver accepts an answer to:
// readNativeQuestions decides both, so a question that the resolver refuses
// reads as unrecognized, and one that it accepts reads as a question.
func TestReadControlRequest_MuseStatesOnlyQuestionsThatTheResolverAccepts(t *testing.T) {
	badCounts := museUserInput(t, []any{museTestQuestion(map[string]any{
		"options": threeOptions(), "selection": map[string]any{"mode": "single", "minSelections": 2, "maxSelections": 1},
	})})
	agenttest.RequireProto(t, agent.UnrecognizedControlPrompt(), agenttest.ControlPromptOf(museProvider{}, badCounts))

	wholeCount := museUserInput(t, []any{museTestQuestion(map[string]any{
		"options": threeOptions(), "selection": map[string]any{"mode": "multiple", "minSelections": 2.0, "maxSelections": 3},
	})})
	question := agenttest.ControlPromptOf(museProvider{}, wholeCount).GetQuestion().GetQuestions()
	require.Len(t, question, 1)
	assert.EqualValues(t, 2, question[0].GetMinimumSelections(), "2.0 is a whole number, as the browser read it")
	var request struct {
		Params controlParams `json:"params"`
	}
	require.NoError(t, json.Unmarshal(wholeCount, &request))
	key, err := controlID(request.Params)
	require.NoError(t, err)
	answer, err := agenttest.AnswerControl(museProvider{}, wholeCount, key, agenttest.QuestionDecision(&leapmuxv1.QuestionAnswer{Selected: []string{"One", "Two"}}))
	require.NoError(t, err)
	resolution := agenttest.ResolveAnswer(museProvider{}, wholeCount, key, answer)
	assert.NoError(t, resolution.Refusal(), "the resolver accepts the count that the prompt stated")
}
