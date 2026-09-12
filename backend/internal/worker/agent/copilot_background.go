package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/internal/worker/bgtask"

	"github.com/leapmux/leapmux/generated/contracts"
)

func copilotTaskOutcome(raw json.RawMessage) (bgtask.Status, bool) {
	var event struct {
		Type string `json:"type"`
		Data struct {
			Cancelled bool `json:"cancelled"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &event) != nil || (event.Type != contracts.CopilotEventSubagentCompleted && event.Type != contracts.CopilotEventSubagentFailed) {
		return bgtask.StatusPending, false
	}
	if event.Data.Cancelled {
		return bgtask.StatusStopped, true
	}
	if event.Type == contracts.CopilotEventSubagentFailed {
		return bgtask.StatusFailed, true
	}
	return bgtask.StatusCompleted, true
}

// ACP acknowledges a background launch but omits the later subagent lifecycle event.
func (a *CopilotCLIAgent) watchBackgroundTask(task *copilotTaskState) {
	a.subagentMu.Lock()
	task.awaitingCompletion = true
	if a.backgroundWatchRunning || a.ctx == nil || a.ctx.Err() != nil {
		a.subagentMu.Unlock()
		return
	}
	a.backgroundWatchRunning = true
	a.subagentMu.Unlock()
	go a.watchBackgroundTasks(a.ctx)
}

func (a *CopilotCLIAgent) pendingBackgroundTasks() map[string]*copilotTaskState {
	a.subagentMu.Lock()
	defer a.subagentMu.Unlock()
	tasks := make(map[string]*copilotTaskState)
	for id, task := range a.subagentTasks {
		if task.awaitingCompletion {
			tasks[id] = task
		}
	}
	return tasks
}

func (a *CopilotCLIAgent) watchBackgroundTasks(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			a.subagentMu.Lock()
			a.backgroundWatchRunning = false
			a.subagentMu.Unlock()
			return
		case <-ticker.C:
			a.refreshBackgroundTasks(ctx)
		}
		a.subagentMu.Lock()
		pending := false
		for _, task := range a.subagentTasks {
			pending = pending || task.awaitingCompletion
		}
		if !pending {
			a.backgroundWatchRunning = false
		}
		a.subagentMu.Unlock()
		if !pending {
			return
		}
	}
}

func (a *CopilotCLIAgent) refreshBackgroundTasks(ctx context.Context) {
	path := copilotToolStorePath(a.currentSessionID(), a.currentWorkingDir())
	for id, expected := range a.pendingBackgroundTasks() {
		readCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		record, err := readCopilotToolEvents(readCtx, path, id, func(record *copilotNativeTool) bool { return record.Finished != nil })
		cancel()
		if err != nil {
			slog.Debug("Read Copilot background completion", "tool_call_id", id, "error", err)
		}
		if record == nil || record.Finished == nil {
			continue
		}
		status, valid := copilotTaskOutcome(record.Finished)
		if !valid {
			continue
		}
		a.subagentOpsMu.Lock()
		a.subagentMu.Lock()
		current := a.subagentTasks[id]
		a.subagentMu.Unlock()
		// A session reset or another completion can replace the task during the file read.
		if current == expected {
			if err := current.base.sink.CloseBackgroundTask(current.rowKey, status); err != nil {
				slog.Warn("Close Copilot background task", "tool_call_id", id, "error", err)
			} else {
				a.finishSubagentTask(id, status)
			}
		}
		a.subagentOpsMu.Unlock()
	}
}
