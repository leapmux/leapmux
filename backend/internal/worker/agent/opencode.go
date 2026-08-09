package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const (
	OpenCodePrimaryAgentBuild = "build"
	OpenCodePrimaryAgentPlan  = "plan"
	openCodeHiddenCompaction  = "compaction"
	openCodeHiddenTitle       = "title"
	openCodeHiddenSummary     = "summary"
)

const (
	openCodeMethodSessionResume = "session/resume"
)

// OpenCodeAgent manages a single OpenCode ACP process.
type OpenCodeAgent struct {
	acpBase
}

// StartOpenCode starts an OpenCode ACP agent process and performs the handshake.
func StartOpenCode(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	return acpStart(ctx, opts, sink, acpStartSpec[OpenCodeAgent]{
		provider:       leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		providerName:   "opencode",
		binaryName:     "opencode",
		baseArgs:       []string{"acp"},
		rcMarkerEnvKey: "OPENCODE_CLIENT",
		sessionConfig:  acpSessionConfig{newMethod: acpMethodSessionNew, resumeMethod: openCodeMethodSessionResume},
		newAgent:       func() *OpenCodeAgent { return &OpenCodeAgent{} },
		base:           func(a *OpenCodeAgent) *acpBase { return &a.acpBase },
		configure: func(a *OpenCodeAgent) {
			a.modeChannel = modeChannelPrimaryAgent
			a.primaryAgentHiddenFilter = isHiddenPrimaryAgent
			// Subagent spawn detection (registry-only; OpenCode drops child
			// sessions over ACP so there is no transcript). Detect by input
			// shape, not tool-name guessing.
			a.subagentFromToolCall = openCodeSubagentFromToolCall
			a.subagentFromToolCallUpdate = openCodeSubagentFromToolCallUpdate
		},
		afterHandshake: func(a *OpenCodeAgent, handshake *acpSessionResult, opts Options) error {
			return a.applyPrimaryAgentStartup(handshake, opts, OpenCodePrimaryAgentBuild)
		},
	})
}

func fallbackOpenCodePrimaryAgents() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: OpenCodePrimaryAgentBuild, Name: titleCaseID(OpenCodePrimaryAgentBuild, "")},
		{Id: OpenCodePrimaryAgentPlan, Name: titleCaseID(OpenCodePrimaryAgentPlan, "")},
	}
}

// isHiddenPrimaryAgent reports whether a primary-agent id is an internal
// pseudo-agent that must be hidden from the picker. These ids originate in
// OpenCode's protocol but are shared by every OpenCode-family ACP provider
// (Kilo included), so both inject this as their primaryAgentHiddenFilter.
func isHiddenPrimaryAgent(id string) bool {
	switch id {
	case openCodeHiddenCompaction, openCodeHiddenTitle, openCodeHiddenSummary:
		return true
	default:
		return false
	}
}

// openCodeSubagentFromToolCall detects an OpenCode/Kilo subagent spawn by the
// rawInput SHAPE {description, prompt, subagent_type} (shape detection, not
// tool-name guessing). Shared by both providers since they run the same ACP
// layer (Kilo is an OpenCode fork). Returns nil for a non-spawn tool call.
func openCodeSubagentFromToolCall(tc acpToolCallEnvelope) *acpSubagentObservation {
	var input struct {
		Description  string          `json:"description"`
		Prompt       json.RawMessage `json:"prompt"`
		SubagentType string          `json:"subagent_type"`
		// Some builds spell it with a different key; the shape still carries a
		// prompt + a type, so detect on the union.
		SubagentID string `json:"subagentID"`
	}
	if len(tc.RawInput) == 0 {
		return nil
	}
	if err := json.Unmarshal(tc.RawInput, &input); err != nil {
		return nil
	}
	// Spawn shape requires at least a prompt and a subagent discriminator.
	if len(input.Prompt) == 0 || (input.SubagentType == "" && input.SubagentID == "") {
		return nil
	}
	title := tc.Title
	if title == "" {
		title = input.Description
	}
	if title == "" {
		title = input.SubagentType
	}
	return &acpSubagentObservation{
		RowKey: tc.ToolCallID,
		Title:  title,
		Status: bgtask.StatusRunning,
	}
}

