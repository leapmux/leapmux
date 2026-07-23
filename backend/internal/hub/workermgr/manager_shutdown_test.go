package workermgr

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestNotifyShutdown_SendsToAllWorkers(t *testing.T) {
	m := New(DenyAllReach())

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

	_, _ = m.Register(makeMockConn("w1"))
	_, _ = m.Register(makeMockConn("w2"))
	_, _ = m.Register(makeMockConn("w3"))

	m.NotifyShutdown(context.Background(), 10)

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
	m := New(DenyAllReach())

	var received *leapmuxv1.ConnectResponse
	_, _ = m.Register(&Conn{
		WorkerID: "w1",
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			received = msg
			return nil
		},
	})

	m.NotifyShutdown(context.Background(), 30)

	require.NotNil(t, received)
	payload, ok := received.GetPayload().(*leapmuxv1.ConnectResponse_HubShuttingDown)
	require.True(t, ok)
	assert.Equal(t, int32(30), payload.HubShuttingDown.GetRetryDelaySeconds())
}

func TestNotifyShutdown_NoWorkers(t *testing.T) {
	m := New(DenyAllReach())
	// Should not panic when no workers are connected.
	m.NotifyShutdown(context.Background(), 10)
}

func TestNotifyShutdown_ContinuesOnSendError(t *testing.T) {
	m := New(DenyAllReach())

	sendCount := 0
	var mu sync.Mutex

	// First worker: send fails.
	_, _ = m.Register(&Conn{
		WorkerID: "w-fail",
		SendFn: func(_ *leapmuxv1.ConnectResponse) error {
			mu.Lock()
			defer mu.Unlock()
			sendCount++
			return fmt.Errorf("connection reset")
		},
	})

	// Second worker: send succeeds.
	_, _ = m.Register(&Conn{
		WorkerID: "w-ok",
		SendFn: func(_ *leapmuxv1.ConnectResponse) error {
			mu.Lock()
			defer mu.Unlock()
			sendCount++
			return nil
		},
	})

	// Should not panic or abort; best-effort delivery.
	m.NotifyShutdown(context.Background(), 10)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 2, sendCount, "should attempt to send to all workers even on error")
}

func TestNotifyShutdown_DoesNotHoldManagerLockDuringSend(t *testing.T) {
	m := New(DenyAllReach())
	started := make(chan struct{})
	release := make(chan struct{})
	_, _ = m.Register(&Conn{WorkerID: "blocked", SendFn: func(*leapmuxv1.ConnectResponse) error {
		close(started)
		<-release
		return nil
	}})

	done := make(chan struct{})
	go func() {
		m.NotifyShutdown(context.Background(), 10)
		close(done)
	}()
	<-started

	registered := make(chan struct{})
	go func() {
		_, _ = m.Register(&Conn{WorkerID: "new"})
		close(registered)
	}()
	select {
	case <-registered:
	case <-time.After(time.Second):
		t.Fatal("Register blocked behind a shutdown notification send")
	}
	close(release)
	<-done
}

func TestNotifyShutdown_ReturnsWhenContextExpires(t *testing.T) {
	m := New(DenyAllReach())
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	_, _ = m.Register(&Conn{WorkerID: "blocked", SendFn: func(*leapmuxv1.ConnectResponse) error {
		close(started)
		<-release
		return nil
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		m.NotifyShutdown(ctx, 10)
		close(done)
	}()
	<-started
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("NotifyShutdown ignored context expiration")
	}
}
