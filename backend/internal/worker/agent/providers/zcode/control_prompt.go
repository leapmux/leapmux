package zcode

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ZCode's control requests as neutral prompts, and its answers.
//
// ZCode multiplexes three prompts over two RPCs, and the Worker records which
// one arrived as the stored request's tool name (control.go):
//
//   - AskUserQuestion is a question (interaction/requestUserInput).
//   - McpElicitation is an MCP server's form (interaction/requestUserInput).
//   - ExitPlanMode is the plan approval (interaction/requestUserInput).
//   - Any other tool is a permission (interaction/requestPermission).
//
// The answers ride the neutral envelope, and ResolveControlResponse turns them
// into the app-server's own reply.

// zcodeAllowOptionIDs are the allow-class option IDs that ZCode's backend
// emits, mirrored from its own fail-safe set. Every other ID answers deny,
// because granting a choice that the backend never offered is the one
// outcome that a permission answer must not have.
var zcodeAllowOptionIDs = map[string]bool{
	"allow":         true,
	"allow_once":    true,
	"allow_always":  true,
	"allow_project": true,
	"allowSession":  true,
}

// ReadControlRequest reads ZCode's control requests: LeapMux's
// workspace-trust question, and the two requests of the app-server, a
// permission and a user input. A user input is a question, an MCP form or the
// plan approval, and any other request reads as a permission.
//
// A request of another method reads as unrecognized: the app-server raises
// no other request, and the resolution builds no reply to one
// (zcodeReplyForAnswer).
func (zcodeProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	if prompt, ok := providerkit.ReadWorkspaceTrustPrompt(input.Payload, leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE); ok {
		return prompt
	}
	frame := providerkit.RawFields(input.Payload)
	method := providerkit.ObjectString(frame, "method")
	switch method {
	case contracts.ZCodeMethodRequestPermission, contracts.ZCodeMethodRequestUserInput:
	default:
		return agent.UnrecognizedControlPrompt()
	}
	request, _ := providerkit.RawObject(frame["request"])
	params := providerkit.RawFields(frame["params"])
	toolName := providerkit.ObjectString(request, "tool_name")
	if toolName == contracts.ZCodeToolNameAskUserQuestion {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
			Questions: zcodeQuestions(params, request["input"]),
		}}}
	}
	if method == contracts.ZCodeMethodRequestUserInput {
		if elicitation, ok := zcodeElicitationPrompt(params); ok {
			return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: elicitation}}
		}
	}
	if toolName == contracts.ZCodeToolNameExitPlanMode {
		planText := zcodeUserInputRequest{Input: params["input"], Context: params["context"]}.planText()
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{Text: planText}}}
	}
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: zcodePermissionPrompt(params, input.Payload, toolName)}}
}

// zcodeQuestionRecords reads the question records of a stored user-input
// request, in the order that the request declares. An element that is no
// object is dropped.
//
// The native request (`params`) comes first, because it keeps the option
// descriptions that the Worker's compact header leaves out: its schema's
// list, then its own, then its input's. The first non-empty list wins. An
// empty native list stays a real answer when no list has questions: the
// app-server that sent it declares no question, so it does not fall back to
// the header. The header (`request.input.questions`) is the last source, for
// a request that carries no native list at all.
func zcodeQuestionRecords(params map[string]json.RawMessage, requestInput json.RawMessage) []map[string]json.RawMessage {
	candidates := []json.RawMessage{
		providerkit.RawFields(params["schema"])["questions"],
		params["questions"],
		providerkit.RawFields(params["input"])["questions"],
	}
	var lists [][]json.RawMessage
	for _, candidate := range candidates {
		if list, ok := providerkit.RawArray(candidate); ok {
			lists = append(lists, list)
		}
	}
	var chosen []json.RawMessage
	switch {
	case len(lists) > 0:
		chosen = lists[0]
		for _, list := range lists {
			if len(list) > 0 {
				chosen = list
				break
			}
		}
	default:
		chosen, _ = providerkit.RawArray(providerkit.RawFields(requestInput)["questions"])
	}
	records := make([]map[string]json.RawMessage, 0, len(chosen))
	for _, element := range chosen {
		if record := providerkit.RawFields(element); record != nil {
			records = append(records, record)
		}
	}
	return records
}

