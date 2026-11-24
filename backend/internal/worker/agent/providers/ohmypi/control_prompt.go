package ohmypi

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The Worker publishes two kinds of control request for omp:
//
//   - The question bridge's request (contracts.OhMyPiAskRequest), which
//     carries the choice fields of a whole `ask` form. One
//     leapmux_ask_answer answers it, and the bridge answers omp's dialog
//     chain from it (ask.go).
//   - An extension_ui_request dialog, published as omp sent it:
//     - A tool approval is a `select` titled "Allow tool: <name>", whose
//       further lines state the call's details, and whose options hold
//       Approve and Deny. It offers no "always" answer.
//     - Any other `select` is a question, answered with one of its options.
//     - A `confirm`, an `input` and an `editor` are dialogs.
//
// omp reads one extension_ui_response for each dialog: a value, a
// confirmation, or a cancellation.

// The detail line of an approval title that states the command of a `bash`
// call, and the section that omp's extension wrapper appends below the
// details.
const (
	approvalCommandLabel = "Command: "
	approvalSafetyChecks = "\nProvider safety checks:"
)

// selectDefaultTitle heads a select that states no title.
const selectDefaultTitle = "Choose an option"

// dialogShape is the control and the heading of a dialog method that draws
// as a dialog.
type dialogShape struct {
	variant      leapmuxv1.DialogVariant
	defaultTitle string
}

var dialogShapes = map[string]dialogShape{
	contracts.OhMyPiDialogMethodConfirm: {leapmuxv1.DialogVariant_DIALOG_VARIANT_CONFIRM, "Confirm"},
	contracts.OhMyPiDialogMethodInput:   {leapmuxv1.DialogVariant_DIALOG_VARIANT_INPUT, "Enter a value"},
	contracts.OhMyPiDialogMethodEditor:  {leapmuxv1.DialogVariant_DIALOG_VARIANT_EDITOR, "Enter your response"},
}

// ompControlPayload holds the fields of one omp control request. Each field
// is read on its own, so a field of the wrong type reads as absent and
// leaves the other fields readable.
type ompControlPayload map[string]json.RawMessage

func readOmpControlPayload(payload []byte) ompControlPayload {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(payload, &fields)
	return fields
}

// text returns the string field key, or "" when it is absent or no string.
func (p ompControlPayload) text(key string) string {
	return rawText(p[key])
}

func (p ompControlPayload) method() string { return p.text("method") }

func (p ompControlPayload) isExtensionUIRequest() bool {
	return p.text("type") == contracts.OhMyPiEventExtensionUIRequest
}

// isAskRequest reports whether the request is the question bridge's request
// for a whole `ask` call.
func (p ompControlPayload) isAskRequest() bool {
	return p.text(contracts.OhMyPiAskEnvelopeType) == contracts.OhMyPiAskTypeRequest
}

// isApproval reports whether the request is omp's tool approval: a `select`
// whose title starts with the approval prefix and whose options hold both
// Approve and Deny. The title may state no tool name, and the options may
// hold more answers, which the approval does not offer.
func (p ompControlPayload) isApproval() bool {
	if !p.isExtensionUIRequest() || p.method() != contracts.OhMyPiDialogMethodSelect {
		return false
	}
	options := p.stringOptions()
	return strings.HasPrefix(p.text("title"), contracts.OhMyPiApprovalDialogTitlePrefix) &&
		slices.Contains(options, contracts.OhMyPiApprovalDialogApprove) &&
		slices.Contains(options, contracts.OhMyPiApprovalDialogDeny)
}

// isDialogQuestion reports whether the request is a `select` that is no
// approval: one that an extension raised, or an `ask` select that the bridge
// handed over because it could not follow its shape.
func (p ompControlPayload) isDialogQuestion() bool {
	return p.isExtensionUIRequest() && p.method() == contracts.OhMyPiDialogMethodSelect && !p.isApproval()
}

// stringOptions returns the string options of a select. An option of another
// type is dropped.
func (p ompControlPayload) stringOptions() []string {
	var elements []json.RawMessage
	if json.Unmarshal(p["options"], &elements) != nil {
		return nil
	}
	options := make([]string, 0, len(elements))
	for _, element := range elements {
		if option, ok := providerkit.RawString(element); ok {
			options = append(options, option)
		}
	}
	return options
}

// rawText reads a JSON string, or "" for any other value.
func rawText(raw json.RawMessage) string {
	value, _ := providerkit.RawString(raw)
	return value
}

