package codewhale

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// extractCodewhaleTodoEvent reads the to-do list off a finished `todo_write`
// call.
//
// It reads the RESULT, not the call's input. The runtime normalizes the list --
// it numbers each row and settles the statuses -- and states the result as
// `metadata.task_updates.checklist.items`, so the list LeapMux shows is the one
// the runtime keeps. A failed call changes nothing, and neither does its start.
//
// The whole list arrives on every call, so this is a SNAPSHOT.
//
// `update_plan` is not read. It is a hidden compatibility tool of the runtime
// that keeps a separate plan list, and one agent has one to-do list.
func extractCodewhaleTodoEvent(spanType string, content []byte) (todoevents.Event, bool) {
	if spanType != contracts.CodewhaleToolTodoWrite {
		return todoevents.Event{}, false
	}
	// The cheap exit first: this runs on every persisted row of the span.
	if !bytes.Contains(content, []byte(contracts.CodewhaleEventItemCompleted)) {
		return todoevents.Event{}, false
	}
	env, ok := parseEnvelope(content)
	if !ok || env.Event != contracts.CodewhaleEventItemCompleted {
		return todoevents.Event{}, false
	}
	var payload itemEventPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return todoevents.Event{}, false
	}
	var metadata todoResultMetadata
	if json.Unmarshal(payload.Item.Metadata, &metadata) != nil {
		return todoevents.Event{}, false
	}
	if metadata.ToolName != contracts.CodewhaleToolTodoWrite || metadata.TaskUpdates.Checklist == nil {
		return todoevents.Event{}, false
	}
	rows := metadata.TaskUpdates.Checklist.Items
	items := make([]todoevents.Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, todoevents.Item{
			ID:      row.id(),
			Content: row.Content,
			Status:  todoevents.StatusFromProviderWord(row.Status),
		})
	}
	return todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: items}, true
}

// todoResultMetadata is the metadata of a finished `todo_write` call: the tool's
// identity and the checklist the runtime kept. contract_tags_test.go pins the
// tags of the three types below to the contract, which the browser plugin reads
// the same names from.
type todoResultMetadata struct {
	itemMetadata
	TaskUpdates todoTaskUpdates `json:"task_updates"`
}

// todoTaskUpdates is the `task_updates` record of a to-do result.
type todoTaskUpdates struct {
	// Checklist is nil for a result that states no list.
	Checklist *todoChecklist `json:"checklist"`
}

// todoChecklist is the list the runtime keeps.
type todoChecklist struct {
	Items []todoRow `json:"items"`
}

// todoRow is one row of the runtime's checklist. Its id is a number.
type todoRow struct {
	ID      json.RawMessage `json:"id"`
	Content string          `json:"content"`
	Status  string          `json:"status"`
}

// id reads the row's id as text, whether the runtime states it as a number or
// as a string.
func (r todoRow) id() string {
	var number json.Number
	if err := json.Unmarshal(r.ID, &number); err == nil {
		if _, err := strconv.ParseFloat(number.String(), 64); err == nil {
			return number.String()
		}
	}
	var text string
	if json.Unmarshal(r.ID, &text) == nil {
		return strings.TrimSpace(text)
	}
	return ""
}
