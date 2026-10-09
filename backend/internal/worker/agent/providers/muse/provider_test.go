package muse

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMuseTokenResumeRule(t *testing.T) { agenttest.AssertTokenResumeRule(t, museProvider{}) }
func TestMusePreservesResponseWithoutARequest(t *testing.T) {
	agenttest.AssertPreservesTheResponseWithoutARequest(t, museProvider{})
}
func TestMuseWithholdsResponseForAMalformedRequest(t *testing.T) {
	agenttest.AssertWithholdsTheResponseForAMalformedRequest(t, museProvider{})
}
func TestMuseChildCapabilities(t *testing.T) {
	agenttest.AssertChildCapabilities(t, museProvider{}, (*Agent)(nil))
}

func TestMuseTodoSnapshotReadsTheActualNativeItemsAndText(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"method":"session/todoListChanged","params":{"sessionId":"session","viewCursor":"todos:1","revision":1,"sourceTool":"write_todos","items":[{"text":"Read the native source","status":"completed"},{"text":"Run the native checks","status":"inProgress","activeForm":"Run the native checks"},{"text":"Keep the next item","status":"pending"},{"text":"Cancel this item","status":"cancelled"}]}}`)
	event, ok := (museProvider{}).ExtractTodoEvent("", raw, nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	assert.Equal(t, []todoevents.Item{
		{Content: "Read the native source", Status: todoevents.StatusCompleted},
		{Content: "Run the native checks", Status: todoevents.StatusInProgress, ActiveForm: "Run the native checks"},
		{Content: "Keep the next item", Status: todoevents.StatusPending},
		{Content: "Cancel this item", Status: todoevents.StatusDeleted},
	}, event.Snapshot)
}

func TestMuseTodoSnapshotDistinguishesAClearFromMalformedNativeData(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{`{"method":"session/todoListChanged","params":{"items":[]}}`, true},
		{`{"method":"session/todoListChanged","params":{}}`, false},
		{`{"method":"session/todoListChanged","params":{"items":null}}`, false},
		{`{"method":"session/todoListChanged","params":{"items":{}}}`, false},
		{`{"method":"session/todoListChanged","params":{"items":[{"text":7,"status":"pending"}]}}`, false},
		{`{"method":"session/todoListChanged","params":{"items":[{"text":"Keep the old list","status":"futureStatus"}]}}`, false},
		{`{"method":"session/todoListChanged","params":{"items":[{"text":null,"status":"pending"}]}}`, false},
	} {
		event, ok := (museProvider{}).ExtractTodoEvent("", []byte(tc.raw), nil)
		assert.Equal(t, tc.valid, ok, tc.raw)
		assert.Empty(t, event.Snapshot)
	}
}

func TestMuseTodoSnapshotPreservesExactTextAndOptionalActiveForm(t *testing.T) {
	t.Parallel()
	text := " \n Native task 文 \n "
	activeForm := " \n Native work 文 \n "
	raw, err := json.Marshal(map[string]any{"method": "session/todoListChanged", "params": map[string]any{"items": []any{
		map[string]any{"text": text, "status": "inProgress", "activeForm": activeForm},
		map[string]any{"text": "Pending task", "status": "pending"},
	}}})
	require.NoError(t, err)
	before := append([]byte(nil), raw...)
	event, ok := (museProvider{}).ExtractTodoEvent("", raw, nil)
	require.True(t, ok)
	assert.Equal(t, []todoevents.Item{
		{Content: text, Status: todoevents.StatusInProgress, ActiveForm: activeForm},
		{Content: "Pending task", Status: todoevents.StatusPending},
	}, event.Snapshot)
	assert.Equal(t, before, raw)
}

func TestMuseTodoSnapshotRejectsACompleteListWhenAnyRowIsMalformed(t *testing.T) {
	t.Parallel()
	for _, row := range []any{
		nil, true, 0, -1, "Native task", []any{},
		map[string]any{"status": "pending"},
		map[string]any{"text": "", "status": "pending"},
		map[string]any{"text": " \n\t ", "status": "pending"},
		map[string]any{"text": "Native task"},
		map[string]any{"text": "Native task", "status": ""},
		map[string]any{"text": "Native task", "status": "in_progress"},
		map[string]any{"text": "Native task", "status": "pending", "activeForm": nil},
		map[string]any{"text": "Native task", "status": "pending", "activeForm": 0},
		map[string]any{"text": "Native task", "status": "pending", "activeForm": []any{}},
	} {
		raw, err := json.Marshal(map[string]any{"method": "session/todoListChanged", "params": map[string]any{"items": []any{
			map[string]any{"text": "Valid earlier task", "status": "pending"}, row,
		}}})
		require.NoError(t, err)
		event, ok := (museProvider{}).ExtractTodoEvent("", raw, nil)
		assert.False(t, ok, string(raw))
		assert.Empty(t, event.Snapshot)
	}
}

func TestMuseTodoSnapshotKeepsLargeTextAndEveryNativeRow(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("Native task 文", 8192)
	items := make([]map[string]any, 128)
	for index := range items {
		items[index] = map[string]any{"text": text, "status": "pending"}
	}
	raw, err := json.Marshal(map[string]any{"method": "session/todoListChanged", "params": map[string]any{"items": items}})
	require.NoError(t, err)
	event, ok := (museProvider{}).ExtractTodoEvent("", raw, nil)
	require.True(t, ok)
	require.Len(t, event.Snapshot, len(items))
	for _, item := range event.Snapshot {
		assert.Equal(t, text, item.Content)
		assert.Equal(t, todoevents.StatusPending, item.Status)
	}
}

func TestMuseTodoSnapshotRejectsTheWrongMethodAndNestedListShape(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"method":"item/completed","params":{"items":[{"text":"Native task","status":"pending"}]}}`,
		`{"method":"session/todoListChanged","params":{"todoList":{"items":[{"text":"Native task","status":"pending"}]}}}`,
	} {
		event, ok := (museProvider{}).ExtractTodoEvent("", []byte(raw), nil)
		assert.False(t, ok)
		assert.Empty(t, event.Snapshot)
	}
}
