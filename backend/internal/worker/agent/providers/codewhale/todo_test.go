package codewhale

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// todoWriteResult is a finished `todo_write` call as the runtime sends it.
func todoWriteResult(t *testing.T, event string, items []map[string]any) []byte {
	t.Helper()
	metadata := map[string]any{
		"canonical_tool": "todo_write",
		"task_updates":   map[string]any{"checklist": map[string]any{"items": items, "completion_pct": 33}},
	}
	return toolEndEvent(9, event, "item_1", "call_1", contracts.CodewhaleToolTodoWrite, "Todo list updated", map[string]any{"todos": []any{}}, metadata)
}

func TestProviderExtractsTheKeptChecklist(t *testing.T) {
	t.Parallel()
	provider := codewhaleProvider{}
	content := todoWriteResult(t, "item.completed", []map[string]any{
		{"id": 1, "content": "Read the code", "status": "completed"},
		{"id": "two", "content": "Write the fix", "status": "in_progress"},
		{"id": 3, "content": "Run the tests", "status": "pending"},
	})

	got, ok := provider.ExtractTodoEvent(contracts.CodewhaleToolTodoWrite, content, nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, got.Kind)
	assert.Equal(t, []todoevents.Item{
		{ID: "1", Content: "Read the code", Status: todoevents.StatusFromProviderWord("completed")},
		{ID: "two", Content: "Write the fix", Status: todoevents.StatusFromProviderWord("in_progress")},
		{ID: "3", Content: "Run the tests", Status: todoevents.StatusFromProviderWord("pending")},
	}, got.Snapshot)
}

func TestProviderExtractsNoChecklistFromAnythingElse(t *testing.T) {
	t.Parallel()
	provider := codewhaleProvider{}
	items := []map[string]any{{"id": 1, "content": "A", "status": "pending"}}
	cases := map[string]struct {
		spanType string
		content  []byte
	}{
		"another tool's span":      {contracts.CodewhaleToolBash, todoWriteResult(t, "item.completed", items)},
		"a failed call":            {contracts.CodewhaleToolTodoWrite, todoWriteResult(t, "item.failed", items)},
		"the call's start":         {contracts.CodewhaleToolTodoWrite, toolStartEvent(1, "item_1", "call_1", contracts.CodewhaleToolTodoWrite, map[string]any{"todos": []any{}})},
		"a result with no list":    {contracts.CodewhaleToolTodoWrite, toolEndEvent(9, "item.completed", "item_1", "call_1", contracts.CodewhaleToolTodoWrite, "done", nil, nil)},
		"a result of another tool": {contracts.CodewhaleToolTodoWrite, toolEndEvent(9, "item.completed", "item_1", "call_1", contracts.CodewhaleToolUpdatePlan, "done", nil, map[string]any{"task_updates": map[string]any{"checklist": map[string]any{"items": items}}})},
		"a broken frame":           {contracts.CodewhaleToolTodoWrite, []byte(`{"event":"item.completed","payload":"x"}`)},
	}
	for name, tc := range cases {
		_, ok := provider.ExtractTodoEvent(tc.spanType, tc.content, nil)
		assert.False(t, ok, name)
	}
}

func TestTodoRowID(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]string{`7`: "7", `" x "`: "x", `1.5`: "1.5", `null`: "", `{}`: "", ``: ""} {
		assert.Equal(t, want, todoRow{ID: json.RawMessage(raw)}.id(), raw)
	}
}
