package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

// OutputSink.SetTurnActive is the ONE signal a provider publishes for "a turn
// is in flight", and the Worker derives both answers from it: the activity
// state a client renders, and the input queue's dispatch guard. These tests pin
// the second one, on both edges.
//
// The queue guard used to have a signal of its own, which only a turn the queue
// itself dispatched ever raised. A turn the agent process started on its own
// was then invisible, and the next message the user sent went straight into it
// instead of waiting in the queue.

func newTurnSignalFixture(t *testing.T) (*Service, string) {
	t.Helper()
	svc, _, _ := setupTestService(t)
	agentID := "agent-turn-signal"
	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	svc.startAgentFn = svc.Agents.MockStartAgent
	t.Cleanup(func() { svc.Agents.StopAgent(agentID) })
	return svc, agentID
}

func TestProviderReportedTurnHoldsQueuedInputUntilItEnds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, agentID := newTurnSignalFixture(t)
	sink := svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	// The agent process reports a turn the queue did not dispatch.
	sink.SetTurnActive(true)
	require.Eventually(t, func() bool {
		snapshot, err := svc.InputQueue.Snapshot(ctx, agentID)
		return err == nil && snapshot.ActiveTurn
	}, time.Second, 10*time.Millisecond)

	_, err := svc.InputQueue.Enqueue(ctx, inputqueue.NewItem{
		ID: newTestAgentInputID(), AgentID: agentID, Text: "hello",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	assert.Never(t, func() bool {
		snapshot, snapshotErr := svc.InputQueue.Snapshot(ctx, agentID)
		return snapshotErr == nil && len(snapshot.Items) == 0
	}, 100*time.Millisecond, 10*time.Millisecond)

	sink.SetTurnActive(false)
	require.Eventually(t, func() bool {
		snapshot, snapshotErr := svc.InputQueue.Snapshot(ctx, agentID)
		return snapshotErr == nil && len(snapshot.Items) == 0
	}, time.Second, 10*time.Millisecond)
}

func TestProviderReportedTurnEndRepeatsWithoutChurningTheQueueRevision(t *testing.T) {
	t.Parallel()

	// Every publish reconciles the queue against the provider's own flag, and a
	// provider republishes the unchanged value freely (a failed send, a steer,
	// an interrupt). A reconciliation that changes nothing must not bump the
	// revision, or every watcher takes a snapshot that says the same thing.
	ctx := context.Background()
	svc, agentID := newTurnSignalFixture(t)
	sink := svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	sink.SetTurnActive(false)
	before, err := svc.InputQueue.Snapshot(ctx, agentID)
	require.NoError(t, err)

	sink.SetTurnActive(false)
	sink.SetTurnActive(false)

	after, err := svc.InputQueue.Snapshot(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, before.Revision, after.Revision)
	assert.False(t, after.ActiveTurn)
}
