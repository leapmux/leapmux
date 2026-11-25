package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func questionInput(questions []*leapmuxv1.ControlQuestion, decision *leapmuxv1.QuestionDecision) ControlAnswerInput {
	return ControlAnswerInput{
		Request:   NewControlRequestInput([]byte(`{}`)),
		RequestID: "req-1",
		Prompt:    &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: questions}}},
		Decision:  &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Question{Question: decision}},
	}
}

func TestQuestionOptionValue(t *testing.T) {
	assert.Equal(t, "", QuestionOptionValue(nil))
	assert.Equal(t, "Red", QuestionOptionValue(&leapmuxv1.QuestionOption{Label: "Red"}))
	assert.Equal(t, "r", QuestionOptionValue(&leapmuxv1.QuestionOption{Label: "Red", Value: new("r")}))
	assert.Equal(t, "", QuestionOptionValue(&leapmuxv1.QuestionOption{Label: "Red", Value: new("")}), "a stated empty value is the value")
}

func TestQuestionDecisionOf(t *testing.T) {
	questions := []*leapmuxv1.ControlQuestion{
		{Question: "One?", Options: []*leapmuxv1.QuestionOption{{Label: "A"}, {Label: "B", Value: new("b")}}},
		{Question: "Many?", MultiSelect: true, Options: []*leapmuxv1.QuestionOption{{Label: "X"}, {Label: "Y"}}},
		{Question: "Options only?", AllowCustomText: new(false), Options: []*leapmuxv1.QuestionOption{{Label: "Z"}}},
		{Question: "Skip?", AllowEmpty: true},
	}
	answers := func(answers ...*leapmuxv1.QuestionAnswer) *leapmuxv1.QuestionDecision {
		return &leapmuxv1.QuestionDecision{Answers: answers}
	}
	pick := func(values ...string) *leapmuxv1.QuestionAnswer { return &leapmuxv1.QuestionAnswer{Selected: values} }
	typed := func(text string) *leapmuxv1.QuestionAnswer { return &leapmuxv1.QuestionAnswer{CustomText: text} }
	for name, decision := range map[string]*leapmuxv1.QuestionDecision{
		"answers by value":                 answers(pick("b"), pick("X", "Y"), pick("Z")),
		"typed answers":                    answers(typed("typed"), typed("W"), pick("Z"), typed("later")),
		"a cancellation":                   {Cancelled: true, Feedback: "later"},
		"options and text of many choices": answers(pick("A"), &leapmuxv1.QuestionAnswer{Selected: []string{"X"}, CustomText: "W"}, pick("Z")),
		"blank text beside one choice":     answers(&leapmuxv1.QuestionAnswer{Selected: []string{"A"}, CustomText: " \n"}, pick("X"), pick("Z")),
		"blank text where options only":    answers(pick("A"), pick("X"), &leapmuxv1.QuestionAnswer{Selected: []string{"Z"}, CustomText: "  "}),
		"a byte order mark is white space": answers(pick("A"), pick("X"), &leapmuxv1.QuestionAnswer{Selected: []string{"Z"}, CustomText: "\uFEFF"}),
	} {
		_, _, err := QuestionDecisionOf(questionInput(questions, decision))
		assert.NoError(t, err, name)
	}
	for name, decision := range map[string]*leapmuxv1.QuestionDecision{
		"more answers than questions":       {Answers: make([]*leapmuxv1.QuestionAnswer, 5)},
		"an unknown option":                 answers(pick("C"), pick("X"), pick("Z")),
		"a label where a value is":          answers(pick("B"), pick("X"), pick("Z")),
		"two options of one choice":         answers(pick("A", "b"), pick("X"), pick("Z")),
		"one option twice":                  answers(pick("A"), pick("X", "X"), pick("Z")),
		"text where options only":           answers(pick("A"), pick("X"), &leapmuxv1.QuestionAnswer{Selected: []string{"Z"}, CustomText: "typed"}),
		"answers beside a cancel":           {Cancelled: true, Answers: []*leapmuxv1.QuestionAnswer{{}}},
		"text beside one choice":            answers(&leapmuxv1.QuestionAnswer{Selected: []string{"A"}, CustomText: "note"}, pick("X"), pick("Z")),
		"a question left out":               answers(pick("A"), pick("X")),
		"no answers":                        {},
		"blank text alone":                  answers(typed(" \t"), pick("X"), pick("Z")),
		"a U+0085 is text for JavaScript":   answers(pick("A"), pick("X"), &leapmuxv1.QuestionAnswer{Selected: []string{"Z"}, CustomText: "\u0085"}),
		"an answer to the last only":        answers(nil, nil, nil, typed("x")),
		"an empty answer where none is due": answers(pick("A"), &leapmuxv1.QuestionAnswer{}, pick("Z")),
	} {
		_, _, err := QuestionDecisionOf(questionInput(questions, decision))
		assert.ErrorIs(t, err, ErrInvalidControlDecision, name)
	}

	// A prompt that keeps a note beside a selection takes both on one choice.
	notes := questionInput(questions, answers(&leapmuxv1.QuestionAnswer{Selected: []string{"A"}, CustomText: "note"}, pick("X"), pick("Z")))
	notes.Prompt.GetQuestion().PreservesSelectionNotes = true
	_, _, err := QuestionDecisionOf(notes)
	assert.NoError(t, err, "a note beside the option of one choice")

	_, _, err = QuestionDecisionOf(questionInput(nil, answers()))
	assert.ErrorContains(t, err, "asks no question", "only a cancel answers a prompt of no question")
	_, _, err = QuestionDecisionOf(questionInput(nil, &leapmuxv1.QuestionDecision{Cancelled: true}))
	assert.NoError(t, err)

	_, _, err = QuestionDecisionOf(ControlAnswerInput{Prompt: GenericPermissionPrompt(), Decision: &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Question{Question: &leapmuxv1.QuestionDecision{}}}})
	assert.ErrorIs(t, err, ErrInvalidControlDecision, "a question decision does not answer a permission")
}

