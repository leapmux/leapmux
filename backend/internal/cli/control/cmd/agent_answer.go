package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// `agent approve`, `agent deny` and `agent answer` answer one pending
// control request. Each command builds a neutral decision from the request's
// prompt and its own flags, and the Worker's provider package turns the
// decision into the provider's answer (AnswerControl). The command only maps
// its flags onto the prompt: the Worker checks that the decision answers the
// prompt, so the CLI and the browser cannot disagree about what is valid.

// controlAnswerResult is the result of an answer.
type controlAnswerResult struct {
	// ID is the handle of the answered request.
	ID      string `json:"id"`
	AgentID string `json:"agent_id"`
	// State is the delivery of the answer: "completed" when the agent has it
	// and the transcript records it, "delivered" when the agent has it and
	// the record follows, and "pending" when the Worker holds it and its
	// delivery follows.
	State string `json:"state"`
}

// controlDecider builds the decision of one command for prompt.
type controlDecider func(prompt *leapmuxv1.ControlPrompt) (*leapmuxv1.ControlDecision, error)

// runControlAnswer answers the request that the arguments give with the
// decision that decide builds.
func runControlAnswer(inv *cli.Invocation, decide controlDecider) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	if err := refuseSelfAnswer(got); err != nil {
		return nil, err
	}
	var listed leapmuxv1.ListControlRequestsResponse
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceListControlRequestsProcedure, &leapmuxv1.ListControlRequestsRequest{AgentId: got.ID}, &listed); err != nil {
		return nil, err
	}
	request, err := findControlRequest(got.ID, listed.GetRequests(), inv.Arg("request"))
	if err != nil {
		return nil, err
	}
	if err := requireAnswerable(got.ID, request); err != nil {
		return nil, err
	}
	decision, err := decide(request.GetPrompt())
	if err != nil {
		return nil, err
	}
	var answered leapmuxv1.AnswerControlResponse
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceAnswerControlProcedure, &leapmuxv1.AnswerControlRequest{
		AgentId: got.ID, RequestId: request.GetRequestId(), ClaimToken: request.GetClaimToken(), Decision: decision,
	}, &answered); err != nil {
		return nil, err
	}
	return controlAnswerResultOf(got.ID, request.GetClaimToken(), &answered)
}

// refuseSelfAnswer refuses an answer to a request of the calling agent. The
// user answers an agent's own prompts: an agent that approved its own
// permission prompt would remove the reason for the prompt.
func refuseSelfAnswer(t tabTarget) error {
	if !t.Self && t.ID != callingTabID() {
		return nil
	}
	return cli.Usagef("agent %s is the calling agent, which cannot answer its own requests", t.ID).
		WithHint("the user answers them, in LeapMux or from another terminal")
}

// findControlRequest returns the request of agentID whose handle is handle,
// or the one request whose handle starts with it.
func findControlRequest(agentID string, requests []*leapmuxv1.AgentControlRequest, handle string) (*leapmuxv1.AgentControlRequest, error) {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return nil, cli.Usagef("the request handle is empty")
	}
	var matches []*leapmuxv1.AgentControlRequest
	for _, r := range requests {
		if r.GetClaimToken() == handle {
			return r, nil
		}
		if strings.HasPrefix(r.GetClaimToken(), handle) {
			matches = append(matches, r)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, cli.Newf(cli.CodeNotFound, "agent %s has no pending request %s", agentID, handle).
			WithHint("list its requests with \"leapmux control agent requests %s\"", agentID)
	}
	e := cli.Newf(cli.CodeAmbiguousTarget, "%q matches %d requests of agent %s", handle, len(matches), agentID).
		WithHint("give more characters of the handle")
	for _, r := range matches {
		e.Candidates = append(e.Candidates, cli.Candidate{ID: r.GetClaimToken(), Kind: controlPromptKind(r.GetPrompt())})
	}
	return nil, e
}

