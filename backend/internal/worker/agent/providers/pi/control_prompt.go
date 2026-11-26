package pi

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Pi asks every control request through one extension_ui_request frame, and
// its method decides what the frame asks:
//
//   - An `input` or a `select` is a question. rpiv-ask-user-question asks
//     each of its questions through one of them, and the call's own
//     arguments state the options that the dialog shortens.
//   - A `select` titled "Proposed plan ready. What next?" whose options hold
//     Pi's three plan actions is the plan menu.
//   - Any other frame is a dialog: a `confirm` asks a yes or no, an `editor`
//     asks for text, and a method that LeapMux does not know reads as a
//     confirm, so the agent never waits on a dialog that the user cannot
//     answer.
//
// Pi reads one extension_ui_response line for each answer: a value, a
// confirmation, or a cancellation.

// The headings of a dialog that states none.
const (
	piDialogDefaultTitle   = "Approval Required"
	piSelectDefaultTitle   = "Choose an option"
	piQuestionDefaultTitle = "Enter a value"
)

// piPlanDetailPrefixes are the lines of the plan menu's title that state the
// settings of the implementation, which the prompt keeps visible beside the
// plan.
var piPlanDetailPrefixes = []string{"Model: ", "Plan reinjection: "}

// piPlanActions are the three answers of the plan menu that LeapMux states as
// an approval or a refusal. Every other option of the menu is a choice.
var piPlanActions = []string{
	contracts.PiPlanActionImplementHere,
	contracts.PiPlanActionImplementFresh,
	contracts.PiPlanActionStay,
}

// piControlPayload holds the fields of one Pi control request. Each field is
// read on its own, so a field of the wrong type reads as absent and leaves
// the other fields readable.
type piControlPayload map[string]json.RawMessage

func readPiControlPayload(payload []byte) piControlPayload {
	return providerkit.RawFields(payload)
}

// text returns the string field key, or "" when it is absent or no string.
func (p piControlPayload) text(key string) string {
	return providerkit.ObjectString(p, key)
}

func (p piControlPayload) method() string { return p.text("method") }

func (p piControlPayload) isExtensionUIRequest() bool {
	return p.text("type") == contracts.PiEventExtensionUIRequest
}

// isQuestion reports whether the request asks an `input` or a `select`. The
// plan menu is a `select` too, so a reader asks isPlanApproval first.
func (p piControlPayload) isQuestion() bool {
	method := p.method()
	return p.isExtensionUIRequest() &&
		(method == contracts.PiDialogMethodInput || method == contracts.PiDialogMethodSelect)
}

// isPlanApproval reports whether the request is Pi's plan menu: a `select`
// whose title's first line is the plan-ready title, and whose options are
// distinct, non-blank strings that hold the three plan actions. The menu's
// options decide what each answer means, so a menu that does not have this
// shape is no plan approval.
func (p piControlPayload) isPlanApproval() bool {
	if !p.isExtensionUIRequest() || p.method() != contracts.PiDialogMethodSelect ||
		strings.TrimSpace(piDialogTitleFirstLine(p.text("title"))) != contracts.PiPlanDialogReadyTitle {
		return false
	}
	options, ok := p.planOptions()
	if !ok {
		return false
	}
	for _, action := range piPlanActions {
		if !slices.Contains(options, action) {
			return false
		}
	}
	return true
}

// strictStringOptions decodes a JSON array where every element is a string.
func strictStringOptions(raw json.RawMessage) ([]string, bool) {
	var elements []json.RawMessage
	if json.Unmarshal(raw, &elements) != nil {
		return nil, false
	}
	options := make([]string, 0, len(elements))
	for _, element := range elements {
		option, ok := providerkit.RawString(element)
		if !ok {
			return nil, false
		}
		options = append(options, option)
	}
	return options, true
}

