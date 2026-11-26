package muse

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// museMethodParams reads the method and the params object of a stored Muse
// frame. It reports false for a frame that states no method, or whose params
// are no object.
func museMethodParams(payload []byte) (string, map[string]json.RawMessage, bool) {
	fields, _ := providerkit.RawObject(payload)
	method := providerkit.ObjectString(fields, "method")
	params, ok := providerkit.RawObject(fields["params"])
	if method == "" || !ok {
		return "", nil, false
	}
	return method, params, true
}

// isMuseApprovalMethod reports whether method asks for an approval.
func isMuseApprovalMethod(method string) bool {
	switch method {
	case contracts.MuseMethodApprovalRequest, contracts.MuseMethodApprovalRequested, contracts.MuseMethodApprovalUpdated:
		return true
	default:
		return false
	}
}

// isMuseUserInputMethod reports whether method asks the user questions.
func isMuseUserInputMethod(method string) bool {
	return method == contracts.MuseMethodUserInputRequest || method == contracts.MuseMethodUserInputRequested
}

// ReadControlRequest reads the two requests that Muse asks the user:
//
//   - A user-input request whose questions are all valid is a question. A
//     typed note and a selection coexist in one Muse answer.
//   - An approval request whose choices are all valid and of a known decision
//     and scope is a permission, with Muse's choices as its options.
//
// Any other frame, and a request that one invalid or unknown element spoils,
// reads as unrecognized: LeapMux invents no choice and no question that Muse
// did not state, and the resolution refuses LeapMux's own Deny to every such
// request (nativeReplyFields, approvalReplyChoice), and its Allow to all but a
// few.
func (museProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	method, params, ok := museMethodParams(input.Payload)
	if !ok {
		return agent.UnrecognizedControlPrompt()
	}
	if isMuseUserInputMethod(method) {
		if questions := museQuestionPrompts(params); len(questions) > 0 {
			return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
				Questions:               questions,
				PreservesSelectionNotes: true,
			}}}
		}
	}
	if permission, _, ok := museApprovalPrompt(method, params); ok {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: permission}}
	}
	return agent.UnrecognizedControlPrompt()
}

// museQuestionPrompts reads the questions of a user-input request, or nil
// when one question or one option is invalid: the whole form is refused, so
// no answer reaches Muse for a form that it did not state.
//
// A question needs a nonblank id that no other question uses, nonblank text,
// a header that is text (empty is allowed), an option list, and a single or
// multiple selection mode. An option needs a nonblank label that no other
// option of its question uses. A count must be a whole number from zero to
// the number of options, with the maximum not below the minimum, on every
// question; only a multiple selection states its counts in the prompt. An
// absent count stays absent.
func museQuestionPrompts(params map[string]json.RawMessage) []*leapmuxv1.ControlQuestion {
	// readNativeQuestions is the one validator of Muse's questions: the
	// resolver checks an answer against it, so a prompt states only questions
	// that an answer can reach.
	native, ok := readNativeQuestions(params["questions"])
	if !ok {
		return nil
	}
	questions := make([]*leapmuxv1.ControlQuestion, 0, len(native))
	for _, read := range native {
		question := &leapmuxv1.ControlQuestion{
			Id:          read.ID,
			Header:      read.Header,
			Question:    read.Question,
			Options:     museQuestionOptions(read.Options),
			MultiSelect: read.Selection.Mode == contracts.MuseQuestionSelectionModeMultiple,
		}
		if question.MultiSelect {
			question.MinimumSelections = int32Of(read.Selection.Min)
			question.MaximumSelections = int32Of(read.Selection.Max)
		}
		questions = append(questions, question)
	}
	return questions
}

// int32Of narrows a count that readNativeQuestions checked against the
// option count, or nil for none.
func int32Of(count *int) *int32 {
	if count == nil {
		return nil
	}
	narrowed := int32(*count)
	return &narrowed
}

// museQuestionOptions converts validated native options into prompt options.
// Each option's value is its label, which Muse matches.
func museQuestionOptions(nativeOptions []nativeQuestionOption) []*leapmuxv1.QuestionOption {
	options := make([]*leapmuxv1.QuestionOption, 0, len(nativeOptions))
	for _, option := range nativeOptions {
		label := option.Label
		options = append(options, &leapmuxv1.QuestionOption{
			Value:       &label,
			Label:       label,
			Description: option.Description,
			Preview:     option.Preview,
		})
	}
	return options
}

