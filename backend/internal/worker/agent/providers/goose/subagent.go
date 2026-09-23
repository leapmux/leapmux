package goose

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// gooseSubagentFromToolCall detects Goose's spawn tool_call by the structured
// _meta.goose.toolCall marker {toolName:"delegate", extensionName:"summon"}.
// This is the spawn detector, not title guessing. The spawn creates the child
// transcript, and later tool-request updates add its live activity.
func gooseSubagentFromToolCall(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
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
	if tc2.ToolName != contracts.GooseSubagentTool || tc2.ExtensionName != contracts.GooseSubagentExtension {
		return nil
	}
	title := tc.Title
	if title == "" {
		title = "Goose subagent"
	}
	return &acp.SubagentObservation{
		RowKey:        tc.ToolCallID,
		Title:         title,
		Status:        bgtask.StatusRunning,
		ChildAgentKey: tc.ToolCallID,
		Spawns:        true,
		// Goose's delegate tool puts its task text in `instructions`, not `prompt`
		// (crates/goose/src/agents/platform_extensions/summon.rs).
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

// gooseSubagentToolRequestType is the `data.type` that marks one subagent tool
// request inside Goose's logging metadata.
const gooseSubagentToolRequestType = "subagent_tool_request"

// gooseSubagentRequestedTool reads the name of the tool one subagent request asks
// for, or the empty string when the request states none.
//
// It indexes the `data` object with the generated constant instead of taking the key
// from a struct tag, which cannot hold one. Goose owns the word, and the browser
// draws the request row from the same object through the same contract table. A tag
// here would therefore keep the old word after a Goose release moved it: every
// request would fall back to the neutral activity line, with no build error and no
// log line.
func gooseSubagentRequestedTool(data json.RawMessage) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return ""
	}
	var call struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(fields[contracts.GooseSubagentRequestToolCall], &call) != nil {
		return ""
	}
	return call.Name
}

// gooseSubagentFromToolCallUpdate observes Goose's subagent tool requests.
// Goose surfaces tool REQUESTS (never results) over ACP via a two-level-nested
// _meta payload: toolNotification.type is "message"; the discriminator is
// params.data.type == "subagent_tool_request". Each request carries the
// subagent_id and the tool_call name, so we upsert a running row with activity
// "tool: <name>" and persist the raw request to the child transcript. The
// spawn tool_call's final update closes the registry row.
//
// The registry row, the EnsureChildAgent linkage, and the closing update all
// key off the SPAWN toolCallId: the final spawn update carries only
// toolCallId, so the row must live under that key, and ChildAgentKey must
// match it or EnsureChildAgent would open a second row keyed by subagent_id
// that the close never reaches.
func gooseSubagentFromToolCallUpdate(tcu acp.ToolCallUpdateEnvelope) *acp.SubagentObservation {
	// Final update on the spawn tool_call itself -> close the registry row.
	// Goose's final spawn update carries no _meta, but the row was created
	// (by the tool_call or a tool-request) under this toolCallId, so closing on
	// the final update is correct. CloseRow is idempotent: a plain tool with
	// no registry row is a no-op (the upsert path finds no row to close).
	if acp.StatusIsFinal(tcu.Status) {
		report := ""
		var input struct {
			Async bool `json:"async"`
		}
		if json.Unmarshal(tcu.RawInput, &input) == nil && !input.Async {
			report = acp.ToolCallText(tcu.Content)
		}
		return &acp.SubagentObservation{
			RowKey:   tcu.ToolCallID,
			Status:   acp.FinalStatus(tcu.Status),
			CloseRow: true,
			Mode:     acp.ModeCloseOnly,
			ReportID: tcu.ToolCallID,
			Report:   agent.SubagentReport{Text: report},
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
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(meta.ToolNotification.Params, &params); err != nil {
		return nil
	}
	var data struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(params.Data, &data); err != nil || data.Type != gooseSubagentToolRequestType {
		return nil
	}
	// Key the registry row, the child-agent linkage, AND the closing update off
	// the SPAWN toolCallId. The final spawn update knows only toolCallId, so
	// the row must live under that key; ChildAgentKey must match it too, or
	// EnsureChildAgent would upsert a SECOND row keyed by subagent_id that the
	// close never reaches (orphaned Running row). One Goose spawn = one child =
	// one transcript, so toolCallId is the correct stable child identity here;
	// the per-request subagent_id is not used as a registry key.
	childKey := tcu.ToolCallID
	activity := "tool request"
	if name := gooseSubagentRequestedTool(params.Data); name != "" {
		activity = "tool: " + name
	}
	// Persist the tool-request update to the child transcript (PersistChildMessage
	// via applySubagentObservation). Goose only ever ships requests, so this is
	// the live child activity. The payload is a tool_call_update-shaped envelope
	// carrying sessionUpdate + status + _meta so the shared ACP classifier
	// recognizes it and routes it to the subagent-tool-request renderer (a plain
	// re-marshal of the parsed struct drops sessionUpdate, leaving the classifier
	// no branch to match and the row renders as a raw-JSON dump).
	payload := gooseSubagentToolRequestPayload(tcu, meta.ToolNotification.Params)
	return &acp.SubagentObservation{
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
func gooseSubagentToolRequestPayload(tcu acp.ToolCallUpdateEnvelope, notificationParams json.RawMessage) []byte {
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
