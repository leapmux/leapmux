package kimi

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Kimi Code's background tasks.
//
// A `Bash` call with `run_in_background`, and an `Agent` call with the same flag,
// run as tasks that outlive the call. The server reports each with task.started
// and task.terminated, and starts a new main turn to notify the model when one
// ends.
//
//   - A process task becomes a shell row of the registry, titled with its command.
//   - An agent task is a subagent, whose row the spawn created (subagent.go); the
//     task events keep that row's status and record the task id, which is what
//     stops the subagent.
//   - A question task -- an AskUserQuestion the model sent to the background --
//     gets no row: the question reaches the user as its control request, and a
//     row beside the banner would state the same thing twice.

// kimiTask is one background task the session reported.
type kimiTask struct {
	kind    string
	rowKey  string
	agentID string
	// ended is true once the task ended. The entry stays after the end, so a
	// resync can tell a task that the stream reported from one it missed.
	ended bool
}

// kimiTaskInfo is the `info` of task.started and task.terminated.
type kimiTaskInfo struct {
	TaskID      string `json:"taskId"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Command     string `json:"command"`
	AgentID     string `json:"agentId"`
	StopReason  string `json:"stopReason"`
}

// kimiTaskEvent is the task.started and task.terminated payload.
type kimiTaskEvent struct {
	Info kimiTaskInfo `json:"info"`
}

// kimiTaskRowKey is the registry row key of a process task. The session
// qualifies it for the reason kimiChildRowKey states.
func kimiTaskRowKey(sessionID, taskID string) string {
	return sessionID + "/task/" + taskID
}

// kimiTaskStatus maps a task's status onto the registry's. final is false for a
// status that is not an end, so the row stays open rather than being closed by a
// value the registry treats as absorbing.
func kimiTaskStatus(status string) (bgtask.Status, bool) {
	switch status {
	case kimiTaskStatusCompleted:
		return bgtask.StatusCompleted, true
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

// kimiTaskItem is the part of one item of GET /sessions/{id}/tasks that a
// resync reads.
type kimiTaskItem struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	Description string `json:"description"`
	Command     string `json:"command"`
	// AgentID, SubagentType and ParentToolCall state a subagent task.
	AgentID         string `json:"agent_id"`
	SubagentType    string `json:"subagent_type"`
	ParentToolCall  string `json:"parent_tool_call_id"`
	StartedAt       string `json:"started_at"`
	RunInBackground bool   `json:"run_in_background"`
}

// kimiWireTaskStatus maps the status word of a task item or of a roster
// subagent onto the registry's. final is false for a status that is not an
// end, so the row stays open rather than being closed by a value the registry
// treats as absorbing.
func kimiWireTaskStatus(status string) (bgtask.Status, bool) {
	switch status {
	case kimiWireStatusCompleted:
		return bgtask.StatusCompleted, true
	case kimiWireStatusFailed:
		return bgtask.StatusFailed, true
	case kimiWireStatusCancelled:
		return bgtask.StatusStopped, true
	case kimiWireStatusRunning:
		return bgtask.StatusRunning, false
	default:
		// A status outside the server's enumeration. Running is the only safe
		// answer, because a final status is absorbing.
		slog.Debug("kimi unknown task status in a listing", "status", status)
		return bgtask.StatusRunning, false
	}
}

// readTasks reads the main agent's tasks. The server lists every task that
// runs, and every detached task that ended. It lists no foreground task that
// ended, and no task of a subagent.
func (a *Agent) readTasks(ctx context.Context, sessionID string) ([]kimiTaskItem, error) {
	var reply struct {
		Items []kimiTaskItem `json:"items"`
	}
	if err := a.api.get(ctx, kimiSessionPath(sessionID, "/tasks"), &reply); err != nil {
		return nil, err
	}
	return reply.Items, nil
}

// kimiStartedBefore reports whether a task item started before at. A start
// time that does not parse counts as before: the task cannot be told from the
// session's history, and a row for history would be wrong.
//
// The server states the time in milliseconds, so at is cut to milliseconds as
// well. Otherwise a task that started in the same millisecond as at would read
// as earlier.
func kimiStartedBefore(startedAt string, at time.Time) bool {
	started, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return true
	}
	return started.Before(at.Truncate(time.Millisecond))
}

// reconcileTasks repairs the rows of the main agent's process tasks after a
// gap that the stream could not replay. A task that ended during the gap
// closes with the status the server states. A task that started during the
// gap opens, and closes again when it ended during the gap too. A task that
// started before the agent attached is the session's history, and gets no
// row. The caller holds dispatchMu.
func (a *Agent) reconcileTasks(items []kimiTaskItem) {
	a.Mu.Lock()
	attachedAt := a.attachedAt
	a.Mu.Unlock()
	for _, item := range items {
		if item.Kind != kimiWireTaskKindBash || kimiCheckID("task", item.ID) != nil {
			continue
		}
		info := kimiTaskInfo{TaskID: item.ID, Kind: kimiTaskKindProcess, Description: item.Description, Command: item.Command}
		a.Mu.Lock()
		task := a.tasks[item.ID]
		ended := task != nil && task.ended
		a.Mu.Unlock()
		if ended {
			continue
		}
		if task == nil {
			if kimiStartedBefore(item.StartedAt, attachedAt) {
				continue
			}
			a.startTask(info)
		}
		if status, final := kimiWireTaskStatus(item.Status); final {
			a.endTask(info, status)
		}
	}
}

func (a *Agent) handleTaskStarted(event kimiEvent) {
	var payload kimiTaskEvent
	if !event.decode(&payload) || payload.Info.TaskID == "" {
		return
	}
	a.startTask(payload.Info)
}

// startTask records one task and opens its row. The caller holds dispatchMu.
func (a *Agent) startTask(info kimiTaskInfo) {
	a.Mu.Lock()
	task := &kimiTask{kind: info.Kind, agentID: info.AgentID}
	switch {
	case info.Kind == kimiTaskKindProcess:
		task.rowKey = kimiTaskRowKey(a.sessionID, info.TaskID)
	case info.Kind == kimiTaskKindAgent && info.AgentID != "":
		task.rowKey = kimiChildRowKey(a.sessionID, info.AgentID)
	}
	if a.tasks == nil {
		a.tasks = make(map[string]*kimiTask)
	}
	a.tasks[info.TaskID] = task
	a.Mu.Unlock()

	switch info.Kind {
	case kimiTaskKindProcess:
		title, isCommand := strings.TrimSpace(info.Command), true
		if title == "" {
			title, isCommand = strings.TrimSpace(info.Description), false
		} else {
			title = bgtask.CleanTitleRunes(bgtask.FirstLine(title), 120)
		}
		providerkit.LogRegistryRefusal("kimi", "upsert", a.sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: task.rowKey, Kind: bgtask.KindShell, Title: title, TitleIsCommand: isCommand,
			Description: strings.TrimSpace(info.Description), Status: bgtask.StatusRunning,
		}))
	case kimiTaskKindAgent:
		if info.AgentID == "" {
			return
		}
		// The spawn creates the row and its transcript. The task only states the
		// id that stops the subagent.
		a.children.update(info.AgentID, func(c *kimiChild) { c.taskID = info.TaskID })
	case kimiTaskKindQuestion:
		// The question reaches the user as its control request. See the file
		// comment.
	default:
		slog.Debug("kimi unknown task kind", "agent_id", a.AgentID(), "kind", info.Kind)
	}
}

func (a *Agent) handleTaskTerminated(event kimiEvent) {
	var payload kimiTaskEvent
	if !event.decode(&payload) || payload.Info.TaskID == "" {
		return
	}
	status, final := kimiTaskStatus(payload.Info.Status)
	if !final {
		return
	}
	a.endTask(payload.Info, status)
}

// endTask closes the row of a task that ended with status. The caller holds
// dispatchMu.
func (a *Agent) endTask(info kimiTaskInfo, status bgtask.Status) {
	a.Mu.Lock()
	task := a.tasks[info.TaskID]
	if task != nil {
		task.ended = true
	}
	sessionID := a.sessionID
	a.Mu.Unlock()
	switch info.Kind {
	case kimiTaskKindProcess:
		rowKey := kimiTaskRowKey(sessionID, info.TaskID)
		if task != nil && task.rowKey != "" {
			rowKey = task.rowKey
		}
		providerkit.LogRegistryRefusal("kimi", "close", a.sink.CloseBackgroundTask(rowKey, status))
	case kimiTaskKindAgent:
		if info.AgentID == "" {
			return
		}
		// The subagent's own subagent.* event closes its row as well. Whichever
		// arrives first closes it, because a final status is absorbing.
		if child, linked := a.children.get(info.AgentID); linked {
			a.children.update(info.AgentID, func(c *kimiChild) {
				c.taskID = ""
				c.ended = true
			})
			providerkit.LogRegistryRefusal("kimi", "close", a.sink.CloseBackgroundTask(child.rowKey, status))
		}
	}
}
