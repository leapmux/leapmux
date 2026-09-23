package pi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/util/validate"
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

// piResumeHandleIsFilePath reports whether a Pi resume handle identifies a session
// FILE rather than a session ID.
//
// The test copies Pi's own resolver (`resolveSessionPath` in pi's main.ts): a
// separator anywhere, or the `.jsonl` suffix. The two answers must stay
// identical, because this decides which rule validates a handle and Pi decides
// which lookup consumes it. A value that one reads as a path and the other as
// an ID is validated against a rule that does not describe what happens to it.
func piResumeHandleIsFilePath(handle string) bool {
	return strings.ContainsAny(handle, `/\`) || strings.HasSuffix(handle, ".jsonl")
}

// ResolveResumeHandle takes EITHER a session file PATH or a session ID.
//
// Pi identifies one session two ways, and `pi --session <path|id>` resolves
// both: a value that holds a separator or ends in `.jsonl` is a path, and
// anything else is matched against the session IDs of this working directory.
// The worker hands the handle to that flag (see `piResumeArgs`), so both shapes
// are legitimate input here.
//
// Two shapes need two rules, and each rule refuses the other shape. A path is
// not a token: a Windows path holds `\`, which the token class bans, and a real
// Pi session path -- an escaped copy of the working directory plus a
// timestamped file name -- runs past the 128-byte token cap, so the token rule
// refused every legitimate session file with "session ID contains invalid
// characters". An ID is not a path: it is relative by construction, so the path
// rule refused the identifier Pi itself reports with "path must be absolute".
//
// A path is still a value a user pastes into a field, so it is not unchecked:
// `SanitizePath` answers the traversal, the reserved device name and the
// absolute-path questions that a path raises, and the byte cap is the token
// cap's counterpart for the longer shape. The empty handle means "no resume"
// and is accepted, exactly as the token rule accepts it.
//
// The PATH shape returns SanitizePath's result, not the handle. SanitizePath
// normalizes before it judges -- it drops control characters, trims edge
// whitespace, expands `~` and cleans the path -- so the string it approved and
// the string the user typed differ whenever any of those applied. Pi's
// SessionManager.open does not require the file to exist, so sending the typed
// string started an EMPTY session at a filename that had a stray control
// character in it, and the user's conversation was simply gone. Returning the
// approved string removes the gap rather than restating the rule at the sink.
func (piProvider) ResolveResumeHandle(handle, homeDir string) (string, error) {
	if handle == "" {
		return "", nil
	}
	if !piResumeHandleIsFilePath(handle) {
		if err := validate.ValidateSessionID(handle); err != nil {
			return "", err
		}
		return handle, nil
	}
	// Measured before SanitizePath, which expands `~` and can therefore only
	// make the value longer than what the user typed.
	if len(handle) > contracts.SessionFilePathByteLimit {
		return "", fmt.Errorf("session file path: must be at most %d bytes", contracts.SessionFilePathByteLimit)
	}
	// An invisible-format character survives SanitizePath -- U+200B is Cf, not
	// a control character -- so a path that carries one would reach Pi and open
	// a different file. The token rule refuses the same class, and refusing it
	// here keeps one answer for both shapes of one field.
	if err := validate.RefuseInvisibleSessionChars(handle); err != nil {
		return "", fmt.Errorf("session file path: %w", err)
	}
	sanitized, err := validate.SanitizePath(handle, homeDir)
	if err != nil {
		return "", fmt.Errorf("session file path: %w", err)
	}
	return sanitized, nil
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
