package service

import (
	"context"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInputRecoveryProcessStatePublishesRootMetadataWithoutChangingItsTurn(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "active parent"}[active], func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			svc, starts := newInputRecoveryService(t, nil)
			const id = "recovery-state-publication"
			seedRecoveryAgent(t, svc, id, "saved-thread")
			require.NoError(t, svc.ensureAgentRunning(id, resumeStoredSession, interactiveStart))
			sink := requireRootOutputSink(t, svc.Output, id)
			publisher, supported := any(sink).(interface{ PublishInputProcessState() })
			require.True(t, supported, "the root output sink must publish process-state changes")
			if active {
				sink.SetTurnState(agent.TurnState{Active: true, Steerable: true}, 1)
			}
			before, err := svc.InputQueue.Snapshot(t.Context(), id)
			require.NoError(t, err)
			require.Empty(t, before.Items, "health publication cannot depend on pending input")
			svc.Output.WaitActivityRefreshes()
			events := make(chan *leapmuxv1.AgentInputQueueSnapshot, 4)
			writer := &turnAdmissionWatchingWriter{testResponseWriter: &testResponseWriter{channelID: "recovery-state-publication"}, onEvent: func(event *leapmuxv1.AgentEvent) {
				if changed := event.GetInputQueueChanged(); changed != nil {
					events <- changed.GetSnapshot()
				}
			}}
			registerAgentWatch(svc, writer.channelID, id, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
			stale := starts.fake(0)
			stale.mu.Lock()
			stale.recoveryRequired = true
			stale.mu.Unlock()
			publisher.PublishInputProcessState()
			select {
			case published := <-events:
				require.NotNil(t, published.ProcessRecovery)
				assert.NotEmpty(t, published.ProcessRecovery.ProcessInstanceId)
				assert.Equal(t, sink.outputGeneration, published.ProcessRecovery.OutputGeneration)
				assert.Equal(t, "saved-thread", published.ProcessRecovery.AgentSessionId)
				assert.Equal(t, active, published.ActiveTurn)
				assert.Greater(t, published.Revision, before.Revision)
			case <-ctx.Done():
				t.Fatal("the active root watcher did not receive its process recovery proposal")
			}
			after, err := svc.InputQueue.Snapshot(t.Context(), id)
			require.NoError(t, err)
			unchanged := after
			unchanged.Revision = before.Revision
			assert.Equal(t, before, unchanged, "process metadata publication cannot change turn, pause, or input state")
			// A replacement source expires the earlier source's notification
			// authority, even before another native process starts.
			fresh := newTestRootOutputSink(t, svc.Output, id, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
			publisher.PublishInputProcessState()
			retired, err := svc.InputQueue.Snapshot(t.Context(), id)
			require.NoError(t, err)
			assert.Equal(t, after, retired, "a retired output source cannot refresh a successor's queue")
			stale.mu.Lock()
			stale.recoveryRequired = false
			stale.mu.Unlock()
			current, supported := any(fresh).(interface{ PublishInputProcessState() })
			require.True(t, supported)
			current.PublishInputProcessState()
			select {
			case published := <-events:
				assert.Nil(t, published.ProcessRecovery, "current health publication also clears an expired proposal")
				assert.Greater(t, published.Revision, after.Revision)
			case <-ctx.Done():
				t.Fatal("the root watcher did not receive the cleared process recovery proposal")
			}
			assert.False(t, stale.wasStopped())
			assert.Equal(t, 1, starts.count())
		})
	}
}

func TestInputRecoveryPublishesHealthObservedBeforeProcessRegistration(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, starts := newInputRecoveryService(t, nil)
	const id = "recovery-startup-state"
	seedRecoveryAgent(t, svc, id, "saved-thread")
	events := make(chan *leapmuxv1.AgentInputQueueSnapshot, 8)
	writer := &turnAdmissionWatchingWriter{testResponseWriter: &testResponseWriter{channelID: "recovery-startup-state"}, onEvent: func(event *leapmuxv1.AgentEvent) {
		if changed := event.GetInputQueueChanged(); changed != nil {
			events <- changed.GetSnapshot()
		}
	}}
	registerAgentWatch(svc, writer.channelID, id, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	svc.startAgentFn = func(ctx context.Context, options agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		return svc.Agents.StartAgentWith(ctx, options, sink, func(ctx context.Context, options agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
			running, err := starts.start(ctx, options, sink)
			if err != nil {
				return running, err
			}
			fake := running.(*recoveryAgent)
			fake.mu.Lock()
			fake.recoveryRequired = true
			fake.mu.Unlock()
			sink.PublishInputProcessState()
			return running, nil
		})
	}
	require.NoError(t, svc.ensureAgentRunning(id, resumeStoredSession, interactiveStart))
	for {
		select {
		case published := <-events:
			if published.ProcessRecovery == nil {
				continue
			}
			assert.Equal(t, "saved-thread", published.ProcessRecovery.AgentSessionId)
			assert.False(t, published.ActiveTurn)
			assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED, published.PauseReason)
			assert.Empty(t, published.Items)
			assert.False(t, starts.fake(0).wasStopped())
			assert.Equal(t, 1, starts.count())
			return
		case <-ctx.Done():
			t.Fatal("startup health did not publish after the physical process registered")
		}
	}
}
