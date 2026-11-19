package service

import (
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConcurrentNativeChildrenShareTheirParentSpawnOrigin(t *testing.T) {
	t.Parallel()
	svc, parent := setupRootSink(t, "concurrent-native-origin")
	const count = 8
	type result struct {
		id, nativeKey, sessionID string
		err                      error
	}
	results := make(chan result, count)
	start := make(chan struct{})
	for index := range count {
		go func() {
			<-start
			key := fmt.Sprintf("native-child-%d", index)
			session := fmt.Sprintf("native-conversation-%d", index)
			childID, err := parent.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildTask, ProviderChildKey: key, AgentSessionID: session, SpawnSpanID: "native-batch-call"})
			results <- result{id: childID, nativeKey: key, sessionID: session, err: err}
		}()
	}
	close(start)
	identities := make(map[string]struct{}, count)
	for range count {
		child := <-results
		require.NoError(t, child.err)
		require.NotEmpty(t, child.id)
		assert.NotContains(t, identities, child.id)
		identities[child.id] = struct{}{}
		stored, err := svc.Queries.GetAgentByID(t.Context(), child.id)
		require.NoError(t, err)
		assert.Equal(t, child.nativeKey, stored.ProviderChildKey)
		assert.Equal(t, child.sessionID, stored.AgentSessionID)
		assert.Equal(t, "native-batch-call", stored.SpawnSpanID)
	}
	assert.Len(t, identities, count)
}

func TestProvisionalChildAdoptionPreservesItsSharedSpawnOrigin(t *testing.T) {
	t.Parallel()
	svc, parent := setupRootSink(t, "provisional-native-origin")
	provisionalID, err := parent.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildTask, SpawnSpanID: "shared-native-origin", AgentSessionID: "first-conversation"})
	require.NoError(t, err)
	first := agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildTask, ProviderChildKey: "first-native-child", SpawnSpanID: "shared-native-origin", AgentSessionID: "first-conversation"}
	firstID, err := parent.EnsureChildAgent(first)
	require.NoError(t, err)
	assert.Equal(t, provisionalID, firstID)
	secondID, err := parent.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildTask, ProviderChildKey: "second-native-child", SpawnSpanID: "shared-native-origin", AgentSessionID: "second-conversation"})
	require.NoError(t, err)
	assert.NotEqual(t, firstID, secondID)
	svc.Output.bgtasks.Delete("provisional-native-origin")
	replayedID, err := parent.EnsureChildAgent(first)
	require.NoError(t, err)
	assert.Equal(t, firstID, replayedID)
	_, err = parent.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildTask, SpawnSpanID: "shared-native-origin"})
	require.ErrorIs(t, err, agent.ErrChildIdentityRefused)
}

func TestProvisionalSpawnIdentityRemainsUniqueInDatabase(t *testing.T) {
	t.Parallel()
	const rootID = "unique-provisional-origin"
	svc, _ := setupRootSink(t, rootID)
	insert := `INSERT INTO agents (id, parent_agent_id, spawn_span_id, working_dir, title, agent_provider)
SELECT ?, id, ?, working_dir, 'Provisional child', agent_provider FROM agents WHERE id = ?`
	_, err := svc.DB.ExecContext(t.Context(), insert, "first-provisional", "one-provisional-origin", rootID)
	require.NoError(t, err)
	_, err = svc.DB.ExecContext(t.Context(), insert, "second-provisional", "one-provisional-origin", rootID)
	require.Error(t, err, "independent writers cannot create two provisional identities for one origin")
	var count int
	require.NoError(t, svc.DB.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM agents WHERE parent_agent_id = ? AND spawn_span_id = ?", rootID, "one-provisional-origin").Scan(&count))
	assert.Equal(t, 1, count)
}

func TestNativeChildrenShareTheirParentSpawnOrigin(t *testing.T) {
	for _, cold := range []bool{false, true} {
		t.Run(map[bool]string{false: "warm registry", true: "cold registry"}[cold], func(t *testing.T) {
			t.Parallel()
			const rootID = "native-batch-origin"
			svc, parent := setupRootSink(t, rootID)
			first := agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildTask, ProviderChildKey: "native-first", AgentSessionID: "conversation-first", SpawnSpanID: "one-native-spawn", Title: "First child"}
			second := agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildTask, ProviderChildKey: "native-second", AgentSessionID: "conversation-second", SpawnSpanID: "one-native-spawn", Title: "Second child"}
			firstID, err := parent.EnsureChildAgent(first)
			require.NoError(t, err)
			if cold {
				svc.Output.bgtasks.Delete(rootID)
			}
			secondID, err := parent.EnsureChildAgent(second)
			require.NoError(t, err, "one native call can create several distinct children")
			require.NotEqual(t, firstID, secondID)
			for _, child := range []struct {
				id   string
				spec agent.ChildAgentSpec
			}{{firstID, first}, {secondID, second}} {
				stored, err := svc.Queries.GetAgentByID(t.Context(), child.id)
				require.NoError(t, err)
				assert.Equal(t, child.spec.AgentSessionID, stored.AgentSessionID)
				assert.Equal(t, child.spec.ProviderChildKey, stored.ProviderChildKey)
				assert.Equal(t, "one-native-spawn", stored.SpawnSpanID)
				replayed, err := parent.EnsureChildAgent(child.spec)
				require.NoError(t, err)
				assert.Equal(t, child.id, replayed)
			}
			resolved, err := parent.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildTask, SpawnSpanID: "one-native-spawn"})
			assert.ErrorIs(t, err, agent.ErrChildIdentityRefused, "a span alone cannot select one member of a native batch")
			assert.Empty(t, resolved)
		})
	}
}
