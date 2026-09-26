package qoder

import (
	"context"
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// qoderProvider is the stateless wire-format plugin for Qoder CLI.
type qoderProvider struct {
	agent.ProviderDefaults
}

// IsInterrupt recognizes Qoder's own interrupt frame: a control_request whose
// subtype is `interrupt`.
func (qoderProvider) IsInterrupt(content string) bool {
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
		frame.Request.Subtype == contracts.QoderControlRequestSubtypeInterrupt
}

// ResolveControlResponse turns the browser's neutral decision into Qoder's
// native answer.
func (qoderProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	res := agent.DefaultControlResponseResolution(ctx)
	if res.Withhold {
		return res
	}
	if translated, ok := translateQoderCanUseTool(res.Content); ok {
		res.Content = translated
	}
	res.SelfDisplayed = false
	res.PlanModeControl = qoderProvider{}.PlanModeControl(ctx.ToolName)
	return res
}

// translateQoderCanUseTool rewrites the neutral behavior envelope into Qoder's
// decision object. ok is false when the payload is not a can_use_tool answer;
// only a payload that actually states allow or deny is translated.
//
// The frame keeps NO top-level `request_id`: Qoder's StructuredIOReader
// validates the envelope and rejects a frame that carries one as a malformed
// control_response, and the pending request matches on `response.request_id`.
func translateQoderCanUseTool(content []byte) ([]byte, bool) {
	requestID, behavior, _, decoded := agent.DecodeControlBehavior(content)
	if !decoded || (behavior != agent.ControlBehaviorAllow && behavior != agent.ControlBehaviorDeny) {
		return nil, false
	}
	allow := behavior == agent.ControlBehaviorAllow
	answer := canUseToolAnswer{
		Behavior: behavior,
		Outcome:  contracts.QoderPermissionOutcomeProceedOnce,
	}
	if !allow {
		answer.Outcome = contracts.QoderPermissionOutcomeCancel
	}
	out := map[string]any{
		"type": frameTypeControlResponse,
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

// ListStoredSessions reads Qoder's own transcripts. See sessions.go.
func (qoderProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return qoderStoredSessions(ctx, q)
}

// PlanModeControl classifies Qoder's plan-mode tools.
func (qoderProvider) PlanModeControl(toolName string) agent.PlanModeControlKind {
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
func (qoderProvider) PlanModePermissionMode(kind agent.PlanModeControlKind) string {
	if kind == agent.PlanModeControlExit {
		return contracts.QoderModeAcceptEdits
	}
	return ""
}

// IsSelfDisplayingControlTool reports false: Qoder echoes no control answer
// back into its output stream as a tool_result.
func (qoderProvider) IsSelfDisplayingControlTool(string) bool { return false }

// TurnEndToolUses reads the tool-use count of a result frame.
func (qoderProvider) TurnEndToolUses(content []byte) (int32, bool) {
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

// EndsSubagentTranscript reports false: Qoder's result ends a turn, and a
// subagent's transcript simply stops at its last message.
func (qoderProvider) EndsSubagentTranscript([]byte) bool { return false }

// SupportsChildSteering reports false: the Agent implements InputSteerer for the
// parent turn, not ChildSteerer for a subagent's conversation.
func (qoderProvider) SupportsChildSteering() bool { return false }

// controlRequestSubtype extracts the subtype of a control request payload.