// planOptions returns the options of a select, and false unless each one is
// a non-blank string that no other option repeats.
func (p piControlPayload) planOptions() ([]string, bool) {
	options, ok := strictStringOptions(p["options"])
	if !ok {
		return nil, false
	}
	for i, option := range options {
		if strings.TrimSpace(option) == "" || slices.Contains(options[:i], option) {
			return nil, false
		}
	}
	return options, true
}

// selectOptions returns the string options of a select. An option of
// another type is dropped: Pi sends only strings, but the stored payload is
// read as untrusted.
func (p piControlPayload) selectOptions() []string {
	return providerkit.RawStrings(p["options"])
}

// ReadControlRequest reads Pi's extension_ui_request into a question, the
// plan menu, or a dialog. Pi states no request that LeapMux reads as a
// permission, so it never returns the generic permission prompt.
func (piProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	payload := readPiControlPayload(input.Payload)
	switch {
	case payload.isPlanApproval():
		return &leapmuxv1.ControlPrompt{
			Kind: &leapmuxv1.ControlPrompt_Plan{Plan: piPlanPrompt(payload)},
			// The composer's text revises the plan: it refuses the plan, and
			// the Worker sends the text after the refusal.
			EditorPurpose: leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_FEEDBACK,
		}
	case payload.isQuestion():
		return &leapmuxv1.ControlPrompt{
			Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
				Questions: piQuestions(payload, piQuestionFromSource(payload, providerkit.ReadSourceObject(input))),
			}},
			// The composer's text is the custom answer: no question of Pi's
			// refuses typed text.
			EditorPurpose: leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_ANSWER,
		}
	default:
		return &leapmuxv1.ControlPrompt{
			Kind: &leapmuxv1.ControlPrompt_Dialog{Dialog: piDialogPrompt(payload)},
			// A dialog takes its answer through its own controls.
			EditorPurpose: leapmuxv1.ControlEditorPurpose_CONTROL_EDITOR_PURPOSE_NONE,
		}
	}
}

// piPlanPrompt reads the plan menu. Pi states the plan in the transcript, not
// in the menu. Each option of the menu other than the three plan actions is a
// choice of an extension, such as "Export plan…" or "Save for later". LeapMux
// cannot know what such a choice does with the plan, so its effect is OTHER.
func piPlanPrompt(payload piControlPayload) *leapmuxv1.PlanPrompt {
	plan := &leapmuxv1.PlanPrompt{Details: piPlanApprovalDetails(payload.text("title"))}
	options, _ := payload.planOptions()
	for _, option := range options {
		if !slices.Contains(piPlanActions, option) {
			plan.Choices = append(plan.Choices, &leapmuxv1.PlanChoice{Id: option, Label: option, Effect: leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_OTHER})
		}
	}
	return plan
}