// ReadControlRequest reads omp's control requests: the bridge's questions, a
// select as a question, a tool approval as a permission, and a confirm, an
// input or an editor as a dialog. Any other request reads as the generic
// permission.
func (ompProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	payload := readOmpControlPayload(input.Payload)
	switch {
	case payload.isAskRequest():
		return questionPrompt(askQuestions(payload[contracts.OhMyPiAskEnvelopeQuestions]))
	case payload.isDialogQuestion():
		return questionPrompt([]*leapmuxv1.ControlQuestion{{
			Question: cmp.Or(payload.text("title"), selectDefaultTitle),
			Options:  selectQuestionOptions(payload),
			// omp takes one of the select's own options as its value.
			AllowCustomText: proto.Bool(false),
		}})
	case payload.isApproval():
		return &leapmuxv1.ControlPrompt{
			Kind: &leapmuxv1.ControlPrompt_Permission{Permission: approvalPrompt(payload, input)},
			// The composer's text explains a Deny, and the Worker sends it
			// after the answer.
			EditorPurpose: leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_FEEDBACK,
		}
	}
	if dialog := dialogPrompt(payload); dialog != nil {
		return &leapmuxv1.ControlPrompt{
			Kind: &leapmuxv1.ControlPrompt_Dialog{Dialog: dialog},
			// A dialog takes its answer through its own controls.
			EditorPurpose: leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE,
		}
	}
	prompt := agent.GenericPermissionPrompt()
	// omp reads no text beside the answer of a request that it did not
	// state.
	prompt.EditorPurpose = leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE
	return prompt
}

// questionPrompt is the prompt of questions whose typed answer the
// composer's text gives. A question whose allow_custom_text is false takes
// its options alone, and a client offers no text for it
// (agent.WithEditorPurpose).
func questionPrompt(questions []*leapmuxv1.ControlQuestion) *leapmuxv1.ControlPrompt {
	return &leapmuxv1.ControlPrompt{
		Kind:          &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{Questions: questions}},
		EditorPurpose: leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_ANSWER,
	}
}

// askQuestions reads the questions of the bridge's request, in the record
// shape that the bridge writes (contracts.OhMyPiAskQuestion): `{id,
// question, context?, header?, options: [{label, value?, markup?}], multi?,
// allowEmpty, allowCustomText, initialSelections?}`.
//
// A record that is no object or states no question is dropped. A flag reads
// only from a JSON boolean: a multiple choice and an empty answer need
// `true`, and allowCustomText is stated only when the record states it. See
// askOptions and askInitialSelections.
func askQuestions(raw json.RawMessage) []*leapmuxv1.ControlQuestion {
	records, _ := providerkit.RawArray(raw)
	questions := make([]*leapmuxv1.ControlQuestion, 0, len(records))
	for _, rawRecord := range records {
		record := providerkit.RawFields(rawRecord)
		text := rawText(record[contracts.OhMyPiAskQuestionQuestion])
		if record == nil || text == "" {
			continue
		}
		options := askOptions(record)
		multi := providerkit.RawBool(record[contracts.OhMyPiAskQuestionMulti])
		questions = append(questions, &leapmuxv1.ControlQuestion{
			Id:                rawText(record[contracts.OhMyPiAskQuestionID]),
			Header:            rawText(record[contracts.OhMyPiAskQuestionHeader]),
			Question:          text,
			Context:           rawText(record[contracts.OhMyPiAskQuestionContext]),
			Options:           options,
			MultiSelect:       multi,
			AllowEmpty:        providerkit.RawBool(record[contracts.OhMyPiAskQuestionAllowEmpty]),
			AllowCustomText:   providerkit.RawOptionalBool(record[contracts.OhMyPiAskQuestionAllowCustomText]),
			InitialSelections: askInitialSelections(record, options, multi),
		})
	}
	return questions
}

