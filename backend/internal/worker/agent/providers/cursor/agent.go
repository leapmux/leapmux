package cursor

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/tooltranscript"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Agent manages a single Cursor CLI ACP process.
type Agent struct {
	acp.Base

	// taskToolCalls holds the toolCallId of every `task` tool call the spawn
	// hook claimed. The closing hook needs it: Cursor reports "this ran in the
	// background" only in the final update's rawOutput, and the tool's identity
	// only in rawInput, which that update does not always carry. Without the
	// note, a backgrounded task and a backgrounded shell are the same wire
	// shape.
	//
	// Guarded by Base's Mu, which neither handleToolCall nor
	// handleToolCallUpdate holds while it calls a hook, so the hooks can take it.
	// An entry is dropped on the final update. A `task` call that never reaches
	// one keeps its entry -- one bool and one id -- for the life of the agent,
	// which matches how Base's subagentPrompts holds a spawn's prompt.
	taskToolCalls map[string]bool
	// taskReports joins the live cursor/task extension with the local-store
	// result. Cursor replays the store record but not the extension, so only a
	// state that saw both can publish a report. Guarded by Base.Mu and capped
	// in tool_transcript.go.
	taskReports map[string]cursorTaskReportState

	// transcript is the sink that configure installed, held under its own type so
	// the extension handler can reach EnrichToolSpan. The transcript is the single
	// writer of a row's supplemental content, and the `cursor/*` frames land on a
	// row that its store pass also enriches -- see EnrichToolSpan for what a second
	// writer would destroy. It is written once, in configure, before the reader
	// goroutine starts.
	transcript *tooltranscript.Transcript
}

// clearTaskToolCalls drops every note. ClearContext calls it: the notes are
// keyed by the OUTGOING session's tool-call ids, which send no closing update
// once that session is gone, and a new session that reuses an id would read a
// stale note and file a backgrounded shell as a subagent.
func (a *Agent) clearTaskToolCalls() {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	clear(a.taskToolCalls)
	clear(a.taskReports)
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
// A Cursor subagent's child transcript arrives in ONE piece, when the task ends:
// neither observation here carries a ChildTranscriptPayload, so nothing reaches
// the child tab between the spawn prompt and the final report. Goose streams by
// setting that field per update, and ZCode and Codex write into the child sink
// directly. Cursor's own wire format nests the child's updates inside the
// parent's `tool_call_delta`, so the shape exists; whether `cursor-agent`
// forwards it over ACP is untested. See
// https://github.com/leapmux/leapmux/issues/487.
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
	return cursorSubagentFromToolCallUpdate(tcu, wasTaskTool)
}

func (a *Agent) setCursorModel(model string) error {
	// Send the wire id but store the normalized (display) id, so b.model never
	// transiently holds the wire form "default[]" (see SetModelViaConfigOption).
	if err := a.SetModelViaConfigOption(cursorModelIDForWire(model)); err != nil {
		return err
	}
	a.SetCurrentModel(model)
	return nil
}

// cursorSubagentFromToolCall detects Cursor's Task delegation tool_call
// (rawInput._toolName == "task", title "Task: <description>"). The observed
// toolCallId can contain an embedded newline; the neutral layer sanitizes row
// keys before use. The tool call is the child key because Cursor exposes no
// separate child-session key.
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
