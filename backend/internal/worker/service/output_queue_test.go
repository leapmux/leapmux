package service

import (
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutputQueueRetriesTheFinalWriteWithoutAnotherNativeFrame(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	svc, _, _ := setupTestService(t, withClock(clock))
	require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{ID: "queue-agent", WorkingDir: t.TempDir(), HomeDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE}))
	sink := agent.NewModelProgressResetSink(newTestRootOutputSink(t, svc.Output, "queue-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	queue := agent.OutputQueueFor(sink)
	var unavailable atomic.Bool
	unavailable.Store(true)
	queue.Append(agent.OutputWrite{Label: "store unavailable", Run: func() error {
		if unavailable.Load() {
			return assert.AnError
		}
		return nil
	}})
	trap := clock.Trap().NewTimer("output-retry")
	t.Cleanup(trap.Close)
	sink.SetTurnState(agent.TurnState{Active: true}, 1)
	require.ErrorIs(t, sink.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"type":"result","subtype":"success"}`)}, agent.SpanInfo{}), assert.AnError)
	sink.SetTurnState(agent.TurnState{}, 2)
	done := make(chan struct{})
	queue.Append(agent.OutputWrite{Label: "completed", Run: func() error { close(done); return nil }})
	assert.True(t, svc.Output.TurnActive("queue-agent"))
	ctx := testutil.DeadlineContext(t)
	delay := testutil.WaitForTimer(t, ctx, trap)
	assert.Equal(t, time.Second, delay)
	unavailable.Store(false)
	clock.Advance(delay).MustWait(ctx)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the output retry did not complete")
	}
	assert.False(t, svc.Output.TurnActive("queue-agent"))
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "queue-agent"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestOutputQueueKeepsPendingRowsAcrossProviderReplacement(t *testing.T) {
	t.Parallel()
	svc, original := setupRootSink(t, "queue-replacement")
	first := agent.NewModelProgressResetSink(original)
	first.UpdateSessionID("old")
	queue := agent.OutputQueueFor(first)
	var unavailable atomic.Bool
	unavailable.Store(true)
	queue.Append(agent.OutputWrite{Label: "store unavailable", Run: func() error {
		if unavailable.Load() {
			return assert.AnError
		}
		return nil
	}})
	require.ErrorIs(t, first.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"text":"old"}`)}, agent.SpanInfo{}), assert.AnError)
	replacement := agent.NewModelProgressResetSink(newTestRootOutputSink(t, svc.Output, "queue-replacement", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	replacement.UpdateSessionID("new")
	unavailable.Store(false)
	require.NoError(t, replacement.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"text":"new"}`)}, agent.SpanInfo{}))
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "queue-replacement"})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "old", rows[0].AgentSessionID)
	assert.Equal(t, "new", rows[1].AgentSessionID)
}