// The browser counts a selection against the question's counts, and typed
// text never completes a selection outside them.
func TestQuestionDecisionOf_Counts(t *testing.T) {
	two := int32(2)
	three := int32(3)
	question := []*leapmuxv1.ControlQuestion{{
		Question: "Pick", MultiSelect: true, MinimumSelections: &two, MaximumSelections: &three,
		Options: []*leapmuxv1.QuestionOption{{Label: "A"}, {Label: "B"}, {Label: "C"}, {Label: "D"}},
	}}
	for selected, valid := range map[string]bool{"A": false, "A,B": true, "A,B,C": true, "A,B,C,D": false} {
		decision := &leapmuxv1.QuestionDecision{Answers: []*leapmuxv1.QuestionAnswer{{Selected: strings.Split(selected, ",")}}}
		_, _, err := QuestionDecisionOf(questionInput(question, decision))
		if valid {
			assert.NoError(t, err, selected)
		} else {
			assert.ErrorIs(t, err, ErrInvalidControlDecision, selected)
		}
	}
	shortWithText := &leapmuxv1.QuestionDecision{Answers: []*leapmuxv1.QuestionAnswer{{Selected: []string{"A"}, CustomText: "and more"}}}
	_, _, err := QuestionDecisionOf(questionInput(question, shortWithText))
	assert.ErrorContains(t, err, "at least 2 options", "text does not complete a short selection")
	textAlone := &leapmuxv1.QuestionDecision{Answers: []*leapmuxv1.QuestionAnswer{{CustomText: "none of these"}}}
	_, _, err = QuestionDecisionOf(questionInput(question, textAlone))
	assert.NoError(t, err, "the counts apply to a selection, and typed text alone answers")
}

