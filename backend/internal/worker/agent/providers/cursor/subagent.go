package cursor

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// clearTaskToolCalls drops every note. ClearContext calls it: the notes are
// keyed by the OUTGOING session's tool-call ids, which send no closing update
// once that session is gone, and a new session that reuses an id would read a
// stale note and file a backgrounded shell as a subagent.
func (a *Agent) clearTaskToolCalls() {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	clear(a.taskToolCalls)
	clear(a.taskReports)
	clear(a.nativeTasks)
}

// rememberTaskToolCall notes that toolCallID is Cursor's `task` tool.
func (a *Agent) rememberTaskToolCall(toolCallID string) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.taskToolCalls == nil {
		a.taskToolCalls = make(map[string]bool)
	}
	a.taskToolCalls[toolCallID] = true
}

// forgetTaskToolCall drops the note for toolCallID and reports whether one was
// there. The call is over when this runs, so the entry cannot accumulate.
func (a *Agent) forgetTaskToolCall(toolCallID string) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	was := a.taskToolCalls[toolCallID]
	delete(a.taskToolCalls, toolCallID)
	return was
}

// spawnObservation runs Cursor's spawn detector and remembers what it claimed,
// so the closing hook does not have to ask the wire a second time.
//
// A tool call that arrives ALREADY final gets no note. handleToolCall applies
// this observation and returns, so no closing update follows to drop one --
// a `session/load` replay of a finished task would leave an entry for the life
// of the agent, and a later call that reuses the id would read it and file a
// backgrounded shell as a subagent. The note has no reader on that path either:
// this observation already carries the kind and the title.
// Cursor's native subagent publisher sends each child update on its own session.
// The lifecycle handler links that session to this Task row.
func (a *Agent) spawnObservation(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	obs := cursorSubagentFromToolCall(tc)
	if obs != nil && !acp.StatusIsFinal(tc.Status) {
		a.rememberTaskToolCall(tc.ToolCallID)
	}
	return obs
}

// finishedObservation answers "was this the task tool" from the tool call
// itself, and falls back to the note the spawn left when this update carries no
// input of its own.
func (a *Agent) finishedObservation(tcu acp.ToolCallUpdateEnvelope) *acp.SubagentObservation {
	wasTaskTool := cursorToolCallIsTaskTool(tcu.RawInput)
	// The row is over on a final status, so drop the note whatever it said.
	if acp.StatusIsFinal(tcu.Status) && a.forgetTaskToolCall(tcu.ToolCallID) {
		wasTaskTool = true
	}
	observation := cursorSubagentFromToolCallUpdate(tcu, wasTaskTool)
	if observation == nil || !wasTaskTool {
		return observation
	}
	a.Mu.Lock()
	native, found := a.nativeTasks[bgtask.NormalizeRowKey(tcu.ToolCallID)]
	a.Mu.Unlock()
	if found && native.status.IsFinished() {
		observation.Status = native.status
	} else if found && cursorToolCallRanInBackground(tcu.RawOutput) {
		observation.Status = bgtask.StatusRunning
		observation.CloseRow = false
	}
	return observation
}

// cursorSubagentFromToolCall detects Cursor's Task delegation tool_call
// (rawInput._toolName == "task", title "Task: <description>"). The observed
// toolCallId can contain an embedded newline; the neutral layer sanitizes row
// keys before use. The tool call identifies the row. Native lifecycle updates
// supply the separate child session ID.
func cursorSubagentFromToolCall(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	if !cursorToolCallIsTaskTool(tc.RawInput) {
		return nil
	}
	title := strings.TrimPrefix(tc.Title, "Task: ")
	if title == "" {
		title = "Cursor subagent"
	}
	var input struct {
		Prompt string `json:"prompt"`
	}
	_ = json.Unmarshal(tc.RawInput, &input)
	return &acp.SubagentObservation{
		RowKey:        tc.ToolCallID,
		Title:         title,
		Status:        bgtask.StatusRunning,
		ChildAgentKey: tc.ToolCallID,
		Prompt:        input.Prompt,
		Spawns:        true,
	}
}

