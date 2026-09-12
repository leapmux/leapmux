package agent

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// ExtractTodoEvent reads rpiv-todo's complete saved state from a finished tool call.
func (piProvider) ExtractTodoEvent(_ string, content []byte, _ func() []byte) (todoevents.Event, bool) {
	var envelope struct {
		Type     string `json:"type"`
		ToolName string `json:"toolName"`
		Result   struct {
			Details struct {
				Tasks *[]struct {
					ID          int64  `json:"id"`
					Subject     string `json:"subject"`
					Description string `json:"description"`
					ActiveForm  string `json:"activeForm"`
					Status      string `json:"status"`
				} `json:"tasks"`
			} `json:"details"`
		} `json:"result"`
	}
	if json.Unmarshal(content, &envelope) != nil || envelope.Type != contracts.PiEventToolExecutionEnd || envelope.ToolName != contracts.PiToolTodo || envelope.Result.Details.Tasks == nil {
		return todoevents.Event{}, false
	}
	items := make([]todoevents.Item, 0, len(*envelope.Result.Details.Tasks))
	seen := make(map[int64]struct{}, len(*envelope.Result.Details.Tasks))
	for _, task := range *envelope.Result.Details.Tasks {
		// Pi stores numeric IDs. Both languages must preserve the same exact integer.
		if task.ID <= 0 || task.ID > 1<<53-1 || strings.TrimSpace(task.Subject) == "" {
			return todoevents.Event{}, false
		}
		if _, duplicate := seen[task.ID]; duplicate {
			return todoevents.Event{}, false
		}
		seen[task.ID] = struct{}{}
		switch task.Status {
		case "pending", "in_progress", "completed", "deleted":
		default:
			return todoevents.Event{}, false
		}
		items = append(items, todoevents.Item{ID: strconv.FormatInt(task.ID, 10), Content: task.Subject, Description: task.Description, ActiveForm: task.ActiveForm, Status: todoevents.StatusFromWire(task.Status)})
	}
	return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: items}, true
}