// requireAnswerable refuses a request that takes no answer now: one that
// LeapMux cannot read, and one whose answer the Worker already holds.
func requireAnswerable(agentID string, r *leapmuxv1.AgentControlRequest) error {
	if fault := r.GetPrompt().GetUnreadable(); fault != leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_UNSPECIFIED {
		return cli.Newf(cli.CodeConflict, "LeapMux cannot read request %s (%s), so it takes no answer", r.GetClaimToken(), enumToken(fault)).
			WithHint("stop the agent's turn with \"leapmux control agent interrupt %s\"", agentID)
	}
	if kind := controlPromptKind(r.GetPrompt()); kind == controlKindUnknown {
		return cli.Newf(cli.CodeVersionMismatch, "request %s asks something that this CLI cannot answer", r.GetClaimToken()).
			WithHint("use a leapmux CLI of the Worker's version")
	}
	if state := r.GetResponseState(); state != leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY {
		return cli.Newf(cli.CodeConflict, "request %s already has an answer (%s)", r.GetClaimToken(), controlResponseStateToken(state))
	}
	return nil
}

// controlAnswerResultOf reads the Worker's response to an answer. A request
// that the agent withdrew, or that another client answered first, is a
// conflict. A failure that left the answer unsent can succeed on a retry;
// one after the Worker saved the answer cannot, because the Worker never
// sends a second answer.
func controlAnswerResultOf(agentID, handle string, r *leapmuxv1.AnswerControlResponse) (controlAnswerResult, error) {
	result := controlAnswerResult{ID: handle, AgentID: agentID, State: controlResponseStateToken(r.GetState())}
	switch {
	case r.GetError() != "" && r.GetState() == leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY:
		return result, cli.Newf(cli.CodeUnavailable, "the answer to request %s was not sent: %s", handle, r.GetError())
	case r.GetError() != "":
		return result, cli.Newf(cli.CodeInternal, "the Worker holds the answer to request %s, but its delivery did not finish (%s): %s", handle, result.State, r.GetError()).
			WithHint("check it with \"leapmux control agent requests %s\"", agentID)
	case r.GetState() == leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_CANCELED:
		return result, cli.Newf(cli.CodeConflict, "request %s is no longer pending: the agent withdrew it, or another client answered it", handle)
	}
	return result, nil
}

// requestArgHelp describes the <request> argument.
const requestArgHelp = `The request's handle, as "agent requests" prints it, or a unique prefix of it.`

// addPlanApprovalFlags registers the flags that an approval of a plan
// applies.
func addPlanApprovalFlags(fs *pflag.FlagSet) {
	fs.String("permission-mode", "", "with a plan's approval, the permission mode to implement it in (default: the agent's own)")
	fs.Bool("clear-context", false, "with a plan's approval, implement it in a fresh context")
}

// planSettings are the flags that an approval of a plan applies.
type planSettings struct {
	permissionMode string
	clearContext   bool
	// set reports whether the command line gave either flag.
	set bool
}

// planSettingsOf reads --permission-mode and --clear-context.
func planSettingsOf(inv *cli.Invocation) planSettings {
	return planSettings{
		permissionMode: stringFlag(inv, "permission-mode"),
		clearContext:   boolFlag(inv, "clear-context"),
		set:            inv.Changed("permission-mode") || inv.Changed("clear-context"),
	}
}

// refusePlanSettings refuses the plan flags on a prompt that is not a plan.
func refusePlanSettings(prompt *leapmuxv1.ControlPrompt, settings planSettings) error {
	if settings.set && prompt.GetPlan() == nil {
		return cli.Usagef("--permission-mode and --clear-context apply to a plan, and this request is a %s", controlPromptKind(prompt))
	}
	return nil
}

func decisionOf(kind any) *leapmuxv1.ControlDecision {
	switch kind := kind.(type) {
	case *leapmuxv1.PermissionDecision:
		return &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Permission{Permission: kind}}
	case *leapmuxv1.PlanDecision:
		return &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Plan{Plan: kind}}
	case *leapmuxv1.QuestionDecision:
		return &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Question{Question: kind}}
	case *leapmuxv1.ElicitationDecision:
		return &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Elicitation{Elicitation: kind}}
	case *leapmuxv1.DialogDecision:
		return &leapmuxv1.ControlDecision{Kind: &leapmuxv1.ControlDecision_Dialog{Dialog: kind}}
	}
	panic(fmt.Sprintf("cmd: no decision kind %T", kind))
}

// firstOptionOfKind returns the first option of kind, or nil.
func firstOptionOfKind(options []*leapmuxv1.PermissionOption, kind leapmuxv1.PermissionOptionKind) *leapmuxv1.PermissionOption {
	i := slices.IndexFunc(options, func(o *leapmuxv1.PermissionOption) bool { return o.GetKind() == kind })
	if i < 0 {
		return nil
	}
	return options[i]
}

