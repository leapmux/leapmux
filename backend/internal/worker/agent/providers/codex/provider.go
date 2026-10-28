package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/leapmux/leapmux/internal/util/optionmap"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// codexProvider embeds ProviderDefaults so it inherits the TurnEndToolUses default
// (Codex puts num_tool_uses at the envelope top level, like every shipped
// provider). Override the method here only if Codex's shape diverges.
type codexProvider struct {
	agent.ProviderDefaults
}

// Codex accepts audio on models whose native catalog declares audio input.
// The input writer checks that model capability before native delivery.
func (codexProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	if attachment.Kind == contracts.AttachmentMediaKindAudio {
		if !codexAudioMIME(attachment.MIMEType) {
			return fmt.Errorf("codex does not support audio format %q", attachment.MIMEType)
		}
		if len(attachment.Data) == 0 {
			return fmt.Errorf("audio attachment %q is empty", attachment.Filename)
		}
		return nil
	}
	return providerkit.ValidateTextAndImageAttachment("codex", attachment)
}

func (p codexProvider) ResolveControlResponse(ctx agent.ControlResponseContext) agent.ControlResponseResolution {
	if result, ok := providerkit.ResolveMCPElicitationResponse(ctx, contracts.MCPElicitationMethodCodex, codexMCPElicitationMeta); ok {
		return result
	}
	res := agent.DefaultControlResponseResolution(ctx)
	if len(ctx.RequestPayload) == 0 {
		return res
	}
	res.PlanModeControl = p.PlanModeControl(ctx.ToolName)
	switch ctx.ToolName {
	case contracts.CodexControlToolPlanModePrompt:
		// LeapMux published this request itself. No Codex request waits for an
		// answer to it.
		res.LeapMuxOwned = true
	case contracts.CodexControlToolAsyncQuestion:
		return resolveCodexAsyncQuestionResponse(res, ctx.ResponseContent)
	}
	return res
}

// ListStoredSessions reads Codex's own rollout index; see sessions.go.
func (codexProvider) ListStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return codexStoredSessions(ctx, q)
}

