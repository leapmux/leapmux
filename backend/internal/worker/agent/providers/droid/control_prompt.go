package droid

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// droidUnnamedToolTitle is the title of a permission whose call states no
// tool name.
const droidUnnamedToolTitle = "Tool"

// droidAnswerPartSeparator is the text that Droid's own client puts between
// the parts of one answer.
const droidAnswerPartSeparator = ", "

// ReadControlRequest reads the two control requests that the Worker
// publishes for Factory Droid (onRequestPermission and onAskUser):
//
//   - A question request is a question, one for each question that Droid
//     asks, in Droid's order.
//   - A permission request whose confirmation type is a spec-mode exit or a
//     mission proposal is a plan, with the document to review.
//   - Any other permission request is a permission, with the options that
//     Droid offers.
//
// Any other payload reads as unrecognized: Droid raises no other request, and
// the resolution refuses an answer to one.
func (droidProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	fields, _ := providerkit.RawObject(input.Payload)
	switch providerkit.ObjectString(fields, "type") {
	case contracts.DroidRequestTypeAskUser:
		questions := []*leapmuxv1.ControlQuestion{}
		for _, question := range droidQuestionsOf(fields) {
			questions = append(questions, question.prompt())
		}
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: questions}}}
	case contracts.DroidRequestTypePermission:
		return droidPermissionRequestPrompt(fields)
	default:
		return agent.UnrecognizedControlPrompt()
	}
}

// droidPermissionRequestPrompt reads a permission request.
//
// The confirmation type, not the tool's name, makes a request a review:
// Droid itself groups exit_spec_mode and propose_mission on it. Both carry
// the document to review in `details`, as `plan` and `proposal`, and the
// transcript holds no other copy of it, so the plan states it.
//
// A Droid Shield refusal states why Droid blocked the call in
// `details.reason`. An Execute call states its command, as `command` or
// `cmd`.
func droidPermissionRequestPrompt(fields map[string]json.RawMessage) *leapmuxv1.ControlPrompt {
	confirmationType := providerkit.ObjectString(fields, "confirmationType")
	details, _ := providerkit.RawObject(fields["details"])
	switch confirmationType {
	case contracts.DroidConfirmationTypeExitSpecMode, contracts.DroidConfirmationTypeProposeMission:
		key := "plan"
		if confirmationType == contracts.DroidConfirmationTypeProposeMission {
			key = "proposal"
		}
		document := providerkit.ObjectString(details, key)
		if strings.TrimSpace(document) == "" {
			document = ""
		}
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{Text: document}}}
	}

	toolUse, _ := providerkit.RawObject(fields[contracts.DroidNotificationFieldToolUse])
	// Droid's `toolUse` marshals `name`, not `toolName`.
	toolName := providerkit.ObjectString(toolUse, "name")
	if toolName == "" {
		toolName = providerkit.ObjectString(toolUse, contracts.DroidNotificationFieldToolName)
	}
	if toolName == "" {
		toolName = droidUnnamedToolTitle
	}
	toolInput := providerkit.RawObjectOrEmpty(toolUse["input"])
	permission := &leapmuxv1.PermissionPrompt{
		Title:     toolName,
		InputJson: string(toolInput),
		Options:   droidPermissionOptions(droidOfferedOptions(fields)),
	}
	if toolName == contracts.DroidToolExecute {
		arguments, _ := providerkit.RawObject(toolInput)
		permission.Command = providerkit.ObjectString(arguments, "command")
		if permission.Command == "" {
			permission.Command = providerkit.ObjectString(arguments, "cmd")
		}
	}
	if confirmationType == contracts.DroidConfirmationTypeDroidShieldViolation {
		permission.Reason = providerkit.ObjectString(details, contracts.DroidNotificationFieldReason)
	}
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: permission}}
}

// droidOfferedOptions reads the option values that a stored permission
// request offers, in Droid's order. A value that is no text, or that is
// empty, is dropped.
func droidOfferedOptions(fields map[string]json.RawMessage) []string {
	var elements []json.RawMessage
	_ = json.Unmarshal(fields["options"], &elements)
	offered := make([]string, 0, len(elements))
	for _, element := range elements {
		if value, ok := providerkit.RawString(element); ok && value != "" {
			offered = append(offered, value)
		}
	}
	return offered
}

// droidOptionKind is the kind of one offered option.
//
// The remembered tiers (`proceed_always*`) allow always; `proceed_once` and
// `cancel` are the once pair. Every other tier that Droid defines is of
// another kind, because the autonomy and new-session choices grant more
// than a scope, and a guessed kind would misstate them.
func droidOptionKind(value string) leapmuxv1.PermissionOptionKind {
	switch value {
	case contracts.DroidPermissionOptionProceedOnce:
		return leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE
	case contracts.DroidPermissionOptionProceedAlways, contracts.DroidPermissionOptionProceedAlwaysFile,
		contracts.DroidPermissionOptionProceedAlwaysTools, contracts.DroidPermissionOptionProceedAlwaysServer:
		return leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS
	case contracts.DroidPermissionOptionCancel:
		return leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE
	default:
		return leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_OTHER
	}
}

