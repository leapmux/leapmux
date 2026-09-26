package codebuddy

import (
	"context"
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// codebuddyProvider is the stateless wire-format plugin for CodeBuddy Code.
//
// Every method below states a decision about CodeBuddy's NATIVE protocol. A
// default that CodeBuddy simply takes needs no method here.
type codebuddyProvider struct {
	agent.ProviderDefaults
}

// IsInterrupt recognizes CodeBuddy's own interrupt frame:
// a control_request whose subtype is `interrupt`.
func (codebuddyProvider) IsInterrupt(content string) bool {
	var frame struct {
		Type    string `json:"type"`
		Request struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(content), &frame); err != nil {
		return false
	}
	return frame.Type == frameTypeControlRequest &&
		frame.Request.Subtype == contracts.CodebuddyControlRequestSubtypeInterrupt
}

// ResolveControlResponse turns the browser's neutral decision into CodeBuddy's
// native answer.
//
// CodeBuddy's can_use_tool parser reads `allowed`, NOT Claude's `behavior`. This
// translation is the single hard incompatibility with providers/claude and the
// reason the packages stay separate. The browser sends
// {response:{request_id, response:{behavior, message}}}; the worker forwards
// {response:{request_id, response:{allowed, reason, interrupt, updatedInput}}}.
func (codebuddyProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	res := agent.DefaultControlResponseResolution(ctx)
	if res.Withhold {
		return res
	}
	translated, ok := translateCanUseToolAnswer(res.Content)
	if ok {
		res.Content = translated
	}
	return res
}

// translateCanUseToolAnswer rewrites the neutral behavior envelope into
// CodeBuddy's `allowed` object. ok is false for a payload this shape cannot
// read, in which case the caller forwards the bytes unchanged. Only a payload
// that actually states allow or deny is translated: a foreign shape (a JSON-RPC
// result, an elicitation reply) passes through untouched.
//
// An allow forwards the `updatedInput` the browser folded its answer into: an
// AskUserQuestion reply arrives as the whole tool input with `answers` added,
// and CodeBuddy merges that object into the tool call. A deny carries the
// user's reason in `reason`, which is the field CodeBuddy's own permission
// reader takes.
func translateCanUseToolAnswer(content []byte) ([]byte, bool) {
	requestID, behavior, message, decoded := agent.DecodeControlBehavior(content)
	if !decoded || (behavior != agent.ControlBehaviorAllow && behavior != agent.ControlBehaviorDeny) {
		return nil, false
	}
	answer := canUseToolAnswer{Allowed: behavior == agent.ControlBehaviorAllow}
	if !answer.Allowed {
		answer.Reason = message
		if answer.Reason == "" {
			answer.Reason = agent.ControlRejectedByUserMessage
		}
	} else if updatedInput := agent.DecodeControlUpdatedInput(content); updatedInput != nil {
		answer.UpdatedInput = updatedInput
	}
	out := map[string]any{
		"type":       frameTypeControlResponse,
		"request_id": requestID,
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   answer,
		},
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, false
	}
	return raw, true
}

// ListStoredSessions reads CodeBuddy's own transcripts. See sessions.go.
func (codebuddyProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return codebuddyStoredSessions(ctx, q)
}

// PlanModeControl classifies CodeBuddy's plan-mode tools.
func (codebuddyProvider) PlanModeControl(toolName string) agent.PlanModeControlKind {
	switch toolName {
	case "EnterPlanMode":
		return agent.PlanModeControlEnter
	case "ExitPlanMode":
		return agent.PlanModeControlExit
	default:
		return agent.PlanModeControlNone
	}
}

// PlanModePermissionMode returns the mode an approved plan exit switches to.
func (codebuddyProvider) PlanModePermissionMode(kind agent.PlanModeControlKind) string {
	if kind == agent.PlanModeControlExit {
		return contracts.CodebuddyModeAcceptEdits
	}
	return ""
}

// IsSelfDisplayingControlTool reports false: CodeBuddy echoes no control answer
// back into its output stream as a tool_result the way Claude does.
func (codebuddyProvider) IsSelfDisplayingControlTool(string) bool { return false }

// TurnEndToolUses reads the tool-use count of a result frame.
func (codebuddyProvider) TurnEndToolUses(content []byte) (int32, bool) {
	var result resultMessage
	if err := json.Unmarshal(content, &result); err != nil {
		return 0, false
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(content, &probe); err != nil {
		return 0, false
	}
	raw, present := probe["num_tool_uses"]
	if !present {
		return 0, false
	}
	var count int32
	if err := json.Unmarshal(raw, &count); err != nil {
		return 0, false
	}
	return count, true
}

// EndsSubagentTranscript reports false: CodeBuddy's result ends a turn, and a
// subagent's transcript simply stops at its last message. The worker adds its
// own closing divider.
func (codebuddyProvider) EndsSubagentTranscript([]byte) bool { return false }

// controlRequestSubtype extracts the subtype of a control request payload.