func (codexProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	var env struct {
		Method string `json:"method"`
		Params *struct {
			Name      string `json:"name,omitempty"`
			WillRetry bool   `json:"willRetry,omitempty"`
			TurnID    string `json:"turnId,omitempty"`
			Item      *struct {
				Type string `json:"type,omitempty"`
			} `json:"item,omitempty"`
		} `json:"params,omitempty"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return agent.NotificationClassification{}
	}
	switch env.Method {
	case contracts.CodexMethodError:
		// Only the retryable error persists. A burst folds into the latest
		// attempt, as Claude's api_retry does.
		if env.Params != nil && env.Params.WillRetry {
			return agent.APIRetryNotification()
		}
		return agent.NotificationClassification{}
	case contracts.CodexMethodModelSafetyBufferingUpdated:
		// Repeated buffering updates of one turn fold into one row.
		turnID := "unknown"
		if env.Params != nil && env.Params.TurnID != "" {
			turnID = env.Params.TurnID
		}
		return agent.ProviderScopedNotification("codex:model/safetyBuffering/updated:" + turnID)
	case contracts.CodexMethodMcpServerStartupStatusUpdated:
		name := "unknown"
		if env.Params != nil && env.Params.Name != "" {
			name = env.Params.Name
		}
		return agent.ProviderScopedNotification("codex:mcpServer/startupStatus/updated:" + name)
	case contracts.CodexMethodItemCompleted:
		// A completed compaction is a boundary. Its start exists only as live state.
		// Other completed items use their own message handlers.
		if env.Params != nil && env.Params.Item != nil && env.Params.Item.Type == contracts.CodexItemTypeContextCompaction {
			return agent.CompactionBoundaryNotification()
		}
		return agent.NotificationClassification{}
	default:
		return agent.NotificationClassification{}
	}
}

func (codexProvider) IsInterrupt(content string) bool {
	var msg struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal([]byte(content), &msg); err != nil {
		return false
	}
	return msg.Method == "turn/interrupt"
}

// Codex consumes control responses internally (only a serverRequest/resolved
// metadata notification returns), so it never self-displays the answer.
func (codexProvider) IsSelfDisplayingControlTool(string) bool { return false }

func (codexProvider) PlanModeControl(toolName string) agent.PlanModeControlKind {
	if toolName == contracts.CodexControlToolPlanModePrompt {
		return agent.PlanModeControlPrompt
	}
	return agent.PlanModeControlNone
}

// PlanModePermissionMode answers for Codex's one plan-mode kind, the prompt. An approval
// that selects no mode returns to Codex's own default permission mode; `acceptEdits` and
// `plan` are Claude words that Codex does not accept.
func (codexProvider) PlanModePermissionMode(kind agent.PlanModeControlKind) string {
	if kind == agent.PlanModeControlPrompt {
		return DefaultPermissionMode
	}
	return ""
}

// PlanApprovalOptions exits plan mode and applies the requested permission choice. A
// plan approval carries only the permission mode, so a mode that a permission preset
// owns selects that complete preset: Approve for Me also selects the workspace
// profile, and Never Ask also selects full access. Any other mode changes only the
// permission mode. contracts/codex-permission-presets.json gives each preset its own
// mode, so the lookup is unambiguous.
func (codexProvider) PlanApprovalOptions(permissionMode string) map[string]string {
	options := map[string]string{contracts.CodexOptionCollaborationMode: CollaborationDefault}
	if permissionMode == "" {
		return options
	}
	options[agent.OptionIDPermissionMode] = permissionMode
	for _, preset := range contracts.CodexPermissionPresets() {
		if preset[agent.OptionIDPermissionMode] != permissionMode {
			continue
		}
		for key, value := range preset {
			options[key] = value
		}
	}
	return options
}

// SyntheticInterruptNotice: Codex resolves turn/interrupt internally and emits only a
// serverRequest/resolved metadata notification -- never a transcript row -- so the service
// persists this synthetic row to record the interrupt. The literal's single home lives here.
func (codexProvider) SyntheticInterruptNotice() string { return "[Request interrupted by user]" }

// PermissionModeFromRawInput: Codex has no set_permission_mode raw control frame.
func (codexProvider) PermissionModeFromRawInput(string) (string, bool) { return "", false }

// Multi-Agent V2 rejects direct app-server input for spawned child threads.

// ChildCapabilities.AcceptsInterrupt is true: Multi-Agent V2 interrupts a spawned child
// thread directly. See InterruptChild in subagent.go.

// ReportsDefaultModelSentinel is false: Codex stores the sentinel until the
// thread/start lifecycle response reports a concrete model, and model/list never
// returns it, so Codex badges the model the CLI itself marks.
func (codexProvider) ReportsDefaultModelSentinel() bool { return false }

// codexMCPElicitationMeta admits the approval scope Codex offers on an MCP
// tool-call or tool-suggestion elicitation. The request's
// `_meta.codex_approval_kind` states which approval it is, and its
// `_meta.persist` lists the scopes it accepts; an answer may name only one of
// those, and only a session or an always scope.
func codexMCPElicitationMeta(requestPayload json.RawMessage, persist string) bool {
	var request struct {
		Params struct {
			Meta struct {
				Kind    string   `json:"codex_approval_kind"`
				Persist []string `json:"persist"`
			} `json:"_meta"`
		} `json:"params"`
	}
	if json.Unmarshal(requestPayload, &request) != nil {
		return false
	}
	meta := request.Params.Meta
	if meta.Kind != contracts.MCPElicitationApprovalKindToolCall && meta.Kind != contracts.MCPElicitationApprovalKindToolSuggestion {
		return false
	}
	if persist != contracts.MCPElicitationApprovalScopeSession && persist != contracts.MCPElicitationApprovalScopeAlways {
		return false
	}
	return slices.Contains(meta.Persist, persist)
}

// ChildCapabilities states the native child operations that this provider supports.
func (codexProvider) ChildCapabilities(optionmap.Map) agent.ChildCapabilities {
	return agent.ChildCapabilities{AcceptsMessages: false, AcceptsInterrupt: true}
}
