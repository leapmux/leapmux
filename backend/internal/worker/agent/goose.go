package agent

import (
	"context"
	"encoding/json"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const (
	GooseCLIModeAuto         = "auto"
	GooseCLIModeApprove      = "approve"
	GooseCLIModeSmartApprove = "smart_approve"
	GooseCLIModeChat         = "chat"
)

// Goose's server-driven ACP config-option ids (surfaced as mutable option groups,
// not static templates). Declared in KnownOptionIDs so a not-running agent validates
// them, matching the ids the live `session/set_config_option` channel reports. Goose
// has no well-known "effort" axis -- its reasoning axis is the config option "thinking_effort".
const (
	GooseConfigThinkingEffort = "thinking_effort"
	GooseConfigProvider       = "provider"
)

// GooseCLIAgent manages a single Goose CLI ACP process.
type GooseCLIAgent struct {
	acpBase
}

// StartGooseCLI starts a Goose CLI ACP agent process and performs the handshake.
func StartGooseCLI(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	return acpStart(ctx, opts, sink, acpStartSpec[GooseCLIAgent]{
		provider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		providerName: "goose",
		binaryName:   "goose",
		baseArgs:     []string{"acp"},
		newAgent:     func() *GooseCLIAgent { return &GooseCLIAgent{} },
		base:         func(a *GooseCLIAgent) *acpBase { return &a.acpBase },
		configure: func(a *GooseCLIAgent) {
			a.modeChannel = modeChannelPermissionMode
			// Goose's reasoning-effort axis is the convention id "thinking_effort", not the
			// well-known "effort" -- declare it so the env-effort override maps onto it.
			a.effortConfigID = GooseConfigThinkingEffort
			// Subagent tool-request observations: Goose surfaces tool REQUESTS
			// (never results) over ACP via _meta.toolNotification, so the hook
			// runs on tool_call_update. The spawn tool_call's terminal update
			// closes the registry row. The spawn tool_call itself carries
			// _meta.goose.toolCall {toolName:"delegate", extensionName:"summon"}.
			a.subagentFromToolCall = gooseSubagentFromToolCall
			a.subagentFromToolCallUpdate = gooseSubagentFromToolCallUpdate
		},
		afterHandshake: func(a *GooseCLIAgent, handshake *acpSessionResult, opts Options) error {
			return a.applyPermissionModeStartup(handshake, opts, GooseCLIModeAuto, opts.Model())
		},
	})
}

func fallbackGooseCLIModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: GooseCLIModeAuto, Name: "Auto"},
		{Id: GooseCLIModeApprove, Name: "Approve"},
		{Id: GooseCLIModeSmartApprove, Name: "Smart Approve"},
		{Id: GooseCLIModeChat, Name: "Chat"},
	}
}

// gooseSubagentFromToolCall detects Goose's spawn tool_call by the structured
// _meta.goose.toolCall marker {toolName:"delegate", extensionName:"summon"}.
// This is the spawn detector (NOT title guessing). Registry-only here -- the
// child transcript is fed by the tool-request updates; the spawn tool_call
// itself just registers a running row with the spawn title.
func gooseSubagentFromToolCall(tc acpToolCallEnvelope) *acpSubagentObservation {
	if len(tc.Meta) == 0 {
		return nil
	}
	var meta struct {
		Goose struct {
			ToolCall struct {
				ToolName      string `json:"toolName"`
				ExtensionName string `json:"extensionName"`
			} `json:"toolCall"`
		} `json:"goose"`
	}
	if err := json.Unmarshal(tc.Meta, &meta); err != nil {
		return nil
	}
	tc2 := meta.Goose.ToolCall
	if tc2.ToolName != "delegate" || tc2.ExtensionName != "summon" {
		return nil
	}
	title := tc.Title
	if title == "" {
		title = "Goose subagent"
	}
	return &acpSubagentObservation{
		RowKey: tc.ToolCallID,
		Title:  title,
		Status: bgtask.StatusRunning,
		// Goose's delegate tool names its task `instructions`, not `prompt`
		// (crates/goose/src/agents/platform_extensions/summon.rs). The child
		// transcript is created later, on the first forwarded tool request, so
		// applySubagentObservation holds this until then.
		Prompt: gooseDelegateInstructions(tc.RawInput),
	}
}

// gooseDelegateInstructions pulls the delegate call's task text out of the
// tool_call's rawInput. Goose fills raw_input from the tool arguments
// (acp/server/tool_calls/conversion.rs), so the delegate arguments arrive
// verbatim. Returns "" when absent.
func gooseDelegateInstructions(rawInput json.RawMessage) string {
	if len(rawInput) == 0 {
		return ""
	}
	var in struct {
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return ""
	}
	return in.Instructions
}

