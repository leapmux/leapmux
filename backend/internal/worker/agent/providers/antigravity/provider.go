package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
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

type antigravityControlReply struct {
	cascadeID      string
	interactionRaw json.RawMessage
	address        antigravityAddress
}

func decodeAntigravityControlReply(data []byte) (antigravityControlReply, error) {
	var envelope struct {
		CascadeID   string          `json:"cascadeId"`
		Interaction json.RawMessage `json:"interaction"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.CascadeID == "" {
		return antigravityControlReply{}, errors.New("the Antigravity control answer is malformed")
	}
	if _, ok := providerkit.RawObject(envelope.Interaction); !ok {
		return antigravityControlReply{}, errors.New("the Antigravity control answer has no interaction object")
	}
	var address struct {
		TrajectoryID string `json:"trajectoryId"`
		StepIndex    *int   `json:"stepIndex"`
	}
	if err := json.Unmarshal(envelope.Interaction, &address); err != nil || address.StepIndex == nil {
		return antigravityControlReply{}, errors.New("the Antigravity control answer has a malformed interaction address")
	}
	parsed := antigravityAddress{
		cascadeID:    envelope.CascadeID,
		trajectoryID: address.TrajectoryID,
		stepIndex:    *address.StepIndex,
	}
	if !parsed.valid() {
		return antigravityControlReply{}, errors.New("the Antigravity control answer states no valid step address")
	}
	return antigravityControlReply{
		cascadeID:      envelope.CascadeID,
		interactionRaw: envelope.Interaction,
		address:        parsed,
	}, nil
}

func (antigravityProvider) ControlResponseRequestID(content []byte) string {
	if reply, err := decodeAntigravityControlReply(content); err == nil {
		return reply.address.requestID()
	}
	return agent.ProviderDefaults{}.ControlResponseRequestID(content)
}

// ControlRequestKey reads the key that the agent publishes a control under.
// An interaction states the address of its step (antigravityAddress), as its
// cascadeId, trajectoryId and stepIndex, and the address gives the key. The
// plan review that the Worker raises itself has a key of its own, which its
// payload states as its request_id (maybePublishPlan).
func (antigravityProvider) ControlRequestKey(payload []byte, _ string) (string, bool) {
	if agent.ControlToolName(payload) == contracts.AntigravityControlToolPlanReview {
		return agent.EnvelopeControlRequestKey(payload)
	}
	var stored struct {
		CascadeID    string `json:"cascadeId"`
		TrajectoryID string `json:"trajectoryId"`
		StepIndex    *int   `json:"stepIndex"`
	}
	if json.Unmarshal(payload, &stored) != nil || stored.StepIndex == nil {
		return "", false
	}
	address := antigravityAddress{cascadeID: stored.CascadeID, trajectoryID: stored.TrajectoryID, stepIndex: *stored.StepIndex}
	if !address.valid() {
		return "", false
	}
	return address.requestID(), true
}

// ResolveControlResponse forwards the language server's own answer that
// AnswerControlRequest built. The Worker resolves only a stored request whose
// address is its key (ControlRequestKey), and the answer echoes the address
// of that key (antigravityAddress.answer), so the answer reaches the step of
// the stored request.
func (p antigravityProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	res := agent.DefaultControlResponseResolution(ctx)
	res.PlanModeControl = p.PlanModeControl(ctx.ToolName)
	if res.PlanModeControl == agent.PlanModeControlPrompt {
		res.LeapMuxOwned = true
		return res
	}
	if _, ok := providerkit.RawObject(ctx.RequestPayload); !ok {
		res.Refuse(agent.RefusalUnreadableRequest)
		return res
	}
	if _, err := decodeAntigravityControlReply(ctx.ResponseContent); err != nil {
		// AnswerControlRequest answers each request of the language server
		// natively. So this guard refuses only an answer that it did not
		// build, such as the shared envelope, which the language server
		// cannot read.
		res.Refuse(agent.RefusalUnreadableAnswer)
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
