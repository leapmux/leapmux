package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/util/drain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type handlerDrainWaitRequest struct {
	timeout time.Duration
	warning string
	expire  chan struct{}
}

type handlerCleanupWaitRequest struct {
	cleanupDone <-chan struct{}
	peerDone    <-chan struct{}
}

type controlledHandlerDrain struct {
	mu              sync.Mutex
	current         time.Time
	requests        chan handlerDrainWaitRequest
	cleanupRequests chan handlerCleanupWaitRequest
}

func newControlledHandlerDrain() *controlledHandlerDrain {
	return &controlledHandlerDrain{
		current:         time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC),
		requests:        make(chan handlerDrainWaitRequest, 1),
		cleanupRequests: make(chan handlerCleanupWaitRequest, 1),
	}
}

func (d *controlledHandlerDrain) now() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.current
}

func (d *controlledHandlerDrain) wait(done <-chan struct{}, timeout time.Duration, warning string) bool {
	request := handlerDrainWaitRequest{timeout: timeout, warning: warning, expire: make(chan struct{})}
	d.requests <- request
	select {
	case <-done:
		return true
	case <-request.expire:
		d.mu.Lock()
		d.current = d.current.Add(timeout)
		d.mu.Unlock()
		return false
	}
}

func (d *controlledHandlerDrain) policy() rpcHandlerDrain {
	policy := newRPCHandlerDrain()
	policy.now, policy.wait = d.now, d.wait
	policy.waitCleanup = func(cleanupDone, peerDone <-chan struct{}) bool {
		d.cleanupRequests <- handlerCleanupWaitRequest{cleanupDone: cleanupDone, peerDone: peerDone}
		return waitShutdownCleanup(cleanupDone, peerDone)
	}
	return policy
}

func (d *controlledHandlerDrain) next(t *testing.T) handlerDrainWaitRequest {
	t.Helper()
	return nextHandlerDrainRequest(t, d.requests)
}

func (d *controlledHandlerDrain) nextCleanup(t *testing.T) handlerCleanupWaitRequest {
	t.Helper()
	return nextHandlerDrainRequest(t, d.cleanupRequests)
}

func nextHandlerDrainRequest[T any](t *testing.T, requests <-chan T) T {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-t.Context().Done():
		t.Fatal("the test ended before the handler drain started")
	case <-time.After(30 * time.Second):
		t.Fatal("the handler drain did not start")
	}
	var zero T
	return zero
}

func TestRPCHandlerDrainKeepsOneDeadlineAndTheInterruptGrace(t *testing.T) {
	control := newControlledHandlerDrain()
	policy := control.policy()
	var handlers drain.Counter
	handlers.Add()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(handlers.Done) }
	t.Cleanup(release)
	interrupted := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		policy.run(&handlers, context.Canceled, func() { close(interrupted) })
	}()
	first := control.next(t)
	assert.Equal(t, handlerDrainTimeout, first.timeout)
	assert.Empty(t, first.warning)
	assert.True(t, handlers.Pending())
	select {
	case <-interrupted:
		t.Fatal("the writer closed before the shared deadline")
	default:
	}
	close(first.expire)
	second := control.next(t)
	require.Equal(t, handlerInterruptGrace, second.timeout)
	require.NotEmpty(t, second.warning)
	select {
	case <-interrupted:
	default:
		t.Fatal("the expired drain did not interrupt the writer")
	}
	release()
	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		t.Fatal("the drain did not join the released handler")
	}
}

func TestRPCHandlerDrainInterruptsADisconnectedPeerBeforeWaiting(t *testing.T) {
	control := newControlledHandlerDrain()
	policy := control.policy()
	var handlers drain.Counter
	handlers.Add()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(handlers.Done) }
	t.Cleanup(release)
	interrupted := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		policy.run(&handlers, errors.New("the peer disconnected"), func() { close(interrupted) })
	}()
	first := control.next(t)
	require.Equal(t, handlerDrainTimeout, first.timeout)
	require.NotEmpty(t, first.warning)
	select {
	case <-interrupted:
	default:
		t.Fatal("the disconnected peer's writer remained open")
	}
	release()
	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		t.Fatal("the drain did not join the released handler")
	}
}