func TestQuestionAnswerValues(t *testing.T) {
	one := &leapmuxv1.ControlQuestion{Question: "One?"}
	many := &leapmuxv1.ControlQuestion{Question: "Many?", MultiSelect: true}
	for name, tc := range map[string]struct {
		question *leapmuxv1.ControlQuestion
		answer   *leapmuxv1.QuestionAnswer
		want     []string
	}{
		"options and text of many choices": {many, &leapmuxv1.QuestionAnswer{Selected: []string{"A", "B"}, CustomText: "  X \n"}, []string{"A", "B", "X"}},
		"options of many choices":          {many, &leapmuxv1.QuestionAnswer{Selected: []string{"A", "B"}}, []string{"A", "B"}},
		"text of many choices":             {many, &leapmuxv1.QuestionAnswer{CustomText: "X"}, []string{"X"}},
		"blank text of many choices":       {many, &leapmuxv1.QuestionAnswer{Selected: []string{"A"}, CustomText: " \t"}, []string{"A"}},
		"the option of one choice":         {one, &leapmuxv1.QuestionAnswer{Selected: []string{"A"}}, []string{"A"}},
		"the text of one choice":           {one, &leapmuxv1.QuestionAnswer{CustomText: " X "}, []string{"X"}},
		"an option before a note":          {one, &leapmuxv1.QuestionAnswer{Selected: []string{"A"}, CustomText: "note"}, []string{"A"}},
		"no answer":                        {one, &leapmuxv1.QuestionAnswer{}, nil},
		"an empty value":                   {many, &leapmuxv1.QuestionAnswer{Selected: []string{""}}, []string{""}},
	} {
		assert.Equal(t, tc.want, QuestionAnswerValues(tc.question, tc.answer), name)
	}
	answer := &leapmuxv1.QuestionAnswer{Selected: []string{"A"}, CustomText: "X"}
	QuestionAnswerValues(many, answer)
	assert.Equal(t, []string{"A"}, answer.GetSelected(), "the values never write into the answer's own selection")
}

func TestQuestionAnswerAt(t *testing.T) {
	decision := &leapmuxv1.QuestionDecision{Answers: []*leapmuxv1.QuestionAnswer{{CustomText: "first"}, nil}}
	assert.Equal(t, "first", QuestionAnswerAt(decision, 0).GetCustomText())
	assert.NotNil(t, QuestionAnswerAt(decision, 1), "a nil entry reads as an empty answer")
	assert.True(t, proto.Equal(&leapmuxv1.QuestionAnswer{}, QuestionAnswerAt(decision, 2)), "an unanswered question reads as empty")
	assert.True(t, proto.Equal(&leapmuxv1.QuestionAnswer{}, QuestionAnswerAt(decision, -1)), "a negative index reads as empty")
}

func planInput(choices []*leapmuxv1.PlanChoice, decision *leapmuxv1.PlanDecision) ControlAnswerInput {
	return ControlAnswerInput{
		Request:   NewControlRequestInput([]byte(`{"request":{"tool_name":"ExitPlanMode","input":{"plan":"1. Edit."}}}`)),
		RequestID: "req-1",
		Prompt:    &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{Choices: choices}}},
		Decision:  &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Plan{Plan: decision}},
	}
}

func TestPlanDecisionOf(t *testing.T) {
	choices := planChoices()
	for name, decision := range map[string]*leapmuxv1.PlanDecision{
		"a plain approve":                {Approve: true},
		"an approval with settings":      {Approve: true, PermissionMode: "acceptEdits", ClearContext: true},
		"a plain refusal":                {Feedback: "no"},
		"an approving choice":            {Approve: true, ChoiceId: "fast", ClearContext: true},
		"a refusing choice":              {ChoiceId: "stop", Feedback: "Add a test."},
		"a choice of another effect":     {ChoiceId: "later"},
		"a choice that states no effect": {ChoiceId: "legacy"},
	} {
		_, err := PlanDecisionOf(planInput(choices, decision))
		assert.NoError(t, err, name)
	}
	for name, decision := range map[string]*leapmuxv1.PlanDecision{
		"an unknown choice":                {Approve: true, ChoiceId: "slow"},
		"an approving choice refused":      {ChoiceId: "fast"},
		"a refusing choice approved":       {Approve: true, ChoiceId: "stop"},
		"another effect approved":          {Approve: true, ChoiceId: "later"},
		"feedback beside an approval":      {Approve: true, Feedback: "Go."},
		"feedback beside another effect":   {ChoiceId: "later", Feedback: "Keep it."},
		"a permission mode with a refusal": {PermissionMode: "acceptEdits"},
		"a fresh context with a refusal":   {ChoiceId: "stop", ClearContext: true},
	} {
		_, err := PlanDecisionOf(planInput(choices, decision))
		assert.ErrorIs(t, err, ErrInvalidControlDecision, name)
	}
}

// planChoices offers a choice of each effect, and one that states none.
func planChoices() []*leapmuxv1.PlanChoice {
	return []*leapmuxv1.PlanChoice{
		{Id: "fast", Effect: leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_APPROVE},
		{Id: "stop", Effect: leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_REFUSE},
		{Id: "later", Effect: leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_OTHER},
		{Id: "legacy"},
	}
}

