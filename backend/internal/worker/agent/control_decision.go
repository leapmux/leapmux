package agent

import (
	"encoding/json"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// QuestionOptionValue is what an answer sends for option: its value, or its
// label when it states none.
func QuestionOptionValue(option *leapmuxv1.QuestionOption) string {
	if option.Value != nil {
		return option.GetValue()
	}
	return option.GetLabel()
}

// QuestionDecisionOf returns the question decision of input, checked
// against the questions of its prompt, as the browser checks an answer
// before it sends one:
//
//   - One answer at most for each question. A question with no answer reads
//     as an answer with nothing selected and no text.
//   - Each selected value is the value of one of the question's options,
//     once, and a question that is not multiple-choice takes one at most.
//   - Typed text only where the question permits it.
//   - A question that is not multiple-choice takes an option or typed text,
//     not both, unless the prompt keeps a note beside a selection: its one
//     answer cannot be two things.
//   - Every question has an answer: options within the question's counts,
//     else typed text, else nothing where the question permits an empty
//     answer. Text cannot complete a selection outside the counts.
//
// Text that is only white space, as JavaScript's trim reads it, is no text.
// A cancelled decision needs no answers, and states none.
func QuestionDecisionOf(input ControlAnswerInput) (*leapmuxv1.QuestionDecision, []*leapmuxv1.ControlQuestion, error) {
	decision := input.Decision.GetQuestion()
	prompt := input.Prompt.GetQuestion()
	if decision == nil || prompt == nil {
		return nil, nil, InvalidControlDecision("a question prompt takes a question decision")
	}
	questions := prompt.GetQuestions()
	if decision.GetCancelled() {
		if len(decision.GetAnswers()) > 0 {
			return nil, nil, InvalidControlDecision("a cancelled answer states no answers")
		}
		return decision, questions, nil
	}
	if len(questions) == 0 {
		return nil, nil, InvalidControlDecision("the prompt asks no question, so only a cancel answers it")
	}
	if len(decision.GetAnswers()) > len(questions) {
		return nil, nil, InvalidControlDecision("the prompt asks %d questions, and the decision answers %d", len(questions), len(decision.GetAnswers()))
	}
	for i, question := range questions {
		if err := checkQuestionAnswer(i+1, question, QuestionAnswerAt(decision, i), prompt.GetPreservesSelectionNotes()); err != nil {
			return nil, nil, err
		}
	}
	return decision, questions, nil
}

// checkQuestionAnswer checks the answer to question n. See
// QuestionDecisionOf.
func checkQuestionAnswer(n int, question *leapmuxv1.ControlQuestion, answer *leapmuxv1.QuestionAnswer, keepsNotes bool) error {
	selected := answer.GetSelected()
	if len(selected) > 1 && !question.GetMultiSelect() {
		return InvalidControlDecision("question %d takes one option", n)
	}
	for i, value := range selected {
		if !slices.ContainsFunc(question.GetOptions(), func(option *leapmuxv1.QuestionOption) bool { return QuestionOptionValue(option) == value }) {
			return InvalidControlDecision("question %d offers no option %q", n, value)
		}
		if slices.Contains(selected[:i], value) {
			return InvalidControlDecision("question %d chooses %q twice", n, value)
		}
	}
	typed := TrimJavaScriptSpace(answer.GetCustomText()) != ""
	if typed && question.AllowCustomText != nil && !question.GetAllowCustomText() {
		return InvalidControlDecision("question %d takes its options only", n)
	}
	if typed && len(selected) > 0 && !question.GetMultiSelect() && !keepsNotes {
		return InvalidControlDecision("question %d takes one answer: an option or typed text, not both", n)
	}
	switch {
	case len(selected) > 0:
		if minimum := question.MinimumSelections; minimum != nil && int64(len(selected)) < int64(*minimum) {
			return InvalidControlDecision("question %d takes at least %d options", n, *minimum)
		}
		if maximum := question.MaximumSelections; maximum != nil && int64(len(selected)) > int64(*maximum) {
			return InvalidControlDecision("question %d takes at most %d options", n, *maximum)
		}
	case typed, question.GetAllowEmpty():
	default:
		return InvalidControlDecision("question %d has no answer", n)
	}
	return nil
}

// QuestionAnswerValues is what one answer says, as a list of values, for a
// runtime that takes no note beside a selection:
//
//   - A multiple-choice question gives each selected value, then the typed
//     text as one more value: the user chose those options AND that text.
//   - Any other question gives its selected value, or else its typed text.
//
// The typed text is trimmed as JavaScript trims it, and text that is only
// white space is none. A prompt that keeps a note beside a selection sends
// the note in its own field instead, so its provider does not call this.
func QuestionAnswerValues(question *leapmuxv1.ControlQuestion, answer *leapmuxv1.QuestionAnswer) []string {
	values := slices.Clone(answer.GetSelected())
	typed := TrimJavaScriptSpace(answer.GetCustomText())
	if typed != "" && (question.GetMultiSelect() || len(values) == 0) {
		values = append(values, typed)
	}
	return values
}

// QuestionAnswerAt returns the answer to question i, or an empty answer for
// a question that the decision leaves unanswered.
func QuestionAnswerAt(decision *leapmuxv1.QuestionDecision, i int) *leapmuxv1.QuestionAnswer {
	if i < len(decision.GetAnswers()) {
		return decision.GetAnswers()[i]
	}
	return &leapmuxv1.QuestionAnswer{}
}

// PlanDecisionOf returns the plan decision of input, checked against its
// prompt:
//
//   - A choice is one that the prompt offers, and the decision approves
//     exactly when the choice approves.
//   - Feedback says what to change, so only a refusal carries it.
//   - The approval settings apply to an approval alone.
func PlanDecisionOf(input ControlAnswerInput) (*leapmuxv1.PlanDecision, error) {
	decision := input.Decision.GetPlan()
	prompt := input.Prompt.GetPlan()
	if decision == nil || prompt == nil {
		return nil, InvalidControlDecision("a plan prompt takes a plan decision")
	}
	refuses := !decision.GetApprove()
	if id := decision.GetChoiceId(); id != "" {
		i := slices.IndexFunc(prompt.GetChoices(), func(choice *leapmuxv1.PlanChoice) bool { return choice.GetId() == id })
		if i < 0 {
			return nil, InvalidControlDecision("the prompt offers no choice %q", id)
		}
		effect := prompt.GetChoices()[i].GetEffect()
		if decision.GetApprove() != (effect == leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_APPROVE) {
			return nil, InvalidControlDecision("the choice %q does not %s the plan", id, map[bool]string{true: "approve", false: "refuse"}[decision.GetApprove()])
		}
		refuses = effect == leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_REFUSE
	}
	if decision.GetFeedback() != "" && !refuses {
		return nil, InvalidControlDecision("feedback says what to change in a refused plan, and this decision does not refuse it")
	}
	if !decision.GetApprove() && (decision.GetPermissionMode() != "" || decision.GetClearContext()) {
		return nil, InvalidControlDecision("a permission mode and a fresh context apply to an approval only")
	}
	return decision, nil
}

// PlanApprovalSettingsOf is the LeapMux settings of an approving plan
// decision. The browser sends them with every approval, empty ones too.
func PlanApprovalSettingsOf(decision *leapmuxv1.PlanDecision) *leapmuxv1.PlanApprovalSettings {
	return &leapmuxv1.PlanApprovalSettings{
		PermissionMode: decision.GetPermissionMode(),
		ClearContext:   decision.GetClearContext(),
	}
}

// AnswerNeutralPlan answers a plan prompt with the neutral envelope: an
// approval allows with the request's own input and carries the LeapMux
// settings, a refusal denies with the feedback, and a choice rides beside
// either.
func AnswerNeutralPlan(input ControlAnswerInput) (ControlAnswer, error) {
	decision, err := PlanDecisionOf(input)
	if err != nil {
		return ControlAnswer{}, err
	}
	var answer ControlAnswer
	if decision.GetApprove() {
		answer = ControlAnswer{
			Content:      AllowResponse(input.RequestID, ControlToolInput(input.Request.Payload)),
			PlanApproval: PlanApprovalSettingsOf(decision),
		}
	} else {
		answer = ControlAnswer{Content: DenyResponse(input.RequestID, decision.GetFeedback())}
	}
	if decision.GetChoiceId() != "" {
		answer.Content = WithControlChoice(answer.Content, decision.GetChoiceId())
	}
	return answer, nil
}

// ElicitationDecisionOf returns the elicitation decision of input, checked
// against its prompt: a known action, form content that is a JSON object,
// and an accept choice that the prompt offers.
func ElicitationDecisionOf(input ControlAnswerInput) (*leapmuxv1.ElicitationDecision, error) {
	decision := input.Decision.GetElicitation()
	prompt := input.Prompt.GetElicitation()
	if decision == nil || prompt == nil {
		return nil, InvalidControlDecision("an elicitation prompt takes an elicitation decision")
	}
	switch decision.GetAction() {
	case leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT:
	case leapmuxv1.ElicitationAction_ELICITATION_ACTION_DECLINE,
		leapmuxv1.ElicitationAction_ELICITATION_ACTION_CANCEL:
		if decision.GetContentJson() != "" || decision.GetAcceptChoiceKey() != "" {
			return nil, InvalidControlDecision("only an accept carries content or an accept choice")
		}
	case leapmuxv1.ElicitationAction_ELICITATION_ACTION_UNSPECIFIED:
		return nil, InvalidControlDecision("the elicitation decision states no action")
	default:
		return nil, InvalidControlDecision("the elicitation action %v is unknown", decision.GetAction())
	}
	if content := decision.GetContentJson(); content != "" {
		if mode := prompt.GetMode(); mode != "" && mode != contracts.MCPElicitationModeForm {
			return nil, InvalidControlDecision("an elicitation in %s mode has no form, so its accept carries no content", mode)
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(content), &fields) != nil || fields == nil {
			return nil, InvalidControlDecision("the form content is not a JSON object")
		}
	}
	if key := decision.GetAcceptChoiceKey(); key != "" &&
		!slices.ContainsFunc(prompt.GetAcceptChoices(), func(choice *leapmuxv1.ElicitationAcceptChoice) bool { return choice.GetKey() == key }) {
		return nil, InvalidControlDecision("the prompt offers no accept choice %q", key)
	}
	return decision, nil
}

// elicitationActionToken is the MCP word of action.
func elicitationActionToken(action leapmuxv1.ElicitationAction) string {
	switch action {
	case leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT:
		return contracts.MCPElicitationActionAccept
	case leapmuxv1.ElicitationAction_ELICITATION_ACTION_DECLINE:
		return contracts.MCPElicitationActionDecline
	default:
		return contracts.MCPElicitationActionCancel
	}
}

// AnswerElicitation answers an MCP elicitation with the neutral envelope:
// the action, and for an accept the form content and the metadata of the
// chosen accept choice. metadata reads that choice's metadata from the
// provider's own payload; a provider whose prompts offer no accept choices
// passes nil.
func AnswerElicitation(input ControlAnswerInput, metadata func(key string) (json.RawMessage, bool)) (ControlAnswer, error) {
	decision, err := ElicitationDecisionOf(input)
	if err != nil {
		return ControlAnswer{}, err
	}
	response := map[string]any{"action": elicitationActionToken(decision.GetAction())}
	if decision.GetAction() == leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT {
		if content := decision.GetContentJson(); content != "" {
			response["content"] = json.RawMessage(content)
		}
		if key := decision.GetAcceptChoiceKey(); key != "" && metadata != nil {
			if meta, ok := metadata(key); ok {
				response["_meta"] = meta
			}
		}
	}
	return ControlAnswer{Content: ControlResponseEnvelope(input.RequestID, response)}, nil
}

// DialogDecisionOf returns the dialog decision of input and its prompt,
// checked against the dialog's variant: a confirm dialog takes a confirm or a
// refusal, an input or an editor dialog takes a submit, and every dialog
// takes a cancel. Only a submit carries a value. A dialog of no stated
// variant is a confirm dialog, as the browser draws it.
func DialogDecisionOf(input ControlAnswerInput) (*leapmuxv1.DialogDecision, *leapmuxv1.DialogPrompt, error) {
	decision := input.Decision.GetDialog()
	prompt := input.Prompt.GetDialog()
	if decision == nil || prompt == nil {
		return nil, nil, InvalidControlDecision("a dialog prompt takes a dialog decision")
	}
	confirm := prompt.GetVariant() == leapmuxv1.DialogVariant_DIALOG_VARIANT_CONFIRM || prompt.GetVariant() == leapmuxv1.DialogVariant_DIALOG_VARIANT_UNSPECIFIED
	switch decision.GetAction() {
	case leapmuxv1.DialogAction_DIALOG_ACTION_CONFIRM, leapmuxv1.DialogAction_DIALOG_ACTION_REFUSE:
		if !confirm {
			return nil, nil, InvalidControlDecision("only a confirm dialog takes %v", decision.GetAction())
		}
	case leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT:
		if confirm {
			return nil, nil, InvalidControlDecision("only an input or an editor dialog takes a value")
		}
	case leapmuxv1.DialogAction_DIALOG_ACTION_CANCEL:
	case leapmuxv1.DialogAction_DIALOG_ACTION_UNSPECIFIED:
		return nil, nil, InvalidControlDecision("the dialog decision states no action")
	default:
		return nil, nil, InvalidControlDecision("the dialog action %v is unknown", decision.GetAction())
	}
	if decision.GetAction() != leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT && decision.GetValue() != "" {
		return nil, nil, InvalidControlDecision("only a submit carries a value")
	}
	return decision, prompt, nil
}
