package qoder

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// qoderProvider is the stateless wire-format plugin for Qoder CLI.
type qoderProvider struct {
	agent.ProviderDefaults
}

// ValidateAttachment permits only the content blocks Qoder's stream input reads.
func (qoderProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if err := providerkit.RejectPDFAndBinaryAttachment("Qoder CLI", attachment); err != nil {
		return err
	}
	if attachment.Kind == agent.AttachmentKindImage && len(attachment.Data) == 0 {
		return fmt.Errorf("qoder CLI does not support an empty image attachment: %s", attachment.Filename)
	}
	return nil
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
// An allow carries the `updatedInput` the browser folded its answer into, and
// states NO outcome beside it: Qoder resolves an explicit outcome first and
// would then drop the input (see canUseToolAnswer). An allow that modifies
// nothing states `proceed_once`. A deny carries the user's words on `message`
// and `reason` and states no outcome either, so the words reach the model.
//
// The frame keeps NO top-level `request_id`: Qoder's StructuredIOReader
// validates the envelope and rejects a frame that carries one as a malformed
// control_response, and the pending request matches on `response.request_id`.
func translateQoderCanUseTool(content []byte) ([]byte, bool) {
	requestID, behavior, message, decoded := agent.DecodeControlBehavior(content)
	if !decoded || (behavior != agent.ControlBehaviorAllow && behavior != agent.ControlBehaviorDeny) {
		return nil, false
	}
	answer := canUseToolAnswer{Behavior: behavior}
	if behavior == agent.ControlBehaviorAllow {
		if updatedInput := agent.DecodeControlUpdatedInput(content); updatedInput != nil {
			answer.UpdatedInput = updatedInput
		} else {
			answer.Outcome = contracts.QoderPermissionOutcomeProceedOnce
		}
	} else if message != "" {
		// Only the words the user typed. A bare deny states no message, so the
		// placeholder the browser auto-fills is never handed to the model as if
		// the user had written it.
		answer.Message = message
		answer.Reason = message
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

// PlanModePermissionMode returns the mode an approved plan transition shows.
func (qoderProvider) PlanModePermissionMode(kind agent.PlanModeControlKind) string {
	switch kind {
	case agent.PlanModeControlEnter:
		return contracts.QoderModePlan
	case agent.PlanModeControlExit:
		return contracts.QoderModeAcceptEdits
	default:
		return ""
	}
}

// PlanApprovalOptions applies the selected policy after native Plan ends.
func (qoderProvider) PlanApprovalOptions(mode string) map[string]string {
	if mode == "" {
		mode = contracts.QoderModeAcceptEdits
	}
	return map[string]string{agent.OptionIDPermissionMode: mode}
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