// gooseSubagentFromToolCallUpdate observes Goose's subagent tool requests.
// Goose surfaces tool REQUESTS (never results) over ACP via a two-level-nested
// _meta payload: toolNotification.type is "message"; the discriminator is
// params.data.type == "subagent_tool_request". Each request carries the
// subagent_id and the tool_call name, so we upsert a running row with activity
// "tool: <name>" and persist the raw request to the child transcript. The
// spawn tool_call's terminal update closes the registry row.
//
// The registry row, the EnsureChildAgent linkage, and the terminal close all
// key off the SPAWN toolCallId: the terminal spawn update carries only
// toolCallId, so the row must live under that key, and ChildAgentKey must
// match it or EnsureChildAgent would open a second row keyed by subagent_id
// that the close never reaches.
func gooseSubagentFromToolCallUpdate(tcu acpToolCallUpdateEnvelope) *acpSubagentObservation {
	// Terminal update on the spawn tool_call itself -> close the registry row.
	// Goose's terminal spawn update carries no _meta, but the row was created
	// (by the tool_call or a tool-request) under this toolCallId, so closing on
	// the terminal update is correct. CloseRow is idempotent: a plain tool with
	// no registry row is a no-op (the upsert path finds no row to close).
	if tcu.Status == "completed" || tcu.Status == "failed" || tcu.Status == "cancelled" {
		return &acpSubagentObservation{
			RowKey:   tcu.ToolCallID,
			Status:   acpTerminalStatus(tcu.Status),
			CloseRow: true,
			Mode:     acpModeCloseOnly,
		}
	}
	if len(tcu.Meta) == 0 {
		return nil
	}
	var meta struct {
		ToolNotification struct {
			Type   string          `json:"type"`
			Params json.RawMessage `json:"params"`
		} `json:"toolNotification"`
	}
	if err := json.Unmarshal(tcu.Meta, &meta); err != nil || meta.ToolNotification.Type != "message" {
		return nil
	}
	var params struct {
		Data struct {
			Type       string `json:"type"`
			SubagentID string `json:"subagent_id"`
			ToolCall   struct {
				Name string `json:"name"`
			} `json:"tool_call"`
		} `json:"data"`
	}
	if err := json.Unmarshal(meta.ToolNotification.Params, &params); err != nil {
		return nil
	}
	if params.Data.Type != "subagent_tool_request" {
		return nil
	}
	// Key the registry row, the child-agent linkage, AND the terminal close off
	// the SPAWN toolCallId. The terminal spawn update knows only toolCallId, so
	// the row must live under that key; ChildAgentKey must match it too, or
	// EnsureChildAgent would upsert a SECOND row keyed by subagent_id that the
	// close never reaches (orphaned Running row). One Goose spawn = one child =
	// one transcript, so toolCallId is the correct stable child identity here;
	// the per-request subagent_id is not used as a registry key.
	childKey := tcu.ToolCallID
	activity := "tool request"
	if params.Data.ToolCall.Name != "" {
		activity = "tool: " + params.Data.ToolCall.Name
	}
	// Persist the tool-request update to the child transcript (PersistChildMessage
	// via applySubagentObservation). Goose only ever ships requests, so this is
	// the live child activity. The payload is a tool_call_update-shaped envelope
	// carrying sessionUpdate + status + _meta so the shared ACP classifier
	// recognizes it and routes it to the subagent-tool-request renderer (a plain
	// re-marshal of the parsed struct drops sessionUpdate, leaving the classifier
	// no arm to match and the row renders as a raw-JSON dump).
	payload := gooseSubagentToolRequestPayload(tcu, meta.ToolNotification.Params)
	return &acpSubagentObservation{
		RowKey:                 tcu.ToolCallID,
		Title:                  "Goose subagent",
		Activity:               activity,
		Status:                 bgtask.StatusRunning,
		ChildAgentKey:          childKey,
		ChildTranscriptPayload: payload,
	}
}

// gooseSubagentToolRequestPayload builds the child-transcript payload for a
// Goose subagent tool-request update. It re-marshals the on-the-wire envelope
// (including sessionUpdate/status/_meta) rather than the parsed struct so the
// shared frontend ACP classifier recognizes the row as a tool_call_update and
// routes it to the subagent-tool-request renderer instead of falling through to
// the raw-JSON last resort.
func gooseSubagentToolRequestPayload(tcu acpToolCallUpdateEnvelope, notificationParams json.RawMessage) []byte {
	type toolRequestUpdate struct {
		SessionUpdate string          `json:"sessionUpdate"`
		ToolCallID    string          `json:"toolCallId"`
		Status        string          `json:"status"`
		Kind          string          `json:"kind,omitempty"`
		Meta          json.RawMessage `json:"_meta,omitempty"`
	}
	// Re-wrap the original _meta (which carries toolNotification.params.data
	// at _meta.toolNotification) verbatim so the renderer can read the tool
	// name from params.data.tool_call.name.
	meta := tcu.Meta
	if len(meta) == 0 {
		// Fall back to a synthesized _meta if the envelope somehow lost it.
		meta = []byte(`{"toolNotification":{"type":"message","params":` + string(notificationParams) + `}}`)
	}
	payload, err := json.Marshal(toolRequestUpdate{
		SessionUpdate: "tool_call_update",
		ToolCallID:    tcu.ToolCallID,
		Status:        tcu.Status,
		Kind:          tcu.Title,
		Meta:          meta,
	})
	if err != nil {
		slog.Warn("goose subagent marshal transcript payload failed", "error", err)
		return []byte{}
	}
	return payload
}

func init() {
	// model + permissionMode (static group) + Goose's server-driven config options.
	registerPermissionModeConfigProvider(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		StartGooseCLI,
		fallbackGooseCLIModes(),
		"LEAPMUX_GOOSE_DEFAULT_MODEL", "goose",
		GooseConfigThinkingEffort, GooseConfigProvider,
	)
}
