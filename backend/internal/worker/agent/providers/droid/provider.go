package droid

import (
	"context"
	"encoding/json"

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

// PlanModeControl reads Droid's spec-mode exit tool.
func (droidProvider) PlanModeControl(toolName string) agent.PlanModeControlKind {
	if toolName == contracts.DroidToolExitSpecMode {
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
	return providerkit.RejectPDFAndBinaryAttachment(droidAttachmentLabel, attachment)
}

// SupportsChildSteering is false: a Droid child session is driven by its own
// Task tool and LeapMux cannot address it.
func (droidProvider) SupportsChildSteering() bool { return false }

// SupportsChildInterrupt is false for the same reason.
func (droidProvider) SupportsChildInterrupt() bool { return false }

// ListStoredSessions reads Factory Droid's own session store.
func (droidProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return droidStoredSessions(ctx, q)
}
