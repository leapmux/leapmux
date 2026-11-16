package kimi

import (
	"context"
	"encoding/json"

	"github.com/leapmux/leapmux/internal/util/optionmap"

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

// Classify folds retries and identifies every stored compaction end as a boundary.
func (kimiProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return agent.NotificationClassification{}
	}
	switch head.Type {
	case contracts.KimiEventTranscriptOps:
		return classifyTranscriptNotification(raw)
	default:
		return agent.NotificationClassification{}
	}
}

func classifyTranscriptNotification(raw json.RawMessage) agent.NotificationClassification {
	row, valid := readKimiTranscriptRecord(raw)
	if !valid {
		return agent.NotificationClassification{}
	}
	op := row.operation
	switch op.Op {
	case contracts.KimiTranscriptOperationStepUpsert:
		var step struct {
			Kind   string               `json:"kind"`
			StepID string               `json:"stepId"`
			State  string               `json:"state"`
			Retry  *kimiTranscriptRetry `json:"retry"`
		}
		if op.TurnID != "" && json.Unmarshal(op.Step, &step) == nil && step.Kind == contracts.KimiTranscriptNodeStep && step.StepID != "" && step.State == contracts.KimiTranscriptStepRunning && step.Retry.valid() {
			return agent.APIRetryNotification()
		}
	case contracts.KimiTranscriptOperationMarkerUpsert:
		var item struct {
			Kind     string `json:"kind"`
			MarkerID string `json:"markerId"`
			Marker   string `json:"marker"`
			Payload  struct {
				Phase string `json:"phase"`
			} `json:"payload"`
		}
		if json.Unmarshal(op.Item, &item) == nil && item.Kind == contracts.KimiTranscriptNodeMarker && item.MarkerID != "" && item.Marker == contracts.KimiTranscriptMarkerCompaction && (item.Payload.Phase == contracts.KimiCompactionPhaseCompleted || item.Payload.Phase == contracts.KimiCompactionPhaseCancelled) {
			return agent.CompactionBoundaryNotification()
		}
	}
	return agent.NotificationClassification{}
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

// ResolveControlResponse turns the browser's decision into the server's own
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
