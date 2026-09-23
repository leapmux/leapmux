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
	event, ok := Registration().Plugin.ExtractTodoEvent("other", []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"todos","status":"completed","rawOutput":{"metadata":{"todos":[]}}}`), nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	assert.Empty(t, event.Snapshot)
}

func TestOpenCodeFamilyTodoReplayAndMalformedResults(t *testing.T) {
	t.Parallel()
	provider := Registration().Plugin
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
	opencodetest.AssertReadsTheFamilyTodoResult(t, Registration().Plugin)
}