// isMuseAllowDecision reports whether a Muse approval decision allows the
// call, and isMuseRejectDecision whether it refuses the call. A decision of
// neither set is unknown.
func isMuseAllowDecision(decision string) bool {
	switch decision {
	case contracts.MuseApprovalDecisionApproved, contracts.MuseApprovalDecisionApprovedForSession, contracts.MuseApprovalDecisionApprovedPolicyAmendment:
		return true
	default:
		return false
	}
}

func isMuseRejectDecision(decision string) bool {
	switch decision {
	case contracts.MuseApprovalDecisionDenied, contracts.MuseApprovalDecisionDeniedPolicyAmendment,
		contracts.MuseApprovalDecisionTimedOut, contracts.MuseApprovalDecisionAbort:
		return true
	default:
		return false
	}
}

// museChoiceScope maps a Muse choice scope onto the neutral scope. The once
// scope, and an unknown one, map to none; known reports whether Muse defines
// the scope.
func museChoiceScope(scope string) (mapped leapmuxv1.PermissionScope, known bool) {
	switch scope {
	case contracts.MuseChoiceScopeOnce:
		return leapmuxv1.PermissionScope_PERMISSION_SCOPE_UNSPECIFIED, true
	case contracts.MuseChoiceScopeSession:
		return leapmuxv1.PermissionScope_PERMISSION_SCOPE_SESSION, true
	case contracts.MuseChoiceScopeLocalPersistent:
		return leapmuxv1.PermissionScope_PERMISSION_SCOPE_WORKSPACE, true
	default:
		return leapmuxv1.PermissionScope_PERMISSION_SCOPE_UNSPECIFIED, false
	}
}

// museApprovalPrompt reads an approval request: the tool, the command of its
// subject, and Muse's choices as the options. A choice that remembers the
// decision, for the session or in the workspace, is an always option with
// that scope. It reports false for another method, and for a choice list
// that is invalid or that holds a choice of an unknown decision or scope.
func museApprovalPrompt(method string, params map[string]json.RawMessage) (*leapmuxv1.PermissionPrompt, []nativeApprovalChoice, bool) {
	if !isMuseApprovalMethod(method) {
		return nil, nil, false
	}
	choices, ok := readApprovalChoices(params["availableChoices"])
	if !ok {
		return nil, nil, false
	}
	options := make([]*leapmuxv1.PermissionOption, 0, len(choices))
	for _, choice := range choices {
		scope, knownScope := museChoiceScope(choice.Scope)
		allow := isMuseAllowDecision(choice.Decision)
		if !knownScope || !allow && !isMuseRejectDecision(choice.Decision) {
			return nil, nil, false
		}
		remembered := scope != leapmuxv1.PermissionScope_PERMISSION_SCOPE_UNSPECIFIED
		var kind leapmuxv1.PermissionOptionKind
		switch {
		case allow && remembered:
			kind = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS
		case allow:
			kind = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE
		case remembered:
			kind = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ALWAYS
		default:
			kind = leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE
		}
		options = append(options, &leapmuxv1.PermissionOption{OptionId: choice.ID, Kind: kind, Name: choice.Label, Scope: scope})
	}
	subject, _ := providerkit.RawObject(params["subject"])
	title := providerkit.ObjectString(params, "toolName")
	if title == "" {
		title = providerkit.ObjectString(subject, "toolName")
	}
	return &leapmuxv1.PermissionPrompt{Title: title, Command: providerkit.ObjectString(subject, "command"), Options: options}, choices, true
}

// AnswerControlRequest builds Muse's answer as a JSON-RPC result, which
// ResolveControlResponse turns into Muse's own reply:
//
//   - A permission option answers with its choice id.
//   - A refusal with feedback answers with the native choice that the user
//     chose, when it accepts feedback (museRefusalWithFeedback).
//   - A question answers each question with its selected labels, its typed
//     note beside a selection, or its typed text alone. A cancel states the
//     reason.
//
// No other request has a prompt that takes an answer (ReadControlRequest).
func (museProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Question:
		return museQuestionAnswer(input)
	case *leapmuxv1.ControlPrompt_Permission:
		return musePermissionAnswer(input)
	default:
		return agent.ControlAnswer{}, agent.InvalidControlDecision("Muse states no %T prompt", input.Prompt.GetKind())
	}
}