func TestAnswerNeutralPlan(t *testing.T) {
	choices := planChoices()
	got, err := AnswerNeutralPlan(planInput(choices, &leapmuxv1.PlanDecision{Approve: true, ChoiceId: "fast"}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"allow","updatedInput":{"plan":"1. Edit."},"choice":"fast"}}}`, string(got.Content))
	assert.True(t, proto.Equal(&leapmuxv1.PlanApprovalSettings{}, got.PlanApproval), "an approval states its settings, empty ones too")

	got, err = AnswerNeutralPlan(planInput(choices, &leapmuxv1.PlanDecision{ChoiceId: "stop"}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"behavior":"deny","message":"Rejected by user.","choice":"stop"}}}`, string(got.Content))
	assert.Nil(t, got.PlanApproval)
}

func elicitationInput(prompt *leapmuxv1.ElicitationPrompt, decision *leapmuxv1.ElicitationDecision) ControlAnswerInput {
	return ControlAnswerInput{
		Request:   NewControlRequestInput([]byte(`{}`)),
		RequestID: "req-1",
		Prompt:    &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: prompt}},
		Decision:  &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Elicitation{Elicitation: decision}},
	}
}

func TestAnswerElicitation(t *testing.T) {
	prompt := &leapmuxv1.ElicitationPrompt{AcceptChoices: []*leapmuxv1.ElicitationAcceptChoice{{Key: "session", Label: "For this session"}}}
	metadata := func(key string) (json.RawMessage, bool) {
		if key == "session" {
			return json.RawMessage(`{"persist":"session"}`), true
		}
		return nil, false
	}
	got, err := AnswerElicitation(elicitationInput(prompt, &leapmuxv1.ElicitationDecision{
		Action: leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, ContentJson: `{"name":"ada"}`, AcceptChoiceKey: "session",
	}), metadata)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"action":"accept","content":{"name":"ada"},"_meta":{"persist":"session"}}}}`, string(got.Content))

	got, err = AnswerElicitation(elicitationInput(prompt, &leapmuxv1.ElicitationDecision{Action: leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT}), nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"action":"accept"}}}`, string(got.Content), "an accept with no form states no content")

	got, err = AnswerElicitation(elicitationInput(prompt, &leapmuxv1.ElicitationDecision{Action: leapmuxv1.ElicitationAction_ELICITATION_ACTION_CANCEL}), nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"action":"cancel"}}}`, string(got.Content))

	for name, decision := range map[string]*leapmuxv1.ElicitationDecision{
		"no action":                {},
		"an unknown action":        {Action: leapmuxv1.ElicitationAction(9)},
		"content that is no form":  {Action: leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, ContentJson: `[1]`},
		"content that is no JSON":  {Action: leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, ContentJson: `{`},
		"an unknown accept choice": {Action: leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, AcceptChoiceKey: "always"},
		"content beside a decline": {Action: leapmuxv1.ElicitationAction_ELICITATION_ACTION_DECLINE, ContentJson: `{}`},
		"a choice beside a cancel": {Action: leapmuxv1.ElicitationAction_ELICITATION_ACTION_CANCEL, AcceptChoiceKey: "session"},
	} {
		_, err := AnswerElicitation(elicitationInput(prompt, decision), metadata)
		assert.ErrorIs(t, err, ErrInvalidControlDecision, name)
	}

	// Only a form has content: the browser sends none for a URL to visit.
	url := &leapmuxv1.ElicitationPrompt{Mode: "url", Url: "https://example.com/auth"}
	_, err = AnswerElicitation(elicitationInput(url, &leapmuxv1.ElicitationDecision{Action: leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, ContentJson: `{}`}), nil)
	assert.ErrorIs(t, err, ErrInvalidControlDecision, "content for a URL")
	got, err = AnswerElicitation(elicitationInput(url, &leapmuxv1.ElicitationDecision{Action: leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT}), nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"action":"accept"}}}`, string(got.Content))
	form := &leapmuxv1.ElicitationPrompt{Mode: "form"}
	_, err = AnswerElicitation(elicitationInput(form, &leapmuxv1.ElicitationDecision{Action: leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, ContentJson: `{"a":1}`}), nil)
	assert.NoError(t, err, "a form states its mode")
}