// openCodeSubagentFromToolCallUpdate closes the registry row on a terminal
// status, and when rawOutput.metadata.sessionId is present, re-keys the row to
// the child session id (the metadata surfaces only on the terminal update).
// The spawn row was opened under the toolCallId, so SpawnRowKey carries it to
// keep the close from leaking it as a Running row.
func openCodeSubagentFromToolCallUpdate(tcu acpToolCallUpdateEnvelope) *acpSubagentObservation {
	if tcu.Status != "completed" && tcu.Status != "failed" && tcu.Status != "cancelled" {
		return nil
	}
	// The terminal rawOutput may carry the child session id under metadata.
	rowKey := tcu.ToolCallID
	renameFrom := ""
	if len(tcu.RawOutput) > 0 {
		var out struct {
			Metadata struct {
				SessionID string `json:"sessionId"`
			} `json:"metadata"`
		}
		if json.Unmarshal(tcu.RawOutput, &out) == nil && out.Metadata.SessionID != "" {
			// Rename the spawn row (toolCallId) to the child session id so one
			// row tracks the lifecycle, then terminalize it.
			rowKey = out.Metadata.SessionID
			renameFrom = tcu.ToolCallID
		}
	}
	return &acpSubagentObservation{
		RowKey:     rowKey,
		RenameFrom: renameFrom,
		Status:     acpTerminalStatus(tcu.Status),
		CloseRow:   true,
		Mode:       acpModeCloseOnly,
	}
}

// buildACPPromptBlocks converts text + classified attachments into ACP prompt
// blocks compatible with ACP agents.
func buildACPPromptBlocks(content string, classified []classifiedAttachment) []map[string]interface{} {
	var prompt []map[string]interface{}
	if content != "" {
		prompt = append(prompt, map[string]interface{}{"type": "text", "text": content})
	}
	for _, attachment := range classified {
		if attachment.kind == attachmentKindImage {
			prompt = append(prompt, map[string]interface{}{
				"type":     "image",
				"mimeType": attachment.mimeType,
				"data":     base64.StdEncoding.EncodeToString(attachment.data),
				"uri":      attachment.filename,
			})
			continue
		}

		resource := map[string]interface{}{
			"uri":      attachment.filename,
			"mimeType": attachment.mimeType,
		}
		if attachment.kind == attachmentKindText {
			resource["text"] = string(attachment.data)
		} else {
			resource["blob"] = base64.StdEncoding.EncodeToString(attachment.data)
		}
		prompt = append(prompt, map[string]interface{}{
			"type":     "resource",
			"resource": resource,
		})
	}
	return prompt
}

// registerOpenCodeFamilyProvider registers an OpenCode-protocol provider (OpenCode, Kilo). The
// two run different daemons but share the SAME registration shape: a primaryAgent secondary
// channel with a per-daemon fallback agent list, dynamically-discovered models, and the
// server-driven "effort" config option (the daemon's per-model reasoning variants, surfaced under
// the well-known id). Only the provider enum, Start function, fallback agents, env keys, and
// binary name vary -- so each init() reduces to one call here, mirroring the frontend's
// registerOpenCodeProtocolProvider, instead of two near-identical registration blocks that can drift.
func registerOpenCodeFamilyProvider(
	provider leapmuxv1.AgentProvider,
	start startFunc,
	fallbackPrimaryAgents []*leapmuxv1.AvailableOption,
	envModelKey, envEffortKey, binaryName string,
) {
	registerAgentFactory(
		provider,
		start,
		nil, // models discovered dynamically from newSession
		staticSecondaryGroup(modeChannelPrimaryAgent, fallbackPrimaryAgents),
		envModelKey,
		envEffortKey,
		binaryName,
	)
	setAdditionalOptionIDs(provider, OptionIDEffort)
}

func init() {
	registerOpenCodeFamilyProvider(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		StartOpenCode,
		fallbackOpenCodePrimaryAgents(),
		"LEAPMUX_OPENCODE_DEFAULT_MODEL", "LEAPMUX_OPENCODE_DEFAULT_EFFORT", "opencode",
	)
}
