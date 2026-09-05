package hub

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestHandleHubShuttingDown_StoresDelay(t *testing.T) {
	c := newBareClient()

	c.handleHubShuttingDown(&leapmuxv1.HubShuttingDownNotification{
		RetryDelaySeconds: 15,
	})

	assert.Equal(t, int64(15), c.hubRetryDelay.Load())
}

func TestHandleHubShuttingDown_OverwritesPreviousDelay(t *testing.T) {
	c := newBareClient()

	c.handleHubShuttingDown(&leapmuxv1.HubShuttingDownNotification{
		RetryDelaySeconds: 10,
	})
	c.handleHubShuttingDown(&leapmuxv1.HubShuttingDownNotification{
		RetryDelaySeconds: 20,
	})

	assert.Equal(t, int64(20), c.hubRetryDelay.Load())
}

func TestConnectWithReconnect_HubRetryDelayApplied(t *testing.T) {
	var attempts atomic.Int32

	clock := testutil.NewQuartzMock(t)
	client := &Client{clock: clock}
	testCtx := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, hubRequestedRetryTimerTag)

	// Simulate hub setting a retry delay of 100ms (scaled down for testing).
	// We set the delay before the first connect call, as if the hub sent
	// HubShuttingDownNotification during the previous connection.
	client.hubRetryDelay.Store(0) // will be set after first connect

	mockConnect := func(_ context.Context, _ string) error {
		n := attempts.Add(1)

		if n == 1 {
			// First connection: simulate hub sending shutdown notification.
			// The handler stores the delay which is consumed after disconnect.
			client.hubRetryDelay.Store(1) // 1 second delay
			return fmt.Errorf("hub shutting down")
		}
		// Second connection attempt: we got here after the delay.
		cancel()
		return fmt.Errorf("done")
	}

	bo := newFastBackoff()
	done := make(chan struct{})
	go func() {
		client.connectWithReconnect(ctx, "token", mockConnect, bo, 5*time.Millisecond)
		close(done)
	}()
	delay := testutil.WaitForTimer(t, testCtx, newTimer)
	assert.Equal(t, time.Second, delay)
	testutil.AdvanceAndAwaitStop(t, testCtx, clock, delay, stopTimer)
	select {
	case <-done:
	case <-testCtx.Done():
		require.FailNow(t, "the Hub retry delay did not lead to the second attempt")
	}
	assert.Equal(t, int32(2), attempts.Load())
}

func TestConnectWithReconnect_HubRetryDelayConsumedOnce(t *testing.T) {
	var attempts atomic.Int32

	clock := testutil.NewQuartzMock(t)
	client := &Client{clock: clock}
	testCtx := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	newHubTimer, stopHubTimer := testutil.NewTimerTraps(t, clock, hubRequestedRetryTimerTag)
	newBackoffTimer, stopBackoffTimer := testutil.NewTimerTraps(t, clock, hubReconnectTimerTag)

	// Pre-set the retry delay as if it was received during a previous connection.
	client.hubRetryDelay.Store(1) // 1 second

	mockConnect := func(_ context.Context, _ string) error {
		n := attempts.Add(1)

		if n >= 3 {
			cancel()
		}
		return fmt.Errorf("fail")
	}

	bo := newFastBackoff()
	done := make(chan struct{})
	go func() {
		client.connectWithReconnect(ctx, "token", mockConnect, bo, 5*time.Millisecond)
		close(done)
	}()
	hubDelay := testutil.WaitForTimer(t, testCtx, newHubTimer)
	assert.Equal(t, time.Second, hubDelay)
	testutil.AdvanceAndAwaitStop(t, testCtx, clock, hubDelay, stopHubTimer)
	backoffDelay := testutil.WaitForTimer(t, testCtx, newBackoffTimer)
	assert.Equal(t, time.Millisecond, backoffDelay,
		"the consumed Hub delay must give the next failure to normal backoff")
	testutil.AdvanceAndAwaitStop(t, testCtx, clock, backoffDelay, stopBackoffTimer)
	select {
	case <-done:
	case <-testCtx.Done():
		require.FailNow(t, "the reconnect loop did not consume the Hub delay once")
	}
	assert.Equal(t, int32(3), attempts.Load())
}

