package agent

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeFamilyNativeTodoResults(t *testing.T) {
	t.Parallel()
	for _, provider := range []leapmuxv1.AgentProvider{leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO} {
		event, ok := ProviderFor(provider).ExtractTodoEvent("other", []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"todos","status":"completed","rawOutput":{"metadata":{"todos":[{"content":"Inspect sample","status":"in_progress"},{"content":"Report findings","status":"completed"}]}}}`), nil)
		require.True(t, ok, provider.String())
		assert.Equal(t, todoevents.KindSnapshot, event.Kind)
		require.Len(t, event.Snapshot, 2)
		assert.Equal(t, "Inspect sample", event.Snapshot[0].Content)
		assert.Equal(t, todoevents.StatusInProgress, event.Snapshot[0].Status)
		assert.Equal(t, todoevents.StatusCompleted, event.Snapshot[1].Status)
	}
}

func TestOpenCodeFamilyEmptyNativeTodoResultClearsTheList(t *testing.T) {
	t.Parallel()
	event, ok := ProviderFor(leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE).ExtractTodoEvent("other", []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"todos","status":"completed","rawOutput":{"metadata":{"todos":[]}}}`), nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	assert.Empty(t, event.Snapshot)
}

func TestOpenCodeFamilyTodoReplayAndMalformedResults(t *testing.T) {
	t.Parallel()
	provider := ProviderFor(leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE)
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
