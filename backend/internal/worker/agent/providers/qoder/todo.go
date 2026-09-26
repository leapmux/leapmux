package qoder

import (
	"encoding/json"

	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// Qoder's to-do list, in its own wire shape.
//
// `WriteTodos` is the only tool that states one, and it re-sends the WHOLE list
// on every call, so it is a snapshot. The list is the tool_use INPUT:
//
//	{"todos":[{"description":str,"status":"pending|in_progress|completed|cancelled|blocked"}]}
//
// The result carries no list. The two row fields are the only ones the CLI
// keeps -- anything else on a row is dropped before the tool runs -- and
// `content` is accepted as a model-side spelling of `description`.
//
// A user frame's tool_result block carries no tool name, and nothing on the
// result states a list, so only the assistant half of a call is read here.

// qoderToolWriteTodos is the wire name of Qoder's to-do tool. The tool shares
// its description with the `TodoWrite` tool of its protocol family, but the
// wire name and the row shape are Qoder's own.
const qoderToolWriteTodos = "WriteTodos"

// qoderToolUseEnvelope is the assistant-shape JSON Qoder emits for tool_use
// messages: `{type:"assistant", message:{content:[{type:"tool_use", name,
// input}]}}`.
type qoderToolUseEnvelope struct {
	Type    string `json:"type"`
	Message struct {
		Content []json.RawMessage `json:"content"`
	} `json:"message"`
}

// qoderToolUseBlock is one entry of that content array. Only the fields the
// extractor reads are declared; the block carries more.
type qoderToolUseBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// qoderWriteTodosInput is the tool_use input of `WriteTodos`.
type qoderWriteTodosInput struct {
	Todos []qoderTodoRow `json:"todos"`
}

// qoderTodoRow is one row of the list. `Content` is the model-side spelling of
// `description`; the CLI maps it before the tool runs, so both arrive.
type qoderTodoRow struct {
	Description string `json:"description"`
	Content     string `json:"content"`
	Status      string `json:"status"`
}

// ExtractTodoEvent reads Qoder's to-do list off one persisted message.
//
// The content is self-describing: the tool name sits on the tool_use block of
// an assistant frame. Qoder persists every frame with an empty SpanInfo, so the
// parser reads the frame rather than a span type.
func (qoderProvider) ExtractTodoEvent(_ string, content []byte, _ func() []byte) (todoevents.Event, bool) {
	if len(content) == 0 {
		return todoevents.Event{}, false
	}
	var envelope qoderToolUseEnvelope
	if err := json.Unmarshal(content, &envelope); err != nil || envelope.Type != "assistant" {
		return todoevents.Event{}, false
	}
	for _, raw := range envelope.Message.Content {
		var block qoderToolUseBlock
		if err := json.Unmarshal(raw, &block); err != nil || block.Type != "tool_use" {
			continue
		}
		if block.Name != qoderToolWriteTodos {
			continue
		}
		var input qoderWriteTodosInput
		if err := json.Unmarshal(block.Input, &input); err != nil {
			return todoevents.Event{}, false
		}
		items := make([]todoevents.Item, 0, len(input.Todos))
		for _, row := range input.Todos {
			description := row.Description
			if description == "" {
				description = row.Content
			}
			items = append(items, todoevents.Item{
				Content: description,
				Status:  todoevents.StatusFromProviderWord(row.Status),
			})
		}
		return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: items}, true
	}
	return todoevents.Event{}, false
}
