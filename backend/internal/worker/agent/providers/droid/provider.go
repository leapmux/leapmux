package droid

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/internal/util/optionmap"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// droidProvider is the stateless wire-format plugin for Factory Droid. It
// answers the questions the service layer asks without a running agent. A
// default the provider takes as it is needs no method here.
type droidProvider struct {
	agent.ProviderDefaults
}

// Classify groups the notices that repeat.
func (droidProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	return agent.NotificationClassification{}
}

// IsInterrupt recognizes the raw interrupt frame that SendRawInput takes. The
// browser interrupts through the InterruptAgent call; the frame is for a caller
// of SendAgentRawMessage.
func (droidProvider) IsInterrupt(content string) bool {
	return isDroidRawInterrupt([]byte(content))
}

// PlanModeControl reads Droid's review tools: the spec-mode exit, and the
// mission proposal, which Droid groups with it for the plan-file handoff.
func (droidProvider) PlanModeControl(toolName string) agent.PlanModeControlKind {
	if toolName == contracts.DroidToolExitSpecMode || toolName == contracts.DroidToolProposeMission {
		return agent.PlanModeControlExit
	}
	return agent.PlanModeControlNone
}

// PlanModePermissionMode states the mode an approved plan switches to. Droid
// leaves spec mode through its own tool, so no LeapMux mode change follows.
func (droidProvider) PlanModePermissionMode(agent.PlanModeControlKind) string { return "" }

// ResolveControlResponse turns the browser's decision into Droid's own answer.
func (droidProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	return droidResolveControlResponse(ctx)
}

// ValidateAttachment accepts text and images. A PDF and a binary file are
// refused with a reason.
func (droidProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if err := providerkit.RejectPDFAndBinaryAttachment(droidAttachmentLabel, attachment); err != nil {
		return err
	}
	if attachment.Kind == agent.AttachmentKindImage && len(attachment.Data) == 0 {
		return fmt.Errorf("factory Droid does not support an empty image attachment: %s", attachment.Filename)
	}
	return nil
}

// ChildCapabilities.AcceptsMessages is true: droid.add_user_message routes by
// params.sessionId, so a child session id addresses the child. The registry
// row key of a child is its childSessionId (see child_steer.go).

// ChildCapabilities.AcceptsInterrupt is false for the same reason.

// ListStoredSessions reads Factory Droid's own session store.
func (droidProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return droidStoredSessions(ctx, q)
}

// ChildCapabilities states the native child operations that this provider supports.
func (droidProvider) ChildCapabilities(optionmap.Map) agent.ChildCapabilities {
	return agent.ChildCapabilities{AcceptsMessages: true, AcceptsInterrupt: false}
}