// answerHint is the hint that points at `agent answer --option`.
const answerHint = `choose an option with "leapmux control agent answer <agent> <request> --option <n>"`

// approveDecision is the decision of `agent approve`:
//
//   - A permission prompt: its first one-time approval.
//   - A plan: the approval, with the plan settings.
//   - A confirm dialog: the confirmation.
//   - An MCP elicitation: the accept, with the form's defaults in form mode.
//
// A question takes `agent answer`.
func approveDecision(prompt *leapmuxv1.ControlPrompt, settings planSettings) (*leapmuxv1.ControlDecision, error) {
	if err := refusePlanSettings(prompt, settings); err != nil {
		return nil, err
	}
	switch kind := prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Permission:
		option := firstOptionOfKind(kind.Permission.GetOptions(), leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE)
		if option == nil {
			return nil, cli.Usagef("the request offers no one-time approval").WithHint(answerHint)
		}
		return decisionOf(&leapmuxv1.PermissionDecision{OptionId: option.GetOptionId()}), nil
	case *leapmuxv1.ControlPrompt_Plan:
		return decisionOf(&leapmuxv1.PlanDecision{Approve: true, PermissionMode: settings.permissionMode, ClearContext: settings.clearContext}), nil
	case *leapmuxv1.ControlPrompt_Dialog:
		if variant := kind.Dialog.GetVariant(); variant != leapmuxv1.DialogVariant_DIALOG_VARIANT_CONFIRM && variant != leapmuxv1.DialogVariant_DIALOG_VARIANT_UNSPECIFIED {
			return nil, cli.Usagef("the dialog asks for text").
				WithHint(`give it with "leapmux control agent answer <agent> <request> <text>"`)
		}
		return decisionOf(&leapmuxv1.DialogDecision{Action: leapmuxv1.DialogAction_DIALOG_ACTION_CONFIRM}), nil
	case *leapmuxv1.ControlPrompt_Elicitation:
		return elicitationAccept(kind.Elicitation, nil, "")
	case *leapmuxv1.ControlPrompt_Question:
		return nil, cli.Usagef("a question takes an answer, not an approval").
			WithHint(`answer it with "leapmux control agent answer <agent> <request> --option <n>" or a text`)
	}
	return nil, cli.Usagef("a %s request takes no approval", controlPromptKind(prompt))
}

// denyDecision is the decision of `agent deny`:
//
//   - A permission prompt: its first one-time refusal, with the reason.
//   - A plan: the refusal, with what to change.
//   - A question: the refusal to answer, with the reason.
//   - An MCP elicitation: the decline.
//   - A dialog: the refusal of a confirm dialog, or the cancel of another.
//
// An MCP elicitation and a dialog take no reason.
func denyDecision(prompt *leapmuxv1.ControlPrompt, message string) (*leapmuxv1.ControlDecision, error) {
	switch kind := prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Permission:
		option := firstOptionOfKind(kind.Permission.GetOptions(), leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE)
		if option == nil {
			return nil, cli.Usagef("the request offers no one-time refusal").WithHint(answerHint)
		}
		return decisionOf(&leapmuxv1.PermissionDecision{OptionId: option.GetOptionId(), Feedback: message}), nil
	case *leapmuxv1.ControlPrompt_Plan:
		return decisionOf(&leapmuxv1.PlanDecision{Feedback: message}), nil
	case *leapmuxv1.ControlPrompt_Question:
		return decisionOf(&leapmuxv1.QuestionDecision{Cancelled: true, Feedback: message}), nil
	}
	if message != "" {
		return nil, cli.Usagef("a %s takes no reason, so --message does not apply", controlPromptKind(prompt))
	}
	switch kind := prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Elicitation:
		return decisionOf(&leapmuxv1.ElicitationDecision{Action: leapmuxv1.ElicitationAction_ELICITATION_ACTION_DECLINE}), nil
	case *leapmuxv1.ControlPrompt_Dialog:
		action := leapmuxv1.DialogAction_DIALOG_ACTION_CANCEL
		if variant := kind.Dialog.GetVariant(); variant == leapmuxv1.DialogVariant_DIALOG_VARIANT_CONFIRM || variant == leapmuxv1.DialogVariant_DIALOG_VARIANT_UNSPECIFIED {
			action = leapmuxv1.DialogAction_DIALOG_ACTION_REFUSE
		}
		return decisionOf(&leapmuxv1.DialogDecision{Action: action}), nil
	}
	return nil, cli.Usagef("a %s request takes no refusal", controlPromptKind(prompt))
}

