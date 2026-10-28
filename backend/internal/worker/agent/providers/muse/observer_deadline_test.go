package muse

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOwnedSubscriptionObserverCanOutliveProcessCancellation(t *testing.T) {
	a, _ := testAgent(t)
	peer := &museManualPeer{requests: make(chan museManualRequest, 1)}
	a.SetStdinForTest(peer)
	entered, resume, delivered := make(chan struct{}), make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := a.awaitOwnedSubscription(a.Context(), func() {}, methodLogSubscribe, map[string]string{"sessionId": "session"}, func(json.RawMessage) error {
			close(entered)
			<-resume
			return errors.New("the late observer refused the reply")
		})
		done <- err
	}()
	request := <-peer.requests
	go func() {
		defer close(delivered)
		a.Deliver(request.ID, agenttest.JSONRPCResponse(request.ID, agenttest.RPCReply{Result: json.RawMessage(`{}`)}))
	}()
	<-entered
	a.CancelForTest()
	// Both paths can resume without a happens-before relation between the
	// request's cancellation and the observer's final return value.
	close(resume)
	require.ErrorIs(t, <-done, context.Canceled)
	<-delivered
	a.subscriptionWork.CloseAndWait()
}

func TestChildHeadObserverCannotPublishAfterItsOperationDeadline(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	a, root := testAgentWithClock(t, clock)
	state, _ := nativeHistoryChild(t, a, root)
	a.viewWork.CloseAndWait()
	recovery := &viewRecovery{}
	state.viewRecovery = recovery
	peer := &museManualPeer{requests: make(chan museManualRequest, 1)}
	a.SetStdinForTest(peer)
	created, stopped := testutil.NewTimerTraps(t, clock, providerkit.AwaitResponseTimerTag)
	type readResult struct {
		start childViewStart
		err   error
	}
	result := make(chan readResult, 1)
	deadline := clock.Now().Add(5 * time.Second)
	a.dispatcher.mu.Lock()
	go func() {
		start, err := a.readChildViewHead("child", state, recovery, deadline)
		result <- readResult{start, err}
	}()
	request := <-peer.requests
	ctx := testutil.DeadlineContext(t)
	delay := testutil.WaitForTimer(t, ctx, created)
	delivered := make(chan struct{})
	go func() {
		defer close(delivered)
		acknowledgeMuseRequest(a, request, `{"session":{"sessionId":"child","status":"running","activeTurnId":"late-turn"},"viewCursor":"late-head","history":{"mode":"inline","items":[]}}`)
	}()
	// Claiming the request precedes expiry. The observer then waits for dispatch.
	require.Eventually(t, func() bool { return !a.IsPendingForTest(request.ID) }, 30*time.Second, time.Millisecond)
	testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopped)
	observed := <-result
	require.Error(t, observed.err)
	assert.Nil(t, observed.start.head, "a failed request cannot return callback-owned metadata")
	assert.Nil(t, observed.start.nativeHead)
	a.dispatcher.mu.Unlock()
	<-delivered
	assert.Nil(t, state.nativeHead, "an expired observer cannot seed native activity")
	assert.False(t, state.childTurnActive())
}
