package ohmypi

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// ExtractTodoEvent reads the to-do list a finished `todo` call states.
//
// Every `todo` operation -- init, start, done, drop, append and the rest --
// returns the WHOLE list after it in its result's `details.phases`, so each end
// frame is a snapshot. omp groups the tasks in phases and gives a task no id: it
// matches a task by its text. A row's id is therefore its phase and its text,
// which stays the same while the task moves from one status to the next.
func (ompProvider) ExtractTodoEvent(spanType string, content []byte, _ func() []byte) (todoevents.Event, bool) {
	if spanType != contracts.OhMyPiToolTodo {
		return todoevents.Event{}, false
	}
	var envelope struct {
		Type     string `json:"type"`
		ToolName string `json:"toolName"`
		IsError  bool   `json:"isError"`
		Result   struct {
			Details struct {
				Phases *[]struct {
					Name  string `json:"name"`
					Tasks []struct {
						Content string `json:"content"`
						Status  string `json:"status"`
						Blocker string `json:"blocker"`
					} `json:"tasks"`
				} `json:"phases"`
			} `json:"details"`
		} `json:"result"`
	}
	if json.Unmarshal(content, &envelope) != nil || envelope.Type != contracts.OhMyPiEventToolExecutionEnd ||
		envelope.ToolName != contracts.OhMyPiToolTodo || envelope.IsError || envelope.Result.Details.Phases == nil {
		return todoevents.Event{}, false
	}
	var items []todoevents.Item
	seen := make(map[string]int)
	for _, phase := range *envelope.Result.Details.Phases {
		for _, task := range phase.Tasks {
			text := strings.TrimSpace(task.Content)
			if text == "" {
				continue
			}
			id := phase.Name + "/" + text
			seen[id]++
			if n := seen[id]; n > 1 {
				id += "#" + strconv.Itoa(n)
			}
			description := phase.Name
			if blocker := strings.TrimSpace(task.Blocker); blocker != "" {
				description = strings.TrimSpace(description + "\nBlocked: " + blocker)
			}
			items = append(items, todoevents.Item{
				ID:          id,
				Content:     text,
				Description: description,
				Status:      todoStatus(task.Status),
			})
		}
	}
	return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: items}, true
}

// todoStatus maps omp's task status onto the neutral one. An abandoned task stays
// visible and stops being work, which is what the neutral deleted status states. A
// blocked task is still work to do.
func todoStatus(status string) todoevents.Status {
	switch status {
	case contracts.OhMyPiTodoStatusInProgress:
		return todoevents.StatusInProgress
	case contracts.OhMyPiTodoStatusCompleted:
		return todoevents.StatusCompleted
	case contracts.OhMyPiTodoStatusAbandoned:
		return todoevents.StatusDeleted
	case contracts.OhMyPiTodoStatusPending, contracts.OhMyPiTodoStatusBlocked:
		return todoevents.StatusPending
	default:
		return todoevents.StatusPending
	}
}