// answerSpec is what `agent answer` gives.
type answerSpec struct {
	// texts are the positional texts. A question prompt of several questions
	// takes each as "<question>:<text>".
	texts []string
	// options are the --option values: a number, or "<question>:<number>".
	options []string
	// fields are the --field values: "<name>=<value>".
	fields   []string
	message  string
	settings planSettings
}

// answersEveryQuestion reports whether answers holds a value for each
// question that needs one: an option or a text, or nothing for a question
// that allows an empty answer, as "agent requests" states with "or nothing".
func answersEveryQuestion(questions []*leapmuxv1.ControlQuestion, answers []*leapmuxv1.QuestionAnswer) bool {
	for i, q := range questions {
		if !q.GetAllowEmpty() && len(answers[i].GetSelected()) == 0 && answers[i].GetCustomText() == "" {
			return false
		}
	}
	return true
}

// answerDecision is the decision of `agent answer`, which can choose any
// answer that the prompt offers:
//
//   - A permission prompt: the option --option gives, with --message as the
//     reason of a refusal.
//   - A plan: the choice --option gives, with --message as what to change
//     and the plan settings for an approval.
//   - Questions: the options and the texts. A question that they leave
//     unanswered takes the options that the agent selected for it, as the
//     browser's answer starts from them. With no value at all, the command
//     sends those, which must answer every question that needs an answer.
//   - An MCP elicitation: the accept of the form that --field fills, with
//     the accept choice that --option gives.
//   - An input or editor dialog: the text.
func answerDecision(prompt *leapmuxv1.ControlPrompt, spec answerSpec) (*leapmuxv1.ControlDecision, error) {
	if err := refusePlanSettings(prompt, spec.settings); err != nil {
		return nil, err
	}
	kind := controlPromptKind(prompt)
	if len(spec.fields) > 0 && kind != controlKindElicitation {
		return nil, cli.Usagef("--field fills an MCP form, and this request is a %s", kind)
	}
	if spec.message != "" && kind != controlKindPermission && kind != controlKindPlan {
		return nil, cli.Usagef("--message gives the reason of a refusal, which a %s answer does not take", kind).
			WithHint(`refuse it with "leapmux control agent deny <agent> <request> --message <text>"`)
	}
	switch p := prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Permission:
		if err := refuseTexts(spec, kind); err != nil {
			return nil, err
		}
		n, err := singleOption(spec.options, len(p.Permission.GetOptions()), "option")
		if err != nil {
			return nil, err
		}
		return decisionOf(&leapmuxv1.PermissionDecision{OptionId: p.Permission.GetOptions()[n-1].GetOptionId(), Feedback: spec.message}), nil
	case *leapmuxv1.ControlPrompt_Plan:
		if err := refuseTexts(spec, kind); err != nil {
			return nil, err
		}
		if len(spec.options) == 0 {
			return nil, cli.Usagef("give a choice of the plan with --option <n>").
				WithHint(`approve or refuse it with "leapmux control agent approve" or "deny"`)
		}
		n, err := singleOption(spec.options, len(p.Plan.GetChoices()), "choice")
		if err != nil {
			return nil, err
		}
		choice := p.Plan.GetChoices()[n-1]
		return decisionOf(&leapmuxv1.PlanDecision{
			Approve: choice.GetEffect() == leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_APPROVE, ChoiceId: choice.GetId(), Feedback: spec.message,
			PermissionMode: spec.settings.permissionMode, ClearContext: spec.settings.clearContext,
		}), nil
	case *leapmuxv1.ControlPrompt_Question:
		questions := p.Question.GetQuestions()
		answers, err := questionAnswers(questions, spec.options, spec.texts)
		if err != nil {
			return nil, err
		}
		// With values, the Worker checks the answer and states what is missing.
		// With none, the command would send only the defaults, so it says here
		// what the command line lacks.
		if len(spec.options) == 0 && len(spec.texts) == 0 && !answersEveryQuestion(questions, answers) {
			return nil, cli.Usagef("give an answer with --option <n> or a text").
				WithHint(`refuse to answer with "leapmux control agent deny <agent> <request>"`)
		}
		return decisionOf(&leapmuxv1.QuestionDecision{Answers: answers}), nil
	case *leapmuxv1.ControlPrompt_Elicitation:
		if err := refuseTexts(spec, kind); err != nil {
			return nil, err
		}
		if len(spec.fields) == 0 && len(spec.options) == 0 {
			// An accept with the defaults alone is what `agent approve` sends, so
			// an empty `agent answer` never accepts a form by accident.
			return nil, cli.Usagef("fill the form with --field, or choose an accept choice with --option").
				WithHint(`accept the defaults with "leapmux control agent approve <agent> <request>"`)
		}
		key := ""
		if len(spec.options) > 0 {
			n, err := singleOption(spec.options, len(p.Elicitation.GetAcceptChoices()), "accept choice")
			if err != nil {
				return nil, err
			}
			key = p.Elicitation.GetAcceptChoices()[n-1].GetKey()
		}
		return elicitationAccept(p.Elicitation, spec.fields, key)
	case *leapmuxv1.ControlPrompt_Dialog:
		if variant := p.Dialog.GetVariant(); variant == leapmuxv1.DialogVariant_DIALOG_VARIANT_CONFIRM || variant == leapmuxv1.DialogVariant_DIALOG_VARIANT_UNSPECIFIED {
			return nil, cli.Usagef("a confirm dialog takes no text").
				WithHint(`confirm or refuse it with "leapmux control agent approve" or "deny"`)
		}
		if len(spec.options) > 0 {
			return nil, cli.Usagef("a dialog offers no options")
		}
		if len(spec.texts) != 1 {
			return nil, cli.Usagef("give the dialog's text as one argument")
		}
		return decisionOf(&leapmuxv1.DialogDecision{Action: leapmuxv1.DialogAction_DIALOG_ACTION_SUBMIT, Value: spec.texts[0]}), nil
	}
	return nil, cli.Usagef("a %s request takes no answer", kind)
}

