package service

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDescendantTasksUseTheRootRegistry(t *testing.T) {
	t.Parallel()
	svc, root := setupRootSink(t, "root-registry-owner")
	parentID, err := root.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "native-parent", Title: "Parent"})
	require.NoError(t, err)
	parent := root.ChildSink(parentID)
	grandchildID, err := parent.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "native-grandchild", Title: "Grandchild"})
	require.NoError(t, err)
	require.NotEqual(t, parentID, grandchildID)
	for _, lookup := range []struct {
		key  string
		want string
	}{{"native-parent", parentID}, {"native-grandchild", grandchildID}} {
		actual, _, found, readError := root.LookupBackgroundTask(lookup.key)
		require.NoError(t, readError)
		assert.True(t, found, "the root registry owns every descendant row")
		assert.Equal(t, lookup.want, actual)
	}
	actual, _, found, err := parent.LookupBackgroundTask("native-parent")
	require.NoError(t, err)
	assert.True(t, found, "a child sink uses the same root registry")
	assert.Equal(t, parentID, actual)
	rows, err := svc.Output.LoadBackgroundTasks(t.Context(), "root-registry-owner")
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}
