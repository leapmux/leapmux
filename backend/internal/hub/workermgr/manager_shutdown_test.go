package workermgr

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestNotifyShutdown_SendsToAllWorkers(t *testing.T) {
	m := New()

	var mu sync.Mutex
	var received []*leapmuxv1.ConnectResponse

	makeMockConn := func(workerID string) *Conn {
		return &Conn{
			WorkerID: workerID,
			SendFn: func(msg *leapmuxv1.ConnectResponse) error {
				mu.Lock()
				defer mu.Unlock()
				received = append(received, msg)
				return nil
			},
		}
	}

	m.Register(makeMockConn("w1"))
	m.Register(makeMockConn("w2"))
	m.Register(makeMockConn("w3"))

	m.NotifyShutdown(10)

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, received, 3)
	for _, msg := range received {
		payload, ok := msg.GetPayload().(*leapmuxv1.ConnectResponse_HubShuttingDown)
		require.True(t, ok, "expected HubShuttingDown payload")
		assert.Equal(t, int32(10), payload.HubShuttingDown.GetRetryDelaySeconds())
	}
}

func TestNotifyShutdown_CustomRetryDelay(t *testing.T) {
	m := New()

	var received *leapmuxv1.ConnectResponse
	m.Register(&Conn{
		WorkerID: "w1",
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			received = msg
			return nil
		},
	})

	m.NotifyShutdown(30)

	require.NotNil(t, received)
	payload, ok := received.GetPayload().(*leapmuxv1.ConnectResponse_HubShuttingDown)
	require.True(t, ok)
	assert.Equal(t, int32(30), payload.HubShuttingDown.GetRetryDelaySeconds())
}

func TestNotifyShutdown_NoWorkers(t *testing.T) {
	m := New()
	// Should not panic when no workers are connected.
	m.NotifyShutdown(10)
}

func TestNotifyShutdown_ContinuesOnSendError(t *testing.T) {
	m := New()

	sendCount := 0
	var mu sync.Mutex

	// First worker: send fails.
	m.Register(&Conn{
		WorkerID: "w-fail",
		SendFn: func(_ *leapmuxv1.ConnectResponse) error {
			mu.Lock()
			defer mu.Unlock()
			sendCount++
			return fmt.Errorf("connection reset")
		},
	})

	// Second worker: send succeeds.
	m.Register(&Conn{
		WorkerID: "w-ok",
		SendFn: func(_ *leapmuxv1.ConnectResponse) error {
			mu.Lock()
			defer mu.Unlock()
			sendCount++
			return nil
		},
	})

	// Should not panic or abort; best-effort delivery.
	m.NotifyShutdown(10)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 2, sendCount, "should attempt to send to all workers even on error")
}