// cursorToolCallIsTaskTool reports whether a Cursor tool call is the `task`
// delegation tool, which is the only Cursor tool that spawns a subagent. It
// reads ONE payload, so it answers only for a payload that carries the input.
//
// An absent rawInput gives false, which means "this payload does not say it is
// the task tool" and NOT "this call is not the task tool". Cursor does not
// always echo the input on an update, so the closing hook must not treat the
// two as the same: finishedObservation falls back to the note the spawn left
// (taskToolCalls) before it classifies a backgrounded call as a shell.
func cursorToolCallIsTaskTool(rawInput json.RawMessage) bool {
	if len(rawInput) == 0 {
		return false
	}
	var input struct {
		ToolName string `json:"_toolName"`
	}
	return json.Unmarshal(rawInput, &input) == nil && input.ToolName == contracts.CursorToolTask
}

// cursorToolCallRanInBackground reports whether a finished Cursor tool call was
// backgrounded, which Cursor states as rawOutput.isBackground.
func cursorToolCallRanInBackground(rawOutput json.RawMessage) bool {
	if len(rawOutput) == 0 {
		return false
	}
	var out struct {
		IsBackground bool `json:"isBackground"`
	}
	return json.Unmarshal(rawOutput, &out) == nil && out.IsBackground
}

// cursorSubagentFromToolCallUpdate maps Cursor's finished tool_call updates to
// registry rows. The final update fires for EVERY finished tool_call (not just
// spawns); a plain foreground tool is a close-only observation, so it does not
// create a spurious row. A backgrounded call carries an activity line and
// upserts before closing.
//
// A backgrounded call is a SHELL unless it is the `task` tool. Cursor's other
// tools are not subagents, and the neutral layer defaults a blank kind to
// Subagent -- so leaving the kind blank here put a shell in the sidebar under a
// Bot icon, in the subagent filter tab, labelled with its raw toolCallId. The
// task-tool branch leaves BOTH the kind and the title blank on purpose: the spawn
// observation already set them, and Item.PreservingBlanksFrom keeps an existing
// value only for a blank incoming one. Writing them here would flip a real
// subagent row to a shell and overwrite its trimmed title with the raw
// "Task: ..." string.
//
// wasTaskTool comes from the caller, not from tcu, because this update does not
// always carry rawInput. Reading the identity off tcu alone made an absent
// rawInput mean "not the task tool", so a backgrounded task whose final update
// omitted its input took the shell branch and flipped its own live row.
func cursorSubagentFromToolCallUpdate(tcu acp.ToolCallUpdateEnvelope, wasTaskTool bool) *acp.SubagentObservation {
	if !acp.StatusIsFinal(tcu.Status) {
		return nil
	}
	obs := &acp.SubagentObservation{
		RowKey:   tcu.ToolCallID,
		Status:   acp.FinalStatus(tcu.Status),
		CloseRow: true,
		Mode:     acp.ModeCloseOnly,
	}
	if !cursorToolCallRanInBackground(tcu.RawOutput) {
		return obs
	}
	obs.Mode = acp.ModeUpsert
	obs.Activity = "background task"
	if !wasTaskTool {
		obs.Kind = bgtask.KindShell
		// This update is the row's only event, so it is also the only chance to
		// give it a readable label. Without one the sidebar shows the raw
		// toolCallId. The row's TitleIsCommand stays false (no observation sets
		// it): Cursor's title is a label, not a verbatim command, and prose in
		// the monospace face reads worse than a command in the normal one.
		obs.Title = tcu.Title
	}
	return obs
}

type cursorSubagentLifecycle struct {
	SessionID string `json:"subagentSessionId"`
	State     string `json:"state"`
	Name      string `json:"name"`
	Task      string `json:"task"`
}

type cursorSubagentIdentity struct {
	ToolCallID string `json:"toolCallId"`
	AgentID    string `json:"agentId"`
}