func TestConnectWithReconnect_HubRetryDelayResetsBackoff(t *testing.T) {
	var attempts atomic.Int32

	clock := testutil.NewQuartzMock(t)
	client := &Client{clock: clock}
	testCtx := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	newHubTimer, stopHubTimer := testutil.NewTimerTraps(t, clock, hubRequestedRetryTimerTag)
	newBackoffTimer, stopBackoffTimer := testutil.NewTimerTraps(t, clock, hubReconnectTimerTag)

	mockConnect := func(_ context.Context, _ string) error {
		n := attempts.Add(1)

		switch n {
		case 1:
			return fmt.Errorf("fail 1") // backoff = 1ms
		case 2:
			return fmt.Errorf("fail 2") // backoff = 2ms
		case 3:
			return fmt.Errorf("fail 3") // backoff = 4ms
		case 4:
			// Simulate hub shutdown notification during this connection.
			client.hubRetryDelay.Store(1) // 1 second
			return fmt.Errorf("hub shutting down")
		case 5:
			// After consuming delay, backoff should be reset.
			return fmt.Errorf("fail 5") // backoff should be 1ms (reset)
		default:
			cancel()
			return fmt.Errorf("done")
		}
	}

	bo := newFastBackoff()
	done := make(chan struct{})
	go func() {
		client.connectWithReconnect(ctx, "token", mockConnect, bo, 5*time.Millisecond)
		close(done)
	}()
	for i, expected := range []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond} {
		delay := testutil.WaitForTimer(t, testCtx, newBackoffTimer)
		assert.Equal(t, expected, delay, "backoff delay %d", i+1)
		testutil.AdvanceAndAwaitStop(t, testCtx, clock, delay, stopBackoffTimer)
	}
	hubDelay := testutil.WaitForTimer(t, testCtx, newHubTimer)
	assert.Equal(t, time.Second, hubDelay)
	testutil.AdvanceAndAwaitStop(t, testCtx, clock, hubDelay, stopHubTimer)
	delayAfterReset := testutil.WaitForTimer(t, testCtx, newBackoffTimer)
	assert.Equal(t, time.Millisecond, delayAfterReset,
		"a Hub-requested delay must reset normal backoff")
	testutil.AdvanceAndAwaitStop(t, testCtx, clock, delayAfterReset, stopBackoffTimer)
	select {
	case <-done:
	case <-testCtx.Done():
		require.FailNow(t, "the reconnect loop did not stop after the reset sequence")
	}
	assert.Equal(t, int32(6), attempts.Load())
}

func TestConnectWithReconnect_HubRetryDelayCancelledByContext(t *testing.T) {
	var attempts atomic.Int32

	clock := testutil.NewQuartzMock(t)
	client := &Client{clock: clock}
	testCtx := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, hubRequestedRetryTimerTag)

	// Pre-set a large retry delay.
	client.hubRetryDelay.Store(60) // 60 seconds -- should not actually wait this long

	mockConnect := func(_ context.Context, _ string) error {
		attempts.Add(1)
		return fmt.Errorf("fail")
	}

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		bo := newFastBackoff()
		client.connectWithReconnect(ctx, "token", mockConnect, bo, 5*time.Millisecond)
	}()
	assert.Equal(t, 60*time.Second, testutil.WaitForTimer(t, testCtx, newTimer))
	cancel()
	stopTimer.MustWait(testCtx).MustRelease(testCtx)
	select {
	case <-returned:
	case <-testCtx.Done():
		t.Fatal("connectWithReconnect ignored the cancelled context and waited on its reconnect delay")
	}
	_, running := clock.Peek()
	assert.False(t, running, "context cancellation must stop the Hub retry timer")
	assert.Equal(t, int32(1), attempts.Load(), "expected exactly 1 attempt before cancel")
}

func TestHandleMessage_HubShuttingDown(t *testing.T) {
	c := newBareClient()

	msg := &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_HubShuttingDown{
			HubShuttingDown: &leapmuxv1.HubShuttingDownNotification{
				RetryDelaySeconds: 25,
			},
		},
	}

	c.handleMessage(msg)

	assert.Equal(t, int64(25), c.hubRetryDelay.Load())
}
