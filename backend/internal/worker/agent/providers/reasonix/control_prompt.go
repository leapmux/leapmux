package reasonix

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// ReadControlRequest reads Reasonix's plan gate as a plan. The gate is a
// permission for its exit_plan_mode tool, and Reasonix streams the proposal
// into the transcript before it asks, so the prompt repeats no plan text.
// Every other request is the base protocol's.
//
// isPlanApproval recognizes the gate here, as it does for
// ResolveControlResponse, so the prompt and the answer agree about which
// request is a plan.
func (reasonixProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	if isPlanApproval(input.Payload) {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{}}}
	}
	return acp.ReadBaseControlRequest(input)
}

// AnswerControlRequest answers the plan gate in the neutral envelope
// (agent.AnswerNeutralPlan). ResolveControlResponse turns an approval into
// the gate's allow_once option and a refusal into its reject_once option, and
// it queues the feedback of a refusal as a message, so the answer itself
// states no follow-up. Every other request takes the base protocol's answer.
func (reasonixProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	if isPlanApproval(input.Request.Payload) {
		return agent.AnswerNeutralPlan(input)
	}
	return acp.AnswerBaseControlRequest(input, nil)
}