// refuseTexts refuses positional texts on a prompt that takes none.
func refuseTexts(spec answerSpec, kind string) error {
	if len(spec.texts) > 0 {
		return cli.Usagef("a %s takes no text", kind).WithHint(answerHint)
	}
	return nil
}

// singleOption reads the one --option value of a prompt with count
// numbered answers, which noun names in the messages.
func singleOption(values []string, count int, noun string) (int, error) {
	if len(values) != 1 {
		return 0, cli.Usagef("give one %s with --option <n>", noun)
	}
	return optionNumber(values[0], count, noun)
}

// optionNumber parses value as a number from 1 to count.
func optionNumber(value string, count int, noun string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < 1 || n > count {
		if count == 0 {
			return 0, cli.Usagef("the request offers no %s", noun)
		}
		return 0, cli.Usagef("--option %q is not a %s number from 1 to %d", value, noun, count).
			WithHint(`"leapmux control agent requests <agent>" numbers them`)
	}
	return n, nil
}

// questionAnswers builds one answer for each question from the --option
// values and the texts. A prompt of one question takes "<n>" or "1:<n>" and
// one text as it is. A prompt of several questions takes "<question>:<n>"
// and "<question>:<text>", so each value says which question it answers.
// A multiple-choice question takes options and a text together; the Worker
// checks every other rule. A question that no --option chooses for keeps
// the options that the agent selected for it, if it states any.
func questionAnswers(questions []*leapmuxv1.ControlQuestion, options, texts []string) ([]*leapmuxv1.QuestionAnswer, error) {
	if len(questions) == 0 {
		return nil, cli.Usagef("the request asks no question")
	}
	answers := make([]*leapmuxv1.QuestionAnswer, len(questions))
	for i := range answers {
		answers[i] = &leapmuxv1.QuestionAnswer{}
	}
	for _, value := range options {
		q, rest, err := questionOf(value, len(questions), false)
		if err != nil {
			return nil, err
		}
		question := questions[q-1]
		n, err := optionNumber(rest, len(question.GetOptions()), fmt.Sprintf("question %d option", q))
		if err != nil {
			return nil, err
		}
		option := question.GetOptions()[n-1]
		chosen := option.GetLabel()
		if option.Value != nil {
			chosen = option.GetValue()
		}
		if slices.Contains(answers[q-1].Selected, chosen) {
			return nil, cli.Usagef("--option %q chooses option %d of question %d twice", value, n, q)
		}
		answers[q-1].Selected = append(answers[q-1].Selected, chosen)
	}
	for _, value := range texts {
		q, text, err := questionOf(value, len(questions), true)
		if err != nil {
			return nil, err
		}
		if answers[q-1].CustomText != "" {
			return nil, cli.Usagef("question %d has two texts", q)
		}
		if strings.TrimSpace(text) == "" {
			return nil, cli.Usagef("the text of question %d is empty", q)
		}
		answers[q-1].CustomText = text
	}
	for i, question := range questions {
		if len(answers[i].Selected) == 0 && question.GetInitialSelections() != nil {
			answers[i].Selected = slices.Clone(question.GetInitialSelections().GetValues())
		}
	}
	return answers, nil
}

