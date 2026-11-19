package mimo

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// mimoQuestionKeyMCPElicitation is the `key` of the one question that MiMo
// asks for an MCP server's confirmation (MiMo 0.1.17, mcp/elicitation.ts).
const mimoQuestionKeyMCPElicitation = "mcp_elicitation"

// mimoPermissionBashDelete is the permission that MiMo always asks for,
// whatever its rules say (FORCED_ASK in MiMo's permission/index.ts). MiMo
// reads an `always` answer to it as `once`, so it saves nothing.
const mimoPermissionBashDelete = "bash_delete"

// mimoElicitationSchema is the schema of the form that answers an MCP
// server's confirmation: an object with no field. MiMo refuses every
// elicitation that asks for a value, so the reader only accepts, declines or
// cancels.
const mimoElicitationSchema = `{"type":"object","properties":{}}`

// mimoPermissionOptions are the answers that a permission request offers, in
// the order of MiMo's own prompt. The ids are MiMo's reply words.
//
// `always` approves every later call that matches the request's `always`
// patterns, for as long as the server runs. MiMo saves those patterns, so a
// request with no pattern saves nothing, and MiMo reads `always` as `once`
// for a permission that it always asks for. MiMo's own prompt hides the
// answer in both cases, so the caller passes offersAlways false for them.
func mimoPermissionOptions(offersAlways bool) []*leapmuxv1.PermissionOption {
	options := []*leapmuxv1.PermissionOption{
		{OptionId: contracts.MiMoPermissionReplyOnce, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow once"},
	}
	if offersAlways {
		options = append(options, &leapmuxv1.PermissionOption{
			OptionId: contracts.MiMoPermissionReplyAlways, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "Always allow",
		})
	}
	return append(options, &leapmuxv1.PermissionOption{
		OptionId: contracts.MiMoPermissionReplyReject, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Reject",
	})
}

// storedControlPayload is what the readers read of a payload that
// controlPayload stored. Each field is read on its own, and a value of the
// wrong type reads as absent, so a malformed field hides no other field.
type storedControlPayload struct {
	eventType  string
	toolName   string
	plan       string
	properties map[string]json.RawMessage
}

// readStoredControlPayload reads the fields of a stored payload that the
// readers need. A payload that is no JSON object reads as empty fields.
func readStoredControlPayload(payload []byte) storedControlPayload {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(payload, &fields)
	stored := storedControlPayload{toolName: agent.ControlToolName(payload)}
	stored.eventType, _ = providerkit.RawString(fields["type"])
	stored.plan, _ = providerkit.RawString(fields[contracts.MiMoControlFieldPlan])
	_ = json.Unmarshal(fields["properties"], &stored.properties)
	return stored
}

// questions returns the elements of properties.questions, or nil for a
// value that is no array.
func (s storedControlPayload) questions() []json.RawMessage {
	var questions []json.RawMessage
	_ = json.Unmarshal(s.properties["questions"], &questions)
	return questions
}

// ReadControlRequest reads MiMo's control requests. The order decides a
// request that two readers could claim:
//
//  1. A `question.asked` that is no plan approval and no MCP server's
//     confirmation is a question, which the OpenCode question protocol reads.
//  2. An MCP server's confirmation is an empty form.
//  3. A `question.asked` that the Worker recorded under the plan tool's name
//     is the plan approval.
//  4. A `permission.asked` is a permission with MiMo's own answers.
//  5. Any other request is the generic permission.
func (mimoProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	stored := readStoredControlPayload(input.Payload)
	elicitation := mimoElicitation(stored)
	isQuestionEvent := stored.eventType == contracts.OpenCodeEventQuestionAsked
	switch {
	case isQuestionEvent && stored.toolName != contracts.MiMoToolPlanExit && elicitation == nil:
		return providerkit.OpenCodeQuestionPrompt(input.Payload)
	case elicitation != nil:
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: elicitation}}
	case isQuestionEvent && stored.toolName == contracts.MiMoToolPlanExit:
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: mimoPlanPrompt(stored)}}
	case stored.eventType == contracts.MiMoEventPermissionAsked:
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: mimoPermissionPrompt(stored)}}
	default:
		return agent.GenericPermissionPrompt()
	}
}

// mimoElicitation reads an MCP server's confirmation, which MiMo asks as ONE
// question keyed mcp_elicitation: the server as the header, and the server,
// the message and a subtitle joined by blank lines as the question. It
// returns nil for any other request.
//
// The question card would offer typed text, which MiMo reads as Cancel, so
// the confirmation is a form instead. The form states the server on its own
// line, so the message leaves out the server in front of it.
func mimoElicitation(stored storedControlPayload) *leapmuxv1.ElicitationPrompt {
	if stored.eventType != contracts.OpenCodeEventQuestionAsked {
		return nil
	}
	questions := stored.questions()
	if len(questions) != 1 {
		return nil
	}
	var question map[string]json.RawMessage
	if json.Unmarshal(questions[0], &question) != nil || question == nil {
		return nil
	}
	if key, _ := providerkit.RawString(question["key"]); key != mimoQuestionKeyMCPElicitation {
		return nil
	}
	server, _ := providerkit.RawString(question["header"])
	message, _ := providerkit.RawString(question["question"])
	if server != "" {
		message = strings.TrimPrefix(message, server+"\n\n")
	}
	return &leapmuxv1.ElicitationPrompt{
		Mode:       contracts.MCPElicitationModeForm,
		Server:     server,
		Message:    message,
		SchemaJson: mimoElicitationSchema,
	}
}

