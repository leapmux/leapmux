package codex

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ToolNamePlanModePrompt is the tool name of the control request that LeapMux
// itself publishes when a Codex plan-mode turn completes with a plan: the request
// asks the reader whether to execute that plan. Codex sends no such request of its
// own.
const ToolNamePlanModePrompt = "CodexPlanModePrompt"

// codexProvider embeds ProviderDefaults so it inherits the TurnEndToolUses default
// (Codex puts num_tool_uses at the envelope top level, like every shipped
// provider). Override the method here only if Codex's shape diverges.
type codexProvider struct {
	agent.ProviderDefaults
}

// Codex accepts text and image blocks; PDF and binary have no representation in its input.
func (codexProvider) ValidateAttachment(attachment agent.ClassifiedAttachment) error {
	return providerkit.RejectPDFAndBinaryAttachment("codex", attachment)
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
			Name string `json:"name,omitempty"`
			Item *struct {
				Type string `json:"type,omitempty"`
			} `json:"item,omitempty"`
		} `json:"params,omitempty"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return agent.NotificationClassification{}
	}
	switch env.Method {
	case "account/rateLimits/updated":
		return agent.NotificationClassification{
			Kind: agent.NotificationKindProviderScoped,
			Key:  "codex:account/rateLimits/updated",
		}
	case contracts.CodexMethodMcpServerStartupStatusUpdated:
		name := "unknown"
		if env.Params != nil && env.Params.Name != "" {
			name = env.Params.Name
		}
		return agent.NotificationClassification{
			Kind: agent.NotificationKindProviderScoped,
			Key:  "codex:mcpServer/startupStatus/updated:" + name,
		}
	case contracts.CodexMethodItemStarted:
		// Codex emits item/started for many item kinds; only the
		// contextCompaction subtype is consolidatable as a compacting
		// indicator. All other item types route through the per-item
		// handler and never hit PersistNotification.
		if env.Params != nil && env.Params.Item != nil && env.Params.Item.Type == contracts.CodexItemTypeContextCompaction {
			return agent.NotificationClassification{
				Kind: agent.NotificationKindStatus,
				Key:  "codex:item/started:contextCompaction",
			}
		}
		return agent.NotificationClassification{}
	case contracts.CodexMethodItemCompleted:
		// The contextCompaction completion is the Codex compaction boundary:
		// it ends the "Compacting context..." status that the matching
		// item/started opened. Every other item type routes through the
		// per-item handler and never hits PersistNotification.
		if env.Params != nil && env.Params.Item != nil && env.Params.Item.Type == contracts.CodexItemTypeContextCompaction {
			return agent.NotificationClassification{
				Kind: agent.NotificationKindCompactionBoundary,
				Key:  "codex:item/completed:contextCompaction",
			}
		}
		return agent.NotificationClassification{}
	default:
		return agent.NotificationClassification{}
	}
}

func (codexProvider) Merge(class agent.NotificationClassification, previous, next json.RawMessage) (json.RawMessage, error) {
	return next, nil
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
	if toolName == ToolNamePlanModePrompt {
		return agent.PlanModeControlPrompt
	}
	return agent.PlanModeControlNone
}

// PlanModePermissionMode answers for Codex's one plan-mode kind, the prompt. An approval
// that selects no mode returns to Codex's own default approval policy; `acceptEdits` and
// `plan` are Claude words that Codex's `--ask-for-approval` rejects.
func (codexProvider) PlanModePermissionMode(kind agent.PlanModeControlKind) string {
	if kind == agent.PlanModeControlPrompt {
		return DefaultApprovalPolicy
	}
	return ""
}

// PlanApprovalOptions exits plan mode and applies the requested permission choice.
// Only the bypass preset's permission mode enables its network and sandbox settings.
func (codexProvider) PlanApprovalOptions(permissionMode string) map[string]string {
	options := map[string]string{contracts.CodexOptionCollaborationMode: CollaborationDefault}
	if permissionMode == "" {
		return options
	}
	options[agent.OptionIDPermissionMode] = permissionMode
	bypass := contracts.CodexBypassOptions()
	if permissionMode == bypass[agent.OptionIDPermissionMode] {
		for key, value := range bypass {
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
func (codexProvider) SupportsChildSteering() bool { return false }

// SupportsChildInterrupt is true: Multi-Agent V2 interrupts a spawned child
// thread directly. See InterruptChild in subagent.go.
func (codexProvider) SupportsChildInterrupt() bool { return true }

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