// zcodeQuestions reads the questions of a stored user-input request. The
// answer is keyed by the question's text, so a record with neither a question
// nor a header could never be answered, and it is dropped. The header stands
// in for absent question text.
func zcodeQuestions(params map[string]json.RawMessage, requestInput json.RawMessage) []*leapmuxv1.ControlQuestion {
	records := zcodeQuestionRecords(params, requestInput)
	questions := make([]*leapmuxv1.ControlQuestion, 0, len(records))
	for _, record := range records {
		text := providerkit.ObjectString(record, "question")
		if text == "" {
			text = providerkit.ObjectString(record, "header")
		}
		if text == "" {
			continue
		}
		questions = append(questions, &leapmuxv1.ControlQuestion{
			Question:    text,
			Header:      providerkit.ObjectString(record, "header"),
			Options:     zcodeQuestionOptions(record),
			MultiSelect: providerkit.RawBool(record["multiSelect"]),
		})
	}
	return questions
}

// zcodeQuestionOptions reads the options of one question. ZCode's wire form
// sets an option's `value` to its own label, and the answer sends the label
// that the option shows, so either field stands in for the other. An option
// with neither has nothing to send, and it is dropped. A preview of only
// whitespace shows nothing, so it is left out.
func zcodeQuestionOptions(record map[string]json.RawMessage) []*leapmuxv1.QuestionOption {
	elements, ok := providerkit.RawArray(record["options"])
	if !ok {
		return nil
	}
	options := make([]*leapmuxv1.QuestionOption, 0, len(elements))
	for _, element := range elements {
		fields := providerkit.RawFields(element)
		if fields == nil {
			continue
		}
		label := providerkit.ObjectString(fields, "label")
		if label == "" {
			label = providerkit.ObjectString(fields, "value")
		}
		if label == "" {
			continue
		}
		option := &leapmuxv1.QuestionOption{Label: label, Description: providerkit.ObjectString(fields, "description")}
		if preview := providerkit.ObjectString(fields, "preview"); strings.TrimSpace(preview) != "" {
			option.Preview = preview
		}
		options = append(options, option)
	}
	return options
}

func zcodeElicitationSchema(primary, fallback json.RawMessage) json.RawMessage {
	if providerkit.RawPresent(primary) {
		return primary
	}
	if providerkit.RawPresent(fallback) {
		return fallback
	}
	return nil
}

// zcodeElicitationPrompt reads an MCP server's input form from a stored
// user-input request of the `mcp_elicitation` interaction. The request also
// flattens the form into questions, which lose each field's type, so the
// prompt states the server's own schema instead. The schema's own form wins
// over the input's, and the input's message over the request's prompt.
func zcodeElicitationPrompt(params map[string]json.RawMessage) (*leapmuxv1.ElicitationPrompt, bool) {
	schema := providerkit.RawFields(params["schema"])
	if providerkit.ObjectString(schema, "interaction") != contracts.ZCodeInteractionMcpElicitation {
		return nil, false
	}
	input := providerkit.RawFields(params["input"])
	prompt := &leapmuxv1.ElicitationPrompt{
		Mode:    contracts.MCPElicitationModeForm,
		Message: providerkit.ObjectString(input, "message"),
		Server:  providerkit.ObjectString(schema, "serverName"),
	}
	if prompt.Message == "" {
		prompt.Message = providerkit.ObjectString(params, "prompt")
	}
	if prompt.Server == "" {
		prompt.Server = providerkit.ObjectString(input, "serverName")
	}
	if requested := zcodeElicitationSchema(schema["requestedSchema"], input["requestedSchema"]); requested != nil {
		prompt.SchemaJson = string(requested)
	}
	return prompt, true
}

// zcodePermissionPrompt reads a permission request: the tool, ZCode's own
// reason, the tool's input, the command of a shell call, and the options
// that the app-server offers. An empty option list gets LeapMux's allow and
// deny, and a list with no refusal gets LeapMux's deny.
func zcodePermissionPrompt(params map[string]json.RawMessage, payload []byte, toolName string) *leapmuxv1.PermissionPrompt {
	toolInput := agent.ControlToolInput(payload)
	permission := &leapmuxv1.PermissionPrompt{
		Title:     toolName,
		Reason:    providerkit.ObjectString(params, "reason"),
		InputJson: string(toolInput),
		Options: agent.WithRefusal(zcodePermissionOptions(params), &leapmuxv1.PermissionOption{
			OptionId: contracts.ControlAnswerOptionDeny,
			Kind:     leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE,
			Name:     "Deny",
		}),
	}
	if toolName == contracts.ZCodeToolNameBash {
		permission.Command = providerkit.ObjectString(providerkit.RawFields(toolInput), "command")
	}
	return permission
}

