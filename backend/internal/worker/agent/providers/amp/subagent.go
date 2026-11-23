package amp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// This file maintains registry rows for subagent calls and background shell commands.
//
// Amp runs these tools as server subagents:
//   - Task starts a general subagent.
//   - oracle requests advice from another model.
//   - librarian researches code on GitHub.
//   - finder searches the workspace.
//
// Each call opens one subagent registry row for its duration.
// Amp's stream-JSON mode omits messages with a parent tool ID, so only the call and its final result reach stdout.
// The protocol therefore supplies no child transcript.
// The call's own transcript card displays the prompt and report.

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

// taskTitleFallback supplies the title for a Task call with neither a description nor a prompt.
const taskTitleFallback = "Subagent"

// subagentRow supplies a call's registry title and description.
// The call's transcript card uses the same title.
// testdata/amp_subagent_title_conformance.json verifies that shared rule.
//
// A Task call supplies a short description beside its prompt.
// The three specialist tools supply only their request, so their titles identify the specialist before the request.
// A reader can therefore distinguish an oracle call from a search.
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
	return bgtask.StatusSucceeded
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

// Amp backgrounds a command that outlives its shell_command call.
// The result supplies running: true and the process ID (PID), and the command continues in a separate process group.
// The model reads it through shell_command_status and stops it through shell_command_kill, both using that PID.
//
// Each command opens a shell registry row.
// A status or kill result that confirms its end closes the row.
// Amp sends no asynchronous event when a command ends independently, so a lazy
// PID liveness poller checks tracked local PIDs and closes exited rows with
// StatusEndedWithUnknownOutcome.
// Amp process exit also closes any remaining open row with the corresponding status:
//   - Stopped after orderly shutdown, because Amp stops every command that it starts.
//   - Interrupted after a crash, which the command can survive.

// ampShellPollInterval paces local PID liveness checks while background shells exist.
const ampShellPollInterval = time.Second

// shellCommandInput takes the command of a `shell_command` call.
type shellCommandInput struct {
	Command string `json:"command"`
}

// shellPIDInput takes the PID targeted by a `shell_command_kill` or
// `shell_command_status` call.
type shellPIDInput struct {
	PID int `json:"pid"`
}

// shellRowKey identifies the registry row for one shell_command call's background command.
// Use the unique call ID instead of its PID, which the system can reuse for a later process.
func shellRowKey(toolUseID string) string { return "amp-shell:" + toolUseID }

// isAmpPluginRefusal reports whether a tool result states a permission or plugin
// refusal rather than a command execution error.
func isAmpPluginRefusal(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "Tool rejected by plugin:") || strings.HasPrefix(trimmed, "Plugin error:")
}

// isPIDAlive checks whether the local process with pid still runs.
func (a *Agent) isPIDAlive(pid int) bool {
	if a.pidAliveFn != nil {
		return a.pidAliveFn(pid)
	}
	return ampProcessAlive(pid)
}

// noteShellResult processes one shell-tool result for a background command.
// text contains that result's text.
// A non-JSON error on shell_command_kill or shell_command_status closes a tracked
// row only when the error is not a plugin refusal and the local PID is no longer alive.
func (a *Agent) noteShellResult(tool *openTool, text string, isError bool) {
	var record contracts.AmpShellResult
	if err := json.Unmarshal([]byte(text), &record); err != nil {
		if !isError || isAmpPluginRefusal(text) {
			return
		}
		var closeStatus bgtask.Status
		switch tool.name {
		case contracts.AmpShellToolShellCommandKill:
			closeStatus = bgtask.StatusStopped
		case contracts.AmpShellToolShellCommandStatus:
			closeStatus = bgtask.StatusFailed
		default:
			return
		}
		var input shellPIDInput
		if json.Unmarshal(tool.input, &input) != nil || input.PID <= 0 {
			return
		}
		a.mu.Lock()
		_, tracked := a.shells[input.PID]
		a.mu.Unlock()
		if !tracked || a.isPIDAlive(input.PID) {
			return
		}
		a.closeShellRow(input.PID, closeStatus)
		return
	}
	if record.PID <= 0 {
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
		// A command that outlives the kill's wait stays open: the next status,
		// the PID liveness poller, or the process exit closes it.
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
	if reused {
		delete(a.shells, pid)
	}
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
	// Track the PID and start the liveness poller only after UpsertBackgroundTask
	// returns, so the poller cannot delete a dead PID and run a no-op close before
	// the registry row exists.
	a.mu.Lock()
	a.shells[pid] = rowKey
	a.ensureShellPollerLocked()
	a.mu.Unlock()
}

// ensureShellPollerLocked starts the lazy PID liveness poller when needed.
// The caller holds a.mu.
func (a *Agent) ensureShellPollerLocked() {
	if a.stopped || a.shellPollerRunning {
		return
	}
	a.shellPollerRunning = true
	a.shellPollerWG.Add(1)
	go a.pollShellLiveness()
}

// pollShellLiveness probes tracked background shell PIDs until none remain or
// the agent stops.
func (a *Agent) pollShellLiveness() {
	defer a.shellPollerWG.Done()
	cleared := false
	defer func() {
		if !cleared {
			a.mu.Lock()
			a.shellPollerRunning = false
			a.mu.Unlock()
		}
	}()
	ticker := a.clock.NewTicker(ampShellPollInterval, "amp", "shell-liveness")
	defer ticker.Stop()
	type shellEntry struct {
		pid    int
		rowKey string
	}
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}
		a.mu.Lock()
		if a.stopped || len(a.shells) == 0 {
			a.shellPollerRunning = false
			cleared = true
			a.mu.Unlock()
			return
		}
		entries := make([]shellEntry, 0, len(a.shells))
		for pid, rowKey := range a.shells {
			entries = append(entries, shellEntry{pid: pid, rowKey: rowKey})
		}
		a.mu.Unlock()

		for _, entry := range entries {
			if a.ctx.Err() != nil {
				return
			}
			if a.isPIDAlive(entry.pid) {
				continue
			}
			a.mu.Lock()
			if a.stopped || a.shells[entry.pid] != entry.rowKey {
				a.mu.Unlock()
				continue
			}
			delete(a.shells, entry.pid)
			a.mu.Unlock()
			providerkit.LogRegistryRefusal("amp", "close shell", a.sink.CloseBackgroundTask(entry.rowKey, bgtask.StatusEndedWithUnknownOutcome))
		}
	}
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

// closeAllShellRows closes every background command row with status when the owning process ends.
// Remove the rows under the lock, so another call cannot close them again.
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

// shellEndStatus selects the final registry status when a status call finds an ended command.
// Exit code zero means success. Any other code or an absent code means failure.
// Amp returns exit code 1 for a PID that it no longer tracks, so that row also closes as failed.
func shellEndStatus(record contracts.AmpShellResult) bgtask.Status {
	if record.ExitCode != nil && *record.ExitCode == 0 {
		return bgtask.StatusSucceeded
	}
	return bgtask.StatusFailed
}