// musePermissionAnswer answers the permission prompt of an approval with one
// of Muse's own choices. Only such an approval reads as a permission
// (ReadControlRequest), so the answer refuses a permission decision about
// any other request.
func musePermissionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, option, err := agent.PermissionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	method, params, _ := museMethodParams(input.Request.Payload)
	_, choices, native := museApprovalPrompt(method, params)
	if !native {
		return agent.ControlAnswer{}, agent.InvalidControlDecision("Muse states no approval that this answers")
	}
	if feedback := decision.GetFeedback(); feedback != "" {
		return museRefusalWithFeedback(choices, option.GetOptionId(), feedback, input.RequestID)
	}
	return agent.ControlAnswer{Content: agent.JSONRPCResult(input.RequestID, map[string]any{"choiceId": option.GetOptionId()})}, nil
}

// museRefusalWithFeedback is the answer of a refusal with feedback: the
// native choice that the user chose, when it accepts feedback. A refusal that
// is remembered keeps its scope. The feedback goes as the user typed it.
//
// A refusal that Muse cannot take fails: the chosen choice takes no feedback,
// or Muse offers no such choice. A prompt offers Muse's own choices alone
// (museApprovalPrompt), so LeapMux's own deny is no choice here.
func museRefusalWithFeedback(choices []nativeApprovalChoice, optionID, feedback, requestID string) (agent.ControlAnswer, error) {
	for _, choice := range choices {
		if choice.ID != optionID {
			continue
		}
		if !choice.Feedback {
			return agent.ControlAnswer{}, agent.InvalidControlDecision("the Muse choice %q takes no feedback", optionID)
		}
		return agent.ControlAnswer{Content: agent.JSONRPCResult(requestID, map[string]any{"choiceId": choice.ID, "feedback": feedback})}, nil
	}
	return agent.ControlAnswer{}, agent.InvalidControlDecision("Muse offers no choice %q", optionID)
}

// museQuestionAnswer answers a question with one Muse answer for each
// question, in the order of the prompt:
//
//   - A selection states its labels (a multiple selection) or its one label,
//     and any non-whitespace typed text beside it as the note.
//   - A question with no selection states its typed text as the free text.
//
// Both free text and notes stay within Muse's 500-rune limit, and text goes
// as the user typed it. A cancel states the reason, an empty one included.
func museQuestionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, questions, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if decision.GetCancelled() {
		return agent.ControlAnswer{Content: agent.JSONRPCResult(input.RequestID, map[string]any{"cancelled": true, "reason": decision.GetFeedback()})}, nil
	}
	answers := make([]map[string]any, 0, len(questions))
	for i, question := range questions {
		answer := agent.QuestionAnswerAt(decision, i)
		typed := answer.GetCustomText()
		entry := map[string]any{"questionId": question.GetId()}
		switch selected := answer.GetSelected(); {
		case len(selected) == 0:
			if utf8.RuneCountInString(typed) > maxMuseQuestionTextRunes {
				return agent.ControlAnswer{}, agent.InvalidControlDecision("question %d free text exceeds %d characters", i+1, maxMuseQuestionTextRunes)
			}
			entry["freeText"] = typed
		case question.GetMultiSelect():
			entry["selectedLabels"] = selected
		default:
			entry["selectedLabel"] = selected[0]
		}
		if len(answer.GetSelected()) > 0 && agent.TrimJavaScriptSpace(typed) != "" {
			if utf8.RuneCountInString(typed) > maxMuseQuestionTextRunes {
				return agent.ControlAnswer{}, agent.InvalidControlDecision("question %d note exceeds %d characters", i+1, maxMuseQuestionTextRunes)
			}
			entry["note"] = typed
		}
		answers = append(answers, entry)
	}
	return agent.ControlAnswer{Content: agent.JSONRPCResult(input.RequestID, map[string]any{"answers": answers})}, nil
}
