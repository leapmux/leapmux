package kimi

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// kimiTodoStatus maps a TodoList status word onto the neutral status. Kimi
// spells a finished item `done`, which the shared parser does not know.
func kimiTodoStatus(word string) todoevents.Status {
	switch word {
	case contracts.KimiTodoStatusPending:
		return todoevents.StatusPending
	case contracts.KimiTodoStatusInProgress:
		return todoevents.StatusInProgress
	case contracts.KimiTodoStatusDone:
		return todoevents.StatusCompleted
	default:
		return todoevents.StatusFromProviderWord(word)
	}
}

// ExtractTodoEvent reads Kimi Code's to-do list off a TodoList call.
//
// Each call states the WHOLE list in `args.todos`, so this is a snapshot. A call
// that omits `todos` only reads the list and changes nothing, and an empty list
// clears it (docs/en/reference/tools.md). The list rides the call's opening
// `tool.call.started`; its result repeats nothing the list needs.
func (kimiProvider) ExtractTodoEvent(spanType string, content []byte, _ func() []byte) (todoevents.Event, bool) {
	if spanType != contracts.KimiToolTodoList {
		return todoevents.Event{}, false
	}
	var call struct {
		Type string `json:"type"`
		Name string `json:"name"`
		Args struct {
			Todos *[]struct {
				Title  string `json:"title"`
				Status string `json:"status"`
			} `json:"todos"`
		} `json:"args"`
	}
	if err := json.Unmarshal(content, &call); err != nil ||
		call.Type != contracts.KimiEventToolCallStarted || call.Name != contracts.KimiToolTodoList || call.Args.Todos == nil {
		return todoevents.Event{}, false
	}
	items := make([]todoevents.Item, 0, len(*call.Args.Todos))
	for _, todo := range *call.Args.Todos {
		items = append(items, todoevents.Item{Content: todo.Title, Status: kimiTodoStatus(todo.Status)})
	}
	return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: items}, true
}
