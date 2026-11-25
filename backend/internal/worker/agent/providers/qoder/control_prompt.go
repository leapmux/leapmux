package qoder

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The permission options that LeapMux offers for Qoder CLI. Qoder's answer
// accepts a scope on an allow: `permissionScope: "session"` keeps the rule
// for the session, and `"persist"` saves it to the project. LeapMux offers the
// session tier alone, so nothing outlives the session that granted it. The
// chosen id rides the neutral envelope as its choice, and
// ResolveControlResponse turns `session` into Qoder's `permissionScope`
// (scopeChoiceOf).
const (
	qoderOptionOnce    = "once"
	qoderOptionSession = "session"
	qoderOptionDeny    = "deny"
)

// qoderPermissionOptions returns the options of a Qoder permission prompt, in
// the order that the decision row lays them out.
func qoderPermissionOptions() []*leapmuxv1.PermissionOption {
	return []*leapmuxv1.PermissionOption{
		{OptionId: qoderOptionOnce, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow once"},
		{OptionId: qoderOptionSession, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "Allow for this session"},
		{OptionId: qoderOptionDeny, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Deny"},
	}
}

// ReadControlRequest reads Qoder CLI's control requests, in this order:
//
//   - An AskUserQuestion request is a question.
//   - An `elicitation` request is a form that an MCP server asks for.
//   - ExitPlanMode is a plan, and any other tool is a permission with Qoder's
//     scope options.
func (qoderProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	if question, ok := providerkit.ClaudeShapedQuestionPrompt(input.Payload); ok {
		return question
	}
	if elicitation, ok := qoderElicitation(input.Payload); ok {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: elicitation}}
	}
	return providerkit.ClaudeShapedToolPrompt(input.Payload, qoderPermissionOptions())
}

// qoderElicitation reads an MCP elicitation that Qoder relays on its control
// channel. It reports false for a request of another subtype.
//
// Each field reads as absent when it is not text. A mode that is text, even
// an empty one, stays as Qoder sent it; an absent mode is a form. The server
// is the display name when Qoder states one as text, even an empty one, and
// the server's own name otherwise.
func qoderElicitation(payload []byte) (*leapmuxv1.ElicitationPrompt, bool) {
	var envelope struct {
		Request json.RawMessage `json:"request"`
	}
	var request map[string]json.RawMessage
	if json.Unmarshal(payload, &envelope) != nil || json.Unmarshal(envelope.Request, &request) != nil {
		return nil, false
	}
	if subtype, _ := providerkit.RawString(request["subtype"]); subtype != contracts.QoderControlRequestSubtypeElicitation {
		return nil, false
	}
	text := func(key string) string {
		value, _ := providerkit.RawString(request[key])
		return value
	}
	mode, ok := providerkit.RawString(request["mode"])
	if !ok {
		mode = contracts.MCPElicitationModeForm
	}
	server, ok := providerkit.RawString(request["display_name"])
	if !ok {
		server = text("mcp_server_name")
	}
	prompt := &leapmuxv1.ElicitationPrompt{
		Mode:        mode,
		Message:     text("message"),
		Server:      server,
		Url:         text("url"),
		Title:       text("title"),
		Description: text("description"),
	}
	if schema := request["requested_schema"]; providerkit.RawPresent(schema) {
		prompt.SchemaJson = string(schema)
	}
	return prompt, true
}

// AnswerControlRequest builds Qoder CLI's answer in the neutral envelope,
// which ResolveControlResponse turns into Qoder's own decision object:
//
//   - An elicitation answers with its action and the filled form.
//   - A question allows with the tool input and the answers beside it.
//   - A plan approval allows with the tool input, and a refusal denies with
//     the user's feedback.
//   - A permission answers with the chosen option (qoderPermissionAnswer).
func (qoderProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Elicitation:
		return agent.AnswerElicitation(input, nil)
	case *leapmuxv1.ControlPrompt_Question:
		return providerkit.AskUserQuestionAnswers(input, agent.ControlToolInput(input.Request.Payload))
	case *leapmuxv1.ControlPrompt_Plan:
		return agent.AnswerNeutralPlan(input)
	case *leapmuxv1.ControlPrompt_Permission:
		return qoderPermissionAnswer(input)
	default:
		return agent.ControlAnswer{}, agent.InvalidControlDecision("Qoder CLI states no %T prompt", input.Prompt.GetKind())
	}
}

// qoderPermissionAnswer answers a permission prompt:
//
//   - A refusal with the user's feedback denies with it, and states no
//     choice.
//   - Any other decision states its behavior and rides its option id as the
//     choice: once and session allow, and deny denies with no message.
//     ResolveControlResponse turns the session choice into Qoder's
//     `permissionScope`.
//
// Feedback beside an allowing option is refused, because an allow has no
// field for it.
func qoderPermissionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, option, err := agent.PermissionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if feedback := decision.GetFeedback(); feedback != "" {
		return agent.ControlAnswer{Content: agent.DenyResponse(input.RequestID, feedback)}, nil
	}
	behavior := contracts.ControlAnswerBehaviorDeny
	if agent.IsAllowOption(option) {
		behavior = contracts.ControlAnswerBehaviorAllow
	}
	return agent.ControlAnswer{Content: agent.ControlResponseEnvelope(input.RequestID, agent.ControlBehaviorResponse{
		Behavior: behavior,
		Choice:   option.GetOptionId(),
	})}, nil
}
