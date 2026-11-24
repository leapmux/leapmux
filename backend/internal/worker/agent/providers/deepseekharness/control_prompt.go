package deepseekharness

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// deepseekHarnessIntentPlanReview is the intent kind of the one question of a
// user-questions request that asks to approve a plan.
const deepseekHarnessIntentPlanReview = "plan-review"

// The options of a DeepSeek Harness approval. The runtime answers an approval
// with `allowed-once` or `rejected` alone (answerControl), so the prompt
// offers those two.
const (
	deepseekHarnessOptionAllow = "allow"
	deepseekHarnessOptionDeny  = "deny"
)

// deepseekHarnessApprovalOptions are the answers of a DeepSeek Harness
// approval: allow once and deny.
func deepseekHarnessApprovalOptions() []*leapmuxv1.PermissionOption {
	return []*leapmuxv1.PermissionOption{
		{OptionId: deepseekHarnessOptionAllow, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow once"},
		{OptionId: deepseekHarnessOptionDeny, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Deny"},
	}
}

// ReadControlRequest reads the two waterfall controls that the Worker
// publishes for DeepSeek Harness (publishNativeControl):
//
//   - A user-questions request is a question, unless a question of it has
//     the plan-review intent.
//   - A user-questions request whose one question has the plan-review intent
//     is a plan, with the question's options as the plan's choices.
//   - An approval request that states its tool is a permission with the
//     runtime's two answers.
//
// Any other payload, and a user-questions request that mixes a plan review
// with other questions, reads as unrecognized: the resolution refuses an
// answer to an approval that states no tool and to another event, and the
// agent cannot send the runtime an allow that answers no question.
func (deepseekHarnessProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	fields, _ := providerkit.RawObject(input.Payload)
	if providerkit.ObjectString(fields, "event") == contracts.DeepseekHarnessControlEventUserQuestions {
		records := deepseekHarnessQuestionRecords(fields)
		if !deepseekHarnessHasPlanReview(records) {
			return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
				Questions:               deepseekHarnessQuestions(records),
				PreservesSelectionNotes: true,
			}}}
		}
		if plan, ok := deepseekHarnessPlanPrompt(records); ok {
			return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: plan}}
		}
		return agent.UnrecognizedControlPrompt()
	}
	if permission, ok := deepseekHarnessApprovalPrompt(fields); ok {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: permission}}
	}
	return agent.UnrecognizedControlPrompt()
}

// deepseekHarnessQuestionRecords reads the question records of a
// user-questions request: `request.questions`, or `questions` of the payload
// itself when it states no `request` object. A record that is no object is
// dropped.
func deepseekHarnessQuestionRecords(payload map[string]json.RawMessage) []map[string]json.RawMessage {
	request, ok := providerkit.RawObject(payload["request"])
	if !ok {
		request = payload
	}
	var elements []json.RawMessage
	_ = json.Unmarshal(request["questions"], &elements)
	records := make([]map[string]json.RawMessage, 0, len(elements))
	for _, element := range elements {
		if record, ok := providerkit.RawObject(element); ok {
			records = append(records, record)
		}
	}
	return records
}

// deepseekHarnessIntent reads the intent object of one question record, or
// nil when it states none.
func deepseekHarnessIntent(record map[string]json.RawMessage) map[string]json.RawMessage {
	intent, _ := providerkit.RawObject(record["intent"])
	return intent
}

// deepseekHarnessHasPlanReview reports whether a question of records has the
// plan-review intent.
func deepseekHarnessHasPlanReview(records []map[string]json.RawMessage) bool {
	for _, record := range records {
		if providerkit.ObjectString(deepseekHarnessIntent(record), "kind") == deepseekHarnessIntentPlanReview {
			return true
		}
	}
	return false
}

// deepseekHarnessOptionRecords reads the options of one question record. An
// option that is no object, or that states no label, is dropped.
func deepseekHarnessOptionRecords(record map[string]json.RawMessage) []map[string]json.RawMessage {
	var elements []json.RawMessage
	_ = json.Unmarshal(record["options"], &elements)
	options := make([]map[string]json.RawMessage, 0, len(elements))
	for _, element := range elements {
		option, ok := providerkit.RawObject(element)
		if ok && providerkit.ObjectString(option, "label") != "" {
			options = append(options, option)
		}
	}
	return options
}

// deepseekHarnessQuestions reads the questions of a user-questions request.
// A question needs its native id and its text, because the runtime matches an
// answer by the id; a record without either is dropped. Each option's value
// is its label, which the runtime matches.
func deepseekHarnessQuestions(records []map[string]json.RawMessage) []*leapmuxv1.ControlQuestion {
	questions := []*leapmuxv1.ControlQuestion{}
	for _, record := range records {
		id := providerkit.ObjectString(record, "id")
		text := providerkit.ObjectString(record, "question")
		if id == "" || text == "" {
			continue
		}
		options := []*leapmuxv1.QuestionOption{}
		for _, option := range deepseekHarnessOptionRecords(record) {
			label := providerkit.ObjectString(option, "label")
			options = append(options, &leapmuxv1.QuestionOption{
				Value:       &label,
				Label:       label,
				Description: providerkit.ObjectString(option, "description"),
			})
		}
		questions = append(questions, &leapmuxv1.ControlQuestion{
			Id:          id,
			Header:      providerkit.ObjectString(record, "header"),
			Question:    text,
			Options:     options,
			MultiSelect: providerkit.RawBool(record["multiSelect"]),
		})
	}
	return questions
}

