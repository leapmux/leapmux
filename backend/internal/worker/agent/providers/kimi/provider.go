package kimi

import (
	"context"
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// kimiProvider is the stateless wire-format plugin for Kimi Code. It answers the
// questions the service layer asks without a running agent. A default the
// provider takes as it is needs no method here.
type kimiProvider struct {
	agent.ProviderDefaults
}

// Classify folds retries and identifies every stored compaction end as a boundary.
func (kimiProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return agent.NotificationClassification{}
	}
	switch head.Type {
	case contracts.KimiConversationRecordKindItem:
		view, err := decodeKimiConversationRecord(raw)
		if err == nil && view.item != nil && view.item.Type == contracts.KimiConversationItemKindSystem &&
			view.item.SystemKind == contracts.KimiConversationSystemKindCompaction && view.item.Status == contracts.KimiConversationStatusEnded {
			return agent.CompactionBoundaryNotification()
		}
		return agent.NotificationClassification{}
	case contracts.KimiConversationRecordKindRetry:
		if _, err := decodeKimiConversationRecord(raw); err == nil {
			return agent.APIRetryNotification()
		}
		return agent.NotificationClassification{}
	default:
		return agent.NotificationClassification{}
	}
}

// IsInterrupt recognizes the raw abort frame that SendRawInput takes. The
// browser interrupts through the InterruptAgent call; the frame is for a caller
// of SendAgentRawMessage.
func (kimiProvider) IsInterrupt(content string) bool {
	return isKimiRawAbort([]byte(content))
}

// PlanModeControl recognizes the native ExitPlanMode approval. The user
// enables Plan mode through settings.
func (kimiProvider) PlanModeControl(toolName string) agent.PlanModeControlKind {
	if toolName == contracts.KimiToolExitPlanMode {
		return agent.PlanModeControlExit
	}
	return agent.PlanModeControlNone
}

// PlanModePermissionMode states the two ends of plan mode on LeapMux's axis.
// An approved plan returns to Kimi's own default, Always Ask, when the user
// picked no other mode.
func (kimiProvider) PlanModePermissionMode(kind agent.PlanModeControlKind) string {
	switch kind {
	case agent.PlanModeControlEnter:
		return contracts.KimiModePlan
	case agent.PlanModeControlExit:
		return contracts.KimiDefaultMode
	case agent.PlanModeControlNone, agent.PlanModeControlPrompt:
		return ""
	}
	return ""
}

// ControlRequestKey reads the key that the agent publishes a Kimi Code request
// under: the server's own `interaction_id` (publishControl). LeapMux's
// workspace-trust question states no key, so its session gives it
// (providerkit.WorkspaceTrustRequestKey).
func (kimiProvider) ControlRequestKey(payload []byte, agentSessionID string) (string, bool) {
	if providerkit.IsWorkspaceTrustQuestion(payload) {
		return providerkit.WorkspaceTrustRequestKey(payload, agentSessionID)
	}
	var interaction struct {
		InteractionID string `json:"interaction_id"`
	}
	if json.Unmarshal(payload, &interaction) != nil || interaction.InteractionID == "" {
		return "", false
	}
	return interaction.InteractionID, true
}

// ResolveControlResponse turns the neutral answer into the server's own
// answer body. See control_response.go.
func (kimiProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	if result, ok := providerkit.ResolveWorkspaceTrustResponse(ctx); ok {
		return result
	}
	return kimiResolveControlResponse(ctx)
}

// ValidateAttachment accepts text and images. See attachments.go for why a PDF
// and a binary file are refused. The browser plugin's attachment policy states
// the same rule.
func (kimiProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	return providerkit.ValidateTextAndImageAttachment(kimiAttachmentLabel, attachment)
}

// ListStoredSessions reads Kimi Code's own session store. See sessions.go.
func (kimiProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return kimiStoredSessions(ctx, q)
}

// ChildCapabilities supports messages from a subagent's tab through its native queue.
func (kimiProvider) ChildCapabilities(optionmap.Map) agent.ChildCapabilities {
	// Native task addresses do not identify the current child turn.
	// Separate registry-task controls: https://github.com/leapmux/leapmux/issues/644
	return agent.ChildCapabilities{AcceptsMessages: true}
}
