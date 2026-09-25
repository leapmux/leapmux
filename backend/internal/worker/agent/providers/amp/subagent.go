package amp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// This file keeps the background-task registry rows of two kinds of call: the
// subagent calls, and the shell commands that go on in the background.
//
// Amp runs four tools as subagents on its server: `Task` starts a general
// subagent, `oracle` asks a second model for advice, `librarian` researches
// code on GitHub, and `finder` searches the workspace.
//
// Each call becomes a subagent row in the background-task registry, open while
// the call runs. The row carries no child transcript, and that is Amp's limit,
// not a gap here: stream-JSON mode drops every message that carries a parent
// tool id, so only the call and its final result reach stdout. The call's own
// card in the transcript shows the prompt and the report.

// isSubagentTool reports whether Amp runs the tool as a subagent.
func isSubagentTool(name string) bool {
	switch name {
	case contracts.AmpSubagentToolTask, contracts.AmpSubagentToolOracle,
		contracts.AmpSubagentToolLibrarian, contracts.AmpSubagentToolFinder:
		return true
	default:
		return false
	}
}

// subagentInput takes the fields of a subagent call that the row shows. Each
// tool states its request under a key of its own.
type subagentInput struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	Task        string `json:"task"`
	Query       string `json:"query"`
	Context     string `json:"context"`
}

// taskTitleFallback titles a Task call that states no description and no
// prompt.
const taskTitleFallback = "Subagent"

// subagentRow is the title and the description of one call's registry row. The
// transcript card of the call takes the same title, and
// testdata/amp_subagent_title_conformance.json holds the two to one rule.
//
// A Task call states a short description beside its prompt. The three
// specialists state their request alone, so their title gives the specialist
// before the request, and a reader can tell an oracle from a search.
func subagentRow(name string, raw json.RawMessage) (title, description string) {
	var input subagentInput
	_ = json.Unmarshal(raw, &input)
	switch name {
	case contracts.AmpSubagentToolTask:
		title = strings.TrimSpace(input.Description)
		if title == "" {
			title = firstLine(input.Prompt)
		}
		if title == "" {
			title = taskTitleFallback
		}
		return title, input.Prompt
	case contracts.AmpSubagentToolOracle:
		return labeled("Oracle", input.Task), joinNonEmpty(input.Task, input.Context)
	case contracts.AmpSubagentToolLibrarian:
		return labeled("Librarian", input.Query), joinNonEmpty(input.Query, input.Context)
	default:
		return labeled("Finder", input.Query), input.Query
	}
}

// openSubagentRow opens the registry row of one subagent call.
func (a *Agent) openSubagentRow(tool *openTool) {
	title, description := subagentRow(tool.name, tool.input)
	providerkit.LogRegistryRefusal("amp", "open subagent", a.sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey:      tool.id,
		Kind:        bgtask.KindSubagent,
		Title:       title,
		Description: description,
		Status:      bgtask.StatusRunning,
	}))
}

// closeSubagentRow closes the registry row of one subagent call.
func (a *Agent) closeSubagentRow(id string, status bgtask.Status) {
	providerkit.LogRegistryRefusal("amp", "close subagent", a.sink.CloseBackgroundTask(id, status))
}

// subagentStatus maps a subagent call's result onto the row's final status.
// Amp marks a failed or cancelled call with `is_error`.
func subagentStatus(isError bool) bgtask.Status {
	if isError {
		return bgtask.StatusFailed
	}
	return bgtask.StatusCompleted
}

// labeled puts a specialist's name before its request.
func labeled(label, request string) string {
	request = firstLine(request)
	if request == "" {
		return label
	}
	return label + ": " + request
}

// firstLine returns the first non-blank line of text, trimmed.
func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// joinNonEmpty joins the non-blank parts with a blank line between them.
func joinNonEmpty(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "\n\n")
}

// Amp moves a shell command to the background when the command outlives its
// call: the `shell_command` result states `running: true` and the command's
// PID, and the command goes on in a process group of its own. The model then
// reads the command with `shell_command_status` and stops it with
// `shell_command_kill`, both by PID.
//
// Each such command becomes a shell row in the background-task registry. A
// status or a kill that states the end of the command closes the row. So does
// the exit of the Amp process: as stopped after an orderly shutdown, because
// Amp then stops every command that it started, and as interrupted after a
// crash, which the command can outlive. Amp states nothing when a command ends
// by itself, so the row of a command that no status call reads stays open until
// the process exits.