// mimoPlanPrompt reads a plan approval. The Worker states the text of the
// plan file beside the event when it could read the file. When it could not,
// the prompt states where the plan is instead: the plan file that MiMo
// gives, else MiMo's own question.
func mimoPlanPrompt(stored storedControlPayload) *leapmuxv1.PlanPrompt {
	if stored.plan != "" {
		return &leapmuxv1.PlanPrompt{Text: stored.plan}
	}
	var question, params map[string]json.RawMessage
	if questions := stored.questions(); len(questions) > 0 {
		_ = json.Unmarshal(questions[0], &question)
	}
	_ = json.Unmarshal(question["params"], &params)
	if path, _ := providerkit.RawString(params["plan"]); strings.TrimSpace(path) != "" {
		return &leapmuxv1.PlanPrompt{Details: []string{"Plan file: " + strings.TrimSpace(path)}}
	}
	if words, _ := providerkit.RawString(question["question"]); strings.TrimSpace(words) != "" {
		return &leapmuxv1.PlanPrompt{Details: []string{strings.TrimSpace(words)}}
	}
	return &leapmuxv1.PlanPrompt{}
}

// mimoPermissionPrompt reads a permission request.
//
// A shell permission states its command: the command in the metadata, else
// the request's patterns, one to a line. Its patterns ARE the command, so the
// arguments leave them out. The arguments state, in this order:
//
//   - the patterns, of a permission that is no shell command
//   - the `always` patterns, which Always allow would approve from now on, of
//     a request that offers it
//   - each field of the metadata
func mimoPermissionPrompt(stored storedControlPayload) *leapmuxv1.PermissionPrompt {
	permission, _ := providerkit.RawString(stored.properties["permission"])
	patterns := providerkit.RawStrings(stored.properties["patterns"])
	always := providerkit.RawStrings(stored.properties["always"])
	metadata := providerkit.ReadOrderedObject(stored.properties["metadata"])

	var command string
	if permission == contracts.MiMoToolBash || permission == mimoPermissionBashDelete {
		command, _ = providerkit.RawString(metadata.Get("command"))
		if command == "" {
			command = strings.Join(patterns, "\n")
		}
	}
	offersAlways := permission != mimoPermissionBashDelete && slices.ContainsFunc(always, func(pattern string) bool { return pattern != "" })

	var arguments providerkit.OrderedObject
	if command == "" && len(patterns) > 0 {
		arguments.Set("patterns", providerkit.JSONText(patterns))
	}
	if offersAlways {
		arguments.Set("always", providerkit.JSONText(always))
	}
	arguments.Merge(metadata)
	return &leapmuxv1.PermissionPrompt{
		Title:     permission,
		Command:   command,
		InputJson: string(arguments.Encode()),
		Options:   mimoPermissionOptions(offersAlways),
	}
}

// AnswerControlRequest builds MiMo's answer in the shape that
// ResolveControlResponse checks and SendRawInput sends (see parseControlAnswer):
//
//   - A question takes the OpenCode question protocol's result.
//   - An MCP server's confirmation takes the neutral elicitation envelope,
//     whose action the Worker turns into MiMo's own answer word.
//   - A plan approval takes the neutral allow or deny, and a refusal carries
//     the user's feedback, which MiMo reads as the plan's revision.
//   - A permission takes the selected option.
func (mimoProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Question:
		return providerkit.AnswerOpenCodeQuestions(input)
	case *leapmuxv1.ControlPrompt_Elicitation:
		return agent.AnswerElicitation(input, nil)
	case *leapmuxv1.ControlPrompt_Plan:
		return agent.AnswerNeutralPlan(input)
	case *leapmuxv1.ControlPrompt_Permission:
		return mimoPermissionAnswer(input)
	default:
		return agent.ControlAnswer{}, agent.InvalidControlDecision("MiMo Code states no %T prompt", input.Prompt.GetKind())
	}
}

// mimoPermissionAnswer answers a permission prompt:
//
//   - A refusal with the user's feedback denies with it, in the neutral
//     envelope, and the Worker sends MiMo a rejection with that message. An
//     option carries no text, so this is the only answer that carries it.
//   - One of MiMo's reply words is selected as it is.
//   - LeapMux's allow and deny, which the generic prompt offers, take the
//     neutral envelope.
func mimoPermissionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, option, err := agent.PermissionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if feedback := decision.GetFeedback(); feedback != "" {
		if agent.IsAllowOption(option) {
			return agent.ControlAnswer{}, agent.InvalidControlDecision("feedback goes with a refusal, and %q allows the call", option.GetOptionId())
		}
		return agent.ControlAnswer{Content: agent.DenyResponse(input.RequestID, feedback)}, nil
	}
	switch option.GetOptionId() {
	case contracts.MiMoPermissionReplyOnce, contracts.MiMoPermissionReplyAlways, contracts.MiMoPermissionReplyReject:
		return agent.ControlAnswer{Content: agent.SelectedOptionResult(input.RequestID, option.GetOptionId())}, nil
	default:
		return agent.AnswerAllowDeny(input)
	}
}
