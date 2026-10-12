package droid

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// droidTodoRow is one item of the whole list a TodoWrite call carries.
type droidTodoRow struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

// ExtractTodoEvent reads the snapshot from a persisted TodoWrite tool call.
// The tool result states no list, so only the call's input changes sidebar state.
func (droidProvider) ExtractTodoEvent(spanType string, content []byte, _ func() []byte) (todoevents.Event, bool) {
	if spanType != "" && spanType != contracts.DroidToolTodoWrite {
		return todoevents.Event{}, false
	}
	var frame struct {
		Type    string `json:"type"`
		ToolUse struct {
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"toolUse"`
	}
	if err := json.Unmarshal(content, &frame); err != nil ||
		frame.Type != contracts.DroidToolNotificationToolCall ||
		frame.ToolUse.Name != contracts.DroidToolTodoWrite {
		return todoevents.Event{}, false
	}
	var input struct {
		Todos *[]droidTodoRow `json:"todos"`
	}
	if err := json.Unmarshal(frame.ToolUse.Input, &input); err != nil || input.Todos == nil {
		return todoevents.Event{}, false
	}
	items := make([]todoevents.Item, 0, len(*input.Todos))
	for _, row := range *input.Todos {
		items = append(items, todoevents.Item{
			Content: row.Content,
			Status:  todoevents.StatusFromProviderWord(row.Status),
		})
	}
	return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: items}, true
}
