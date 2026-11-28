package qoder

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// qoderProvider is the stateless wire-format plugin for Qoder CLI.
type qoderProvider struct {
	agent.ProviderDefaults
}

// Classify folds retries and model queue notices. A native compaction end is a boundary.
func (qoderProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	if providerkit.StringField(raw, "type") != contracts.QoderFrameKindSystem {
		return agent.NotificationClassification{}
	}
	switch providerkit.StringField(raw, "subtype") {
	case contracts.QoderSystemSubtypeCompactBoundary:
		return agent.CompactionBoundaryNotification()
	case contracts.QoderSystemSubtypeApiRetry:
		return agent.APIRetryNotification()
	case contracts.QoderSystemSubtypeModelQueueStatus:
		return agent.ProviderScopedNotification("qoder:model-queue")
	default:
		return agent.NotificationClassification{}
	}
}

// ValidateAttachment permits only the content blocks Qoder's stream input reads.
func (qoderProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if err := providerkit.ValidateTextAndImageAttachment("Qoder CLI", attachment); err != nil {
		return err
	}
	if attachment.Kind == contracts.AttachmentMediaKindImage && len(attachment.Data) == 0 {
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

// ResolveControlResponse turns the neutral answer into Qoder's native answer.
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
// An allow carries the `updatedInput` that AnswerControlRequest folded the
// answers into, and states NO outcome beside it: Qoder resolves an explicit
// outcome first and would then drop the input (see canUseToolAnswer). An allow that modifies
// nothing states `proceed_once`, or the `permissionScope` of the session pill
// the reader selected (see scopeChoiceOf). A deny carries the user's words on
// `message` and `reason` and states no outcome either, so the words reach the
// model.
//
// The frame keeps NO top-level `request_id`: Qoder's StructuredIOReader
// validates the envelope and rejects a frame that carries one as a malformed
// control_response, and the pending request matches on `response.request_id`.
// The answer's CHOICE names the scope pill the reader selected. `session` is
// Qoder's own session tier: the answer carries its `permissionScope`, and the
// runtime derives and keeps the session rule itself. Any other id (or none) is
// the once answer, so a stale pill can never invent a wider grant.
func scopeChoiceOf(content []byte) string {
	// The option id of the session pill (qoderPermissionOptions), and Qoder's
	// own word for the session scope, share one spelling.
	if choice := agent.DecodeControlChoice(content); choice == qoderOptionSession {
		return "session"
	}
	return ""
}

func translateQoderCanUseTool(content []byte) ([]byte, bool) {
	requestID, behavior, message, decoded := agent.DecodeControlBehavior(content)
	if !decoded || (behavior != contracts.ControlAnswerBehaviorAllow && behavior != contracts.ControlAnswerBehaviorDeny) {
		return nil, false
	}
	answer := canUseToolAnswer{Behavior: behavior}
	if behavior == contracts.ControlAnswerBehaviorAllow {
		if updatedInput := agent.DecodeControlUpdatedInput(content); updatedInput != nil {
			answer.UpdatedInput = updatedInput
		} else if scope := scopeChoiceOf(content); scope != "" {
			answer.PermissionScope = scope
		} else {
			answer.Outcome = contracts.QoderPermissionOutcomeProceedOnce
		}
	} else if message != "" {
		// Only the words the user typed. A bare deny states no message, so the
		// placeholder that DenyResponse fills in is never handed to the model as
		// if the user had written it.
		answer.Message = message
		answer.Reason = message
	}
	return agent.ControlResponseEnvelope(requestID, answer), true
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

// ChildCapabilities states what a child tab offers.
//
// AcceptsMessages reports false: the Agent implements InputSteerer for the
// parent turn, not ChildSteerer for a subagent's conversation. AcceptsInterrupt
// reports true for a child that an Agent tool call started, because Qoder's
// stop_task stops that task alone. A Workflow script's child is not a task of
// the session, and stop_task leaves it running.
func (qoderProvider) ChildCapabilities(options optionmap.Map) agent.ChildCapabilities {
	return agent.ChildCapabilities{AcceptsInterrupt: options[qoderOptionChildKind] == qoderChildKindTask}
}
