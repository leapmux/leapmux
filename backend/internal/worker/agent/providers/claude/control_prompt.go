package claude

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Claude Code's shell tools, whose permission prompts state a command.
const (
	toolNameBash       = "Bash"
	toolNamePowerShell = "PowerShell"
)

// The permission options that LeapMux offers for Claude Code. Its control
// protocol states no option list, but its answer accepts a remembered grant:
// the Worker attaches `updatedPermissions` with a session destination, and the
// CLI keeps the rule for the session. Only the session tier is offered,
// because a saved rule file would outlive the private session that granted it.
const (
	claudeOptionOnce    = "once"
	claudeOptionSession = "session"
	claudeOptionDeny    = "deny"
)

func claudePermissionOptions() []*leapmuxv1.PermissionOption {
	return []*leapmuxv1.PermissionOption{
		{OptionId: claudeOptionOnce, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ONCE, Name: "Allow once"},
		{OptionId: claudeOptionSession, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_ALLOW_ALWAYS, Name: "Allow for this session"},
		{OptionId: claudeOptionDeny, Kind: leapmuxv1.PermissionOptionKind_PERMISSION_OPTION_KIND_REJECT_ONCE, Name: "Deny"},
	}
}

// claudeElicitationRequest is the request of an MCP elicitation that Claude
// Code relays.
type claudeElicitationRequest struct {
	Request struct {
		Subtype         string          `json:"subtype"`
		Mode            *string         `json:"mode"`
		Message         string          `json:"message"`
		DisplayName     string          `json:"display_name"`
		MCPServerName   string          `json:"mcp_server_name"`
		RequestedSchema json.RawMessage `json:"requested_schema"`
		URL             string          `json:"url"`
		Title           string          `json:"title"`
		Description     string          `json:"description"`
	} `json:"request"`
}

// ReadControlRequest reads Claude Code's control requests. Claude states
// every prompt in one request shape, a tool name and its input, so the tool
// name decides: AskUserQuestion is a question, ExitPlanMode a plan, an MCP
// elicitation a form, and any other tool a permission.
func (claudeProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	toolName := agent.ControlToolName(input.Payload)
	toolInput := agent.ControlToolInput(input.Payload)
	if toolName == ToolNameAskUserQuestion {
		var fields struct {
			Questions json.RawMessage `json:"questions"`
		}
		_ = json.Unmarshal(toolInput, &fields)
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
			Questions: providerkit.QuestionsFromWire(fields.Questions),
		}}}
	}
	if elicitation, ok := claudeElicitation(input.Payload); ok {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: elicitation}}
	}
	switch toolName {
	case ToolNameExitPlanMode:
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{
			Permissions: claudePlanPermissions(toolInput),
		}}}
	default:
		permission := &leapmuxv1.PermissionPrompt{
			Title:     toolName,
			InputJson: string(toolInput),
			Options:   claudePermissionOptions(),
		}
		if toolName == toolNameBash || toolName == toolNamePowerShell {
			var fields struct {
				Command json.RawMessage `json:"command"`
			}
			_ = json.Unmarshal(toolInput, &fields)
			permission.Command, _ = providerkit.RawString(fields.Command)
		}
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: permission}}
	}
}

// claudePlanPermissions reads the tool permissions that a plan asks for
// beside itself, `allowedPrompts: [{tool, prompt}]`. A pair with either half
// blank states no permission that a reader could weigh, so it is dropped.
func claudePlanPermissions(toolInput json.RawMessage) []*leapmuxv1.PlanPermission {
	var fields struct {
		AllowedPrompts []json.RawMessage `json:"allowedPrompts"`
	}
	if json.Unmarshal(toolInput, &fields) != nil {
		return nil
	}
	var permissions []*leapmuxv1.PlanPermission
	for _, raw := range fields.AllowedPrompts {
		var pair map[string]json.RawMessage
		if json.Unmarshal(raw, &pair) != nil {
			continue
		}
		tool, _ := providerkit.RawString(pair["tool"])
		prompt, _ := providerkit.RawString(pair["prompt"])
		if strings.TrimSpace(tool) != "" && strings.TrimSpace(prompt) != "" {
			permissions = append(permissions, &leapmuxv1.PlanPermission{Tool: tool, Prompt: prompt})
		}
	}
	return permissions
}

// claudeElicitation reads an MCP elicitation that Claude Code relays.
func claudeElicitation(payload []byte) (*leapmuxv1.ElicitationPrompt, bool) {
	var envelope claudeElicitationRequest
	if json.Unmarshal(payload, &envelope) != nil || envelope.Request.Subtype != contracts.MCPElicitationSubtypeClaude {
		return nil, false
	}
	request := envelope.Request
	mode := contracts.MCPElicitationModeForm
	if request.Mode != nil {
		mode = *request.Mode
	}
	server := request.DisplayName
	if server == "" {
		server = request.MCPServerName
	}
	prompt := &leapmuxv1.ElicitationPrompt{
		Mode:        mode,
		Message:     request.Message,
		Server:      server,
		Url:         request.URL,
		Title:       request.Title,
		Description: request.Description,
	}
	if providerkit.RawPresent(request.RequestedSchema) {
		prompt.SchemaJson = string(request.RequestedSchema)
	}
	return prompt, true
}

// AnswerControlRequest builds Claude Code's answer in the neutral envelope,
// which ResolveControlResponse turns into the CLI's own answer.
func (claudeProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	switch input.Prompt.GetKind().(type) {
	case *leapmuxv1.ControlPrompt_Elicitation:
		return agent.AnswerElicitation(input, nil)
	case *leapmuxv1.ControlPrompt_Question:
		return providerkit.AskUserQuestionAnswers(input, agent.ControlToolInput(input.Request.Payload))
	case *leapmuxv1.ControlPrompt_Plan:
		return agent.AnswerNeutralPlan(input)
	case *leapmuxv1.ControlPrompt_Permission:
		return claudePermissionAnswer(input)
	default:
		return agent.ControlAnswer{}, agent.InvalidControlDecision("Claude Code states no %T prompt", input.Prompt.GetKind())
	}
}

// claudePermissionAnswer answers a permission prompt. A refusal with the
// user's feedback, such as the composer's typed refusal, denies with it.
// Otherwise the chosen option rides the neutral envelope as its choice, and
// the Worker turns `session` into the session grant.
func claudePermissionAnswer(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	decision, option, err := agent.PermissionDecisionOf(input)
	if err != nil {
		return agent.ControlAnswer{}, err
	}
	if !agent.IsAllowOption(option) && decision.GetFeedback() != "" {
		return agent.ControlAnswer{Content: agent.DenyResponse(input.RequestID, decision.GetFeedback())}, nil
	}
	behavior := contracts.ControlAnswerBehaviorAllow
	if option.GetOptionId() == claudeOptionDeny {
		behavior = contracts.ControlAnswerBehaviorDeny
	}
	content := agent.ControlResponseEnvelope(input.RequestID, map[string]any{"behavior": behavior})
	return agent.ControlAnswer{Content: agent.WithControlChoice(content, option.GetOptionId())}, nil
}