// askOptions reads the options of one question record. An option that is no
// object or states no label is dropped.
//
// The option's markup, the HTML of its form label, states what the option
// shows (readChoiceMarkup). An answer selects the option by the value that
// the bridge matches (askOptionValue): the record's value, or the record's
// own label when the value is absent or empty. The prompt states that value
// when the record states one, or when it differs from the label that the
// option shows. A preview that is all white space states none.
func askOptions(record map[string]json.RawMessage) []*leapmuxv1.QuestionOption {
	elements, _ := providerkit.RawArray(record[contracts.OhMyPiAskQuestionOptions])
	options := make([]*leapmuxv1.QuestionOption, 0, len(elements))
	for _, element := range elements {
		fields := providerkit.RawFields(element)
		label := rawText(fields[contracts.OhMyPiAskOptionLabel])
		if label == "" {
			continue
		}
		native := contracts.OhMyPiAskOption{Label: label, Value: rawText(fields[contracts.OhMyPiAskOptionValue])}
		presentation := readChoiceMarkup(rawText(fields[contracts.OhMyPiAskOptionMarkup]), label)
		option := &leapmuxv1.QuestionOption{Label: presentation.label, Description: presentation.description}
		if value := askOptionValue(native); native.Value != "" || value != presentation.label {
			option.Value = proto.String(value)
		}
		if agent.TrimJavaScriptSpace(presentation.preview) != "" {
			option.Preview = presentation.preview
		}
		options = append(options, option)
	}
	return options
}

// askInitialSelections reads the options that the form selects before the
// user answers: each string of the record's initialSelections that an option
// offers as its value, once, and the first one alone for a question that is
// not multiple-choice. The bridge also lists a disabled option that the form
// selects, and no option offers it.
//
// A record that states no list states no selection, so the question stays
// unanswered until the user answers it. A list with no value that an option
// offers is an empty selection, which answers a question that permits an
// empty answer.
func askInitialSelections(record map[string]json.RawMessage, options []*leapmuxv1.QuestionOption, multi bool) *leapmuxv1.QuestionSelections {
	elements, ok := providerkit.RawArray(record[contracts.OhMyPiAskQuestionInitialSelections])
	if !ok {
		return nil
	}
	offered := make(map[string]bool, len(options))
	for _, option := range options {
		offered[agent.QuestionOptionValue(option)] = true
	}
	selected := make(map[string]bool, len(elements))
	values := make([]string, 0, len(elements))
	for _, element := range elements {
		value, isText := providerkit.RawString(element)
		if !isText || !offered[value] || selected[value] {
			continue
		}
		selected[value] = true
		values = append(values, value)
	}
	if !multi && len(values) > 1 {
		values = values[:1]
	}
	return &leapmuxv1.QuestionSelections{Values: values}
}

// selectQuestionOptions reads the options of a select, with the description
// that omp sends beside each one in `optionDetails`, matched by position.
func selectQuestionOptions(payload ompControlPayload) []*leapmuxv1.QuestionOption {
	var details []json.RawMessage
	_ = json.Unmarshal(payload["optionDetails"], &details)
	labels := payload.stringOptions()
	options := make([]*leapmuxv1.QuestionOption, 0, len(labels))
	for i, label := range labels {
		option := &leapmuxv1.QuestionOption{Label: label}
		if i < len(details) {
			option.Description = rawText(providerkit.RawFields(details[i])["description"])
		}
		options = append(options, option)
	}
	return options
}

// approvalOptions are the two answers of omp's tool approval.
func approvalOptions() []*leapmuxv1.PermissionOption {
	return []*leapmuxv1.PermissionOption{
		{
			OptionId: contracts.OhMyPiApprovalDialogApprove,
			Kind:     leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE,
			Name:     contracts.OhMyPiApprovalDialogApprove,
		},
		{
			OptionId: contracts.OhMyPiApprovalDialogDeny,
			Kind:     leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE,
			Name:     contracts.OhMyPiApprovalDialogDeny,
		},
	}
}

// approvalPrompt reads a tool approval. The first line of its title names
// the tool. The further lines state the call's details, the command of a
// `bash` call last, and omp's extension wrapper can append a section of
// safety checks below them.
//
// The Worker points the request at the start frame of the call when exactly
// one call of the tool runs (approvalSourceSeq). The frame's arguments are
// then the call's input, and its command wins over the title's, which omp
// shortens.
func approvalPrompt(payload ompControlPayload, input agent.ControlRequestInput) *leapmuxv1.PermissionPrompt {
	first, body, _ := strings.Cut(payload.text("title"), "\n")
	toolName := strings.TrimSpace(strings.TrimPrefix(first, contracts.OhMyPiApprovalDialogTitlePrefix))
	command, rest := splitApprovalCommand(body)
	permission := &leapmuxv1.PermissionPrompt{
		Title:   toolName,
		Reason:  strings.TrimSpace(rest),
		Command: command,
		Options: approvalOptions(),
	}
	if args, ok := approvalArgs(input, toolName); ok {
		permission.InputJson = string(args)
		if argCommand := rawText(providerkit.RawFields(args)["command"]); argCommand != "" {
			permission.Command = argCommand
		}
	}
	return permission
}