// questionOf splits value into the number of the question that it answers
// and the rest. With one question, a text is all rest: a text that starts
// with "2:" is still that text.
func questionOf(value string, count int, isText bool) (int, string, error) {
	if count == 1 && isText {
		return 1, value, nil
	}
	prefix, rest, found := strings.Cut(value, ":")
	if !found {
		if count == 1 {
			return 1, value, nil
		}
		form := "<question>:<n>"
		if isText {
			form = "<question>:<text>"
		}
		return 0, "", cli.Usagef("the request asks %d questions, so %q must say which one it answers", count, value).
			WithHint("write it as %s, such as 2:%s", form, map[bool]string{false: "1", true: "Use the cache"}[isText])
	}
	q, err := strconv.Atoi(strings.TrimSpace(prefix))
	if err != nil || q < 1 || q > count {
		return 0, "", cli.Usagef("%q answers no question: the request asks questions 1 to %d", value, count)
	}
	return q, rest, nil
}

// elicitationAccept accepts an MCP elicitation. A form sends its content: the
// defaults of the schema, with the fields over them, and every required
// field must have a value. A form in any other mode, such as a URL to visit,
// sends no content, as the browser does.
func elicitationAccept(e *leapmuxv1.ElicitationPrompt, assignments []string, acceptChoice string) (*leapmuxv1.ControlDecision, error) {
	decision := &leapmuxv1.ElicitationDecision{Action: leapmuxv1.ElicitationAction_ELICITATION_ACTION_ACCEPT, AcceptChoiceKey: acceptChoice}
	if e.GetMode() != "" && e.GetMode() != elicitationModeForm {
		if len(assignments) > 0 {
			return nil, cli.Usagef("an MCP elicitation in %s mode has no form to fill", e.GetMode())
		}
		return decisionOf(decision), nil
	}
	content, err := elicitationContent(e.GetSchemaJson(), fieldRuleViewsOf(e.GetFieldRules()), assignments)
	if err != nil {
		return nil, err
	}
	decision.ContentJson = content
	return decisionOf(decision), nil
}

// elicitationModeForm is the MCP elicitation mode of a form. An elicitation
// that states no mode is a form, as MCP defines.
const elicitationModeForm = "form"

// elicitationField is one property of an MCP elicitation schema.
type elicitationField struct {
	Name        string
	Type        string
	Required    bool
	Description string
	// Enum lists the values that the field accepts, or nil for any value.
	Enum []string
	// Default is the schema's default value, or nil.
	Default json.RawMessage
	// ItemType is the type of each value of an array field.
	ItemType string
	// Hidden reports a field that the form never shows. Its default is still
	// sent while the field is active.
	Hidden bool
	// Conditions must all hold for the form to ask the field.
	Conditions []fieldConditionView
}

// elicitationFields lists the properties of schema in the order that the
// schema states them, each with its rule. A schema that does not parse lists
// none.
func elicitationFields(schema json.RawMessage, rules []fieldRuleView) []elicitationField {
	ruleOf := make(map[string]fieldRuleView, len(rules))
	for _, rule := range rules {
		ruleOf[rule.Key] = rule
	}
	var parsed struct {
		Properties json.RawMessage `json:"properties"`
		Required   []string        `json:"required"`
	}
	if len(schema) == 0 || json.Unmarshal(schema, &parsed) != nil {
		return nil
	}
	names, properties := orderedObject(parsed.Properties)
	fields := make([]elicitationField, 0, len(names))
	for _, name := range names {
		var property struct {
			Type        string          `json:"type"`
			Title       string          `json:"title"`
			Description string          `json:"description"`
			Enum        []any           `json:"enum"`
			OneOf       []schemaConst   `json:"oneOf"`
			Default     json.RawMessage `json:"default"`
			Items       struct {
				Type  string        `json:"type"`
				Enum  []any         `json:"enum"`
				AnyOf []schemaConst `json:"anyOf"`
			} `json:"items"`
		}
		_ = json.Unmarshal(properties[name], &property)
		description := property.Description
		if property.Title != "" && description == "" {
			description = property.Title
		}
		field := elicitationField{
			Name:        name,
			Type:        property.Type,
			Required:    slices.Contains(parsed.Required, name),
			Description: description,
			Enum:        enumValues(property.Enum, property.OneOf),
			Default:     property.Default,
			ItemType:    property.Items.Type,
			Hidden:      ruleOf[name].Hidden,
			Conditions:  ruleOf[name].Conditions,
		}
		if property.Type == "array" {
			field.Enum = enumValues(property.Items.Enum, property.Items.AnyOf)
		}
		fields = append(fields, field)
	}
	return fields
}

