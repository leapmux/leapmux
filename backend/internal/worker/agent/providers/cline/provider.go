package cline

import (
	"context"
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// clineProvider is the stateless wire-format plugin for Cline. It answers the
// questions the service layer asks without a running agent.
//
// Every question it answers with the neutral default is one Cline gives no
// provider-specific answer to: no interrupt frame (a hub command interrupts
// Cline), no control frame on the wire, a session id that the token rule
// accepts, no to-do list, and a turn-end row whose tool count rides in the
// worker's metadata.
type clineProvider struct {
	agent.ProviderDefaults
	// cli finds the `cline` program that lists the stored sessions. The zero
	// value finds the one on the user's PATH, as clineLocator does. A test
	// states the fake program by its absolute path, so the reader can never
	// reach the user's real Cline data, whatever the shell's profile puts on
	// PATH.
	cli launch.Locator
}

// Classify groups the notices that repeat within one notification thread:
//
//   - A compaction's start and its skip are one status, which its completion
//     replaces as the context boundary.
//   - A retry of a failed model call replaces the retry before it.
//   - Every other status notice replaces the status before it.
//   - The run events of one teammate run fold into the latest.
func (clineProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	var envelope struct {
		Event   string `json:"event"`
		Payload struct {
			Metadata struct {
				Kind  string `json:"kind"`
				Phase string `json:"phase"`
			} `json:"metadata"`
			LastEvent struct {
				RunID string `json:"runId"`
			} `json:"lastEvent"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return agent.NotificationClassification{}
	}
	switch envelope.Event {
	case contracts.ClineEventSessionNotice:
		return classifyNotice(envelope.Payload.Metadata.Kind, envelope.Payload.Metadata.Phase)
	case contracts.ClineEventTeamProgress:
		if runID := envelope.Payload.LastEvent.RunID; runID != "" {
			return agent.NotificationClassification{Kind: agent.NotificationKindProviderScoped, Key: teamRowPrefix + runID}
		}
	}
	return agent.NotificationClassification{}
}

// noticeKey groups the notices of Cline's compactions.
const noticeKey = "cline:compaction"

// classifyNotice classifies one `session.notice` by its kind and phase.
func classifyNotice(kind, phase string) agent.NotificationClassification {
	switch kind {
	case contracts.ClineNoticeKindAutoCompaction, contracts.ClineNoticeKindManualCompaction, contracts.ClineNoticeKindOverflowRecoveryCompaction:
		if phase == contracts.ClineNoticePhaseCompleted {
			return agent.NotificationClassification{Kind: agent.NotificationKindCompactionBoundary, Key: noticeKey}
		}
		return agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: noticeKey}
	case contracts.ClineNoticeKindProviderErrorRetry:
		return agent.NotificationClassification{Kind: agent.NotificationKindAPIRetry, Key: "cline:retry"}
	default:
		return agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: "cline:status"}
	}
}

// PlanModeControl reads the plan tool. Its approval is the plan approval, and
// Cline has no tool that enters plan mode: the user picks Plan on the mode
// axis.
func (clineProvider) PlanModeControl(toolName string) agent.PlanModeControlKind {
	if toolName == contracts.ClineToolSwitchToActMode {
		return agent.PlanModeControlExit
	}
	return agent.PlanModeControlNone
}

// PlanModePermissionMode states the two ends of plan mode on LeapMux's axis.
// An approved plan switches to Act, Cline's own mode after a plan, when the
// user picked no other mode.
func (clineProvider) PlanModePermissionMode(kind agent.PlanModeControlKind) string {
	switch kind {
	case agent.PlanModeControlEnter:
		return contracts.ClinePermissionModePlan
	case agent.PlanModeControlExit:
		return contracts.ClinePermissionModeAct
	case agent.PlanModeControlNone, agent.PlanModeControlPrompt:
		return ""
	}
	return ""
}

// ResolveControlResponse turns the browser's decision into Cline's own answer.
// See control.go.
func (clineProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	return resolveControlResponse(ctx)
}

// ValidateAttachment accepts text and images. See attachments.go for what Cline
// refuses. The browser plugin's attachment policy states the same rule.
func (clineProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	return validateAttachment(attachment)
}

// ListStoredSessions reads Cline's own session history. See sessions.go.
func (p clineProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	cli := p.cli
	if !cli.Valid() {
		cli = clineLocator
	}
	return storedSessions(ctx, cli, q)
}
