package kimi

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func TestKimiRegistryMutationUnlockWakesItsActiveDrainer(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	queue := agent.OutputQueueFor(rig.agent.sink)
	require.NotNil(t, queue)
	dispatch, session := rig.agent.dispatchMu.mutex(), rig.agent.sessionMu.mutex()
	blocked, released := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	var attempts atomic.Int64
	writer := rig.sink
	queue.Append(agent.OutputWrite{Owner: rig.agent.registryOutputOwner(), Label: "publish the retained Kimi registry row", Run: func() error {
		attempt := attempts.Add(1)
		if err := kimiRegistryPublicationLockError(dispatch, session); err != nil {
			if attempt == 1 {
				close(blocked)
				<-released
			}
			return err
		}
		return writer.UpsertBackgroundTask(bgtask.Upsert{RowKey: "registry-wake", Kind: bgtask.KindShell, Title: "Recovered task", Status: bgtask.StatusSucceeded})
	}})
	rig.agent.dispatchMu.Lock()
	locked := true
	defer func() {
		if locked {
			rig.agent.dispatchMu.Unlock()
		}
	}()
	done := make(chan error, 1)
	go func() { done <- queue.Flush() }()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal("the registry callback did not observe the held mutation lock")
	}
	rig.agent.finishDispatch()
	locked = false
	release()
	select {
	case err := <-done:
		require.NoError(t, err, "the unlock notification must survive the still-running drainer")
	case <-ctx.Done():
		t.Fatal("the registry drainer did not finish after the mutation unlocked")
	}
	row, exists := rig.sink.BackgroundTask("registry-wake")
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	assert.EqualValues(t, 2, attempts.Load())
	assert.False(t, queue.HasPending())
}

func TestKimiIndependentChildCloseRemainsRetainedAfterStorageRefusal(t *testing.T) {
	t.Parallel()
	for _, reset := range []bool{false, true} {
		t.Run(fmt.Sprintf("reset=%t", reset), func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			failure := &kimiTaskProjectionFailureSink{ControlSink: rig.sink}
			rig.agent.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(failure))
			queue := agent.OutputQueueFor(rig.agent.sink)
			require.NotNil(t, queue)
			clock := testutil.NewQuartzMock(t)
			queue.ConfigureRetry(t.Context(), clock, nil)
			t.Cleanup(queue.StopRetries)
			created, stopped := testutil.NewTimerTraps(t, clock, agent.OutputRetryTimerTag)
			rig.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"description":"Reviewer","prompt":"Review it."},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
			rig.transcriptAgentOps(t, "agent-0", 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"},"prompt":"Review it."}}`)
			failure.closeErr = errors.New("registry storage unavailable")
			if reset {
				turn := map[string]any{"kind": "turn", "turnId": "t0", "ordinal": 0, "state": "completed",
					"origin": map[string]any{"kind": "user"}, "prompt": "Review it.", "endedAt": time.Now().UTC().Format(time.RFC3339Nano), "steps": []any{}}
				rig.agent.HandleOutput(nativeKimiRecoveryReset(t, rig.sessionID(), "agent-0", 2, turn))
			} else {
				rig.transcriptAgentOps(t, "agent-0", 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"completed","origin":{"kind":"user"}}}`)
			}
			require.True(t, queue.HasPending(), "the output queue retains the refused independent-turn close")
			ctx := testutil.DeadlineContext(t)
			delay := testutil.WaitForTimer(t, ctx, created)
			failure.closeErr = nil
			testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopped)
			queue.StopRetries()
			row, child := childOf(t, rig.sink, "session_1/agent-0")
			assert.Equal(t, bgtask.StatusSucceeded, row.Status)
			assert.Equal(t, []bool{true, false}, child.TurnActives())
			assert.False(t, queue.HasPending())
		})
	}
}

func TestKimiRegistryPublicationAllowsAReentrantNativeInteraction(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	var reenter sync.Once
	rig.sink.OnUpsertBackgroundTask = func(update bgtask.Upsert) {
		if update.RowKey != "session_1/agent-0" {
			return
		}
		if !rig.agent.dispatchMu.TryLock() {
			t.Error("the registry callback runs while native dispatch is locked")
			return
		}
		rig.agent.dispatchMu.Unlock()
		reenter.Do(func() { rig.feed(t, questionEvent("registry-reentry")) })
	}
	defer func() { rig.sink.OnUpsertBackgroundTask = nil }()
	rig.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"description":"Reviewer","prompt":"Review it."},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	assert.Equal(t, 1, rig.sink.PublishedControlCount())
}

func TestKimiRegistryPublicationWaitsForSessionMutation(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	queue := agent.OutputQueueFor(rig.agent.sink)
	require.NotNil(t, queue)
	func() {
		rig.agent.sessionMu.Lock()
		defer rig.agent.sessionMu.Unlock()
		rig.agent.HandleOutput([]byte(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[{"task_id":"queued-task","kind":"subagent","status":"running","detached":false}]}`))
		_, exists := rig.sink.BackgroundTask("session_1/task/main/queued-task")
		assert.False(t, exists, "registry observers wait until the session mutation releases its lock")
		assert.True(t, queue.HasPending())
	}()
	require.NoError(t, queue.Flush())
	row, exists := rig.sink.BackgroundTask("session_1/task/main/queued-task")
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusPending, row.Status)
}
