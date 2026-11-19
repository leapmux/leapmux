package opencode

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The option ids that the family's daemon takes back for a permission request
// that offers no option list. They are the daemon's real ids: the daemon reads
// an unknown id as a refusal, so an allow with LeapMux's own id would refuse
// the call.
const (
	openCodeOptionOnce   = "once"
	openCodeOptionReject = "reject"
)

// openCodeDefaultPermissionOptions are the answers to a permission request
// that offers no option list: the daemon's own allow once and reject.
func openCodeDefaultPermissionOptions() []*leapmuxv1.PermissionOption {
	return []*leapmuxv1.PermissionOption{
		{OptionId: openCodeOptionOnce, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow"},
		{OptionId: openCodeOptionReject, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Deny"},
	}
}

// ReadControlRequest reads the control requests of the family's Agent Client
// Protocol transport, which Kilo speaks. OpenCode speaks its native protocol
// instead, and opencodeProvider reads its requests (control_prompt.go).
//
// The order decides a request that two readers could claim:
//
//  1. A stored `question.asked` event is a question. The daemon raises it on
//     its own event stream, and the question bridge publishes it.
//  2. An MCP elicitation is a form.
//  3. A permission request of the Agent Client Protocol is a permission. A
//     request that offers no option list gets the daemon's own allow once and
//     reject, and a list with no reject option gains acp.CancelOptionID.
//  4. Any other request is the generic permission.
func (FamilyProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	if providerkit.IsOpenCodeQuestionEvent(input.Payload) {
		return providerkit.OpenCodeQuestionPrompt(input.Payload)
	}
	if elicitation, ok := acp.ReadElicitation(input.Payload); ok {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: elicitation}}
	}
	permission := acp.ReadPermissionPrompt(input)
	if permission == nil {
		return agent.GenericPermissionPrompt()
	}
	if len(acp.PermissionOptions(input.Payload)) == 0 {
		permission.Options = openCodeDefaultPermissionOptions()
	}
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: permission}}
}

// AnswerControlRequest answers the control requests of the family's Agent
// Client Protocol transport.
//
// A question takes the result that the question bridge reads back
// (openCodeAnswerResult). Every other request takes the answer of the Agent
// Client Protocol base:
//
//   - An option of a permission is selected as it is, and the reason of a
//     refusal follows the answer as a message, because an option carries no
//     text.
//   - LeapMux's allow and deny of the generic prompt take the neutral
//     envelope.
//   - An elicitation takes the neutral elicitation envelope.
func (FamilyProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	if _, ok := input.Prompt.GetKind().(*leapmuxv1.ControlPrompt_Question); ok {
		return providerkit.AnswerOpenCodeQuestions(input)
	}
	return acp.AnswerBaseControlRequest(input, nil)
}
