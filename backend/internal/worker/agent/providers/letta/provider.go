package letta

import (
	"context"
	"encoding/json"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// lettaProvider is the stateless wire-format plugin for Letta Code. It answers
// the questions the service layer asks without a running agent.
type lettaProvider struct {
	agent.ProviderDefaults
}

// Classify groups the notices that repeat.
func (lettaProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	return agent.NotificationClassification{}
}

// IsInterrupt recognizes the raw interrupt frame that SendRawInput takes.
func (lettaProvider) IsInterrupt(content string) bool {
	return isLettaRawInterrupt([]byte(content))
}

// PlanModeControl reports none: Letta has no plan-mode tool. `UpdatePlan` is a
// to-do-style tool, not a plan approval flow.
func (lettaProvider) PlanModeControl(string) agent.PlanModeControlKind {
	return agent.PlanModeControlNone
}

// ResolveControlResponse turns the browser's decision into the flat
// approval_response payload Letta reads.
func (lettaProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	return lettaResolveControlResponse(ctx)
}

// ValidateAttachment accepts text and images.
func (lettaProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	return providerkit.RejectPDFAndBinaryAttachment(lettaAttachmentLabel, attachment)
}

// SupportsChildSteering is false: a subagent's tab is read-only on the wire
// LeapMux drives.
func (lettaProvider) SupportsChildSteering() bool { return false }

// SupportsChildInterrupt is false for the same reason.
func (lettaProvider) SupportsChildInterrupt() bool { return false }

// ListStoredSessions reads Letta Code's own local-backend store.
func (lettaProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return lettaStoredSessions(ctx, q)
}