// approvalArgs returns the arguments of the call that an approval asks
// about, from the call's own start frame, and false when the request's
// source is no start frame of the tool or states no arguments object.
func approvalArgs(input agent.ControlRequestInput, toolName string) (json.RawMessage, bool) {
	if input.Source == nil {
		return nil, false
	}
	row, ok := input.Source()
	if !ok {
		return nil, false
	}
	frame := providerkit.RawFields(row.Original)
	if rawText(frame["type"]) != contracts.OhMyPiEventToolExecutionStart ||
		rawText(frame[contracts.OhMyPiFrameFieldToolName]) != toolName {
		return nil, false
	}
	args := frame[contracts.OhMyPiFrameFieldArgs]
	if providerkit.RawFields(args) == nil {
		return nil, false
	}
	return args, true
}

// splitApprovalCommand splits the details of an approval title into the
// command and the lines around it. The command is the last detail and can
// hold newlines of its own, so it runs to the end of the title, or to the
// safety-check section. A title with no command line returns body as the
// rest.
func splitApprovalCommand(body string) (command, rest string) {
	start := -1
	if strings.HasPrefix(body, approvalCommandLabel) {
		start = 0
	} else if i := strings.Index(body, "\n"+approvalCommandLabel); i >= 0 {
		start = i + 1
	}
	if start < 0 {
		return "", body
	}
	after := body[start+len(approvalCommandLabel):]
	command, tail := after, ""
	if end := strings.Index(after, approvalSafetyChecks); end >= 0 {
		command, tail = after[:end], after[end:]
	}
	return command, strings.TrimSpace(body[:start] + tail)
}

// dialogPrompt reads a `confirm`, an `input` or an `editor`, or returns nil
// for any other request. omp states each field that a dialog carries: a
// confirm's message, an input's hint, an editor's draft, and the deadline
// after which omp answers the dialog itself.
func dialogPrompt(payload ompControlPayload) *leapmuxv1.DialogPrompt {
	if !payload.isExtensionUIRequest() {
		return nil
	}
	shape, ok := dialogShapes[payload.method()]
	if !ok {
		return nil
	}
	return &leapmuxv1.DialogPrompt{
		Title:       cmp.Or(payload.text("title"), shape.defaultTitle),
		Message:     payload.text("message"),
		Placeholder: payload.text("placeholder"),
		Prefill:     payload.text("prefill"),
		Variant:     shape.variant,
		TimeoutMs:   providerkit.DialogTimeoutMillis(payload["timeout"]),
	}
}

// AnswerControlRequest builds omp's answer for one decision: the bridge's
// leapmux_ask_answer for an `ask` call, or an extension_ui_response for a
// dialog.
func (ompProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	payload := readOmpControlPayload(input.Request.Payload)
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Question:
		if payload.isAskRequest() {
			return askRequestAnswer(input)
		}
		return selectQuestionAnswer(input)
	case *leapmuxv1.ControlPrompt_Permission:
		if payload.isApproval() {
			return approvalAnswer(input)
		}
		return genericAnswer(input)
	case *leapmuxv1.ControlPrompt_Dialog:
		return dialogAnswer(input)
	default:
		return agent.ControlAnswer{}, agent.InvalidControlDecision("Oh My Pi states no %T prompt", input.Prompt.GetKind())
	}
}

// dialogResponse is omp's answer to one dialog: an extension_ui_response
// whose field states a value, a confirmation or a cancellation.
func dialogResponse(requestID, field string, value any) (agent.ControlAnswer, error) {
	content, err := json.Marshal(map[string]any{
		contracts.OhMyPiDialogResponseType: contracts.OhMyPiEventExtensionUIResponse,
		contracts.OhMyPiDialogResponseID:   requestID,
		field:                              value,
	})
	if err != nil {
		return agent.ControlAnswer{}, fmt.Errorf("encode the Oh My Pi dialog answer: %w", err)
	}
	return agent.ControlAnswer{Content: content}, nil
}

func valueResponse(requestID, value string) (agent.ControlAnswer, error) {
	return dialogResponse(requestID, contracts.OhMyPiDialogResponseValue, value)
}

