package agenttest

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryRoleRejectsAnotherCanonicalActivityRow(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildActivity, ProviderChildKey: "canonical", Title: "Child"})
	require.NoError(t, err)
	err = sink.UpsertBackgroundTask(bgtask.Upsert{Role: bgtask.RoleChildTask, RowKey: "duplicate", Kind: bgtask.KindSubagent, ChildAgentID: childID, Status: bgtask.StatusRunning})
	assert.Error(t, err, "one child has exactly one canonical activity row")
	require.Len(t, sink.BackgroundTasks(), 1)
	for _, key := range []string{"task-a", "task-b"} {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{Role: bgtask.RoleTaskOutcome, RowKey: key, Kind: bgtask.KindSubagent, ChildAgentID: childID, Status: bgtask.StatusRunning}))
	}
	assert.Len(t, sink.BackgroundTasks(), 3, "independent outcomes may share a transcript")
}

func TestRegistryRoleRefusedAdoptionPreservesChildIdentity(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildActivity, SpawnSpanID: "role-span", Title: "Child"})
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{Role: bgtask.RoleChildTask, RowKey: "existing-route", Kind: bgtask.KindSubagent, ChildAgentID: childID, Status: bgtask.StatusRunning}))
	_, err = sink.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildActivity, SpawnSpanID: "role-span", ProviderChildKey: "refused-route", Title: "Child"})
	require.Error(t, err)
	require.Len(t, sink.BackgroundTasks(), 1)
	child := sink.children[childSinkIdentity{spanID: "role-span"}]
	require.NotNil(t, child)
	assert.Empty(t, child.nativeChildKey, "a refused canonical row does not adopt a native identity")
	assert.Equal(t, childID, child.childAgentID())
}

func TestRegistryRoleNestedParentSpansHaveDistinctStableChildren(t *testing.T) {
	t.Parallel()
	root := &Sink{}
	parentA, err := root.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildTask, SpawnSpanID: "parent-a", ProviderChildKey: "parent-a"})
	require.NoError(t, err)
	parentB, err := root.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildTask, SpawnSpanID: "parent-b", ProviderChildKey: "parent-b"})
	require.NoError(t, err)
	a := root.Child(parentA)
	b := root.Child(parentB)
	specA := agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildTask, SpawnSpanID: "same-local-span", ProviderChildKey: "leaf-a"}
	specB := agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildTask, SpawnSpanID: "same-local-span", ProviderChildKey: "leaf-b"}
	childA, err := a.EnsureChildAgent(specA)
	require.NoError(t, err)
	childB, err := b.EnsureChildAgent(specB)
	require.NoError(t, err, "a different parent can reuse its own local span ID")
	assert.NotEqual(t, childA, childB)
	repeated, err := a.EnsureChildAgent(specA)
	require.NoError(t, err)
	assert.Equal(t, childA, repeated)
	assert.Len(t, root.BackgroundTasks(), 4)
}

func TestRegistryRoleUnlinkRetainsOnlyAValidProvisionalTask(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	_, err := sink.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildTask, ProviderChildKey: "unlink", SpawnSpanID: "unlink-span"})
	require.NoError(t, err)
	sink.UnlinkBackgroundTask("unlink")
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Empty(t, rows[0].ChildAgentID)
	assert.Equal(t, bgtask.RoleTaskOutcome, rows[0].Role)
	assert.NoError(t, rows[0].ValidateRole(), "a missing-link fixture must satisfy production role constraints")
}

func TestRegistryRoleRenamePreservesCanonicalResponsibility(t *testing.T) {
	t.Parallel()
	for _, role := range []bgtask.Role{bgtask.RoleChildTask, bgtask.RoleChildActivity} {
		sink := &Sink{}
		childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: role, ProviderChildKey: "canonical", SpawnSpanID: "rename-span"})
		require.NoError(t, err)
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{Role: bgtask.RoleTaskOutcome, RowKey: "outcome", Kind: bgtask.KindSubagent, ChildAgentID: childID, Status: bgtask.StatusRunning}))
		before := sink.BackgroundTasks()
		err = sink.RenameBackgroundTask("canonical", "outcome")
		if role == bgtask.RoleChildActivity {
			assert.Error(t, err)
			assert.Equal(t, before, sink.BackgroundTasks())
		} else {
			require.NoError(t, err)
			rows := sink.BackgroundTasks()
			require.Len(t, rows, 1)
			assert.Equal(t, bgtask.RoleChildTask, rows[0].Role)
			assert.Equal(t, childID, rows[0].ChildAgentID)
		}
	}
}

func TestRegistryOutcomeAliasKeepsKnownFinishedEvidence(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	for _, key := range []string{"invocation", "task"} {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{Role: bgtask.RoleTaskOutcome, RowKey: key, Kind: bgtask.KindSubagent, Status: bgtask.StatusRunning}))
	}
	require.NoError(t, sink.CloseBackgroundTask("invocation", bgtask.StatusSucceeded))
	require.NoError(t, sink.RenameBackgroundTask("invocation", "task"))
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status)
	assert.Equal(t, testFakeEndedAt, rows[0].EndedAt)
}

func TestRegistryRoleAliasPreservesIndependentTaskOutcome(t *testing.T) {
	t.Parallel()
	for _, linked := range []bool{false, true} {
		for _, status := range []bgtask.Status{bgtask.StatusRunning, bgtask.StatusFailed} {
			name := "unlinked/" + status.String()
			if linked {
				name = "linked/" + status.String()
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				sink := &Sink{}
				childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildActivity, ProviderChildKey: "activity", Title: "Child"})
				require.NoError(t, err)
				sourceChild := ""
				if linked {
					sourceChild = childID
				}
				require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{Role: bgtask.RoleTaskOutcome, RowKey: "task", Kind: bgtask.KindSubagent, ChildAgentID: sourceChild, Status: bgtask.StatusRunning}))
				if status == bgtask.StatusFailed {
					require.NoError(t, sink.CloseBackgroundTask("task", status))
				}
				before := sink.BackgroundTasks()
				require.Error(t, sink.RenameBackgroundTask("task", "activity"))
				assert.Equal(t, before, sink.BackgroundTasks())
				task, exists := sink.BackgroundTask("task")
				require.True(t, exists)
				assert.Equal(t, status, task.Status)
				activity, exists := sink.BackgroundTask("activity")
				require.True(t, exists)
				assert.Equal(t, bgtask.RoleChildActivity, activity.Role)
				assert.Equal(t, childID, activity.ChildAgentID)
			})
		}
	}
}

func TestRegistryOutcomeAliasRetainsCombinedChildTask(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildTask, ProviderChildKey: "combined", Title: "Child"})
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{Role: bgtask.RoleTaskOutcome, RowKey: "provisional", Kind: bgtask.KindSubagent, ChildAgentID: childID, Status: bgtask.StatusRunning}))
	require.NoError(t, sink.RenameBackgroundTask("provisional", "combined"))
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.RoleChildTask, rows[0].Role)
	assert.Equal(t, childID, rows[0].ChildAgentID)
}
