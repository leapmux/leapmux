package agenttest

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeChildrenShareTheirParentSpawnOrigin(t *testing.T) {
	t.Parallel()
	parent := &Sink{}
	first := agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildTask, ProviderChildKey: "native-first", AgentSessionID: "conversation-first", SpawnSpanID: "one-native-spawn"}
	second := agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildTask, ProviderChildKey: "native-second", AgentSessionID: "conversation-second", SpawnSpanID: "one-native-spawn"}
	firstID, err := parent.EnsureChildAgent(first)
	require.NoError(t, err)
	secondID, err := parent.EnsureChildAgent(second)
	require.NoError(t, err)
	assert.NotEqual(t, firstID, secondID)
	for _, child := range []struct {
		id   string
		spec agent.ChildAgentSpec
	}{{firstID, first}, {secondID, second}} {
		assert.Equal(t, child.spec.AgentSessionID, parent.Child(child.id).LastSessionID())
		span, err := parent.ChildSpawnSpan(child.id)
		require.NoError(t, err)
		assert.Equal(t, "one-native-spawn", span)
		replayed, err := parent.EnsureChildAgent(child.spec)
		require.NoError(t, err)
		assert.Equal(t, child.id, replayed)
	}
	resolved, err := parent.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildTask, SpawnSpanID: "one-native-spawn"})
	assert.ErrorIs(t, err, agent.ErrChildIdentityRefused)
	assert.Empty(t, resolved)
	assert.Len(t, parent.BackgroundTasks(), 2)
}