// piPlanApprovalDetails returns the lines of the menu's title that state the
// implementation's model and plan reinjection, so they stay visible when the
// user approves. Each prefix takes its first line.
func piPlanApprovalDetails(title string) []string {
	lines := strings.Split(title, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	var details []string
	for _, prefix := range piPlanDetailPrefixes {
		if i := slices.IndexFunc(lines, func(line string) bool { return strings.HasPrefix(line, prefix) }); i >= 0 {
			details = append(details, lines[i])
		}
	}
	return details
}

// piDialogPrompt reads a request that is neither a question nor the plan
// menu. A method without a control of its own reads as a confirm, and so
// does a frame of another type, whatever its method: a confirm offers both a
// yes and a no, so the user can always answer it.
func piDialogPrompt(payload piControlPayload) *leapmuxv1.DialogPrompt {
	title := payload.text("title")
	if title == "" {
		title = piDialogDefaultTitle
	}
	return &leapmuxv1.DialogPrompt{
		Title:       title,
		Message:     payload.text("message"),
		Placeholder: payload.text("placeholder"),
		Prefill:     payload.text("prefill"),
		Variant:     piDialogVariant(payload.method()),
		TimeoutMs:   providerkit.DialogTimeoutMillis(payload["timeout"]),
	}
}

func piDialogVariant(method string) leapmuxv1.DialogVariant {
	switch method {
	case contracts.PiDialogMethodInput:
		return leapmuxv1.DialogVariant_DIALOG_VARIANT_INPUT
	case contracts.PiDialogMethodEditor:
		return leapmuxv1.DialogVariant_DIALOG_VARIANT_EDITOR
	default:
		return leapmuxv1.DialogVariant_DIALOG_VARIANT_CONFIRM
	}
}

// piSourceOption is one option of a question, as the arguments of the
// ask_user_question call state it.
type piSourceOption struct {
	label       string
	description string
	preview     string
}

// questionOption converts the option into a prompt option that sends value.
func (o piSourceOption) questionOption(value string) *leapmuxv1.QuestionOption {
	return &leapmuxv1.QuestionOption{Value: &value, Label: o.label, Description: o.description, Preview: o.preview}
}

// piSourceQuestion is the question of an ask_user_question call that one
// dialog asks.
type piSourceQuestion struct {
	// index is the question's position in the call's arguments.
	index       int
	prompt      string
	header      string
	title       string
	multiSelect bool
	options     []piSourceOption
}

// piQuestionOptionLine is the line by which rpiv-ask-user-question states
// one option in its dialog, and which its `select` answers with.
func piQuestionOptionLine(option piSourceOption, index int) string {
	return strconv.Itoa(index+1) + ". " + option.label + " — " + option.description
}

// piQuestionFromSource returns the question of the source frame's
// ask_user_question call that the dialog asks, or nil.
//
// The dialog shortens a question: its title can cut an option's preview, and
// its options state no preview at all. The call's own arguments state the
// whole question, so the prompt reads them, but only for a dialog that
// matches exactly one question as rpiv-ask-user-question formats it:
//
//   - A single-choice question asks a `select` whose options are the
//     numbered option lines plus one numbered custom-answer line, and whose
//     title is the question's title, optionally followed by a preview.
//   - A multiple-choice question asks an `input` with the multiple-choice
//     placeholder, whose title states the question and its option lines.
//   - A custom answer to a single-choice question asks an `input` with no
//     placeholder, whose title states the question.
//
// A question that is no object, states no question text or no options, or
// holds an option without a string label and description is skipped.
func piQuestionFromSource(dialog piControlPayload, frame map[string]json.RawMessage) *piSourceQuestion {
	if frame == nil {
		return nil
	}
	if kind, _ := providerkit.RawString(frame["type"]); kind != contracts.PiEventToolExecutionStart {
		return nil
	}
	if tool, _ := providerkit.RawString(frame["toolName"]); tool != contracts.PiToolAskUserQuestion {
		return nil
	}
	args := frame["args"]
	if len(args) == 0 {
		args = frame["input"]
	}
	return matchPiSourceQuestion(dialog.dialogView(), args)
}

func matchPiSourceQuestion(view piDialogView, rawArgs json.RawMessage) *piSourceQuestion {
	args := providerkit.RawFields(rawArgs)
	if args == nil {
		return nil
	}
	var questions []json.RawMessage
	if json.Unmarshal(args["questions"], &questions) != nil {
		return nil
	}
	var match *piSourceQuestion
	for index, raw := range questions {
		question := readPiSourceQuestion(index, raw)
		if question == nil || !question.matches(view) {
			continue
		}
		if match != nil {
			// Two questions match the dialog, so neither is known to be it.
			return nil
		}
		match = question
	}
	return match
}

// readPiSourceQuestion reads one question of the call's arguments, or nil for
// a question that the dialog cannot have asked.
func readPiSourceQuestion(index int, raw json.RawMessage) *piSourceQuestion {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil
	}
	text, ok := providerkit.RawString(fields["question"])
	if !ok || text == "" {
		return nil
	}
	var rawOptions []json.RawMessage
	if json.Unmarshal(fields["options"], &rawOptions) != nil || len(rawOptions) == 0 {
		return nil
	}
	options := make([]piSourceOption, 0, len(rawOptions))
	for _, rawOption := range rawOptions {
		var option map[string]json.RawMessage
		if json.Unmarshal(rawOption, &option) != nil || option == nil {
			return nil
		}
		label, labelOK := providerkit.RawString(option["label"])
		description, descriptionOK := providerkit.RawString(option["description"])
		if !labelOK || !descriptionOK {
			return nil
		}
		// The preview keeps its line endings: no match compares it with
		// the dialog.
		preview, _ := providerkit.RawString(option["preview"])
		options = append(options, piSourceOption{
			label:       piQuestionText(label),
			description: piQuestionText(description),
			preview:     preview,
		})
	}
	header, _ := providerkit.RawString(fields["header"])
	question := &piSourceQuestion{
		index:       index,
		prompt:      piQuestionText(text),
		header:      piQuestionText(header),
		multiSelect: providerkit.RawBool(fields["multiSelect"]),
		options:     options,
	}
	question.title = question.prompt
	if question.header != "" {
		question.title = "[" + question.header + "] " + question.prompt
	}
	return question
}

