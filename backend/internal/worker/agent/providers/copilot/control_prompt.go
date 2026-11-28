package copilot

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Copilot's control requests as neutral prompts, and its answers.
//
// Copilot states each request as a native session event, and its permission
// request carries no option list of its own. The reader states the decisions
// that the runtime accepts for the request, and each option ID is the approval
// scope that the answer carries. ResolveControlResponse (control_response.go)
// turns the answer into the runtime's own value.

// The IDs of the permission options that LeapMux offers for Copilot. The three
// allow options are the approval scopes that the answer carries, and the
// reject option refuses.
const (
	copilotControlOptionOnce    = contracts.CopilotApprovalScopeOnce
	copilotControlOptionSession = contracts.CopilotApprovalScopeSession
	copilotControlOptionProject = contracts.CopilotApprovalScopeProject
	copilotControlOptionReject  = "reject"
)

// copilotAutoSwitchRateLimitCode is the error code of an automatic model
// switch that the current model's rate limit caused.
const copilotAutoSwitchRateLimitCode = "user_model_rate_limited"

// ReadControlRequest reads Copilot's control requests:
//
//   - user_input.requested is a question.
//   - elicitation.requested is an MCP form.
//   - LeapMux's workspace-trust question is a permission.
//   - exit_plan_mode.requested is a plan, which carries its own text.
//   - auto_mode_switch.requested, sampling.requested and
//     permission.requested are permissions with LeapMux's options.
//
// Any other payload reads as unrecognized: the runtime raises no other
// request, and SendRawInput reaches no request of the runtime with an answer
// to one.
func (copilotProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	eventType, data, _ := copilotControlEvent(input.Payload)
	if eventType == "" {
		if prompt, ok := providerkit.ReadWorkspaceTrustPrompt(input.Payload, leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT); ok {
			return prompt
		}
		return agent.UnrecognizedControlPrompt()
	}
	fields := providerkit.RawFields(data)
	switch eventType {
	case contracts.CopilotEventUserInputRequested:
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
			Questions: []*leapmuxv1.ControlQuestion{copilotQuestion(fields)},
		}}}
	case contracts.CopilotEventElicitationRequested:
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: copilotElicitationPrompt(fields)}}
	case contracts.CopilotEventExitPlanModeRequested:
		// Copilot sends the whole plan in its approval request, where other
		// providers identify a plan that the transcript already holds.
		text := providerkit.ObjectString(fields, "planContent")
		if text == "" {
			text = providerkit.ObjectString(fields, "summary")
		}
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{Text: text}}}
	default:
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: copilotPermissionPrompt(eventType, fields)}}
	}
}

// copilotQuestion reads the one question of a user_input.requested. The
// runtime states its choices as plain strings, so each choice is a label and
// sends itself. `allowFreeform` states whether the runtime takes an answer
// that is none of them:
//
//   - The empty answer is the shortest such answer, so the question permits
//     it exactly where the runtime takes one.
//   - `allowFreeform: false` beside choices takes a choice alone, so the
//     question refuses typed text. With no choice, typed text stays the only
//     way to answer, and the runtime decides.
func copilotQuestion(fields map[string]json.RawMessage) *leapmuxv1.ControlQuestion {
	choices := providerkit.RawStrings(fields["choices"])
	options := make([]*leapmuxv1.QuestionOption, 0, len(choices))
	for _, label := range choices {
		options = append(options, &leapmuxv1.QuestionOption{Label: label})
	}
	question := &leapmuxv1.ControlQuestion{
		Question:   providerkit.ObjectString(fields, "question"),
		Options:    options,
		AllowEmpty: providerkit.RawBool(fields["allowFreeform"]),
	}
	if freeform := providerkit.RawOptionalBool(fields["allowFreeform"]); freeform != nil && !*freeform && len(options) > 0 {
		question.AllowCustomText = freeform
	}
	return question
}

// copilotElicitationPrompt reads the MCP form of an elicitation.requested.
// The mode defaults to a form, and the server is the elicitation's source.
func copilotElicitationPrompt(fields map[string]json.RawMessage) *leapmuxv1.ElicitationPrompt {
	prompt := &leapmuxv1.ElicitationPrompt{
		Mode:    copilotElicitationMode(fields),
		Message: providerkit.ObjectString(fields, "message"),
		Server:  providerkit.ObjectString(fields, "elicitationSource"),
		Url:     providerkit.ObjectString(fields, "url"),
	}
	if schema := fields["requestedSchema"]; providerkit.RawPresent(schema) {
		prompt.SchemaJson = string(schema)
	}
	return prompt
}

