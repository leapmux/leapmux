package qwen

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Qwen's wire words of its question dialog that only these readers spell, so
// the contract holds none of them.
const (
	// qwenUserQuestion is the interaction kind that Qwen states in a tool
	// call's `_meta` for a question dialog.
	qwenUserQuestion = "user_question"
	// qwenMultiSelectSeparator is the text that Qwen's own dialog puts between
	// the labels of a multiple selection. Qwen reads each answer as one
	// string, so the reply joins the labels as Qwen's dialog does.
	qwenMultiSelectSeparator = ", "
)

// qwenPermissionToolCall is the tool call that one permission request is
// about, in the compact form that the request itself states: its `_meta` and
// its arguments. A member that is no object reads as absent.
type qwenPermissionToolCall struct {
	meta     map[string]json.RawMessage
	rawInput map[string]json.RawMessage
}

func readQwenPermissionToolCall(payload []byte) qwenPermissionToolCall {
	toolCall := acp.PermissionToolCall(payload)
	return qwenPermissionToolCall{
		meta:     providerkit.RawFields(toolCall["_meta"]),
		rawInput: providerkit.RawFields(toolCall[contracts.ACPSupplementRequestRawInput]),
	}
}

// asksQuestion reports whether the request is Qwen's question dialog. Qwen
// raises its questions as a standard permission request, and it marks them in
// the tool call's `_meta` with the interaction kind.
func (c qwenPermissionToolCall) asksQuestion() bool {
	kind, _ := providerkit.RawString(c.meta[contracts.QwenMetaInteractionKind])
	return kind == qwenUserQuestion
}

// questions reads the questions of a question dialog. Qwen states them in
// `_meta`, and again as the tool call's own arguments. The `_meta` copy is the
// one that Qwen marks for a client, so it wins when it is a list.
func (c qwenPermissionToolCall) questions() []*leapmuxv1.ControlQuestion {
	raw := c.meta[contracts.QwenMetaQuestions]
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '[' {
		raw = c.rawInput["questions"]
	}
	return providerkit.QuestionsFromWire(raw)
}

// plan is the plan that a plan approval states as its argument, or "".
func (c qwenPermissionToolCall) plan() string {
	plan, _ := providerkit.RawString(c.rawInput["plan"])
	return plan
}

// ReadControlRequest reads the requests that Qwen raises as permissions of its
// own tools, and every other request through the base reader:
//
//   - A permission whose `_meta` marks a question dialog is a question.
//   - A permission about `exit_plan_mode` is a plan approval, with the plan
//     that the tool call states. Its four options are Qwen's own ways to leave
//     plan mode, and resolvePlanApproval picks one from the reader's decision
//     and the mode that the reader chose, so the prompt offers a plain
//     approve and reject.
//
// The standard `elicitation/create` reads as a form before either.
func (qwenProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	if elicitation, ok := acp.ReadElicitation(input.Payload); ok {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: elicitation}}
	}
	toolCall := readQwenPermissionToolCall(input.Payload)
	if toolCall.asksQuestion() {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
			Questions: toolCall.questions(),
		}}}
	}
	// isPlanApproval is the one plan predicate: the resolution of an answer
	// reads the same request through it, so a request reads as a plan
	// exactly when its answer resolves as one.
	if isPlanApproval(input.Payload) {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{Text: toolCall.plan()}}}
	}
	return acp.ReadBaseControlRequest(input)
}

// AnswerControlRequest builds Qwen's answer to one decision:
//
//   - A question takes Qwen's own reply, which ResolveControlResponse passes
//     on unchanged.
//   - A plan takes the neutral allow or deny, which resolvePlanApproval turns
//     into one of Qwen's options. It sends a refusal's reason as a message of
//     its own, so the answer states no follow-up.
//   - An MCP form and a permission take the base answer. Qwen's reply has no
//     field for a refusal's reason, so the reason follows the answer as a
//     message of its own.
func (qwenProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Question:
		return qwenQuestionAnswer(input)
	case *leapmuxv1.ControlPrompt_Plan:
		return agent.AnswerNeutralPlan(input)
	default:
		return acp.AnswerBaseControlRequest(input, nil)
	}
}

// qwenQuestionAnswer answers a question dialog through the permission options
// that Qwen sent beside it: its `Submit` is an allow option, and its `Cancel`
// is a reject option.
//
// An answer selects the submit option, and states the answers in a field of
// Qwen's own beside it, keyed by the question's index. Each answer is one
// string: the values of agent.QuestionAnswerValues, joined as Qwen's dialog
// joins its labels. A multiple-choice question thus states its typed words as
// one more entry after the chosen values, where the browser sent the chosen
// values alone. A question that the reader left empty is omitted.
//
// A cancellation selects the cancel option, which carries no reason, so a
// reason that the decision states is not sent.
func qwenQuestionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, questions, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	options := acp.PermissionOptions(input.Request.Payload)
	if decision.GetCancelled() {
		return agent.ControlAnswer{Content: acp.SelectedOptionResult(input.RequestID,
			qwenOptionOfKind(options, leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, contracts.QwenPermissionOptionCancel))}, nil
	}
	answers := map[string]string{}
	for i, question := range questions {
		value := strings.Join(agent.QuestionAnswerValues(question, agent.QuestionAnswerAt(decision, i)), qwenMultiSelectSeparator)
		if value != "" {
			answers[strconv.Itoa(i)] = value
		}
	}
	return agent.ControlAnswer{Content: agent.JSONRPCResult(input.RequestID, map[string]any{
		"outcome": map[string]any{
			"outcome":  contracts.ACPPermissionOutcomeSelected,
			"optionId": qwenOptionOfKind(options, leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, contracts.QwenPermissionOptionProceedOnce),
		},
		contracts.QwenReplyFieldAnswers: answers,
	})}, nil
}

// qwenOptionOfKind is the id of the first option of kind that the request
// offers, else fallback, Qwen's own id for it. An option with an empty id is
// no answer that Qwen can read, so it is skipped.
func qwenOptionOfKind(options []*leapmuxv1.PermissionOption, kind leapmuxv1.PermissionOptionKind, fallback string) string {
	for _, option := range options {
		if option.GetKind() == kind && option.GetOptionId() != "" {
			return option.GetOptionId()
		}
	}
	return fallback
}
