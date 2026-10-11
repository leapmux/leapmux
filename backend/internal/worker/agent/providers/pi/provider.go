package pi

import (
	"context"
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// piProvider consolidates lifecycle notifications and recognizes the interrupt frame.
// Repeated compaction and retry events replace earlier status events.
// Each extension error stays separate so the transcript preserves partial failures.
type piProvider struct {
	agent.ProviderDefaults
}

// Pi accepts text and image blocks. Its input cannot represent PDF or binary attachments.
func (piProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	return providerkit.RejectPDFAndBinaryAttachment("pi", attachment)
}

// ListStoredSessions reads Pi's native transcripts from sessions.go.
// It returns the session ID that Pi reports at runtime.
// The worker uses that ID to identify duplicate session records.
func (piProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return piStoredSessions(ctx, q)
}

// ResolveResumeHandle accepts a session file path or a session ID.
// Pi's --session flag resolves both forms. A separator or .jsonl suffix identifies a path.
// Other values match the session IDs of the current working directory.
// The shared validator applies each form's rules separately.
// A token validator rejects file paths. A path validator rejects native session IDs.
func (piProvider) ResolveResumeHandle(handle, homeDir string) (string, error) {
	return providerkit.ResolveSessionFileOrIDHandle(handle, homeDir)
}

func (piProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	var env struct {
		Type    string                     `json:"type"`
		Aborted bool                       `json:"aborted"`
		Result  map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return agent.NotificationClassification{}
	}
	switch env.Type {
	case contracts.PiEventCompactionEnd:
		if env.Aborted || env.Result == nil {
			// A failed end replaces its start status without creating a boundary.
			return agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: "pi:" + contracts.PiEventCompactionStart}
		}
		// Each completed boundary stays in the transcript.
		return agent.NotificationClassification{Kind: agent.NotificationKindCompactionBoundary, Key: "pi:" + contracts.PiEventCompactionEnd}
	case contracts.PiEventCompactionStart:
		// Replace the earlier status so the UI shows one compaction indicator.
		return agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: "pi:" + contracts.PiEventCompactionStart}
	case contracts.PiEventAutoRetryStart, contracts.PiEventAutoRetryEnd:
		return agent.NotificationClassification{Kind: agent.NotificationKindAPIRetry, Key: "pi:" + env.Type}
	default:
		return agent.NotificationClassification{}
	}
}

func (piProvider) Merge(class agent.NotificationClassification, previous, next json.RawMessage) (json.RawMessage, error) {
	return next, nil
}

func (piProvider) IsInterrupt(content string) bool {
	var msg struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return false
	}
	return msg.Type == "abort"
}

// Pi reads extension_ui_response from stdin. It does not repeat the answer on stdout.
func (piProvider) IsSelfDisplayingControlTool(string) bool { return false }

func (piProvider) PlanModeControl(string) agent.PlanModeControlKind { return agent.PlanModeControlNone }

// Pi has no plan-mode-prompt flow, so it settles no options on approval.
func (piProvider) PlanApprovalOptions(string) map[string]string { return nil }

// SyntheticInterruptNotice returns no notice because Pi records the abort in its own transcript.
func (piProvider) SyntheticInterruptNotice() string { return "" }

// PermissionModeFromRawInput: Pi has no set_permission_mode raw control frame.
func (piProvider) PermissionModeFromRawInput(string) (string, bool) { return "", false }
