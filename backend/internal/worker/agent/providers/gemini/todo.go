package gemini

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// A recovered native write_todos result supplies the completed snapshot.
func (provider geminiProvider) ExtractTodoEvent(spanType string, content []byte, paired func() []byte) (todoevents.Event, bool) {
	if event, present := provider.Provider.ExtractTodoEvent(spanType, content, paired); present {
		return event, true
	}
	var frame struct {
		SessionUpdate string                     `json:"sessionUpdate"`
		ToolCallID    string                     `json:"toolCallId"`
		Status        string                     `json:"status"`
		RawOutput     map[string]json.RawMessage `json:"rawOutput"`
	}
	if json.Unmarshal(content, &frame) != nil || frame.SessionUpdate != contracts.ACPUpdateToolCallUpdate || frame.Status != "completed" || frame.ToolCallID == "" {
		return todoevents.Event{}, false
	}
	var record struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Status        string `json:"status"`
		ResultDisplay struct {
			Todos json.RawMessage `json:"todos"`
		} `json:"resultDisplay"`
	}
	if json.Unmarshal(frame.RawOutput[contracts.GeminiSupplementStoredToolRecord], &record) != nil ||
		record.ID != frame.ToolCallID || record.Name != contracts.GeminiToolTodoWrite || record.Status != "success" || len(record.ResultDisplay.Todos) == 0 {
		return todoevents.Event{}, false
	}
	var todos []struct {
		Description string `json:"description"`
		Status      string `json:"status"`
	}
	if json.Unmarshal(record.ResultDisplay.Todos, &todos) != nil || todos == nil {
		return todoevents.Event{}, false
	}
	items := make([]todoevents.Item, 0, len(todos))
	for _, todo := range todos {
		items = append(items, todoevents.Item{Content: todo.Description, Status: todoevents.StatusFromProviderWord(todo.Status)})
	}
	return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: items}, true
}
