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
)

func TestHandleHubShuttingDown_StoresDelay(t *testing.T) {
	c := &Client{}

	c.handleHubShuttingDown(&leapmuxv1.HubShuttingDownNotification{
		RetryDelaySeconds: 15,
	})

	assert.Equal(t, int64(15), c.hubRetryDelay.Load())
}

func TestHandleHubShuttingDown_OverwritesPreviousDelay(t *testing.T) {
	c := &Client{}

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
	var timestamps []time.Time

	client := &Client{}
	ctx, cancel := context.WithCancel(context.Background())

	// Simulate hub setting a retry delay of 100ms (scaled down for testing).
	// We set the delay before the first connect call, as if the hub sent
	// HubShuttingDownNotification during the previous connection.
	client.hubRetryDelay.Store(0) // will be set after first connect

	mockConnect := func(_ context.Context, _ string) error {
		n := attempts.Add(1)
		timestamps = append(timestamps, time.Now())

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
	client.connectWithReconnect(ctx, "token", mockConnect, bo, 5*time.Millisecond)

	require.GreaterOrEqual(t, len(timestamps), 2, "expected at least 2 connect attempts")

	// The gap between attempt 1 and 2 should be at least ~1 second (the retry delay),
	// not just the fast backoff interval.
	gap := timestamps[1].Sub(timestamps[0])
	assert.GreaterOrEqual(t, gap, 900*time.Millisecond,
		"gap should be at least ~1 second due to hub retry delay, got %v", gap)
}

func TestConnectWithReconnect_HubRetryDelayConsumedOnce(t *testing.T) {
	var attempts atomic.Int32
	var timestamps []time.Time

	client := &Client{}
	ctx, cancel := context.WithCancel(context.Background())

	// Pre-set the retry delay as if it was received during a previous connection.
	client.hubRetryDelay.Store(1) // 1 second

	mockConnect := func(_ context.Context, _ string) error {
		n := attempts.Add(1)
		timestamps = append(timestamps, time.Now())

		if n >= 3 {
			cancel()
		}
		return fmt.Errorf("fail")
	}

	bo := newFastBackoff()
	client.connectWithReconnect(ctx, "token", mockConnect, bo, 5*time.Millisecond)

	require.GreaterOrEqual(t, len(timestamps), 3, "expected at least 3 connect attempts")

	// First gap: should include the 1-second retry delay.
	gap1 := timestamps[1].Sub(timestamps[0])
	assert.GreaterOrEqual(t, gap1, 900*time.Millisecond,
		"first gap should include retry delay, got %v", gap1)

	// Second gap: delay was consumed, so should fall back to fast backoff (< 100ms).
	gap2 := timestamps[2].Sub(timestamps[1])
	assert.Less(t, gap2, 100*time.Millisecond,
		"second gap should use normal backoff (delay consumed), got %v", gap2)
}

func TestConnectWithReconnect_HubRetryDelayResetsBackoff(t *testing.T) {
	var attempts atomic.Int32
	var timestamps []time.Time

	client := &Client{}
	ctx, cancel := context.WithCancel(context.Background())

	mockConnect := func(_ context.Context, _ string) error {
		n := attempts.Add(1)
		timestamps = append(timestamps, time.Now())

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
	client.connectWithReconnect(ctx, "token", mockConnect, bo, 5*time.Millisecond)

	require.GreaterOrEqual(t, len(timestamps), 6)

	// Gap between 5 and 6 should be small (reset backoff), not escalated.
	gap56 := timestamps[5].Sub(timestamps[4])
	assert.Less(t, gap56, 50*time.Millisecond,
		"backoff should be reset after hub retry delay, got %v", gap56)
}

func TestConnectWithReconnect_HubRetryDelayCancelledByContext(t *testing.T) {
	var attempts atomic.Int32

	client := &Client{}
	ctx, cancel := context.WithCancel(context.Background())

	// Pre-set a large retry delay.
	client.hubRetryDelay.Store(60) // 60 seconds -- should not actually wait this long

	mockConnect := func(_ context.Context, _ string) error {
		attempts.Add(1)
		return fmt.Errorf("fail")
	}

	// Cancel after a short time.
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	bo := newFastBackoff()
	client.connectWithReconnect(ctx, "token", mockConnect, bo, 5*time.Millisecond)
	elapsed := time.Since(start)

	// Should have exited well before the 60-second delay.
	assert.Less(t, elapsed, 2*time.Second,
		"should exit promptly on context cancel, not wait for full delay")
	assert.Equal(t, int32(1), attempts.Load(), "expected exactly 1 attempt before cancel")
}

func TestHandleMessage_HubShuttingDown(t *testing.T) {
	c := &Client{
		agentWorkspaces:    make(map[string]string),
		terminalWorkspaces: make(map[string]terminalMeta),
	}

	msg := &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_HubShuttingDown{
			HubShuttingDown: &leapmuxv1.HubShuttingDownNotification{
				RetryDelaySeconds: 25,
			},
		},
	}

	c.handleMessage(context.Background(), msg)

	assert.Equal(t, int64(25), c.hubRetryDelay.Load())
}
