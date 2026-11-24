package codebuddy

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ReadControlRequest reads CodeBuddy Code's control requests. CodeBuddy's
// stream is Claude-shaped, so the tool name decides: AskUserQuestion is a
// question, ExitPlanMode a plan, and any other tool a permission.
//
// A permission offers LeapMux's allow and deny. CodeBuddy's answer
// (contracts.CodebuddyCanUseToolAnswer) states whether the call may run and a
// reason, and LeapMux offers no remembered grant for it.
// ResolveControlResponse turns the neutral envelope into that answer.
func (codebuddyProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	if question, ok := providerkit.ClaudeShapedQuestionPrompt(input.Payload); ok {
		return question
	}
	return providerkit.ClaudeShapedToolPrompt(input.Payload, agent.AllowDenyOptions())
}

// AnswerControlRequest builds CodeBuddy Code's answer in the neutral
// envelope, which ResolveControlResponse turns into CodeBuddy's `allowed`
// answer:
//
//   - A question allows with the tool input and the answers beside it.
//   - A plan approval allows with the tool input, and a refusal denies with
//     the user's feedback.
//   - A permission allows with the tool input, or denies with the feedback.
func (codebuddyProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Question:
		return providerkit.AskUserQuestionAnswers(input, agent.ControlToolInput(input.Request.Payload))
	case *leapmuxv1.ControlPrompt_Plan:
		return agent.AnswerNeutralPlan(input)
	case *leapmuxv1.ControlPrompt_Permission:
		return agent.AnswerAllowDeny(input)
	default:
		return agent.ControlAnswer{}, agent.InvalidControlDecision("CodeBuddy Code states no %T prompt", input.Prompt.GetKind())
	}
}