// piDialogView is what matches compares of a dialog: its method, title,
// placeholder and options. optionsOK is false when an option is no string,
// which no question's dialog states.
type piDialogView struct {
	method, title, placeholder string
	options                    []string
	optionsOK                  bool
}

// dialogView reads what matches compares of the request.
func (p piControlPayload) dialogView() piDialogView {
	view := piDialogView{method: p.method(), title: p.text("title"), placeholder: p.text("placeholder"), optionsOK: true}
	if len(p["options"]) == 0 {
		return view
	}
	options, ok := strictStringOptions(p["options"])
	if ok {
		view.options = options
		return view
	}
	// Match existing behavior: non-array options yield empty options with optionsOK=true;
	// only an array containing a non-string element sets optionsOK=false.
	var offered []json.RawMessage
	if json.Unmarshal(p["options"], &offered) == nil {
		view.optionsOK = false
	}
	return view
}

// matches reports whether the dialog of view asks the question, as the doc
// of piQuestionFromSource states. The runtime's matcher (piQuestionIndex)
// and the prompt both decide through it.
func (q *piSourceQuestion) matches(view piDialogView) bool {
	lines := make([]string, len(q.options))
	for i, option := range q.options {
		lines[i] = piQuestionOptionLine(option, i)
	}
	switch view.method {
	case contracts.PiDialogMethodSelect:
		if q.multiSelect || !view.optionsOK || len(view.options) != len(lines)+1 {
			return false
		}
		for i, line := range lines {
			if view.options[i] != line {
				return false
			}
		}
		return strings.HasPrefix(view.options[len(lines)], strconv.Itoa(len(lines)+1)+". ") &&
			(view.title == q.title || strings.HasPrefix(view.title, q.title+"\n\n--- "))
	case contracts.PiDialogMethodInput:
		if q.multiSelect {
			return view.placeholder == contracts.PiQuestionDialogMultiSelectPlaceholder &&
				strings.HasPrefix(view.title, q.title+"\n\n"+strings.Join(lines, "\n")+"\n\n")
		}
		return view.placeholder == "" && strings.HasPrefix(view.title, q.title+"\n\n") && !strings.HasPrefix(view.title, q.title+"\n\n--- ")
	default:
		return false
	}
}

