package agent_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type managerInputProcessAgent struct {
	agenttest.IdleAgent
	required func() bool
}

func (managerInputProcessAgent) PrepareInputProcess() error { return nil }

func (process managerInputProcessAgent) InputProcessRecoveryRequired() bool {
	return process.required()
}

func TestManagerInputProcessIdentityBelongsToItsPhysicalRegistration(t *testing.T) {
	t.Parallel()
	manager := agent.NewManager(testRegistry, nil)
	var required atomic.Bool
	process := managerInputProcessAgent{required: required.Load}
	manager.PutAgentForTest("process", process)
	needsRecovery, first := manager.InputProcessRecoveryState("process")
	assert.False(t, needsRecovery)
	require.NotEmpty(t, first)
	required.Store(true)
	stale, unchanged := manager.InputProcessRecoveryState("process")
	assert.True(t, stale)
	assert.Equal(t, first, unchanged, "a health transition preserves physical identity")
	manager.PutAgentForTest("process", process)
	stale, replaced := manager.InputProcessRecoveryState("process")
	assert.True(t, stale)
	assert.NotEmpty(t, replaced)
	assert.NotEqual(t, first, replaced, "registering the same provider value still creates a different physical lifetime")
	manager.PutAgentForTest("unsupported", agenttest.IdleAgent{})
	for _, id := range []string{"absent", "unsupported"} {
		state, instanceID := manager.InputProcessRecoveryState(id)
		assert.False(t, state)
		assert.Empty(t, instanceID)
	}
}

func TestManagerInputProcessReadCannotCombineDifferentRegistrations(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	manager := agent.NewManager(testRegistry, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	releaseRead := sync.OnceFunc(func() { close(release) })
	defer releaseRead()
	var reads atomic.Int32
	old := managerInputProcessAgent{required: func() bool {
		if reads.Add(1) == 2 {
			close(entered)
			<-release
		}
		return true
	}}
	manager.PutAgentForTest("process", old)
	_, previous := manager.InputProcessRecoveryState("process")
	type observation struct {
		required bool
		instance string
	}
	observed := make(chan observation, 1)
	go func() {
		required, instance := manager.InputProcessRecoveryState("process")
		observed <- observation{required: required, instance: instance}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the old process health read did not reach its controlled pause")
	}
	manager.PutAgentForTest("process", managerInputProcessAgent{required: func() bool { return true }})
	releaseRead()
	select {
	case result := <-observed:
		assert.False(t, result.required, "a replaced provider's health result has no current authority")
		assert.Empty(t, result.instance)
	case <-ctx.Done():
		t.Fatal("the old process health read did not finish after release")
	}
	current, instance := manager.InputProcessRecoveryState("process")
	assert.True(t, current)
	assert.NotEmpty(t, instance)
	assert.NotEqual(t, previous, instance)
}
