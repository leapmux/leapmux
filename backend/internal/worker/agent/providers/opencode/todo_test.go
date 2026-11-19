package opencode

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodetest"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeFamilyEmptyNativeTodoResultClearsTheList(t *testing.T) {
	t.Parallel()
	event, ok := (FamilyProvider{}).ExtractTodoEvent("other", []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"todos","status":"completed","rawOutput":{"metadata":{"todos":[]}}}`), nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	assert.Empty(t, event.Snapshot)
}

func TestOpenCodeFamilyTodoReplayAndMalformedResults(t *testing.T) {
	t.Parallel()
	provider := (FamilyProvider{})
	event, ok := provider.ExtractTodoEvent("other", []byte(`{"sessionUpdate":"tool_call","toolCallId":"todos","status":"completed","rawOutput":{"metadata":{"todos":[{"content":"Cancelled task","status":"cancelled"}]}}}`), nil)
	require.True(t, ok)
	require.Len(t, event.Snapshot, 1)
	assert.Equal(t, todoevents.StatusDeleted, event.Snapshot[0].Status)
	for _, raw := range []string{
		`{"sessionUpdate":"tool_call_update","toolCallId":"todos","status":"failed","rawOutput":{"metadata":{"todos":[]}}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"todos","status":"completed","rawOutput":{"metadata":{"todos":null}}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"todos","status":"completed","rawOutput":{"metadata":{"todos":[null,17,{}]}}}`,
	} {
		_, ok := provider.ExtractTodoEvent("other", []byte(raw), nil)
		assert.False(t, ok, raw)
	}
}

func TestOpenCodeFamilyNativeTodoResults(t *testing.T) {
	t.Parallel()
	opencodetest.AssertReadsTheFamilyTodoResult(t, (FamilyProvider{}))
}

func TestOpenCodeNativeTodoSnapshots(t *testing.T) {
	t.Parallel()
	provider := opencodeProvider{}
	for _, content := range []string{
		`{"type":"session.tool.success","data":{"name":"todowrite","metadata":{"todos":[{"content":"Inspect","status":"in_progress"},{"content":"Done","status":"completed"},{"content":"Removed","status":"cancelled"}]}}}`,
		`{"type":"tool","name":"todowrite","state":{"status":"completed","metadata":{"todos":[{"content":"Inspect","status":"in_progress"},{"content":"Done","status":"completed"},{"content":"Removed","status":"cancelled"}]}}}`,
	} {
		event, ok := provider.ExtractTodoEvent("result", []byte(content), nil)
		require.True(t, ok, content)
		require.Len(t, event.Snapshot, 3)
		assert.Equal(t, todoevents.StatusInProgress, event.Snapshot[0].Status)
		assert.Equal(t, todoevents.StatusCompleted, event.Snapshot[1].Status)
		assert.Equal(t, todoevents.StatusDeleted, event.Snapshot[2].Status)
	}
	event, ok := provider.ExtractTodoEvent("result", []byte(`{"type":"session.tool.success","data":{"name":"todowrite","metadata":{"todos":[]}}}`), nil)
	require.True(t, ok)
	assert.Empty(t, event.Snapshot)
	_, ok = provider.ExtractTodoEvent("todowrite", []byte(`{"type":"session.tool.success","data":{"metadata":{"todos":[]}}}`), nil)
	require.True(t, ok, "native success events omit the tool name")
	_, ok = provider.ExtractTodoEvent("shell", []byte(`{"type":"session.tool.success","data":{"metadata":{"todos":[]}}}`), nil)
	assert.False(t, ok, "another tool must not replace the native to-do list")
	for _, content := range []string{
		`{"type":"session.tool.failed","data":{"name":"todowrite","metadata":{"todos":[]}}}`,
		`{"type":"session.tool.success","data":{"name":"other","metadata":{"todos":[]}}}`,
		`{"type":"session.tool.success","data":{"name":"todowrite","metadata":{"todos":null}}}`,
		`{"type":"session.tool.success","data":{"name":"todowrite","metadata":{"todos":[{}]}}}`,
	} {
		_, ok := provider.ExtractTodoEvent("result", []byte(content), nil)
		assert.False(t, ok, content)
	}
}
