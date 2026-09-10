package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

// ProviderServices.SetTurnState is the ONE signal a provider publishes for "a turn
// is in flight", and the Worker derives both answers from it: the activity
// state a client renders, and the input queue's dispatch guard. These tests pin
// the second one, on both edges.
//
// The queue guard used to have a signal of its own, which only a turn the queue
// itself dispatched ever raised. A turn the agent process started on its own
// was then invisible, and the next message the user sent went straight into it
// instead of waiting in the queue.

// turnPublisher mints the ordering token a provider takes under its own lock,
// and adopts the sink the way a launch does. Going through both is deliberate:
// a test that published a bare token would pass against a Worker that stopped
// ordering the publishes at all.
type turnPublisher struct {
	sink      agent.ProviderServices
	seq       uint64
	steerable bool
}

func (p *turnPublisher) publish(active bool) {
	p.seq++
	p.sink.SetTurnState(agent.TurnState{Active: active, Steerable: active && p.steerable}, p.seq)
}

// launchTurnPublisher registers a sink and runs the two things startAgent runs
// for a new process: adopt the publisher, and release a turn the old one left.
func launchTurnPublisher(svc *Service, agentID string) *turnPublisher {
	sink := svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	svc.Output.NoteAgentProcessStarted(agentID)
	svc.abandonUnownedTurn(agentID)
	return &turnPublisher{sink: sink}
}

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
	sink := launchTurnPublisher(svc, agentID)

	// The agent process reports a turn the queue did not dispatch.
	sink.publish(true)
	require.Eventually(t, func() bool {
		snapshot, err := svc.InputQueue.Snapshot(ctx, agentID)
		return err == nil && snapshot.ActiveTurn
	}, time.Second, 10*time.Millisecond)
	providerTurn, err := svc.InputQueue.Snapshot(ctx, agentID)
	require.NoError(t, err)
	assert.False(t, providerTurn.ActiveTurnSteerable,
		"a provider without steering support must remain unclassified")

	_, err = svc.InputQueue.Enqueue(ctx, inputqueue.NewItem{
		ID: newTestAgentInputID(), AgentID: agentID, Text: "hello",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	assert.Never(t, func() bool {
		snapshot, snapshotErr := svc.InputQueue.Snapshot(ctx, agentID)
		return snapshotErr == nil && len(snapshot.Items) == 0
	}, 100*time.Millisecond, 10*time.Millisecond)

	sink.publish(false)
	require.Eventually(t, func() bool {
		snapshot, snapshotErr := svc.InputQueue.Snapshot(ctx, agentID)
		return snapshotErr == nil && len(snapshot.Items) == 0
	}, time.Second, 10*time.Millisecond)
}

func TestClassifiedProviderTurnMarksAQueuedInputSteerable(t *testing.T) {
	t.Parallel()

	// Codex can start a goal-continuation turn without a queue dispatch. Its
	// turn signal classifies that turn, so the queue head must not lose Steer.
	ctx := context.Background()
	svc, agentID := newTurnSignalFixture(t)
	sink := launchTurnPublisher(svc, agentID)
	sink.steerable = true
	sink.publish(true)
	require.Eventually(t, func() bool {
		snapshot, err := svc.InputQueue.Snapshot(ctx, agentID)
		return err == nil && snapshot.ActiveTurn && snapshot.ActiveTurnSteerable
	}, time.Second, 10*time.Millisecond)

	_, err := svc.InputQueue.Enqueue(ctx, inputqueue.NewItem{
		ID: newTestAgentInputID(), AgentID: agentID, Text: "steer this turn",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	snapshot, err := svc.InputQueue.Snapshot(ctx, agentID)
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.True(t, snapshot.Items[0].CanSteer)
}

func TestProviderCapabilityDoesNotOverrideReportedTurnState(t *testing.T) {
	t.Parallel()

	// A static provider capability does not prove that this turn accepts a
	// steer. The provider must state that property for each active turn.
	ctx := context.Background()
	svc, agentID := newTurnSignalFixture(t)
	sink := launchTurnPublisher(svc, agentID)
	dbAgent, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	_, err = svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID: agentID, WorkingDir: dbAgent.WorkingDir,
	}, sink.sink)
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent(agentID) })

	sink.publish(true)
	require.Eventually(t, func() bool {
		snapshot, snapshotErr := svc.InputQueue.Snapshot(ctx, agentID)
		return snapshotErr == nil && snapshot.ActiveTurn
	}, time.Second, 10*time.Millisecond)
	snapshot, err := svc.InputQueue.Snapshot(ctx, agentID)
	require.NoError(t, err)
	assert.False(t, snapshot.ActiveTurnSteerable,
		"a provider capability must not override the reported turn state")
}