// droidOptionNames are the words of each option that Droid defines beyond
// the once pair, as the Factory Droid SDK describes the option. Droid states
// each option as a value and no label, so a button and a saved answer read
// these words. The once pair has none: a client words `proceed_once` and
// `cancel` as its own Allow and Deny buttons.
var droidOptionNames = map[string]string{
	// Droid saves a rule. For a file, the rule covers the parent directory.
	contracts.DroidPermissionOptionProceedAlways:       "Allow always",
	contracts.DroidPermissionOptionProceedAlwaysFile:   "Allow always for this file",
	contracts.DroidPermissionOptionProceedAlwaysTools:  "Allow always for these MCP tools",
	contracts.DroidPermissionOptionProceedAlwaysServer: "Allow always for this MCP server",
	// The autonomy level and the new session apply beside the approval.
	contracts.DroidPermissionOptionProceedAutoRun:          "Allow and raise the autonomy level",
	contracts.DroidPermissionOptionProceedAutoRunLow:       "Allow and set the autonomy level to Low",
	contracts.DroidPermissionOptionProceedAutoRunMedium:    "Allow and set the autonomy level to Medium",
	contracts.DroidPermissionOptionProceedAutoRunHigh:      "Allow and set the autonomy level to High",
	contracts.DroidPermissionOptionProceedNewSession:       "Allow in a new session",
	contracts.DroidPermissionOptionProceedNewSessionLow:    "Allow in a new session at autonomy level Low",
	contracts.DroidPermissionOptionProceedNewSessionMedium: "Allow in a new session at autonomy level Medium",
	contracts.DroidPermissionOptionProceedNewSessionHigh:   "Allow in a new session at autonomy level High",
	// Droid requires `editedSpecContent` beside this option and implements
	// that spec.
	contracts.DroidPermissionOptionProceedEdit: "Allow with an edited spec",
	// Droid Shield: Droid proceeds once and reports its findings as a false
	// positive.
	contracts.DroidPermissionOptionProceedReportFalsePositive: "Allow and report a false positive",
}

// droidPermissionOptions states the options of a permission prompt:
//
//   - Droid's offered options, each with its kind and its words
//     (droidOptionNames). An option of another kind that this build does not
//     know takes its value as its name, the only word that Droid states.
//   - LeapMux's allow and deny, when Droid offers none.
//   - LeapMux's deny after Droid's options, when they include no `cancel`.
//     Droid still accepts a refusal there: droidPermissionReply turns a deny
//     into `cancel`, which needs no offer.
func droidPermissionOptions(offered []string) []*leapmuxv1.PermissionOption {
	options := make([]*leapmuxv1.PermissionOption, 0, len(offered)+1)
	for _, value := range offered {
		option := &leapmuxv1.PermissionOption{OptionId: value, Kind: droidOptionKind(value), Name: droidOptionNames[value]}
		if option.Name == "" && option.Kind == leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_OTHER {
			option.Name = value
		}
		options = append(options, option)
	}
	return agent.WithRefusal(options, &leapmuxv1.PermissionOption{OptionId: contracts.ControlAnswerOptionDeny, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE})
}

// droidQuestion is one question of a stored question request, as Droid
// states it.
type droidQuestion struct {
	// index is Droid's own number for the question, valid when hasIndex is
	// true: the request states it as a whole number (RawWholeNumber).
	index       int64
	hasIndex    bool
	question    string
	options     []string
	multiSelect bool
}

// droidQuestionsOf reads the questions of a stored question request, in the
// order that the request states. A question that is no object is dropped,
// and so is an option that is no text. The prompt and the answer both read
// this one list, so the position of a question in the prompt is its
// position here.
func droidQuestionsOf(fields map[string]json.RawMessage) []droidQuestion {
	var elements []json.RawMessage
	_ = json.Unmarshal(fields[contracts.DroidAskUserFieldQuestions], &elements)
	questions := make([]droidQuestion, 0, len(elements))
	for _, element := range elements {
		record, ok := providerkit.RawObject(element)
		if !ok {
			continue
		}
		question := droidQuestion{
			question:    providerkit.ObjectString(record, contracts.DroidAskUserFieldQuestion),
			options:     []string{},
			multiSelect: providerkit.RawBool(record[contracts.DroidAskUserFieldMultiSelect]),
		}
		question.index, question.hasIndex = providerkit.RawWholeNumber(record[contracts.DroidAskUserFieldIndex])
		var options []json.RawMessage
		_ = json.Unmarshal(record[contracts.DroidAskUserFieldOptions], &options)
		for _, option := range options {
			if label, ok := providerkit.RawString(option); ok {
				question.options = append(question.options, label)
			}
		}
		questions = append(questions, question)
	}
	return questions
}

// prompt is the question as the prompt states it. Each option's value is
// its text, which the answer pairs back with Droid's options.
func (q droidQuestion) prompt() *leapmuxv1.ControlQuestion {
	options := make([]*leapmuxv1.QuestionOption, 0, len(q.options))
	for _, label := range q.options {
		options = append(options, &leapmuxv1.QuestionOption{Value: &label, Label: label})
	}
	return &leapmuxv1.ControlQuestion{Question: q.question, Options: options, MultiSelect: q.multiSelect}
}

