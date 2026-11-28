package gemini

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func geminiOffersPlanOptions(payload []byte) bool {
	var hasAllow, hasDeny bool
	for _, option := range acp.PermissionOptions(payload) {
		switch option.GetOptionId() {
		case "proceed_once":
			hasAllow = true
		case "cancel":
			hasDeny = true
		}
	}
	return hasAllow && hasDeny
}

// ReadControlRequest reads Gemini CLI's plan approval as a plan. Gemini asks
// for it as a permission for its exit_plan_mode call, whose title states the
// path of the plan file. Every other request is the base protocol's.
//
// geminiPlanPermission recognizes the plan approval here, as it does for
// PublishControlRequest and ResolveControlResponse, so the prompt, the source
// link and the answer agree about which request is a plan.
func (geminiProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	callID, planPath, _, plan := geminiPlanPermission(input.Payload)
	if !plan || !geminiOffersPlanOptions(input.Payload) {
		return acp.ReadBaseControlRequest(input)
	}
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{
		Text: geminiPlanSourceText(input, callID, planPath),
	}}}
}

// geminiPlanSourceText reads the plan that PublishControlRequest stored on the
// request's source row: the exit_plan_mode call itself, whose supplement holds
// the plan file's path and content. It returns "" unless the row and its
// supplement both belong to this call and this path, so a plan of another
// call or another file never reaches the prompt.
func geminiPlanSourceText(input agent.ControlRequestInput, callID, planPath string) string {
	source, ok := input.Source()
	if !ok || !geminiPlanSourceMatches(source.Original, callID, planPath) {
		return ""
	}
	supplement, ok := providerkit.RawObject(source.Supplemental)
	if !ok {
		return ""
	}
	path, _ := providerkit.RawString(supplement[contracts.GeminiSupplementPlanPath])
	id, _ := providerkit.RawString(supplement[contracts.ACPSupplementIdentityToolCallID])
	if path != planPath || id != callID {
		return ""
	}
	text, _ := providerkit.RawString(supplement[contracts.GeminiSupplementPlanContent])
	return text
}

// AnswerControlRequest answers the plan approval in the neutral envelope
// (agent.AnswerNeutralPlan). ResolveControlResponse turns an approval
// into Gemini's proceed_once option and a refusal into its cancel option, and
// it queues the feedback of a refusal as a message, so the answer itself
// states no follow-up. Every other request takes the base protocol's answer.
func (geminiProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	if _, _, _, plan := geminiPlanPermission(input.Request.Payload); plan && geminiOffersPlanOptions(input.Request.Payload) {
		return agent.AnswerNeutralPlan(input)
	}
	return acp.AnswerBaseControlRequest(input, nil)
}