// shellCommandInput takes the command of a `shell_command` call.
type shellCommandInput struct {
	Command string `json:"command"`
}

// shellRowKey is the registry row of the background command that one
// `shell_command` call started. The key takes the call's id, which is unique,
// and not the PID, which the system can give to a later process.
func shellRowKey(toolUseID string) string { return "amp-shell:" + toolUseID }

// noteShellResult follows a background command through the result of one call
// of a shell tool. text is the text of the result. A result that is not Amp's
// record -- a refusal, a failure, or the text of another tool -- changes
// nothing.
func (a *Agent) noteShellResult(tool *openTool, text string) {
	var record contracts.AmpShellResult
	if json.Unmarshal([]byte(text), &record) != nil || record.PID <= 0 {
		return
	}
	switch tool.name {
	case contracts.AmpShellToolShellCommand:
		if record.Running {
			a.openShellRow(tool, record.PID)
		}
	case contracts.AmpShellToolShellCommandStatus:
		if !record.Running {
			a.closeShellRow(record.PID, shellEndStatus(record))
		}
	case contracts.AmpShellToolShellCommandKill:
		// A command that outlives the kill's wait stays open: the next status
		// or the process exit closes it.
		if !record.Running {
			a.closeShellRow(record.PID, bgtask.StatusStopped)
		}
	}
}

// openShellRow opens the registry row of the background command that one
// `shell_command` call started.
func (a *Agent) openShellRow(tool *openTool, pid int) {
	var input shellCommandInput
	_ = json.Unmarshal(tool.input, &input)
	title := strings.TrimSpace(input.Command)
	titleIsCommand := title != ""
	if !titleIsCommand {
		title = fmt.Sprintf("Process %d", pid)
	}
	rowKey := shellRowKey(tool.id)
	a.mu.Lock()
	earlier, reused := a.shells[pid]
	a.shells[pid] = rowKey
	a.mu.Unlock()
	if reused {
		// The system gave the PID of an earlier command to this one, so the
		// earlier command ended, and Amp never stated how.
		providerkit.LogRegistryRefusal("amp", "close shell", a.sink.CloseBackgroundTask(earlier, bgtask.StatusStopped))
	}
	providerkit.LogRegistryRefusal("amp", "open shell", a.sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey:         rowKey,
		Kind:           bgtask.KindShell,
		Title:          title,
		TitleIsCommand: titleIsCommand,
		Description:    fmt.Sprintf("Process %d, in the background", pid),
		Status:         bgtask.StatusRunning,
	}))
}

// closeShellRow closes the row of the background command with pid. It does
// nothing for a PID that has no open row.
func (a *Agent) closeShellRow(pid int, status bgtask.Status) {
	a.mu.Lock()
	rowKey, ok := a.shells[pid]
	delete(a.shells, pid)
	a.mu.Unlock()
	if ok {
		providerkit.LogRegistryRefusal("amp", "close shell", a.sink.CloseBackgroundTask(rowKey, status))
	}
}

// closeAllShellRows closes the row of every background command with status,
// when the process that ran the commands ends. It takes the rows under the
// lock, so a second call closes none of them again.
func (a *Agent) closeAllShellRows(status bgtask.Status) {
	a.mu.Lock()
	rowKeys := make([]string, 0, len(a.shells))
	for _, rowKey := range a.shells {
		rowKeys = append(rowKeys, rowKey)
	}
	clear(a.shells)
	a.mu.Unlock()
	sort.Strings(rowKeys)
	for _, rowKey := range rowKeys {
		providerkit.LogRegistryRefusal("amp", "close shell", a.sink.CloseBackgroundTask(rowKey, status))
	}
}

// shellEndStatus is the final status of a background command that a status
// call found ended: completed for exit code 0, and failed for any other code
// or none. Amp answers a PID that it no longer tracks with exit code 1, so such
// a row closes as failed.
func shellEndStatus(record contracts.AmpShellResult) bgtask.Status {
	if record.ExitCode != nil && *record.ExitCode == 0 {
		return bgtask.StatusCompleted
	}
	return bgtask.StatusFailed
}