// copilotPermissionPrompt reads a request that a permission answers: the
// automatic model switch, an MCP server's sampling request, or a tool's
// permission request.
func copilotPermissionPrompt(eventType string, fields map[string]json.RawMessage) *leapmuxv1.PermissionPrompt {
	switch eventType {
	case contracts.CopilotEventAutoModeSwitchRequested:
		permission := &leapmuxv1.PermissionPrompt{
			Title: "Copilot asks to switch to Auto",
			Options: []*leapmuxv1.PermissionOption{
				{OptionId: copilotControlOptionOnce, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Switch this turn"},
				{OptionId: copilotControlOptionSession, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "Always switch"},
				{OptionId: copilotControlOptionReject, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Decline"},
			},
			// Each answer is its own decision, and no permission preset applies.
			IndependentOptions: true,
		}
		if providerkit.ObjectString(fields, "errorCode") == copilotAutoSwitchRateLimitCode {
			permission.Reason = "The current model reached its rate limit."
		}
		return permission
	case contracts.CopilotEventSamplingRequested:
		title := "The MCP server asks the model to answer"
		if server := providerkit.ObjectString(fields, "serverName"); server != "" {
			title = "The MCP server " + server + " asks the model to answer"
		}
		return &leapmuxv1.PermissionPrompt{
			Title:     title,
			InputJson: string(providerkit.RawObjectOrEmpty(fields["request"])),
			Options: []*leapmuxv1.PermissionOption{
				{OptionId: copilotControlOptionOnce, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow"},
				{OptionId: copilotControlOptionReject, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Deny"},
			},
		}
	}
	var request map[string]json.RawMessage
	if eventType == contracts.CopilotEventPermissionRequested {
		request, _ = providerkit.RawObject(fields["permissionRequest"])
	}
	kind := providerkit.ObjectString(request, "kind")
	permission := &leapmuxv1.PermissionPrompt{Title: kind}
	for _, key := range []string{"intention", "toolTitle", "toolName"} {
		if title := providerkit.ObjectString(request, key); title != "" {
			permission.Title = title
			break
		}
	}
	if request != nil {
		permission.InputJson = string(fields["permissionRequest"])
	}
	if kind == "shell" {
		permission.Command = providerkit.ObjectString(request, "fullCommandText")
	}
	options := []*leapmuxv1.PermissionOption{
		{OptionId: copilotControlOptionOnce, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow once"},
	}
	// The two wider scopes need the same approval rule, so a request that can
	// express one can express the other. The project scope outlives the
	// session: the runtime stores it against the working directory's own
	// location key.
	if copilotSessionApproval(readCopilotPermissionRequest(request)) != nil {
		options = append(options,
			&leapmuxv1.PermissionOption{OptionId: copilotControlOptionSession, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "Allow for this session"},
			&leapmuxv1.PermissionOption{OptionId: copilotControlOptionProject, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "Allow for this project"},
		)
	}
	permission.Options = append(options, &leapmuxv1.PermissionOption{
		OptionId: copilotControlOptionReject, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Reject",
	})
	return permission
}

// AnswerControlRequest builds Copilot's answer in the neutral envelope, which
// ResolveControlResponse turns into the runtime's own value.
func (copilotProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Question:
		return copilotQuestionAnswer(input)
	case *leapmuxv1.ControlPrompt_Elicitation:
		return agent.AnswerElicitation(input, nil)
	case *leapmuxv1.ControlPrompt_Plan:
		return agent.AnswerNeutralPlan(input)
	case *leapmuxv1.ControlPrompt_Permission:
		if providerkit.IsWorkspaceTrustQuestion(input.Request.Payload) {
			return providerkit.AnswerWorkspaceTrust(input)
		}
		return copilotPermissionAnswer(input)
	default:
		return agent.ControlAnswer{}, agent.InvalidControlDecision("Copilot states no %T prompt", input.Prompt.GetKind())
	}
}

// copilotPermissionAnswer answers a permission. An approval carries its
// option ID as the approval scope. A refusal denies with the user's feedback,
// which the runtime receives beside its reject decision, or with the
// placeholder message when the user typed none (agent.DenyResponse).
func copilotPermissionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, option, err := agent.PermissionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if !agent.IsAllowOption(option) {
		return agent.ControlAnswer{Content: agent.DenyResponse(input.RequestID, decision.GetFeedback())}, nil
	}
	return agent.ControlAnswer{Content: agent.ControlResponseEnvelope(input.RequestID, map[string]any{
		"behavior": contracts.ControlAnswerBehaviorAllow,
		"scope":    option.GetOptionId(),
	})}, nil
}

// copilotQuestionAnswer answers the one question of a user_input.requested.
// The runtime takes one answer string, and `wasFreeform` tells it whether
// the user typed any of it. The string is the answer values
// (agent.QuestionAnswerValues) joined with ", ": the selected choice, or
// else the trimmed typed text, which may be the explicit empty answer. A
// multiple-choice answer would list its typed text after its choices and
// count as typed, but no Copilot question is multiple-choice. A cancelled
// decision denies with its feedback, which the runtime receives as the
// answer that the user typed in place of one.
func copilotQuestionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, questions, err := agent.QuestionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if decision.GetCancelled() {
		return agent.ControlAnswer{Content: agent.DenyResponse(input.RequestID, decision.GetFeedback())}, nil
	}
	if len(questions) == 0 {
		return agent.ControlAnswer{}, agent.InvalidControlDecision("the request carries no question to answer")
	}
	answer := agent.QuestionAnswerAt(decision, 0)
	values := agent.QuestionAnswerValues(questions[0], answer)
	return agent.ControlAnswer{Content: agent.ControlResponseEnvelope(input.RequestID, map[string]any{
		"behavior":    contracts.ControlAnswerBehaviorAllow,
		"answer":      strings.Join(values, ", "),
		"wasFreeform": len(values) > len(answer.GetSelected()) || len(values) == 0,
	})}, nil
}