// schemaConst is one titled value of a JSON Schema oneOf or anyOf.
type schemaConst struct {
	Const any `json:"const"`
}

// enumValues lists the values of an enum, or of a oneOf of constants.
func enumValues(enum []any, consts []schemaConst) []string {
	var out []string
	for _, v := range enum {
		out = append(out, fmt.Sprint(v))
	}
	for _, c := range consts {
		if c.Const != nil {
			out = append(out, fmt.Sprint(c.Const))
		}
	}
	return out
}

// orderedObject returns the keys of a JSON object in their order, with each
// value. Anything other than an object gives no keys.
func orderedObject(raw json.RawMessage) ([]string, map[string]json.RawMessage) {
	values := map[string]json.RawMessage{}
	if json.Unmarshal(raw, &values) != nil {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, nil
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil
		}
		key, _ := tok.(string)
		keys = append(keys, key)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, nil
		}
	}
	return keys, values
}

// elicitationContent builds the content of a form, as the browser's form
// reads it. It reads the fields in the order of the schema. A field is
// active when every condition of its rule holds for the content of the
// earlier active fields, and an inactive field is neither sent nor required.
// An active field sends its assignment, or else its default. Each
// assignment is "<name>=<value>", converted to the field's type, and an
// array field takes one assignment for each of its values.
func elicitationContent(schemaJSON string, rules []fieldRuleView, assignments []string) (string, error) {
	fields := elicitationFields(json.RawMessage(schemaJSON), rules)
	byName := map[string]elicitationField{}
	for _, field := range fields {
		byName[field.Name] = field
	}
	values := map[string]any{}
	for _, assignment := range assignments {
		name, value, found := strings.Cut(assignment, "=")
		if !found || name == "" {
			return "", cli.Usagef("--field %q is not <name>=<value>", assignment)
		}
		field, ok := byName[name]
		if ok && field.Hidden {
			return "", cli.Usagef("the form keeps field %q hidden and sends its default", name)
		}
		if !ok {
			var names []string
			for _, f := range fields {
				if !f.Hidden {
					names = append(names, f.Name)
				}
			}
			return "", cli.Usagef("the form has no field %q", name).WithHint("its fields are: %s", strings.Join(names, ", "))
		}
		converted, err := fieldValue(field, value)
		if err != nil {
			return "", err
		}
		if field.Type == "array" {
			list, _ := values[name].([]any)
			values[name] = append(list, converted)
			continue
		}
		if _, twice := values[name]; twice {
			return "", cli.Usagef("--field gives %q twice", name)
		}
		values[name] = converted
	}
	content := map[string]any{}
	var missing []string
	for _, field := range fields {
		if !conditionsHold(field.Conditions, content) {
			if _, given := values[field.Name]; given {
				return "", cli.Usagef("the form asks field %q only %s", field.Name, conditionText(field.Conditions))
			}
			continue
		}
		switch value, given := values[field.Name]; {
		case given:
			content[field.Name] = value
		case len(field.Default) > 0:
			content[field.Name] = field.Default
		case field.Required:
			missing = append(missing, field.Name)
		}
	}
	if len(missing) > 0 {
		return "", cli.Usagef("the form requires %s", strings.Join(missing, ", ")).
			WithHint(`fill each with "leapmux control agent answer <agent> <request> --field <name>=<value>"`)
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return "", cli.Wrap(cli.CodeInternal, err, "encode the form")
	}
	return string(encoded), nil
}