// deepseekHarnessPlanPrompt reads a plan review: the one question of the
// request, with the plan-review intent. Its detail is the plan, and each of
// its options is a choice, which approves the plan when its label is the
// intent's `approve` label. It reports false for any other request.
func deepseekHarnessPlanPrompt(records []map[string]json.RawMessage) (*leapmuxv1.PlanPrompt, bool) {
	if len(records) != 1 {
		return nil, false
	}
	review := records[0]
	intent := deepseekHarnessIntent(review)
	if providerkit.ObjectString(intent, "kind") != deepseekHarnessIntentPlanReview {
		return nil, false
	}
	approve := providerkit.ObjectString(intent, "approve")
	plan := &leapmuxv1.PlanPrompt{Text: providerkit.ObjectString(review, "detail")}
	for _, option := range deepseekHarnessOptionRecords(review) {
		label := providerkit.ObjectString(option, "label")
		effect := leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_REFUSE
		if label == approve {
			effect = leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_APPROVE
		}
		plan.Choices = append(plan.Choices, &leapmuxv1.PlanChoice{Id: label, Label: label, Effect: effect})
	}
	return plan, true
}

// deepseekHarnessApprovalPrompt reads an approval request: the tool as the
// title, and the reason that DeepSeek Harness's own client shows. That is
// the English display reason, else the audit reason. It reports false for
// another event, and for an approval that states no tool.
func deepseekHarnessApprovalPrompt(payload map[string]json.RawMessage) (*leapmuxv1.PermissionPrompt, bool) {
	if providerkit.ObjectString(payload, "event") != contracts.DeepseekHarnessControlEventApproval {
		return nil, false
	}
	request, _ := providerkit.RawObject(payload["request"])
	title := providerkit.ObjectString(request, "toolName")
	if title == "" {
		return nil, false
	}
	displayReason, _ := providerkit.RawObject(request["displayReason"])
	reason := providerkit.ObjectString(displayReason, "en")
	if reason == "" {
		reason = providerkit.ObjectString(request, "reason")
	}
	return &leapmuxv1.PermissionPrompt{Title: title, Reason: reason, Options: deepseekHarnessApprovalOptions()}, true
}

// AnswerControlRequest builds DeepSeek Harness's answer in the neutral
// envelope, which the agent turns into the runtime's waterfall outcome
// (answerControl).
//
// The runtime's outcome has no place for the reason of a refusal. So a
// refusal of a permission or of a plan with feedback also sends the feedback
// as the user's next message (ControlAnswer.FollowUp).
func (deepseekHarnessProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Question:
		return deepseekHarnessQuestionAnswer(input)
	case *leapmuxv1.ControlPrompt_Plan:
		answer, err := agent.AnswerNeutralPlan(input)
		if err != nil {
			return agent.ControlAnswer{}, err
		}
		if plan := input.Decision.GetPlan(); !plan.GetApprove() {
			answer.FollowUp = deepseekHarnessFeedbackFollowUp(plan.GetFeedback())
		}
		return answer, nil
	case *leapmuxv1.ControlPrompt_Permission:
		return deepseekHarnessPermissionAnswer(input)
	default:
		return agent.ControlAnswer{}, agent.InvalidControlDecision("DeepSeek Harness states no %T prompt", input.Prompt.GetKind())
	}
}

// deepseekHarnessFeedbackFollowUp is the message that follows a refusal: the
// feedback as the user typed it, or "" for feedback with no words.
func deepseekHarnessFeedbackFollowUp(feedback string) string {
	if strings.TrimSpace(feedback) == "" {
		return ""
	}
	return feedback
}

// deepseekHarnessPermissionAnswer answers a permission prompt:
//
//   - The approval's allow is a bare allow, which the runtime reads as
//     `allowed-once`.
//   - A deny carries the feedback, or the placeholder for none, and the
//     feedback also follows as a message.
//
// Only an approval reads as a permission (ReadControlRequest), so the answer
// refuses a permission decision about any other request.
func deepseekHarnessPermissionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, option, err := agent.PermissionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	fields, _ := providerkit.RawObject(input.Request.Payload)
	if _, approval := deepseekHarnessApprovalPrompt(fields); !approval {
		return agent.ControlAnswer{}, agent.InvalidControlDecision("DeepSeek Harness asks no permission for this request")
	}
	if agent.IsRefusalOption(option) {
		return agent.ControlAnswer{
			Content:  agent.DenyResponse(input.RequestID, decision.GetFeedback()),
			FollowUp: deepseekHarnessFeedbackFollowUp(decision.GetFeedback()),
		}, nil
	}
	return agent.ControlAnswer{Content: agent.ControlResponseEnvelope(input.RequestID, map[string]any{"behavior": contracts.ControlAnswerBehaviorAllow})}, nil
}

// deepseekHarnessQuestionAnswer answers a question with the runtime's own
// answer list: for each question, its native id, the selected labels (one at
// most for a single choice), and the typed note, trimmed, when it holds
// words. A selection and a note coexist. A cancelled question denies with its
// feedback.
func deepseekHarnessQuestionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, questions, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if decision.GetCancelled() {
		return agent.ControlAnswer{Content: agent.DenyResponse(input.RequestID, decision.GetFeedback())}, nil
	}
	answers := make([]map[string]any, 0, len(questions))
	for i, question := range questions {
		answer := agent.QuestionAnswerAt(decision, i)
		selected := append([]string{}, answer.GetSelected()...)
		entry := map[string]any{"id": question.GetId(), "selected": selected}
		if custom := strings.TrimSpace(answer.GetCustomText()); custom != "" {
			entry["custom"] = custom
		}
		answers = append(answers, entry)
	}
	return agent.ControlAnswer{Content: agent.ControlResponseEnvelope(input.RequestID, map[string]any{
		"behavior": contracts.ControlAnswerBehaviorAllow,
		"answers":  answers,
	})}, nil
}
