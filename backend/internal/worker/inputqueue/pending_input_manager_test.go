package inputqueue

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pendingInputDispatcher struct {
	recordingDispatcher
	dispatch func(DispatchItem) (DispatchResult, error)
}

func (dispatcher *pendingInputDispatcher) Dispatch(item DispatchItem) (DispatchResult, error) {
	return dispatcher.dispatch(item)
}

type testInputReadiness struct{ current bool }
type testInputReadinessLease struct{}

func (testInputReadinessLease) Release() {}
func (admission testInputReadiness) Acquire() agent.InputReadinessLease {
	if !admission.current {
		return nil
	}
	return testInputReadinessLease{}
}

func TestPendingInputReadinessResumesAutomaticDispatch(t *testing.T) {
	for _, timing := range []string{"before acceptance", "during refusal", "after refusal"} {
		t.Run(timing, func(t *testing.T) {
			t.Parallel()
			_, store := newStoreFixture(t)
			var pending atomic.Bool
			pending.Store(true)
			var nextAttempts atomic.Int32
			var manager *Manager
			dispatcher := &pendingInputDispatcher{}
			dispatcher.dispatch = func(item DispatchItem) (DispatchResult, error) {
				if item.ID == "first" {
					if timing == "before acceptance" {
						pending.Store(false)
						manager.NotifyInputReady("agent-1", testInputReadiness{current: true})
					}
					return DispatchResult{}, nil
				}
				nextAttempts.Add(1)
				if pending.Load() {
					if timing == "during refusal" {
						pending.Store(false)
						manager.NotifyInputReady("agent-1", testInputReadiness{current: true})
					}
					return DispatchResult{}, &DeliveryError{Err: agent.ErrInputPending, Outcome: DispatchPending}
				}
				return DispatchResult{StartsTurn: true}, nil
			}
			manager = NewManager(store, dispatcher, &recordingObserver{})
			t.Cleanup(manager.StopAndWait)
			for _, id := range []string{"first", "next"} {
				_, err := store.Enqueue(t.Context(), NewItem{ID: id, AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: id})
				require.NoError(t, err)
			}
			manager.NotifyDependencyReady("agent-1")
			if timing == "after refusal" {
				require.Eventually(t, func() bool {
					coordinator := manager.coordinator("agent-1")
					coordinator.mu.Lock()
					defer coordinator.mu.Unlock()
					return nextAttempts.Load() == 1 && !coordinator.draining
				}, eventuallyWait, time.Millisecond)
				waiting, err := manager.Snapshot(t.Context(), "agent-1")
				require.NoError(t, err)
				assert.False(t, waiting.ActiveTurn)
				require.Len(t, waiting.Items, 1)
				pending.Store(false)
				manager.NotifyInputReady("agent-1", testInputReadiness{current: true})
			}
			require.Eventually(t, func() bool {
				snapshot, err := manager.Snapshot(t.Context(), "agent-1")
				return err == nil && len(snapshot.Items) == 0 && snapshot.ActiveTurn
			}, eventuallyWait, time.Millisecond)
			expected := int32(2)
			if timing == "before acceptance" {
				expected = 1
			}
			assert.Equal(t, expected, nextAttempts.Load(), fmt.Sprintf("the %s readiness must survive its dispatch round", timing))
		})
	}
}

type countedReadiness struct{ releases *atomic.Int32 }
type countedReadinessLease struct{ releases *atomic.Int32 }

func (admission countedReadiness) Acquire() agent.InputReadinessLease {
	return countedReadinessLease(admission)
}
func (lease countedReadinessLease) Release() { lease.releases.Add(1) }

func TestInputReadinessReleasesEachLeaseExactlyOnce(t *testing.T) {
	for _, state := range []string{"new drain", "already draining", "retired coordinator"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			_, store := newStoreFixture(t)
			manager := NewManager(store, &recordingDispatcher{}, &recordingObserver{})
			t.Cleanup(manager.StopAndWait)
			c := manager.coordinator("agent-1")
			c.mu.Lock()
			c.draining = state == "already draining"
			c.retired = state == "retired coordinator"
			c.mu.Unlock()
			var releases atomic.Int32
			manager.NotifyInputReady("agent-1", countedReadiness{&releases})
			assert.Equal(t, int32(1), releases.Load())
		})
	}
}
