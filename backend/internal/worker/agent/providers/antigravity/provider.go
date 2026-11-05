package antigravity

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type antigravityProvider struct {
	agent.ProviderDefaults
}

func (antigravityProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if attachment.Kind == contracts.AttachmentMediaKindAudio && len(attachment.Data) == 0 {
		return fmt.Errorf("the audio attachment %q is empty", attachment.Filename)
	}
	return nil
}

func (antigravityProvider) ListStoredSessions(ctx context.Context, query agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return antigravityStoredSessions(ctx, query)
}

func (antigravityProvider) ChildCapabilities(optionmap.Map) agent.ChildCapabilities {
	return agent.ChildCapabilities{AcceptsMessages: true, AcceptsInterrupt: true}
}

func (p antigravityProvider) PlanModeControl(toolName string) agent.PlanModeControlKind {
	if toolName == contracts.AntigravityControlToolPlanReview {
		return agent.PlanModeControlPrompt
	}
	return agent.PlanModeControlNone
}

func (antigravityProvider) PlanModePermissionMode(kind agent.PlanModeControlKind) string {
	if kind == agent.PlanModeControlPrompt {
		return contracts.AntigravityModeDefault
	}
	return ""
}

func (antigravityProvider) PlanApprovalOptions(permissionMode string) map[string]string {
	if permissionMode == "" {
		permissionMode = contracts.AntigravityModeDefault
	}
	return map[string]string{agent.OptionIDPermissionMode: permissionMode}
}

func (antigravityProvider) ControlResponseRequestID(content []byte) string {
	var response struct {
		CascadeID   string `json:"cascadeId"`
		Interaction struct {
			TrajectoryID string `json:"trajectoryId"`
			StepIndex    int    `json:"stepIndex"`
		} `json:"interaction"`
	}
	if err := json.Unmarshal(content, &response); err == nil && response.CascadeID != "" && response.Interaction.TrajectoryID != "" && response.Interaction.StepIndex >= 0 {
		return fmt.Sprintf("antigravity:%s:%s:%d", response.CascadeID, response.Interaction.TrajectoryID, response.Interaction.StepIndex)
	}
	return agent.ProviderDefaults{}.ControlResponseRequestID(content)
}

func (p antigravityProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	res := agent.DefaultControlResponseResolution(ctx)
	res.PlanModeControl = p.PlanModeControl(ctx.ToolName)
	if res.PlanModeControl == agent.PlanModeControlPrompt {
		res.LeapMuxOwned = true
		return res
	}
	var request struct {
		CascadeID string `json:"cascadeId"`
	}
	if err := json.Unmarshal(ctx.RequestPayload, &request); err != nil || request.CascadeID == "" {
		res.Refuse(agent.RefusalUnreadableRequest)
		return res
	}
	var response struct {
		CascadeID   string         `json:"cascadeId"`
		Interaction map[string]any `json:"interaction"`
	}
	if err := json.Unmarshal(ctx.ResponseContent, &response); err != nil || response.CascadeID == "" || response.Interaction == nil {
		res.Refuse("the Antigravity control answer is malformed")
		return res
	}
	if request.CascadeID != response.CascadeID {
		res.Refuse(fmt.Sprintf("%s: request cascade %q, response cascade %q", agent.RefusalOtherRequest, request.CascadeID, response.CascadeID))
		return res
	}
	return res
}

func (antigravityProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	var frame struct {
		Name         string          `json:"name"`
		Type         string          `json:"type"`
		ModelRequest json.RawMessage `json:"modelRequest"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return agent.NotificationClassification{}
	}
	if len(frame.ModelRequest) > 0 {
		return agent.APIRetryNotification()
	}
	if frame.Type == contracts.AntigravityStepTypeCheckpoint {
		return agent.CompactionBoundaryNotification()
	}
	return agent.NotificationClassification{}
}
