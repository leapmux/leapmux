package service

import (
	"github.com/leapmux/leapmux/internal/worker/bgtask"

	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildSinkLease_PreservesNativeAdoptionAndOrdinaryTurns(t *testing.T) {
	t.Parallel()
	_, parent := setupRootSink(t, "child-lease-turns")
	id, err := parent.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  "spawn", ProviderChildKey: "native-child", Title: "Child"})
	require.NoError(t, err)
	lease := parent.AcquireChildSink(id)
	require.NotNil(t, lease.Sink)
	lease.Activate()
	lease.Sink.UpdateSessionID("native-child")
	lease.Sink.SetTurnState(agent.TurnState{Active: true}, 1)
	lease.Sink.SetTurnState(agent.TurnState{}, 2)
	owner := lease.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{}).Publication.Owner()
	require.True(t, owner.IsSessionCurrent())
	assert.True(t, lease.Cleanup(), "ordinary turn scopes and native session adoption must preserve the lifetime")
	assert.False(t, owner.IsSessionCurrent())
	assert.False(t, lease.Cleanup(), "cleanup must claim its physical child once")
}

func TestChildSinkLease_SupersededCleanupPreservesTheNewAcquisition(t *testing.T) {
	t.Parallel()
	_, parent := setupRootSink(t, "child-lease-reuse")
	id, err := parent.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  "spawn", ProviderChildKey: "native-child", AgentSessionID: "native-child", Title: "Child"})
	require.NoError(t, err)
	first := parent.AcquireChildSink(id)
	first.Sink.OpenSpan("old-call", "")
	span := agent.SpanInfo{SpanID: "old-call", Closing: true}
	before := first.CaptureMessage(agent.MessageContent{}, span).Publication.Owner().(*transcriptOwner)
	second := parent.AcquireChildSink(id)
	second.Sink.SetTurnState(agent.TurnState{Active: true}, 1)
	current := second.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{}).Publication.Owner()
	assert.True(t, current.IsCurrent())
	assert.False(t, first.Cleanup())
	assert.True(t, current.IsCurrent(), "the earlier lifetime cannot retire the current physical child")
	after := first.CaptureMessage(agent.MessageContent{}, span).Publication.Owner().(*transcriptOwner)
	assert.False(t, after.IsCurrent())
	assert.True(t, after.IsSessionCurrent(), "old rows retain data recovery authority in the same native session")
	assert.Equal(t, before.spanLines, after.spanLines)
	assert.Equal(t, before.spanColor, after.spanColor)
	assert.True(t, second.Cleanup())
}

func TestChildSinkLease_ActivationCannotResetANewerAcquisition(t *testing.T) {
	t.Parallel()
	svc, parent := setupRootSink(t, "child-lease-reset")
	id, err := parent.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  "spawn", ProviderChildKey: "native-child", AgentSessionID: "native-child", Title: "Child"})
	require.NoError(t, err)
	first := parent.AcquireChildSink(id)
	second := parent.AcquireChildSink(id)
	second.Activate()
	second.Sink.ReportProgress(agent.ModelTextProgress("new-output", "abcdefghijklmnop"))
	first.Activate()
	sink := requireRootOutputSink(t, svc.Output, "child-lease-reset").rawChildSink(id)
	sink.progress.mu.Lock()
	snapshot, _ := sink.progress.counter.Apply(agent.ProgressUpdate{})
	sink.progress.mu.Unlock()
	assert.Equal(t, int64(4), snapshot.ThinkingTokens)
}

func TestChildStateSinkLeasePreservesScopeAndPromotesOnce(t *testing.T) {
	t.Parallel()
	svc, parent := setupRootSink(t, "child-state-lease")
	id, err := parent.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildActivity, ProviderChildKey: "native-child", Title: "Child"})
	require.NoError(t, err)
	first := parent.AcquireChildSink(id)
	first.Activate()
	first.Sink.OpenSpan("held", "")
	first.Sink.SetSpanType("held", "existing-tool")
	owner := first.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{SpanID: "held"}).Publication.Owner()
	require.True(t, owner.IsCurrent())
	first.Sink.ReportProgress(agent.ModelTextProgress("old-output", "abcdefghijklmnop"))
	state := parent.AcquireChildStateSink(id)
	assert.True(t, owner.IsCurrent(), "state acquisition must preserve accepted transcript authority")
	assert.Equal(t, "existing-tool", state.Sink.GetSpanType("held"))
	assert.False(t, first.Cleanup(), "older cleanup cannot remove the state acquisition")
	physical := requireRootOutputSink(t, svc.Output, "child-state-lease").rawChildSink(id)
	thinking := func() int64 {
		physical.progress.mu.Lock()
		defer physical.progress.mu.Unlock()
		snapshot, _ := physical.progress.counter.Apply(agent.ProgressUpdate{})
		return snapshot.ThinkingTokens
	}
	assert.Equal(t, int64(4), thinking())
	state.Activate()
	assert.False(t, owner.IsCurrent(), "only promotion to transcript activity replaces the scope")
	assert.Empty(t, state.Sink.GetSpanType("held"))
	assert.Zero(t, thinking())
	state.Sink.ReportProgress(agent.ModelTextProgress("new-output", "abcdefgh"))
	var activations sync.WaitGroup
	for range 16 {
		activations.Go(state.Activate)
	}
	activations.Wait()
	assert.Equal(t, int64(2), thinking(), "repeated and concurrent activation must preserve newer progress")
	assert.True(t, state.Cleanup())
}

func TestChildStateSinkLeaseRefusesSupersededPreparedActivation(t *testing.T) {
	t.Parallel()
	svc, parent := setupRootSink(t, "child-state-stale-activation")
	id, err := parent.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildActivity, ProviderChildKey: "native-child", Title: "Child"})
	require.NoError(t, err)
	first := parent.AcquireChildSink(id)
	owner := first.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{}).Publication.Owner()
	first.Sink.ReportProgress(agent.ModelTextProgress("output", "abcdefghijklmnop"))
	physical := requireRootOutputSink(t, svc.Output, "child-state-stale-activation").rawChildSink(id)
	mutation := svc.Output.transcriptMutationMutex("child-state-stale-activation")
	mutation.RLock()
	preparedReset := physical.childLifetime.reset
	mutation.RUnlock()
	require.NotNil(t, preparedReset)
	state := parent.AcquireChildStateSink(id)
	require.True(t, owner.IsCurrent(), "the preserved scope alone cannot reject obsolete activation")
	first.Activate()
	preparedReset.ReportProgress(agent.ResetProgress())
	physical.progress.mu.Lock()
	snapshot, _ := physical.progress.counter.Apply(agent.ProgressUpdate{})
	physical.progress.mu.Unlock()
	assert.Equal(t, int64(4), snapshot.ThinkingTokens)
	assert.True(t, state.Cleanup())
}