func TestSuccessfulStartAbandonsATurnReportedDuringHandshake(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, agentID := newTurnSignalFixture(t)
	sink := svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	svc.startAgentFn = func(_ context.Context, _ agent.Options, services agent.ProviderServices) (map[string]string, error) {
		services.SetTurnState(agent.TurnState{Active: true, Steerable: true}, 1)
		return nil, nil
	}

	_, err := svc.startAgent(ctx, agent.Options{AgentID: agentID}, sink)
	require.NoError(t, err)
	snapshot, err := svc.InputQueue.Snapshot(ctx, agentID)
	require.NoError(t, err)
	assert.False(t, snapshot.ActiveTurn,
		"a completed handshake must leave no unowned startup turn")
}

func TestProviderReportedTurnRepeatsWithoutChurningTheQueueRevision(t *testing.T) {
	t.Parallel()

	// Every publish reconciles the queue against the provider's own flag, and a
	// provider republishes the unchanged value freely (a failed send, a steer,
	// an interrupt). A reconciliation that changes nothing must not bump the
	// revision, or every watcher takes a snapshot that says the same thing.
	//
	// Both edges are measured after a REAL transition. A queue that never held
	// a turn answers every clear with "nothing to do", so a repeat measured
	// from there proves nothing about the guard.
	ctx := context.Background()
	svc, agentID := newTurnSignalFixture(t)
	sink := launchTurnPublisher(svc, agentID)

	sink.publish(true)
	var active inputqueue.Snapshot
	require.Eventually(t, func() bool {
		snapshot, err := svc.InputQueue.Snapshot(ctx, agentID)
		if err != nil || !snapshot.ActiveTurn {
			return false
		}
		active = snapshot
		return true
	}, time.Second, 10*time.Millisecond)

	sink.publish(true)
	sink.publish(true)
	repeated, err := svc.InputQueue.Snapshot(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, active.Revision, repeated.Revision, "a repeated turn start moves nothing")
	assert.True(t, repeated.ActiveTurn)

	sink.publish(false)
	var cleared inputqueue.Snapshot
	require.Eventually(t, func() bool {
		snapshot, snapshotErr := svc.InputQueue.Snapshot(ctx, agentID)
		if snapshotErr != nil || snapshot.ActiveTurn {
			return false
		}
		cleared = snapshot
		return true
	}, time.Second, 10*time.Millisecond)
	assert.Greater(t, cleared.Revision, active.Revision, "the real transition does move it")

	sink.publish(false)
	sink.publish(false)
	after, err := svc.InputQueue.Snapshot(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, cleared.Revision, after.Revision, "a repeated clear moves nothing either")
	assert.False(t, after.ActiveTurn)
}

func TestProcessExitReleasesATurnTheProviderNeverEnded(t *testing.T) {
	t.Parallel()

	// A provider can report a turn and then neither end it nor exit -- a CLI
	// that stalls after its first assistant block. Stopping the agent is the
	// user's only escape, and an explicit stop skips the AGENT_STOPPED pause on
	// purpose, so before this the stop left the turn standing and the queue held
	// every later message with no pause, no error, and no Retry.
	ctx := context.Background()
	svc, agentID := newTurnSignalFixture(t)
	sink := launchTurnPublisher(svc, agentID)

	sink.publish(true)
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

	// The user stops the agent. stopped=true, so nothing pauses the queue -- and
	// nothing ended the turn either, which is what stranded the message.
	svc.HandleAgentProcessExit(agentID, 0, nil, true)

	require.Eventually(t, func() bool {
		snapshot, snapshotErr := svc.InputQueue.Snapshot(ctx, agentID)
		return snapshotErr == nil && len(snapshot.Items) == 0
	}, time.Second, 10*time.Millisecond)
	snapshot, err := svc.InputQueue.Snapshot(ctx, agentID)
	require.NoError(t, err)
	assert.False(t, snapshot.Paused, "an explicit stop still does not pause the queue")
}

