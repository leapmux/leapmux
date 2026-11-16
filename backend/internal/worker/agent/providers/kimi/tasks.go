package kimi

import (
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Native task state identifies each task by session, owner, and task ID.
// Agent tasks retain independent outcome rows, and child headers control the
// separate activity marker. Shell tasks retain their native process rows.
// Question tasks use their control requests rather than duplicate task rows.

type kimiTaskAddress struct {
	ownerAgentID string
	taskID       string
}

// kimiTask is one background task the session reported.
type kimiTask struct {
	kind    string
	rowKey  string
	agentID string
	// ended is true once the task ended. The entry stays after the end, so a
	// resync can tell a task that the stream reported from one it missed.
	ended bool
	// native retains metadata needed when a task or its owner has no child yet.
	native            kimiNativeTaskState
	projectionPending bool
	boundInvocation   *kimiChildInvocation
	// endStatus retains the native outcome across projection retries.
	endStatus bgtask.Status
	// childEndRetained records acknowledgement of this task's registry completion.
	childEndRetained bool
	// childAgentID identifies the persisted child that this task has adopted.
	childAgentID   string
	reportText     string
	reportReady    bool
	reportComplete bool
	// dispatchMu owns captured destinations and output-retention acknowledgements.
	childOutput, parentOutput                 *kimiTaskOutputAnchor
	diagnosticCapture                         agent.CapturedTranscript
	diagnosticRetained                        bool
	reportContent                             *agent.MessageContent
	childReportCapture, parentReportCapture   agent.CapturedTranscript
	childReportRetained, parentReportRetained bool
}

// kimiTaskRowKey qualifies a process task by its session and owning agent.
func kimiTaskRowKey(sessionID, ownerAgentID, taskID string) string {
	return sessionID + "/task/" + ownerAgentID + "/" + taskID
}

// kimiTaskStatus maps a task's status onto the registry's. final is false for a
// status that is not an end, so the row stays open rather than being closed by a
// value the registry treats as absorbing.
func kimiTaskStatus(status string) (bgtask.Status, bool) {
	switch status {
	case kimiTaskStatusCompleted:
		return bgtask.StatusSucceeded, true
	case kimiTaskStatusFailed, kimiTaskStatusTimedOut, kimiTaskStatusLost:
		return bgtask.StatusFailed, true
	case kimiTaskStatusKilled:
		return bgtask.StatusStopped, true
	case kimiTaskStatusRunning, "":
		return bgtask.StatusRunning, false
	default:
		// A status outside the server's enumeration. Running is the only safe
		// answer, because a final status is absorbing.
		slog.Debug("kimi unknown task status", "status", status)
		return bgtask.StatusRunning, false
	}
}

// startShellTask creates the native detached process row. The caller holds dispatchMu.
func (a *Agent) startShellTask(ownerAgentID string, state kimiNativeTaskState) {
	a.Mu.Lock()
	task := &kimiTask{kind: kimiTaskKindProcess, rowKey: kimiTaskRowKey(a.sessionID, ownerAgentID, state.TaskID)}
	if a.tasks == nil {
		a.tasks = make(map[kimiTaskAddress]*kimiTask)
	}
	a.tasks[kimiTaskAddress{ownerAgentID: ownerAgentID, taskID: state.TaskID}] = task
	a.Mu.Unlock()
	title, isCommand := strings.TrimSpace(state.Command), true
	if title == "" {
		title, isCommand = strings.TrimSpace(state.Description), false
	} else {
		title = bgtask.CleanTitleRunes(bgtask.FirstLine(title), 120)
	}
	providerkit.LogRegistryRefusal("kimi", "upsert", a.queueRegistryUpsert(a.sink, bgtask.Upsert{RowKey: task.rowKey, Kind: bgtask.KindShell, Title: title, TitleIsCommand: isCommand, Description: strings.TrimSpace(state.Description), Status: bgtask.StatusRunning}))
}

// endShellTask closes the native process row. The caller holds dispatchMu.
func (a *Agent) endShellTask(ownerAgentID string, state kimiNativeTaskState, status bgtask.Status) {
	a.Mu.Lock()
	address := kimiTaskAddress{ownerAgentID: ownerAgentID, taskID: state.TaskID}
	task := a.tasks[address]
	rowKey := kimiTaskRowKey(a.sessionID, ownerAgentID, state.TaskID)
	if task != nil {
		task.ended = true
		if task.rowKey != "" {
			rowKey = task.rowKey
		}
	}
	a.Mu.Unlock()
	providerkit.LogRegistryRefusal("kimi", "close", a.queueRegistryClose(a.sink, rowKey, status))
}
