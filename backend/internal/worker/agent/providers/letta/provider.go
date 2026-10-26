package letta

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// lettaProvider is the stateless wire-format plugin for Letta Code. It answers
// the questions the service layer asks without a running agent.
type lettaProvider struct {
	agent.ProviderDefaults
}

// Classify folds retries and warning status rows. A summary marks a compaction boundary.
func (lettaProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	switch providerkit.StringField(raw, contracts.LettaDeltaFieldMessageType) {
	case contracts.LettaDeltaKindRetry:
		return agent.APIRetryNotification()
	case contracts.LettaDeltaKindEventMessage:
		if providerkit.StringField(raw, contracts.LettaDeltaFieldEventType) == contracts.LettaEventTypeRetry {
			return agent.APIRetryNotification()
		}
	case contracts.LettaDeltaKindSummaryMessage:
		return agent.CompactionBoundaryNotification()
	case contracts.LettaDeltaKindSlashCommandEnd:
		var delta lettaCompactionDelta
		if json.Unmarshal(raw, &delta) == nil {
			help, valid := lettaCompactionKind(&delta)
			if valid && !help && delta.Success != nil && *delta.Success {
				return agent.CompactionBoundaryNotification()
			}
		}
	case contracts.LettaDeltaKindStatus:
		level := providerkit.StringField(raw, "level")
		if lettaWarningLevel(level) {
			return agent.ProviderScopedNotification("letta:status")
		}
	}
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

// ResolveControlResponse turns the browser's decision into the bytes that Letta
// Code reads: the flat approval_response payload for a permission, and the
// question response for a question.
func (lettaProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	return lettaResolveControlResponse(ctx)
}

// ValidateAttachment accepts text and nonempty images of supported types.
func (lettaProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if err := providerkit.ValidateTextAndImageAttachment(lettaAttachmentLabel, attachment); err != nil {
		return err
	}
	if attachment.Kind == contracts.AttachmentMediaKindImage && len(attachment.Data) == 0 {
		return fmt.Errorf("letta code cannot send an empty image attachment: %s", attachment.Filename)
	}
	return nil
}

// ChildCapabilities.AcceptsMessages is false: a subagent's tab is read-only on the wire
// LeapMux drives.

// ChildCapabilities.AcceptsInterrupt is false for the same reason.

// ListStoredSessions reads Letta Code's own local-backend store.
func (lettaProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return lettaStoredSessions(ctx, q)
}
