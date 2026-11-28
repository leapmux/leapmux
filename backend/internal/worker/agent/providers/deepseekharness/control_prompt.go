package deepseekharness

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
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
	parsed := parseWaterfallControl(input.Payload)
	switch parsed.Kind {
	case nativeControlKindQuestions:
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
			Questions:               deepseekHarnessQuestions(parsed.Questions),
			PreservesSelectionNotes: true,
		}}}
	case nativeControlKindPlanReview:
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: deepseekHarnessPlanPrompt(parsed.Questions[0])}}
	case nativeControlKindApproval:
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{
			Title:   parsed.Approval.ToolName,
			Reason:  parsed.Approval.Reason,
			Options: deepseekHarnessApprovalOptions(),
		}}}
	default:
		return agent.UnrecognizedControlPrompt()
	}
}

// deepseekHarnessQuestions converts parsed questions into the neutral prompt.
// Each option's value is its label, which the runtime matches.
func deepseekHarnessQuestions(records []nativeQuestion) []*leapmuxv1.ControlQuestion {
	questions := make([]*leapmuxv1.ControlQuestion, 0, len(records))
	for _, record := range records {
		options := make([]*leapmuxv1.QuestionOption, 0, len(record.Options))
		for _, option := range record.Options {
			label := option.Label
			options = append(options, &leapmuxv1.QuestionOption{
				Value:       &label,
				Label:       label,
				Description: option.Description,
			})
		}
		questions = append(questions, &leapmuxv1.ControlQuestion{
			Id:          record.ID,
			Header:      record.Header,
			Question:    record.Question,
			Options:     options,
			MultiSelect: record.MultiSelect,
		})
	}
	return questions
}

// deepseekHarnessPlanPrompt builds a neutral PlanPrompt from a parsed plan-review question.
func deepseekHarnessPlanPrompt(review nativeQuestion) *leapmuxv1.PlanPrompt {
	approve := ""
	if review.Intent != nil {
		approve = review.Intent.Approve
	}
	plan := &leapmuxv1.PlanPrompt{Text: review.Detail}
	for _, option := range review.Options {
		effect := leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_REFUSE
		if option.Label == approve {
			effect = leapmuxv1.PlanChoiceEffect_PLAN_CHOICE_EFFECT_APPROVE
		}
		plan.Choices = append(plan.Choices, &leapmuxv1.PlanChoice{Id: option.Label, Label: option.Label, Effect: effect})
	}
	return plan
}

// AnswerControlRequest builds DeepSeek Harness's answer in the neutral
// envelope, which the agent turns into the runtime's waterfall outcome
// (answerControl).
//
// The runtime's outcome has no place for the reason of a refusal. So a
// refusal of a permission, a plan, or a cancelled question with feedback also
// sends the feedback as the user's next message (ControlAnswer.FollowUp).
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
	if agent.TrimJavaScriptSpace(feedback) == "" {
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
	if parseWaterfallControl(input.Request.Payload).Kind != nativeControlKindApproval {
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
// words. A selection and a note coexist. A cancelled question denies with the
// default message and sends any non-blank feedback as a follow-up message.
func deepseekHarnessQuestionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, questions, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if decision.GetCancelled() {
		return agent.ControlAnswer{
			Content:  agent.DenyResponse(input.RequestID, ""),
			FollowUp: deepseekHarnessFeedbackFollowUp(decision.GetFeedback()),
		}, nil
	}
	answers := make([]map[string]any, 0, len(questions))
	for i, question := range questions {
		answer := agent.QuestionAnswerAt(decision, i)
		selected := append([]string{}, answer.GetSelected()...)
		entry := map[string]any{"id": question.GetId(), "selected": selected}
		if custom := agent.TrimJavaScriptSpace(answer.GetCustomText()); custom != "" {
			entry["custom"] = custom
		}
		answers = append(answers, entry)
	}
	return agent.ControlAnswer{Content: agent.ControlResponseEnvelope(input.RequestID, map[string]any{
		"behavior": contracts.ControlAnswerBehaviorAllow,
		"answers":  answers,
	})}, nil
}
