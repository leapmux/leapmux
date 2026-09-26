package codebuddy

import (
	"encoding/json"

	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// CodeBuddy's to-do list, in its own wire shapes.
//
// Two families feed one list, and BOTH state the WHOLE list on every call, so
// both are snapshots.
//
// `TodoWrite` re-sends the list in its tool_use INPUT as `newTodos`, beside the
// `oldTodos` it replaced. Its result carries no list at all.
//
// The `Task*` family addresses one row by id and its tool_result states the
// OUTCOME, but every one of them also returns the full list under
// `tool_use_result.todos` -- the shape their own UI reads -- so the result is a
// snapshot too. `TaskGet` alone is the exception: it returns the single task it
// read, with no `todos`, which is a one-row detail.
//
// Where each half lives decides where each parser reads. The tool name is on
// the tool_use block of an `assistant` frame. A `user` frame's tool_result
// block carries NO name, so the result parser discriminates on the shape of the
// structured payload instead: `todos` marks a full-list result, and a bare task
// object (`id`, `subject`, `status`) marks the `TaskGet` detail. That payload
// sits on the tool_result block's `_meta.rawResponse`, not at the top level the
// way Claude Code states its `tool_use_result`.

// Wire tool names of CodeBuddy's to-do list family.
const (
	codebuddyToolTodoWrite = "TodoWrite"
)

// codebuddyToolUseEnvelope is the message envelope of both halves of a call:
// `{type:"assistant"|"user", message:{content:[...]}}`. An assistant frame
// carries the tool_use blocks and a user frame the tool_result ones, so one
// shape serves both parsers.
type codebuddyToolUseEnvelope struct {
	Type    string `json:"type"`
	Message struct {
		Content []json.RawMessage `json:"content"`
	} `json:"message"`
}

// codebuddyToolUseBlock is one entry of that content array. Only the fields the
// extractor reads are declared; the block carries more.
type codebuddyToolUseBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// codebuddyToolResultBlock is the tool_result block of a user frame. The
// structured payload CodeBuddy returns rides its `_meta.rawResponse`.
type codebuddyToolResultBlock struct {
	Type    string `json:"type"`
	IsError bool   `json:"is_error"`
	Meta    struct {
		RawResponse json.RawMessage `json:"rawResponse"`
	} `json:"_meta"`
}

// codebuddyTodoWriteInput is the tool_use input of `TodoWrite`. `Todos` is the
// list after the call (`newTodos`); the `oldTodos` beside it is the list it
// replaced and is not read. A model that spells the field `todos` reaches the
// same parser: the CLI's own converter accepts both.
type codebuddyTodoWriteInput struct {
	Todos    []codebuddyTodoRow `json:"newTodos"`
	TodosAlt []codebuddyTodoRow `json:"todos"`
}

// codebuddyTodoRow is one row of a `TodoWrite` list: content, status and the
// spinner text, with no id.
type codebuddyTodoRow struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm"`
}

// codebuddyTaskRow is one row of a `Task*` result's `todos` list. That list is
// already mapped by the CLI into the shape its own UI reads, so the row carries
// the id and the subject under `content`.
type codebuddyTaskRow struct {
	ID          string `json:"id"`
	Content     string `json:"content"`
	Status      string `json:"status"`
	ActiveForm  string `json:"activeForm"`
	Description string `json:"description"`
}

// codebuddyTaskDetail is the single task a `TaskGet` result returns, in the
// storage shape rather than the mapped one: the subject is `subject`, not
// `content`.
type codebuddyTaskDetail struct {
	ID          string `json:"id"`
	Subject     string `json:"subject"`
	Status      string `json:"status"`
	ActiveForm  string `json:"activeForm"`
	Description string `json:"description"`
}

// codebuddyTaskListResult is the structured payload of a `TaskCreate`,
// `TaskUpdate` or `TaskList` result: the full list, already mapped. `Todos`
// being present is what marks a result as a full-list one.
type codebuddyTaskListResult struct {
	Todos []codebuddyTaskRow `json:"todos"`
}

