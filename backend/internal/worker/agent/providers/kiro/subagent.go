package kiro

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Kiro runs a subagent as the tool call of its `invoke_sub_agent` tool, inside
// the parent's session. The child streams its text, its thinking and its tool
// calls in the parent session, and each of those updates carries the id of the
// child's subtask in `_meta.kiro.agentSubtaskId`. The spawn's own tool call
// carries the same id beside `_meta.kiro.kind: "agent-subtask"`, and it ends
// with the child's answer.
//
// The registry row of a child is keyed by the spawn's tool-call id, because
// that id is the span in the parent transcript that the child tab belongs to.
// The base routes each tagged update to the child through childUpdateRoute.

// kiroSubagentTitle is the title of a subagent whose spawn states no agent.
const kiroSubagentTitle = "Kiro subagent"

// childState links Kiro's subagent subtasks to LeapMux's registry rows.
// Guarded by Agent.stateMu.
type childState struct {
	// rowBySubtask maps a subtask id to the registry row of its spawn.
	rowBySubtask map[string]string
	// subtaskBySpawn maps a spawn that did not end to its subtask id.
	subtaskBySpawn map[string]string
}

// childUpdateRoute reads the subagent that one update of the main session
// belongs to. The spawn's own updates belong to the parent, where the row of
// the spawn is drawn.
func (a *Agent) childUpdateRoute(_ string, metadata map[string]json.RawMessage) string {
	fields := kiroFields(metadata[contracts.KiroMetaNamespace])
	subtask := fieldString(fields, contracts.KiroMetaAgentSubtaskId)
	if subtask == "" || fieldString(fields, contracts.KiroMetaKind) == contracts.KiroKindAgentSubtask {
		return ""
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.children.rowBySubtask[subtask]
}

// kiroSpawnInput is the part of a spawn's arguments that LeapMux reads.
type kiroSpawnInput struct {
	Name        string `json:"name"`
	Prompt      string `json:"prompt"`
	Explanation string `json:"explanation"`
}

// spawnObservation states what one frame tells about a spawn: its row, and
// the prompt that opens the child transcript. The agent names the row, and
// the model's reason for the spawn is its activity.
func spawnObservation(toolCallID string, input kiroSpawnInput, spawns bool) *acp.SubagentObservation {
	title := strings.TrimSpace(input.Name)
	if title == "" {
		title = kiroSubagentTitle
	}
	return &acp.SubagentObservation{
		RowKey:        toolCallID,
		ChildAgentKey: toolCallID,
		Title:         title,
		Activity:      strings.TrimSpace(input.Explanation),
		Prompt:        input.Prompt,
		Status:        bgtask.StatusRunning,
		Spawns:        spawns,
	}
}

// subagentFromToolCall claims a spawn at its first frame. The row and the
// child transcript open at once, with the prompt, so the child tab opens on
// what was asked before the child says anything.
func (a *Agent) subagentFromToolCall(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	fields := kiroFields(metaNamespace(tc.Meta))
	if fieldString(fields, contracts.KiroMetaKind) != contracts.KiroKindAgentSubtask {
		return nil
	}
	subtask := fieldString(fields, contracts.KiroMetaAgentSubtaskId)
	a.stateMu.Lock()
	if a.children.rowBySubtask == nil {
		a.children.rowBySubtask = make(map[string]string)
		a.children.subtaskBySpawn = make(map[string]string)
	}
	if subtask != "" {
		a.children.rowBySubtask[subtask] = tc.ToolCallID
	}
	a.children.subtaskBySpawn[tc.ToolCallID] = subtask
	a.stateMu.Unlock()
	var input kiroSpawnInput
	_ = json.Unmarshal(tc.RawInput, &input)
	return spawnObservation(tc.ToolCallID, input, true)
}

// subagentFromToolCallUpdate follows a spawn through its later frames. A frame
// that states the arguments completes the row. The final frame closes it with
// the child's answer, which Kiro states as the spawn's raw output.
func (a *Agent) subagentFromToolCallUpdate(tcu acp.ToolCallUpdateEnvelope) *acp.SubagentObservation {
	final := acp.StatusIsFinal(tcu.Status)
	a.stateMu.Lock()
	subtask, spawn := a.children.subtaskBySpawn[tcu.ToolCallID]
	if spawn && final {
		delete(a.children.subtaskBySpawn, tcu.ToolCallID)
		delete(a.children.rowBySubtask, subtask)
	}
	a.stateMu.Unlock()
	if !spawn {
		return nil
	}
	if !final {
		var input kiroSpawnInput
		if len(tcu.RawInput) == 0 || json.Unmarshal(tcu.RawInput, &input) != nil || input.Prompt == "" {
			return nil
		}
		return spawnObservation(tcu.ToolCallID, input, false)
	}
	report := ""
	if json.Unmarshal(tcu.RawOutput, &report) != nil || report == "" {
		report = acp.ToolCallText(tcu.Content)
	}
	return &acp.SubagentObservation{
		RowKey:   tcu.ToolCallID,
		Status:   acp.FinalStatus(tcu.Status),
		CloseRow: true,
		Mode:     acp.ModeCloseOnly,
		ReportID: tcu.ToolCallID,
		Report:   agent.SubagentReport{Text: strings.TrimSpace(report)},
	}
}

// metaNamespace returns the `kiro` value of a `_meta` object.
func metaNamespace(meta json.RawMessage) json.RawMessage {
	var envelope map[string]json.RawMessage
	if len(meta) == 0 || json.Unmarshal(meta, &envelope) != nil {
		return nil
	}
	return envelope[contracts.KiroMetaNamespace]
}