// conditionsHold reports whether every condition holds for content. A
// condition on a field with no value holds for no comparison, as in the
// browser.
func conditionsHold(conditions []fieldConditionView, content map[string]any) bool {
	equal := enumToken(leapmuxv1.ElicitationFieldComparison_ELICITATION_FIELD_COMPARISON_EQUAL)
	for _, condition := range conditions {
		value, ok := content[condition.Field]
		if !ok || answerMatches(value, condition.Value) != (condition.Comparison == equal) {
			return false
		}
	}
	return true
}

// answerMatches reports whether a field's value is want, or holds want for
// a list. Both are compared as JSON values, so the number 1 of an integer
// field matches the condition value 1.
func answerMatches(value any, want json.RawMessage) bool {
	var target any
	if json.Unmarshal(want, &target) != nil {
		return false
	}
	switch target.(type) {
	case string, float64, bool:
	default:
		// A condition value is a string, a number or a boolean. Any other
		// value matches nothing, and it would not compare safely.
		return false
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	var got any
	if json.Unmarshal(encoded, &got) != nil {
		return false
	}
	if list, ok := got.([]any); ok {
		return slices.Contains(list, target)
	}
	return got == target
}

// conditionText states conditions for a reader, such as
// `when env = "prod" and replicas != 0`.
func conditionText(conditions []fieldConditionView) string {
	equal := enumToken(leapmuxv1.ElicitationFieldComparison_ELICITATION_FIELD_COMPARISON_EQUAL)
	parts := make([]string, 0, len(conditions))
	for _, condition := range conditions {
		operator := "!="
		if condition.Comparison == equal {
			operator = "="
		}
		parts = append(parts, fmt.Sprintf("%s %s %s", condition.Field, operator, condition.Value))
	}
	return "when " + strings.Join(parts, " and ")
}

// fieldValue converts value to the type of field.
func fieldValue(field elicitationField, value string) (any, error) {
	if len(field.Enum) > 0 && !slices.Contains(field.Enum, value) {
		return nil, cli.Usagef("field %q takes one of %s, not %q", field.Name, strings.Join(field.Enum, ", "), value)
	}
	kind := field.Type
	if kind == "array" {
		kind = field.ItemType
	}
	switch kind {
	case "number":
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return nil, cli.Usagef("field %q takes a number, not %q", field.Name, value)
		}
		return json.Number(value), nil
	case "integer":
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return nil, cli.Usagef("field %q takes an integer, not %q", field.Name, value)
		}
		return json.Number(value), nil
	case "boolean":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return nil, cli.Usagef("field %q takes true or false, not %q", field.Name, value)
		}
		return b, nil
	default:
		return value, nil
	}
}

// answerTexts reads the positional texts of `agent answer`. A text "-"
// reads standard input, once.
func answerTexts(inv *cli.Invocation) ([]string, error) {
	texts := inv.Variadic("text")
	stdinRead := false
	for i, text := range texts {
		if text != "-" {
			continue
		}
		if stdinRead {
			return nil, cli.Usagef(`only one text can be "-"`)
		}
		data, err := io.ReadAll(inv.Stdin)
		if err != nil {
			return nil, cli.Wrap(cli.CodeInternal, err, "read stdin")
		}
		texts[i] = string(data)
		stdinRead = true
	}
	return texts, nil
}

func runAgentApprove(inv *cli.Invocation) (any, error) {
	settings := planSettingsOf(inv)
	return runControlAnswer(inv, func(prompt *leapmuxv1.ControlPrompt) (*leapmuxv1.ControlDecision, error) {
		return approveDecision(prompt, settings)
	})
}

func runAgentDeny(inv *cli.Invocation) (any, error) {
	message := stringFlag(inv, "message")
	return runControlAnswer(inv, func(prompt *leapmuxv1.ControlPrompt) (*leapmuxv1.ControlDecision, error) {
		return denyDecision(prompt, message)
	})
}

func runAgentAnswer(inv *cli.Invocation) (any, error) {
	texts, err := answerTexts(inv)
	if err != nil {
		return nil, err
	}
	options := inv.StringArray("option")
	fields := inv.StringArray("field")
	// answerDecision refuses a command line that gives no answer, except for
	// questions that allow an empty one, which only the prompt can tell.
	spec := answerSpec{texts: texts, options: options, fields: fields, message: stringFlag(inv, "message"), settings: planSettingsOf(inv)}
	return runControlAnswer(inv, func(prompt *leapmuxv1.ControlPrompt) (*leapmuxv1.ControlDecision, error) {
		return answerDecision(prompt, spec)
	})
}
