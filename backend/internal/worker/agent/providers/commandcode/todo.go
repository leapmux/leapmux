package commandcode

import (
	"encoding/json"
	"regexp"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

var createdTask = regexp.MustCompile(`^Task #([0-9]+) created: `)
var changedTask = regexp.MustCompile(`^(?:Updated|Deleted) task #([0-9]+)`)

func todoStatus(value string) (todoevents.Status, bool) {
	switch value {
	case "pending":
		return todoevents.StatusPending, true
	case "in_progress":
		return todoevents.StatusInProgress, true
	case "completed":
		return todoevents.StatusCompleted, true
	case "deleted":
		return todoevents.StatusDeleted, true
	}
	return todoevents.StatusUnspecified, false
}

func todoEvent(tool string, raw json.RawMessage, result string) (todoevents.Event, bool) {
	var input struct {
		TaskID      string  `json:"taskId"`
		Subject     *string `json:"subject"`
		Description *string `json:"description"`
		ActiveForm  *string `json:"activeForm"`
		Status      string  `json:"status"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return todoevents.Event{}, false
	}
	switch tool {
	case contracts.CommandCodeToolTaskCreate:
		match := createdTask.FindStringSubmatch(result)
		if len(match) != 2 || input.Subject == nil {
			return todoevents.Event{}, false
		}
		item := todoevents.Item{ID: match[1], Content: *input.Subject, Status: todoevents.StatusPending}
		if input.Description != nil {
			item.Description = *input.Description
		}
		if input.ActiveForm != nil {
			item.ActiveForm = *input.ActiveForm
		}
		return todoevents.Event{Kind: todoevents.KindCreate, Item: item}, true
	case contracts.CommandCodeToolTaskUpdate:
		match := changedTask.FindStringSubmatch(result)
		if len(match) != 2 || match[1] != input.TaskID {
			return todoevents.Event{}, false
		}
		patch := todoevents.Patch{Content: input.Subject, Description: input.Description, ActiveForm: input.ActiveForm}
		if input.Status != "" {
			status, ok := todoStatus(input.Status)
			if !ok {
				return todoevents.Event{}, false
			}
			patch.Status = &status
		}
		return todoevents.Event{Kind: todoevents.KindUpdate, ID: input.TaskID, Patch: patch}, true
	}
	return todoevents.Event{}, false
}
