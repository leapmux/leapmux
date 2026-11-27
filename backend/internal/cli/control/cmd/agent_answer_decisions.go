package cmd

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

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
const answerHint = `choose an option with "leapmux agent answer <agent> <request> --option <n>"`

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
				WithHint(`give it with "leapmux agent answer <agent> <request> <text>"`)
		}
		return decisionOf(&leapmuxv1.DialogDecision{Action: leapmuxv1.DialogAction_DIALOG_ACTION_CONFIRM}), nil
	case *leapmuxv1.ControlPrompt_Elicitation:
		return elicitationAccept(kind.Elicitation, nil, "")
	case *leapmuxv1.ControlPrompt_Question:
		return nil, cli.Usagef("a question takes an answer, not an approval").
			WithHint(`answer it with "leapmux agent answer <agent> <request> --option <n>" or a text`)
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
			WithHint(`refuse it with "leapmux agent deny <agent> <request> --message <text>"`)
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
				WithHint(`approve or refuse it with "leapmux agent approve" or "deny"`)
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
				WithHint(`refuse to answer with "leapmux agent deny <agent> <request>"`)
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
				WithHint(`accept the defaults with "leapmux agent approve <agent> <request>"`)
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
				WithHint(`confirm or refuse it with "leapmux agent approve" or "deny"`)
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
// numbered answers, which noun identifies in the messages.
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
			WithHint(`"leapmux agent requests <agent>" numbers them`)
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