type cursorNativeTask struct {
	sessionID       string
	parentSessionID string
	status          bgtask.Status
}

// handleCursorSessionUpdate reads lifecycle updates from the actual parent conversation.
func (a *Agent) handleCursorSessionUpdate(sessionID string, owner agent.ProviderServices, update json.RawMessage) bool {
	var header struct {
		Type     string                     `json:"sessionUpdate"`
		Metadata map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(update, &header) != nil {
		return false
	}
	return a.applyCursorSubagentLifecycle(sessionID, owner, header.Type, header.Metadata, update)
}

// handleCursorSubagentLifecycle preserves the existing root metadata hook.
func (a *Agent) handleCursorSubagentLifecycle(updateType string, metadata map[string]json.RawMessage, update json.RawMessage) bool {
	return a.applyCursorSubagentLifecycle(a.CurrentSessionID(), a.Sink(), updateType, metadata, update)
}

func (a *Agent) applyCursorSubagentLifecycle(parentSessionID string, owner agent.ProviderServices, updateType string, metadata map[string]json.RawMessage, update json.RawMessage) bool {
	if updateType != "subagent_spawned" && updateType != "subagent_state_update" {
		return false
	}
	var lifecycle cursorSubagentLifecycle
	var identity cursorSubagentIdentity
	if owner == nil || parentSessionID == "" || json.Unmarshal(update, &lifecycle) != nil || json.Unmarshal(metadata["cursor"], &identity) != nil ||
		strings.TrimSpace(lifecycle.SessionID) == "" || strings.TrimSpace(identity.ToolCallID) == "" || strings.TrimSpace(identity.AgentID) == "" {
		return true
	}
	rowKey := bgtask.NormalizeRowKey(identity.ToolCallID)
	if updateType == "subagent_spawned" {
		childID, status, found, err := owner.LookupBackgroundTask(rowKey)
		if err != nil {
			slog.Warn("Read Cursor child task", "agent_id", a.AgentID(), "tool_call_id", identity.ToolCallID, "error", err)
			return true
		}
		if found && (childID == "" || status.IsFinished()) {
			return true
		}
		if !found {
			title := lifecycle.Name
			if title == "" {
				title = "Cursor subagent"
			}
			if !a.ApplySubagentObservationForSession(parentSessionID, &acp.SubagentObservation{
				RowKey: rowKey, Title: title, Prompt: lifecycle.Task, ChildAgentKey: rowKey,
				Kind: bgtask.KindSubagent, Status: bgtask.StatusRunning, Spawns: true,
			}) {
				return true
			}
			childID, _, found, err = owner.LookupBackgroundTask(rowKey)
			if err != nil || !found || childID == "" {
				return true
			}
		}
		a.Mu.Lock()
		if a.nativeTasks == nil {
			a.nativeTasks = make(map[string]cursorNativeTask)
		}
		a.nativeTasks[rowKey] = cursorNativeTask{sessionID: lifecycle.SessionID, parentSessionID: parentSessionID, status: bgtask.StatusRunning}
		a.Mu.Unlock()
		a.AttachChildSession(lifecycle.SessionID, rowKey)
		return true
	}
	var status bgtask.Status
	switch lifecycle.State {
	case "completed":
		status = bgtask.StatusCompleted
	case "failed":
		status = bgtask.StatusFailed
	case "cancelled", "disconnected":
		status = bgtask.StatusStopped
	default:
		return true
	}
	a.Mu.Lock()
	current, found := a.nativeTasks[rowKey]
	if found && current.sessionID == lifecycle.SessionID && current.parentSessionID == parentSessionID && !current.status.IsFinished() {
		current.status = status
		a.nativeTasks[rowKey] = current
	} else {
		found = false
	}
	a.Mu.Unlock()
	if !found {
		return true
	}
	a.ApplySubagentObservationForSession(parentSessionID, &acp.SubagentObservation{RowKey: rowKey, Status: status, CloseRow: true, Mode: acp.ModeCloseOnly})
	return true
}