// zcodePermissionOptions reads the options of a permission request, in the
// order that the app-server sent them. An option with no ID cannot be
// answered, so it is dropped. The app-server embeds the complete reply of
// each option, so the ID is what the answer echoes.
func zcodePermissionOptions(params map[string]json.RawMessage) []*leapmuxv1.PermissionOption {
	elements, ok := providerkit.RawArray(params["options"])
	if !ok {
		return nil
	}
	var options []*leapmuxv1.PermissionOption
	for _, element := range elements {
		fields := providerkit.RawFields(element)
		id := providerkit.ObjectString(fields, "optionId")
		if id == "" {
			continue
		}
		native := providerkit.ObjectString(fields, "kind")
		option := &leapmuxv1.PermissionOption{OptionId: id, Kind: zcodeOptionKind(native), Name: providerkit.ObjectString(fields, "name")}
		if option.Kind == leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_OTHER && option.Name == "" {
			option.Name = native
			if option.Name == "" {
				option.Name = id
			}
		}
		options = append(options, option)
	}
	return options
}

// zcodeOptionKind maps a native option kind onto the neutral kinds. A kind
// that matches no neutral kind is OTHER, so the client keeps the option apart
// instead of guessing a slot for it.
func zcodeOptionKind(kind string) leapmuxv1.PermissionOptionKind {
	switch kind {
	case "allow_once":
		return leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE
	case "allow_always", "allow_project", "allow_session":
		return leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS
	case "deny", "deny_once", "reject_once":
		return leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE
	case "reject_always":
		return leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ALWAYS
	default:
		return leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_OTHER
	}
}

// AnswerControlRequest builds ZCode's answer in the neutral envelope, which
// ResolveControlResponse turns into the app-server's own reply.
func (zcodeProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Question:
		return providerkit.AskUserQuestionAnswers(input, agent.ControlToolInput(input.Request.Payload))
	case *leapmuxv1.ControlPrompt_Elicitation:
		return agent.AnswerElicitation(input, nil)
	case *leapmuxv1.ControlPrompt_Plan:
		return agent.AnswerNeutralPlan(input)
	case *leapmuxv1.ControlPrompt_Permission:
		if providerkit.IsWorkspaceTrustQuestion(input.Request.Payload) {
			return providerkit.AnswerWorkspaceTrust(input)
		}
		return zcodePermissionAnswer(input)
	default:
		return agent.ControlAnswer{}, agent.InvalidControlDecision("ZCode states no %T prompt", input.Prompt.GetKind())
	}
}

// zcodePermissionAnswer answers a permission:
//
//   - LeapMux's allow and deny, for a request that offers no options, take
//     the neutral allow with the tool's input, and the neutral deny.
//   - LeapMux's deny beside the app-server's options is the composer's
//     typed refusal: a deny with the user's feedback, or with the
//     placeholder message.
//   - An option of the app-server goes back as the choice, beside the
//     behavior that its ID allows. A refusal's feedback rides beside it as
//     the message, because the reply takes the chosen option's own response
//     and adds the reason.
func zcodePermissionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, option, err := agent.PermissionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	feedback := decision.GetFeedback()
	if len(zcodePermissionOptions(providerkit.RawFields(providerkit.RawFields(input.Request.Payload)["params"]))) == 0 {
		return agent.AnswerAllowDeny(input)
	}
	if option.GetOptionId() == contracts.ControlAnswerOptionDeny {
		return agent.ControlAnswer{Content: agent.DenyResponse(input.RequestID, feedback)}, nil
	}
	if feedback != "" {
		return agent.ControlAnswer{Content: agent.ControlResponseEnvelope(input.RequestID, agent.ControlBehaviorResponse{
			Behavior: contracts.ControlAnswerBehaviorDeny,
			Message:  feedback,
			Choice:   option.GetOptionId(),
		})}, nil
	}
	behavior := contracts.ControlAnswerBehaviorDeny
	if zcodeAllowOptionIDs[option.GetOptionId()] {
		behavior = contracts.ControlAnswerBehaviorAllow
	}
	return agent.ControlAnswer{Content: agent.ControlResponseEnvelope(input.RequestID, agent.ControlBehaviorResponse{
		Behavior: behavior,
		Choice:   option.GetOptionId(),
	})}, nil
}