func cancelResponse(requestID string) (agent.ControlAnswer, error) {
	return dialogResponse(requestID, contracts.OhMyPiDialogResponseCancelled, true)
}

// askRequestAnswer answers the bridge's request with one answer for each
// question, the ones that the user left empty included, because the bridge
// matches each answer to its question by id. An answer states the values of
// the chosen options in `selected` and the typed text in `custom`, and
// leaves out an empty half. A multiple choice states both halves: the user
// chose those options and that text, and the bridge answers omp's toggles,
// then its "Other" and the input that follows it, with them (newAskPlan).
//
// A refusal cancels omp's first dialog. The `ask` tool then ends omp's run,
// and the cancellation carries no reason, so the decision's feedback goes
// nowhere.
func askRequestAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, questions, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if decision.GetCancelled() {
		return cancelResponse(input.RequestID)
	}
	answers := make([]contracts.OhMyPiAskQuestionAnswer, 0, len(questions))
	for i, question := range questions {
		answer := agent.QuestionAnswerAt(decision, i)
		values := agent.QuestionAnswerValues(question, answer)
		// QuestionAnswerValues states the selected labels first, then the
		// typed text as one more value.
		selected := len(answer.GetSelected())
		entry := contracts.OhMyPiAskQuestionAnswer{ID: question.GetId(), Selected: values[:selected]}
		if typed := values[selected:]; len(typed) > 0 {
			entry.Custom = typed[0]
		}
		answers = append(answers, entry)
	}
	content, err := json.Marshal(contracts.OhMyPiAskAnswer{
		Type:    contracts.OhMyPiAskTypeAnswer,
		ID:      input.RequestID,
		Answers: answers,
	})
	if err != nil {
		return agent.ControlAnswer{}, fmt.Errorf("encode the Oh My Pi question answer: %w", err)
	}
	return agent.ControlAnswer{Content: content}, nil
}

// selectQuestionAnswer answers a select with its one value: the chosen
// option. The prompt states the select's options alone, so
// QuestionDecisionOf refuses typed text, and refuses an answer with no
// option. An option whose label is blank cancels the select, because omp
// refuses a blank value for one. A refusal cancels it too, with no reason.
func selectQuestionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, questions, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if decision.GetCancelled() {
		return cancelResponse(input.RequestID)
	}
	// ReadControlRequest states exactly one question for a select; a nil
	// question reads as a question that is not multiple-choice.
	var question *leapmuxv1.ControlQuestion
	if len(questions) > 0 {
		question = questions[0]
	}
	values := agent.QuestionAnswerValues(question, agent.QuestionAnswerAt(decision, 0))
	if len(values) == 0 || agent.TrimJavaScriptSpace(values[0]) == "" {
		return cancelResponse(input.RequestID)
	}
	return valueResponse(input.RequestID, values[0])
}

// approvalAnswer answers a tool approval with the chosen option's word as the
// dialog's value. omp's Deny carries no reason, so the feedback of a refusal
// follows the answer as the user's next message.
func approvalAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, option, err := agent.PermissionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	feedback := decision.GetFeedback()
	answer, err := valueResponse(input.RequestID, option.GetOptionId())
	if err == nil && strings.TrimSpace(feedback) != "" {
		answer.FollowUp = feedback
	}
	return answer, err
}

// genericAnswer answers a request that omp did not state, with LeapMux's
// neutral allow or deny. The request takes no text, so a decision with
// feedback does not answer it.
func genericAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	if input.Decision.GetPermission().GetFeedback() != "" {
		return agent.ControlAnswer{}, agent.InvalidControlDecision("the request takes no feedback")
	}
	return agent.AnswerAllowDeny(input)
}

// dialogAnswer answers a dialog: a confirm takes a confirmation, an input or
// an editor takes its value, the empty text included, and a cancellation
// dismisses any dialog.
func dialogAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, _, err := agent.DialogDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	switch decision.GetAction() {
	case leapmuxv1.DialogAction_DIALOG_ACTION_CONFIRM:
		return dialogResponse(input.RequestID, contracts.OhMyPiDialogResponseConfirmed, true)
	case leapmuxv1.DialogAction_DIALOG_ACTION_REFUSE:
		return dialogResponse(input.RequestID, contracts.OhMyPiDialogResponseConfirmed, false)
	case leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT:
		return valueResponse(input.RequestID, decision.GetValue())
	default:
		return cancelResponse(input.RequestID)
	}
}
