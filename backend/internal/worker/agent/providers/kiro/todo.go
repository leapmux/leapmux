package kiro

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// Kiro keeps its to-do list in the `todo_list` tool, not in the protocol's
// plan update. Every call -- create, add, complete, remove, list -- answers
// with the whole list in `rawOutput.tasks`, so each finished call is a
// snapshot. A task is done or not: Kiro has no state for a task that runs.

// kiroTodoResult is the part of a finished `todo_list` call that the list
// reads.
type kiroTodoResult struct {
	SessionUpdate string `json:"sessionUpdate"`
	Status        string `json:"status"`
	Title         string `json:"title"`
	RawOutput     struct {
		Tasks []struct {
			ID              string `json:"id"`
			TaskDescription string `json:"task_description"`
			Details         string `json:"details"`
			Completed       bool   `json:"completed"`
		} `json:"tasks"`
	} `json:"rawOutput"`
}

// ExtractTodoEvent reads Kiro's to-do list off one persisted message: the
// finished update of a `todo_list` call. Kiro's tool call states no tool name,
// so the title that Kiro gives the tool identifies it.
//
// It also reads the protocol's plan update through the shared reader, for a
// later Kiro that sends one.
func (p kiroProvider) ExtractTodoEvent(spanType string, content []byte, pairedToolUse func() []byte) (todoevents.Event, bool) {
	if event, ok := p.Provider.ExtractTodoEvent(spanType, content, pairedToolUse); ok {
		return event, true
	}
	// The cheap exit for every message that is not the list.
	if !bytes.Contains(content, []byte(contracts.KiroToolTitleTaskList)) {
		return todoevents.Event{}, false
	}
	var result kiroTodoResult
	if json.Unmarshal(content, &result) != nil ||
		result.SessionUpdate != contracts.ACPUpdateToolCallUpdate ||
		result.Status != "completed" ||
		result.Title != contracts.KiroToolTitleTaskList ||
		result.RawOutput.Tasks == nil {
		return todoevents.Event{}, false
	}
	items := make([]todoevents.Item, 0, len(result.RawOutput.Tasks))
	for _, task := range result.RawOutput.Tasks {
		text := strings.TrimSpace(task.TaskDescription)
		if text == "" {
			continue
		}
		status := todoevents.StatusPending
		if task.Completed {
			status = todoevents.StatusCompleted
		}
		items = append(items, todoevents.Item{
			ID:          task.ID,
			Content:     text,
			Description: strings.TrimSpace(task.Details),
			Status:      status,
		})
	}
	return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: items}, true
}