// answerText is the answer to the question, as Droid's own client writes it:
// the values of the answer (agent.QuestionAnswerValues), joined with ", ".
//
// The chosen options come first, in the order of the options and once each,
// as Droid's client lists them. The typed text follows the chosen options of
// a multiple choice as one more value, and answers a single choice alone
// when nothing is chosen. A question with neither takes "", which Droid's
// client also sends.
func (q droidQuestion) answerText(question *leapmuxv1.ControlQuestion, answer *leapmuxv1.QuestionAnswer) string {
	values := agent.QuestionAnswerValues(question, answer)
	parts := []string{}
	for _, option := range q.options {
		if slices.Contains(answer.GetSelected(), option) {
			parts = append(parts, option)
		}
	}
	// QuestionAnswerValues lists the selected values first, so what follows
	// them is the typed text.
	parts = append(parts, values[len(answer.GetSelected()):]...)
	return strings.Join(parts, droidAnswerPartSeparator)
}

// AnswerControlRequest builds Droid's answer in the neutral envelope, which
// droidResolveControlResponse turns into Droid's JSON-RPC reply:
//
//   - A permission option rides as `selectedOption`, with the behavior that
//     it implies: `cancel` denies, and every other option allows.
//   - A refusal with feedback, and contracts.ControlAnswerOptionDeny, deny with the
//     feedback. Droid's reply has no place for the feedback, so the Worker
//     sends it as the user's next message.
//   - A plan takes the shared plan answer. The Worker turns its permission
//     mode into Droid's own approval option.
//   - A question answers each question by Droid's index, in the order of the
//     request. A multiple choice answers with its options and its typed text
//     together. A question with no whole-number index answers without one,
//     and the Worker then refuses the whole set, so the user sees why.
func (droidProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Question:
		return droidQuestionAnswer(input)
	case *leapmuxv1.ControlPrompt_Plan:
		return agent.AnswerNeutralPlan(input)
	case *leapmuxv1.ControlPrompt_Permission:
		return droidPermissionAnswer(input)
	default:
		return agent.ControlAnswer{}, agent.InvalidControlDecision("Factory Droid states no %T prompt", input.Prompt.GetKind())
	}
}

// droidPermissionAnswer answers a permission prompt. A prompt of LeapMux's
// allow and deny, for a request that offers no options, takes the shared
// answers. Only a permission request reads as a permission
// (ReadControlRequest), so the answer refuses a permission decision about any
// other request.
func droidPermissionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	fields, _ := providerkit.RawObject(input.Request.Payload)
	if providerkit.ObjectString(fields, "type") != contracts.DroidRequestTypePermission {
		return agent.ControlAnswer{}, agent.InvalidControlDecision("Factory Droid asks no permission for this request")
	}
	if len(droidOfferedOptions(fields)) == 0 {
		return agent.AnswerAllowDeny(input)
	}
	decision, option, err := agent.PermissionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if decision.GetFeedback() != "" || option.GetOptionId() == contracts.ControlAnswerOptionDeny {
		return agent.ControlAnswer{Content: agent.DenyResponse(input.RequestID, decision.GetFeedback())}, nil
	}
	behavior := contracts.ControlAnswerBehaviorAllow
	if option.GetOptionId() == contracts.DroidPermissionOptionCancel {
		behavior = contracts.ControlAnswerBehaviorDeny
	}
	return agent.ControlAnswer{Content: agent.ControlResponseEnvelope(input.RequestID, map[string]any{
		"behavior":                         behavior,
		contracts.DroidReplySelectedOption: option.GetOptionId(),
	})}, nil
}

// droidQuestionAnswer answers a question request: each answer states the
// index of its question and Droid's one string for it. A cancelled request
// denies with its feedback, which the Worker sends as the user's next
// message.
func droidQuestionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, prompted, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if decision.GetCancelled() {
		return agent.ControlAnswer{Content: agent.DenyResponse(input.RequestID, decision.GetFeedback())}, nil
	}
	fields, _ := providerkit.RawObject(input.Request.Payload)
	questions := droidQuestionsOf(fields)
	if len(questions) != len(prompted) {
		return agent.ControlAnswer{}, agent.InvalidControlDecision("the prompt states %d questions, and the request %d", len(prompted), len(questions))
	}
	answers := make([]map[string]any, 0, len(questions))
	for i, question := range questions {
		answer := agent.QuestionAnswerAt(decision, i)
		entry := map[string]any{contracts.DroidAskUserFieldAnswer: question.answerText(prompted[i], answer)}
		if question.hasIndex {
			entry[contracts.DroidAskUserFieldIndex] = question.index
		}
		answers = append(answers, entry)
	}
	return agent.ControlAnswer{Content: agent.ControlResponseEnvelope(input.RequestID, map[string]any{
		"behavior":                  contracts.ControlAnswerBehaviorAllow,
		contracts.DroidReplyAnswers: answers,
	})}, nil
}
