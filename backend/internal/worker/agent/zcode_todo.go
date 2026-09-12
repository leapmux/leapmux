package agent

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// ExtractTodoEvent reads ZCode's to-do list off a TodoWrite tool call.
//
// ZCode gives the tool exactly the name Claude Code does and takes the same
// `{todos:[{content,status}]}` input, but it carries it in its OWN envelope -- a
// `tool.updated` event whose payload holds the tool name and the input -- so the
// Claude reader finds nothing in it.
//
// The whole list arrives on every call, so this is a SNAPSHOT: the app-server
// re-sends the complete list rather than a delta, and an incremental apply would
// keep a row the model deleted.
//
// Scheduled input updates the projection. Result rows do not repeat that update.
func (zcodeProvider) ExtractTodoEvent(spanType string, content []byte, _ func() []byte) (todoevents.Event, bool) {
	if spanType != contracts.ZCodeToolNameTodoWrite {
		return todoevents.Event{}, false
	}
	var env zcodeEventEnvelope
	if err := json.Unmarshal(content, &env); err != nil || env.Type != contracts.ZCodeEventToolUpdated {
		return todoevents.Event{}, false
	}
	var payload struct {
		Kind     string          `json:"kind"`
		ToolName string          `json:"toolName"`
		Input    json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return todoevents.Event{}, false
	}
	if payload.Kind != contracts.ZCodeToolKindScheduled || (payload.ToolName != "" && payload.ToolName != contracts.ZCodeToolNameTodoWrite) {
		return todoevents.Event{}, false
	}
	// The resolver supplies streamed arguments from supplemental content.
	// Missing arguments leave the list unchanged. Only an explicit empty todos array clears it.
	var input struct {
		Todos *[]struct {
			Content    string `json:"content"`
			Status     string `json:"status"`
			ActiveForm string `json:"activeForm"`
		} `json:"todos"`
	}
	if err := json.Unmarshal(payload.Input, &input); err != nil || input.Todos == nil {
		return todoevents.Event{}, false
	}
	items := make([]todoevents.Item, 0, len(*input.Todos))
	for _, t := range *input.Todos {
		items = append(items, todoevents.Item{
			Content:    t.Content,
			Status:     todoevents.StatusFromWire(t.Status),
			ActiveForm: t.ActiveForm,
		})
	}
	return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: items}, true
}