// piQuestions reads the one question that a Pi `input` or `select` asks.
//
//   - A multiple-choice question that the source states takes the numbers of
//     its options, as rpiv's `input` reads them (piQuestionAnswerValue).
//   - A `select` answers with the line of the chosen option, which the
//     source's options state in full, or else with the option itself.
//   - Any other `input` takes typed text, an empty answer included.
func piQuestions(payload piControlPayload, source *piSourceQuestion) []*leapmuxv1.ControlQuestion {
	method := payload.method()
	question := &leapmuxv1.ControlQuestion{Id: payload.text("id")}
	switch {
	case source != nil && method == contracts.PiDialogMethodInput && source.multiSelect:
		question.Question = source.prompt
		question.Header = source.header
		question.MultiSelect = true
		question.AllowEmpty = true
		for i, option := range source.options {
			question.Options = append(question.Options, option.questionOption(strconv.Itoa(i+1)))
		}
	case method == contracts.PiDialogMethodSelect && source != nil:
		question.Question = source.prompt
		question.Header = source.header
		for i, option := range source.options {
			question.Options = append(question.Options, option.questionOption(piQuestionOptionLine(option, i)))
		}
	case method == contracts.PiDialogMethodSelect:
		question.Question = cmp.Or(payload.text("title"), piSelectDefaultTitle)
		for _, label := range payload.selectOptions() {
			question.Options = append(question.Options, &leapmuxv1.QuestionOption{Label: label})
		}
	default:
		if source != nil {
			question.Question = source.title
		} else {
			question.Question = cmp.Or(payload.text("title"), piQuestionDefaultTitle)
		}
		question.AllowEmpty = method == contracts.PiDialogMethodInput
	}
	return []*leapmuxv1.ControlQuestion{question}
}

// piDialogResponse is Pi's answer to one dialog, the extension_ui_response
// line that Pi reads on its stdin: a value for a select, an input or an
// editor, a confirmation for a confirm, or a cancellation of any dialog.
type piDialogResponse struct {
	Type      string  `json:"type"`
	ID        string  `json:"id"`
	Value     *string `json:"value,omitempty"`
	Confirmed *bool   `json:"confirmed,omitempty"`
	Cancelled bool    `json:"cancelled,omitempty"`
}

func piValueResponse(requestID, value string) piDialogResponse {
	return piDialogResponse{Type: contracts.PiEventExtensionUIResponse, ID: requestID, Value: &value}
}

func piConfirmResponse(requestID string, confirmed bool) piDialogResponse {
	return piDialogResponse{Type: contracts.PiEventExtensionUIResponse, ID: requestID, Confirmed: &confirmed}
}

func piCancelResponse(requestID string) piDialogResponse {
	return piDialogResponse{Type: contracts.PiEventExtensionUIResponse, ID: requestID, Cancelled: true}
}

// answer encodes the response as the content of an answer. The Worker sends
// it to Pi unchanged, except that SendRawInput turns a custom answer to a
// question's `select` into rpiv's select-then-input exchange.
func (r piDialogResponse) answer() (agent.ControlAnswer, error) {
	content, err := json.Marshal(r)
	if err != nil {
		return agent.ControlAnswer{}, fmt.Errorf("encode the Pi dialog answer: %w", err)
	}
	return agent.ControlAnswer{Content: content}, nil
}

// AnswerControlRequest builds Pi's extension_ui_response for one decision.
func (piProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Question:
		return piQuestionAnswer(input)
	case *leapmuxv1.ControlPrompt_Plan:
		return piPlanAnswer(input)
	case *leapmuxv1.ControlPrompt_Dialog:
		return piDialogAnswer(input)
	default:
		return agent.ControlAnswer{}, agent.InvalidControlDecision("Pi states no %T prompt", input.Prompt.GetKind())
	}
}

// piQuestionAnswer answers the one question of a dialog with one value:
//
//   - A refusal cancels the dialog. Pi's cancellation carries no reason, so
//     the decision's feedback goes nowhere.
//   - A `select` takes the chosen value or the typed text, and cancels when
//     it has neither, because Pi's select cannot take an empty value.
//   - An `input` takes its value, the empty text included, because Pi tells
//     an empty answer apart from a cancellation.
func piQuestionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, questions, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if decision.GetCancelled() {
		return piCancelResponse(input.RequestID).answer()
	}
	// piQuestions states exactly one question; a nil question reads as a
	// question that is not multiple-choice.
	var question *leapmuxv1.ControlQuestion
	if len(questions) > 0 {
		question = questions[0]
	}
	value := piQuestionAnswerValue(question, agent.QuestionAnswerAt(decision, 0))
	if readPiControlPayload(input.Request.Payload).method() == contracts.PiDialogMethodSelect && agent.TrimJavaScriptSpace(value) == "" {
		return piCancelResponse(input.RequestID).answer()
	}
	return piValueResponse(input.RequestID, value).answer()
}