func TestRPCHandlerDrainRetainsCleanupAndLimitsTheResponseGrace(t *testing.T) {
	for _, testCase := range []struct {
		label             string
		cleanupEnded      bool
		peerDisconnects   bool
		responseIsBlocked bool
	}{
		{label: "cleanup ends after the handler deadline"},
		{label: "cleanup ends before the handler deadline", cleanupEnded: true},
		{label: "the reply writer blocks after cleanup", responseIsBlocked: true},
		{label: "the peer disconnects during cleanup", peerDisconnects: true},
	} {
		t.Run(testCase.label, func(t *testing.T) {
			control := newControlledHandlerDrain()
			policy := control.policy()
			cleanupDone, peerDone := make(chan struct{}), make(chan struct{})
			finishCleanup := sync.OnceFunc(func() { close(cleanupDone) })
			disconnectPeer := sync.OnceFunc(func() { close(peerDone) })
			t.Cleanup(finishCleanup)
			t.Cleanup(disconnectPeer)
			if testCase.cleanupEnded {
				finishCleanup()
			}
			policy.cleanupDone, policy.peerDone = cleanupDone, peerDone
			var handlers drain.Counter
			handlers.Add()
			release := sync.OnceFunc(handlers.Done)
			t.Cleanup(release)
			interrupted, finished := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(finished)
				policy.run(&handlers, context.Canceled, func() { close(interrupted) })
			}()
			first := control.next(t)
			require.Equal(t, handlerDrainTimeout, first.timeout)
			close(first.expire)
			cleanupWait := control.nextCleanup(t)
			require.Equal(t, (<-chan struct{})(cleanupDone), cleanupWait.cleanupDone)
			require.Equal(t, (<-chan struct{})(peerDone), cleanupWait.peerDone)
			select {
			case <-interrupted:
				t.Fatal("the writer closed before cleanup ended or the peer disconnected")
			default:
			}
			if testCase.peerDisconnects {
				disconnectPeer()
			} else {
				finishCleanup()
			}
			second := control.next(t)
			require.Equal(t, handlerInterruptGrace, second.timeout)
			if testCase.peerDisconnects {
				require.NotEmpty(t, second.warning)
			} else {
				require.Empty(t, second.warning)
				select {
				case <-interrupted:
					t.Fatal("the writer closed before the response grace ended")
				default:
				}
			}
			if testCase.responseIsBlocked {
				close(second.expire)
				third := control.next(t)
				require.Equal(t, handlerInterruptGrace, third.timeout)
				require.NotEmpty(t, third.warning)
			}
			if testCase.peerDisconnects || testCase.responseIsBlocked {
				select {
				case <-interrupted:
				default:
					t.Fatal("the drain did not interrupt the blocked writer")
				}
			}
			release()
			select {
			case <-finished:
			case <-time.After(30 * time.Second):
				t.Fatal("the drain did not join the released handler")
			}
			if !testCase.peerDisconnects && !testCase.responseIsBlocked {
				select {
				case <-interrupted:
					t.Fatal("a completed response must retain its writer")
				default:
				}
			}
			require.Empty(t, control.requests, "the drain must not start another timed wait")
			require.Empty(t, control.cleanupRequests, "the drain must wait for cleanup once")
		})
	}
}

func TestShutdownCleanupPrefersAnAlreadyDisconnectedPeer(t *testing.T) {
	cleanupDone, peerDone := make(chan struct{}), make(chan struct{})
	close(cleanupDone)
	close(peerDone)
	require.False(t, waitShutdownCleanup(cleanupDone, peerDone), "a disconnected peer must receive no response grace")
}
