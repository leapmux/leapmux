package kimi

import (
	"context"
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// kimiProvider is the stateless wire-format plugin for Kimi Code. It answers the
// questions the service layer asks without a running agent. A default the
// provider takes as it is needs no method here.
type kimiProvider struct {
	agent.ProviderDefaults
}

// Classify groups the notices that repeat. A retry replaces the retry before
// it, a compaction's progress replaces the notice that it started, and its
// completion is the context boundary.
func (kimiProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return agent.NotificationClassification{}
	}
	switch head.Type {
	case contracts.KimiEventTurnStepRetrying:
		return agent.NotificationClassification{Kind: agent.NotificationKindAPIRetry, Key: "kimi:retry"}
	case contracts.KimiEventCompactionStarted, contracts.KimiEventCompactionBlocked, contracts.KimiEventCompactionCancelled:
		return agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: "kimi:compaction"}
	case contracts.KimiEventCompactionCompleted:
		return agent.NotificationClassification{Kind: agent.NotificationKindCompactionBoundary, Key: "kimi:compaction"}
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

// PlanModeControl reads Kimi's plan tools, which take Claude's names. An
// ExitPlanMode reaches LeapMux as an approval, which is answered through the
// shared plan approval.
func (kimiProvider) PlanModeControl(toolName string) agent.PlanModeControlKind {
	switch toolName {
	case contracts.KimiToolEnterPlanMode:
		return agent.PlanModeControlEnter
	case contracts.KimiToolExitPlanMode:
		return agent.PlanModeControlExit
	default:
		return agent.PlanModeControlNone
	}
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

// ResolveControlResponse turns the browser's decision into the server's own
// answer body. See control_response.go.
func (kimiProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	return kimiResolveControlResponse(ctx)
}

// ValidateAttachment accepts text and images. See attachments.go for why a PDF
// and a binary file are refused. The browser plugin's attachment policy states
// the same rule.
func (kimiProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	return providerkit.RejectPDFAndBinaryAttachment(kimiAttachmentLabel, attachment)
}

// SupportsChildSteering is true: a subagent's tab sends it messages, which the
// server runs as the subagent's next turn. See subagent.go.
func (kimiProvider) SupportsChildSteering() bool { return true }

// SupportsChildInterrupt is true: a subagent's tab can stop its running turn.
// See InterruptChild in subagent.go.
func (kimiProvider) SupportsChildInterrupt() bool { return true }

// ListStoredSessions reads Kimi Code's own session store. See sessions.go.
func (kimiProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return kimiStoredSessions(ctx, q)
}