// ExtractTodoEvent reads CodeBuddy's to-do list off one persisted message.
//
// The content is self-describing: an assistant frame names the tool on its
// tool_use block, and a result states the shape of its structured payload. So
// the parser needs no span type, which is how CodeBuddy persists (every frame
// is stored with an empty SpanInfo).
func (codebuddyProvider) ExtractTodoEvent(_ string, content []byte, _ func() []byte) (todoevents.Event, bool) {
	if len(content) == 0 {
		return todoevents.Event{}, false
	}
	var envelope codebuddyToolUseEnvelope
	if err := json.Unmarshal(content, &envelope); err != nil {
		return todoevents.Event{}, false
	}
	switch envelope.Type {
	case "assistant":
		return codebuddyTodoWriteEvent(envelope.Message.Content)
	case "user":
		return codebuddyTaskResultEvent(envelope.Message.Content)
	}
	return todoevents.Event{}, false
}

// codebuddyTodoWriteEvent reads the whole list off the `TodoWrite` tool_use
// input.
func codebuddyTodoWriteEvent(blocks []json.RawMessage) (todoevents.Event, bool) {
	for _, raw := range blocks {
		var block codebuddyToolUseBlock
		if err := json.Unmarshal(raw, &block); err != nil || block.Type != "tool_use" {
			continue
		}
		if block.Name != codebuddyToolTodoWrite {
			continue
		}
		var input codebuddyTodoWriteInput
		if err := json.Unmarshal(block.Input, &input); err != nil {
			return todoevents.Event{}, false
		}
		rows := input.Todos
		if rows == nil {
			rows = input.TodosAlt
		}
		return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: codebuddyTodoSnapshot(rows)}, true
	}
	return todoevents.Event{}, false
}

// codebuddyTaskResultEvent reads the list a `Task*` result returned, or the one
// row a `TaskGet` returned.
//
// A failed call returns an error payload with no list, and leaves the list
// alone: the shape checks below report "no event" rather than an empty snapshot
// that would wipe every row.
func codebuddyTaskResultEvent(blocks []json.RawMessage) (todoevents.Event, bool) {
	for _, raw := range blocks {
		var block codebuddyToolResultBlock
		if err := json.Unmarshal(raw, &block); err != nil || block.Type != "tool_result" {
			continue
		}
		if block.IsError || len(block.Meta.RawResponse) == 0 {
			continue
		}
		if event, ok := codebuddyTaskListEvent(block.Meta.RawResponse); ok {
			return event, true
		}
		return codebuddyTaskDetailEvent(block.Meta.RawResponse)
	}
	return todoevents.Event{}, false
}

// codebuddyTaskListEvent builds a snapshot of the full list a result carried.
// ok is false when the payload states none.
func codebuddyTaskListEvent(raw json.RawMessage) (todoevents.Event, bool) {
	var result codebuddyTaskListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return todoevents.Event{}, false
	}
	if result.Todos == nil {
		return todoevents.Event{}, false
	}
	items := make([]todoevents.Item, 0, len(result.Todos))
	for _, row := range result.Todos {
		items = append(items, todoevents.Item{
			ID:          row.ID,
			Content:     row.Content,
			Status:      todoevents.StatusFromProviderWord(row.Status),
			ActiveForm:  row.ActiveForm,
			Description: row.Description,
		})
	}
	return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: items}, true
}

// codebuddyTaskDetailEvent builds the one-row detail a `TaskGet` returned. ok
// is false when the payload is not a bare task, so a foreign structured result
// never becomes a row of the list.
func codebuddyTaskDetailEvent(raw json.RawMessage) (todoevents.Event, bool) {
	var task codebuddyTaskDetail
	if err := json.Unmarshal(raw, &task); err != nil {
		return todoevents.Event{}, false
	}
	if task.ID == "" || task.Subject == "" || task.Status == "" {
		return todoevents.Event{}, false
	}
	return todoevents.Event{
		Kind: todoevents.KindDetail,
		Item: todoevents.Item{
			ID:          task.ID,
			Content:     task.Subject,
			Status:      todoevents.StatusFromProviderWord(task.Status),
			ActiveForm:  task.ActiveForm,
			Description: task.Description,
		},
	}, true
}

// codebuddyTodoSnapshot maps `TodoWrite` rows onto the neutral items. The rows
// carry no id, which a snapshot does not need.
func codebuddyTodoSnapshot(rows []codebuddyTodoRow) []todoevents.Item {
	items := make([]todoevents.Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, todoevents.Item{
			Content:    row.Content,
			Status:     todoevents.StatusFromProviderWord(row.Status),
			ActiveForm: row.ActiveForm,
		})
	}
	return items
}