func TestClearContextRefusesToStopAnAgentInsideATurn(t *testing.T) {
	t.Parallel()

	// A clear STOPS the process, and it is the one dispatch that destroys a turn
	// instead of joining it. The queue's active_turn guard normally holds it
	// back, but that guard is a durable COPY of the provider's flag. When the
	// copy is stale the clear went through and killed the user's in-flight
	// reply, which ended with no result at all.
	svc, agentID := newTurnSignalFixture(t)
	sink := launchTurnPublisher(svc, agentID)
	sink.publish(true)

	adapter := &agentInputQueueAdapter{svc: svc}
	_, err := adapter.Dispatch(inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{
		ID: newTestAgentInputID(), AgentID: agentID, Text: "/clear",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT,
	}})
	assert.Equal(t, inputqueue.DispatchBusy, dispatchOutcomeOf(t, err),
		"the flag itself says a turn runs, so the clear waits for it")
	assert.ErrorIs(t, err, agent.ErrAgentBusy)

	// And once the turn ends, the same clear goes through.
	sink.publish(false)
	_, err = adapter.Dispatch(inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{
		ID: newTestAgentInputID(), AgentID: agentID, Text: "/clear",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT,
	}})
	require.NoError(t, err)
}

func TestAStalePublishFromTheSameProcessLosesToTheOneItOvertook(t *testing.T) {
	t.Parallel()

	// Two goroutines reach the sink unordered: the reader that ends a turn, and
	// the drain that a busy refusal answers. Each reads the provider's flag under
	// a lock and calls the sink without one, so the older value can land second.
	// It must lose, because nothing later clears a turn that is already over --
	// the queue would hold every message the user sends after it.
	ctx := context.Background()
	svc, agentID := newTurnSignalFixture(t)
	sink := launchTurnPublisher(svc, agentID)

	sink.sink.SetTurnState(agent.TurnState{Active: true}, 1)
	sink.sink.SetTurnState(agent.TurnState{}, 2)
	require.Eventually(t, func() bool {
		snapshot, err := svc.InputQueue.Snapshot(ctx, agentID)
		return err == nil && !snapshot.ActiveTurn
	}, time.Second, 10*time.Millisecond)

	// The refusal read its flag BEFORE the clear did, so its token is lower.
	sink.sink.SetTurnState(agent.TurnState{Active: true}, 1)

	assert.Never(t, func() bool {
		snapshot, err := svc.InputQueue.Snapshot(ctx, agentID)
		return err == nil && snapshot.ActiveTurn
	}, 100*time.Millisecond, 10*time.Millisecond)
	assert.False(t, svc.Output.TurnActive(agentID), "and the activity latch agrees")
}

func TestAPublishFromAReplacedProcessCannotReopenATurn(t *testing.T) {
	t.Parallel()

	// A replaced process keeps publishing while its reader drains the pipe, and
	// its tokens mean nothing to the new process, whose counter restarted at
	// zero. Identity, not the token, is what separates them: one sink per launch.
	ctx := context.Background()
	svc, agentID := newTurnSignalFixture(t)
	old := launchTurnPublisher(svc, agentID)
	old.publish(true)
	old.publish(false)
	old.publish(true)

	// The Worker replaces the process. The new launch registers its own sink.
	fresh := launchTurnPublisher(svc, agentID)
	require.Eventually(t, func() bool {
		snapshot, err := svc.InputQueue.Snapshot(ctx, agentID)
		return err == nil && !snapshot.ActiveTurn
	}, time.Second, 10*time.Millisecond)

	// The dead process reports a turn, with a token far above the new one's.
	old.publish(true)
	assert.Never(t, func() bool {
		snapshot, err := svc.InputQueue.Snapshot(ctx, agentID)
		return err == nil && snapshot.ActiveTurn
	}, 100*time.Millisecond, 10*time.Millisecond)

	// The new process starts its first turn, whose token is 1 -- far BELOW the
	// dead process's. The adoption forgot that count, so this one still wins.
	fresh.publish(true)
	require.Eventually(t, func() bool {
		snapshot, err := svc.InputQueue.Snapshot(ctx, agentID)
		return err == nil && snapshot.ActiveTurn
	}, time.Second, 10*time.Millisecond)
}