func dialogAnswerInput(variant leapmuxv1.DialogVariant, decision *leapmuxv1.ControlDecision) ControlAnswerInput {
	return ControlAnswerInput{
		Prompt:   &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Dialog{Dialog: &leapmuxv1.DialogPrompt{Title: "T", Variant: variant}}},
		Decision: decision,
	}
}

func dialogDecision(action leapmuxv1.DialogAction, value string) *leapmuxv1.ControlDecision {
	return &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Dialog{Dialog: &leapmuxv1.DialogDecision{Action: action, Value: value}}}
}

func TestDialogDecisionOf_AcceptsTheActionsOfEachVariant(t *testing.T) {
	confirm := leapmuxv1.DialogVariant_DIALOG_VARIANT_CONFIRM
	input := leapmuxv1.DialogVariant_DIALOG_VARIANT_INPUT
	editor := leapmuxv1.DialogVariant_DIALOG_VARIANT_EDITOR
	for name, tc := range map[string]struct {
		variant  leapmuxv1.DialogVariant
		decision *leapmuxv1.ControlDecision
	}{
		"a confirm confirmed":      {confirm, dialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CONFIRM, "")},
		"a confirm refused":        {confirm, dialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_REFUSE, "")},
		"a confirm dismissed":      {confirm, dialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CANCEL, "")},
		"an input's value":         {input, dialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT, "main")},
		"an input's empty value":   {input, dialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT, "")},
		"an input dismissed":       {input, dialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CANCEL, "")},
		"an editor's value":        {editor, dialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT, "a\nb")},
		"an editor dismissed":      {editor, dialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CANCEL, "")},
		"a dialog of no variant":   {leapmuxv1.DialogVariant_DIALOG_VARIANT_UNSPECIFIED, dialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CANCEL, "")},
		"a confirm of no variant":  {leapmuxv1.DialogVariant_DIALOG_VARIANT_UNSPECIFIED, dialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CONFIRM, "")},
		"a value with white space": {editor, dialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT, "  ")},
	} {
		decision, prompt, err := DialogDecisionOf(dialogAnswerInput(tc.variant, tc.decision))
		require.NoError(t, err, name)
		assert.Equal(t, tc.decision.GetDialog().GetAction(), decision.GetAction(), name)
		assert.Equal(t, tc.decision.GetDialog().GetValue(), decision.GetValue(), name)
		assert.Equal(t, tc.variant, prompt.GetVariant(), name)
	}
}

func TestDialogDecisionOf_RefusesADecisionThatDoesNotFitTheDialog(t *testing.T) {
	confirm := leapmuxv1.DialogVariant_DIALOG_VARIANT_CONFIRM
	editor := leapmuxv1.DialogVariant_DIALOG_VARIANT_EDITOR
	for name, input := range map[string]ControlAnswerInput{
		"a confirmation of an editor": dialogAnswerInput(editor, dialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CONFIRM, "")),
		"a refusal of an input":       dialogAnswerInput(leapmuxv1.DialogVariant_DIALOG_VARIANT_INPUT, dialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_REFUSE, "")),
		"a value for a confirm":       dialogAnswerInput(confirm, dialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT, "yes")),
		"a value of no variant":       dialogAnswerInput(leapmuxv1.DialogVariant_DIALOG_VARIANT_UNSPECIFIED, dialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT, "")),
		"a value with a confirmation": dialogAnswerInput(confirm, dialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CONFIRM, "yes")),
		"a value with a dismissal":    dialogAnswerInput(editor, dialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CANCEL, "draft")),
		"no action":                   dialogAnswerInput(editor, dialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_UNSPECIFIED, "")),
		"an unknown action":           dialogAnswerInput(editor, dialogDecision(leapmuxv1.DialogAction(99), "")),
		"another kind of decision": dialogAnswerInput(confirm, &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Permission{
			Permission: &leapmuxv1.PermissionDecision{OptionId: contracts.ControlAnswerOptionAllow},
		}}),
		"no decision": dialogAnswerInput(confirm, nil),
		"another kind of prompt": {
			Prompt:   GenericPermissionPrompt(),
			Decision: dialogDecision(leapmuxv1.DialogAction_DIALOG_ACTION_CANCEL, ""),
		},
	} {
		_, _, err := DialogDecisionOf(input)
		assert.ErrorIs(t, err, ErrInvalidControlDecision, name)
	}
}
