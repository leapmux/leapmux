package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Claude Code's tool names that reach LeapMux as control requests.
const (
	ToolNameAskUserQuestion = "AskUserQuestion"
	ToolNameEnterPlanMode   = "EnterPlanMode"
	ToolNameExitPlanMode    = "ExitPlanMode"
)

// EffortUltracode is LeapMux's internal name for the Claude CLI's xhigh+ultracode
// combo. At the provider wire boundary it maps to {effortLevel:"xhigh",
// ultracode:true}; the CLI's --effort launch flag does not accept it.
const EffortUltracode = "ultracode"

type claudeProvider struct {
	agent.ProviderDefaults
}

// Claude Code accepts text, image, and PDF blocks but has no binary content block.
func (claudeProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if attachment.Kind == agent.AttachmentKindBinary {
		return fmt.Errorf("claude code does not support binary attachments: %s", attachment.Filename)
	}
	return nil
}

func (p claudeProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	res := agent.DefaultControlResponseResolution(ctx)
	if isClaudeMCPElicitation(ctx.RequestPayload) {
		return res
	}
	res.SelfDisplayed = p.IsSelfDisplayingControlTool(ctx.ToolName)
	res.PlanModeControl = p.PlanModeControl(ctx.ToolName)
	if !res.Withhold && res.PlanModeControl == agent.PlanModeControlExit && ctx.PlanApproval.GetPermissionMode() != "" && !ctx.PlanApproval.GetClearContext() {
		if _, behavior, _, ok := agent.DecodeControlBehavior(res.Content); ok && behavior == agent.ControlBehaviorAllow {
			content, err := applyClaudePlanPermission(res.Content, ctx.PlanApproval.GetPermissionMode())
			if err != nil {
				res.Withhold = true
			} else {
				res.Content = content
			}
		}
	}
	return res
}

func isClaudeMCPElicitation(payload json.RawMessage) bool {
	var root struct {
		Request struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	return json.Unmarshal(payload, &root) == nil && root.Request.Subtype == contracts.MCPElicitationSubtypeClaude
}

// ReportsDefaultModelSentinel is true: the Claude CLI lists a "default" entry in
// its own initialize response, and convertClaudeModels owns that reserved id, so
// the sentinel is a real selectable option that tracks the account's default
// across plan tiers.
func (claudeProvider) ReportsDefaultModelSentinel() bool { return true }

// ListStoredSessions reads Claude Code's own transcripts; see
// sessions.go.
func (claudeProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return claudeStoredSessions(ctx, q)
}

func (claudeProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	var env struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return agent.NotificationClassification{}
	}
	switch env.Type {
	case contracts.NotificationTypeRateLimitEvent:
		// Consolidate by keeping only the latest rate-limit snapshot in
		// the thread; older entries collapse so the UI shows one current
		// status, not a wall of repeated tier updates.
		return agent.NotificationClassification{Kind: agent.NotificationKindProviderScoped, Key: "claude:rate_limit_event"}
	case "system":
		// fall through to the subtype switch below
	default:
		return agent.NotificationClassification{}
	}
	switch env.Subtype {
	case "status":
		return agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: "claude:system:status"}
	case "api_retry":
		return agent.NotificationClassification{Kind: agent.NotificationKindAPIRetry, Key: "claude:system:api_retry"}
	case "compact_boundary", "microcompact_boundary":
		return agent.NotificationClassification{Kind: agent.NotificationKindCompactionBoundary, Key: "claude:system:" + env.Subtype}
	default:
		return agent.NotificationClassification{}
	}
}

func (claudeProvider) Merge(class agent.NotificationClassification, previous, next json.RawMessage) (json.RawMessage, error) {
	return next, nil
}

func (claudeProvider) IsInterrupt(content string) bool {
	var msg struct {
		Request struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return false
	}
	return msg.Request.Subtype == "interrupt"
}

// Claude re-emits AskUserQuestion / ExitPlanMode answers as a user-envelope
// tool_result in its own transcript, so the rail marks that ingested row directly
// (claudeUserEnvelopeMarkType) and no synthetic display row is persisted for them. The single
// home for this set, shared by the mark classifier and the synthetic-row skip.
func (claudeProvider) IsSelfDisplayingControlTool(name string) bool {
	return name == ToolNameAskUserQuestion || name == ToolNameExitPlanMode
}

func (claudeProvider) PlanModeControl(toolName string) agent.PlanModeControlKind {
	switch toolName {
	case ToolNameEnterPlanMode:
		return agent.PlanModeControlEnter
	case ToolNameExitPlanMode:
		return agent.PlanModeControlExit
	default:
		return agent.PlanModeControlNone
	}
}

// PlanModePermissionMode gives Claude Code's own two modes. An approved exit lands on
// `acceptEdits`, which is what the plan banner's unchecked state means for Claude: run
// the plan, and do not ask again for each edit.
func (claudeProvider) PlanModePermissionMode(kind agent.PlanModeControlKind) string {
	switch kind {
	case agent.PlanModeControlEnter:
		return contracts.ClaudeModePlan
	case agent.PlanModeControlExit:
		return contracts.ClaudeModeAcceptEdits
	case agent.PlanModeControlNone, agent.PlanModeControlPrompt:
		return ""
	}
	return ""
}

// Claude's plan flow is EnterPlanMode/ExitPlanMode (never PlanModeControlPrompt), so no
// plan-approval option settlement runs for it.
func (claudeProvider) PlanApprovalOptions(string) map[string]string { return nil }

// EndsSubagentTranscript recognizes Claude's final `{"type":"result",...}`.
// With --forward-subagent-text a subagent's own result is forwarded into the
// child transcript, and a Claude subagent gets exactly one, so a subagent that
// runs to completion already closes itself and needs no neutral divider
// stacked on top. A subagent stopped mid-flight forwards no result, so its
// transcript does not end here and still gets the neutral divider.
func (claudeProvider) EndsSubagentTranscript(content []byte) bool {
	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(content, &env); err != nil {
		return false
	}
	return env.Type == claudeMsgTypeResult
}

// SyntheticInterruptNotice: Claude's interrupt surfaces in its own transcript, so no synthetic
// notice is persisted for a forwarded interrupt frame.
func (claudeProvider) SyntheticInterruptNotice() string { return "" }

// PermissionModeFromRawInput parses Claude's set_permission_mode control_request
// ({"request":{"subtype":"set_permission_mode","mode":"..."}}) and returns the requested mode.
// Returns ("", false) when the frame isn't a set_permission_mode request. The service eagerly
// writes the returned mode to the DB (so /clear, which reads the DB, sees the latest mode -- Claude
// doesn't echo the mode back in its control_response) and still forwards the raw frame to the
// subprocess.
func (claudeProvider) PermissionModeFromRawInput(content string) (string, bool) {
	if !strings.Contains(content, "set_permission_mode") {
		return "", false
	}
	var msg struct {
		Request struct {
			Subtype string `json:"subtype"`
			Mode    string `json:"mode"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return "", false
	}
	if msg.Request.Subtype != "set_permission_mode" || msg.Request.Mode == "" {
		return "", false
	}
	return msg.Request.Mode, true
}