// piQuestionAnswerValue is the one value that answers a question, in the
// form that rpiv-ask-user-question reads (agent.QuestionAnswerValues says
// what the answer holds):
//
//   - A multiple choice states the numbers of the chosen options, joined by
//     commas, and then the typed text after "; ", as rpiv's placeholder
//     "1,3; custom answer" shows. rpiv reads a list that is not all numbers
//     as typed text, so text joined by a comma would turn the whole answer
//     into text and drop the chosen options. Typed text alone keeps its
//     "; " too, so that text such as "2" never reads as an option number.
//   - Any other question states its option, or else the typed text.
//
// A prompt offers options for a multiple choice only when it read them from
// the source, which also marks the question as one, so the question alone
// says which form to use.
func piQuestionAnswerValue(question *leapmuxv1.ControlQuestion, answer *leapmuxv1.QuestionAnswer) string {
	values := agent.QuestionAnswerValues(question, answer)
	if !question.GetMultiSelect() {
		if len(values) == 0 {
			return ""
		}
		return values[0]
	}
	// QuestionAnswerValues states the selected values first, then the
	// typed text as one more value.
	selected := len(answer.GetSelected())
	value := strings.Join(values[:selected], ",")
	if typed := values[selected:]; len(typed) > 0 {
		value += "; " + typed[0]
	}
	return value
}

// piPlanAnswer answers the plan menu with one of its own options, as the
// menu's buttons do:
//
//   - An approval implements the plan here, or in a fresh session when the
//     decision clears the context. Pi starts the fresh session itself, so
//     the answer carries no LeapMux settings, and a permission mode has no
//     way to reach Pi.
//   - A choice sends the chosen option.
//   - A plain refusal stays in plan mode. Pi's menu takes no text, so the
//     feedback follows the answer as the user's next message.
//
// Feedback goes with a plain refusal only: the menu offers no approval or
// choice that carries text.
func piPlanAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, err := agent.PlanDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	feedback := decision.GetFeedback()
	switch {
	case decision.GetApprove():
		if feedback != "" {
			return agent.ControlAnswer{}, agent.InvalidControlDecision("feedback goes with a refusal of the plan")
		}
		if decision.GetPermissionMode() != "" {
			return agent.ControlAnswer{}, agent.InvalidControlDecision("Pi's plan menu applies no permission mode")
		}
		value := contracts.PiPlanActionImplementHere
		if decision.GetClearContext() {
			value = contracts.PiPlanActionImplementFresh
		}
		return piValueResponse(input.RequestID, value).answer()
	case decision.GetChoiceId() != "":
		if feedback != "" {
			return agent.ControlAnswer{}, agent.InvalidControlDecision("feedback goes with a plain refusal of the plan, and %q is a choice", decision.GetChoiceId())
		}
		return piValueResponse(input.RequestID, decision.GetChoiceId()).answer()
	default:
		answer, err := piValueResponse(input.RequestID, contracts.PiPlanActionStay).answer()
		if err == nil && agent.TrimJavaScriptSpace(feedback) != "" {
			answer.FollowUp = feedback
		}
		return answer, err
	}
}

// piDialogAnswer answers a dialog: a confirm takes a confirmation, an input
// or an editor takes its value, and a cancellation dismisses any dialog.
func piDialogAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, _, err := agent.DialogDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	switch decision.GetAction() {
	case leapmuxv1.DialogAction_DIALOG_ACTION_CONFIRM:
		return piConfirmResponse(input.RequestID, true).answer()
	case leapmuxv1.DialogAction_DIALOG_ACTION_REFUSE:
		return piConfirmResponse(input.RequestID, false).answer()
	case leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT:
		return piValueResponse(input.RequestID, decision.GetValue()).answer()
	default:
		return piCancelResponse(input.RequestID).answer()
	}
}
