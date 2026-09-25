package pi

import (
	"context"
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// piProvider collapses Pi's lifecycle notifications and recognizes
// Pi's interrupt frame. Pi emits compaction_start/end whenever a turn
// crosses the compaction threshold; without consolidation, long sessions
// accumulate one notification per cycle. auto_retry_start/end follow the
// same pattern as Claude's api_retry. extension_error stays
// unconsolidated: each error message is meaningful and merging would hide
// partial failures.
type piProvider struct {
	agent.ProviderDefaults
}

// Pi accepts text and image blocks; PDF and binary have no representation in its input.
func (piProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	return providerkit.RejectPDFAndBinaryAttachment("pi", attachment)
}

func (piProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	if result, ok := resolvePiMCPApprovalResponse(ctx); ok {
		return result
	}
	return agent.DefaultControlResponseResolution(ctx)
}

// ListStoredSessions reads Pi's own transcripts; see sessions.go. It returns
// each session's ID rather than its file path, which is the form Pi reports at
// runtime and therefore the form that dedupes against the worker's own record.
func (piProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return piStoredSessions(ctx, q)
}

// ResolveResumeHandle takes EITHER a session file PATH or a session ID.
//
// Pi identifies one session two ways, and `pi --session <path|id>` resolves
// both: a value that holds a separator or ends in `.jsonl` is a path, and
// anything else is matched against the session IDs of this working directory.
// The worker hands the handle to that flag (see `piResumeArgs`), so both shapes
// are legitimate input here. providerkit.ResolveSessionFileOrIDHandle states the
// two rules and why each shape needs its own; Oh My Pi's `--resume` resolves the
// same two shapes with the same test.
//
// One rule for both refused a legitimate Pi resume: the token rule refused every
// session file with "session ID contains invalid characters", and the path rule
// refused the identifier Pi itself reports with "path must be absolute".
func (piProvider) ResolveResumeHandle(handle, homeDir string) (string, error) {
	return providerkit.ResolveSessionFileOrIDHandle(handle, homeDir)
}

func (piProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return agent.NotificationClassification{}
	}
	switch env.Type {
	case contracts.PiEventCompactionEnd:
		// The boundary signal — repeated boundaries collapse so the chat
		// shows one marker for "the conversation was compacted at this
		// point", not a sequence.
		return agent.NotificationClassification{Kind: agent.NotificationKindCompactionBoundary, Key: "pi:" + contracts.PiEventCompactionEnd}
	case contracts.PiEventCompactionStart:
		// In-progress indicator. Latest wins so the UI shows "compacting…"
		// once, not once per attempt.
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

// Pi consumes extension_ui_response on stdin without echoing the answer to stdout,
// so it never self-displays a control answer.
func (piProvider) IsSelfDisplayingControlTool(string) bool { return false }

func (piProvider) PlanModeControl(string) agent.PlanModeControlKind { return agent.PlanModeControlNone }

// Pi has no plan-mode-prompt flow, so it settles no options on approval.
func (piProvider) PlanApprovalOptions(string) map[string]string { return nil }

// SyntheticInterruptNotice: Pi's abort surfaces in its own transcript, so no synthetic notice is
// persisted for a forwarded interrupt frame.
func (piProvider) SyntheticInterruptNotice() string { return "" }

// PermissionModeFromRawInput: Pi has no set_permission_mode raw control frame.
func (piProvider) PermissionModeFromRawInput(string) (string, bool) { return "", false }
