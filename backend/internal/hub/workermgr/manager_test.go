package workermgr

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestWaitForRegistrationChange_Notified(t *testing.T) {
	m := New()

	done := make(chan error, 1)
	go func() {
		done <- m.WaitForRegistrationChange(context.Background(), "token-1", 5*time.Second)
	}()

	// Wait for the goroutine to register the waiter.
	testutil.AssertEventually(t, func() bool {
		m.regMu.Lock()
		defer m.regMu.Unlock()
		_, exists := m.regWaiters["token-1"]
		return exists
	})

	m.NotifyRegistrationChange("token-1")

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(1 * time.Second):
		require.Fail(t, "WaitForRegistrationChange did not return after Notify")
	}
}

func TestWaitForRegistrationChange_Timeout(t *testing.T) {
	m := New()

	err := m.WaitForRegistrationChange(context.Background(), "token-2", 10*time.Millisecond)
	require.Error(t, err)
	assert.Equal(t, "wait for registration change timed out", err.Error())
}

func TestWaitForRegistrationChange_ContextCancel(t *testing.T) {
	m := New()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- m.WaitForRegistrationChange(ctx, "token-3", 5*time.Second)
	}()

	// Wait for the goroutine to register the waiter.
	testutil.AssertEventually(t, func() bool {
		m.regMu.Lock()
		defer m.regMu.Unlock()
		_, exists := m.regWaiters["token-3"]
		return exists
	})

	cancel()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(1 * time.Second):
		require.Fail(t, "WaitForRegistrationChange did not return after context cancel")
	}
}

func TestNotifyRegistrationChange_NoWaiters(t *testing.T) {
	m := New()
	// Should not panic.
	m.NotifyRegistrationChange("nonexistent-token")
}

func TestMarkDeregistering(t *testing.T) {
	m := New()

	assert.False(t, m.IsDeregistering("b1"))

	m.MarkDeregistering("b1")
	assert.True(t, m.IsDeregistering("b1"))

	// Other workers should not be affected.
	assert.False(t, m.IsDeregistering("b2"))
}

func TestRegister_ReturnsReplacedFlag(t *testing.T) {
	m := New()

	conn1 := &Conn{WorkerID: "w1"}
	conn2 := &Conn{WorkerID: "w1"}
	conn3 := &Conn{WorkerID: "w2"}

	// First registration for w1: not a replacement.
	assert.False(t, m.Register(conn1))
	// Second registration for w1: replaces conn1.
	assert.True(t, m.Register(conn2))
	// First registration for w2: not a replacement.
	assert.False(t, m.Register(conn3))

	// Verify conn2 is the current connection for w1.
	assert.Equal(t, conn2, m.Get("w1"))
	// Unregister with old conn1 should return false (already replaced).
	assert.False(t, m.Unregister("w1", conn1))
	// Unregister with current conn2 should return true.
	assert.True(t, m.Unregister("w1", conn2))
}

func TestRegister_FencesReplacedConnection(t *testing.T) {
	m := New()
	oldSends := 0
	cancelled := false
	oldConn := &Conn{WorkerID: "w1", SendFn: func(*leapmuxv1.ConnectResponse) error {
		oldSends++
		return nil
	}, Cancel: func() { cancelled = true }}
	m.Register(oldConn)

	assert.True(t, m.Register(&Conn{WorkerID: "w1"}))
	assert.ErrorIs(t, oldConn.Send(&leapmuxv1.ConnectResponse{}), ErrConnectionClosed)
	assert.Zero(t, oldSends)
	assert.True(t, cancelled)
}

func TestConnCloseRejectsLaterSend(t *testing.T) {
	sent := 0
	conn := &Conn{SendFn: func(*leapmuxv1.ConnectResponse) error {
		sent++
		return nil
	}}
	conn.Close()

	err := conn.Send(&leapmuxv1.ConnectResponse{})

	assert.ErrorIs(t, err, ErrConnectionClosed)
	assert.Zero(t, sent)
}

func TestConnCloseWaitsForInFlightSend(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	conn := &Conn{SendFn: func(*leapmuxv1.ConnectResponse) error {
		close(started)
		<-release
		return nil
	}}
	sendDone := make(chan error, 1)
	go func() { sendDone <- conn.Send(&leapmuxv1.ConnectResponse{}) }()
	<-started
	closeDone := make(chan struct{})
	go func() {
		conn.Close()
		close(closeDone)
	}()

	select {
	case <-closeDone:
		t.Fatal("Close returned while a send was still using the stream")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-sendDone)
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not return after the in-flight send completed")
	}
	assert.ErrorIs(t, conn.Send(&leapmuxv1.ConnectResponse{}), ErrConnectionClosed)
}

func TestUnregisterStopsRoutingBeforeWaitingForInFlightSend(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	conn := &Conn{WorkerID: "worker", SendFn: func(*leapmuxv1.ConnectResponse) error {
		close(started)
		<-release
		return nil
	}}
	mgr := New()
	mgr.Register(conn)
	go func() { _ = conn.Send(&leapmuxv1.ConnectResponse{}) }()
	<-started
	unregistered := make(chan bool, 1)
	go func() { unregistered <- mgr.Unregister("worker", conn) }()

	testutil.AssertEventually(t, func() bool { return !mgr.IsOnline("worker") })
	select {
	case <-unregistered:
		t.Fatal("Unregister returned before the in-flight send completed")
	default:
	}
	close(release)
	assert.True(t, <-unregistered)
}

func TestManager_Get_NotRegistered(t *testing.T) {
	m := New()

	conn := m.Get("nonexistent-worker")
	assert.Nil(t, conn, "Get on unregistered worker should return nil")
}

func TestManager_IsOnline_NotRegistered(t *testing.T) {
	m := New()

	assert.False(t, m.IsOnline("nonexistent-worker"), "IsOnline should return false for unknown worker")
}

func TestClearDeregistering(t *testing.T) {
	m := New()

	m.MarkDeregistering("b1")
	m.ClearDeregistering("b1")
	assert.False(t, m.IsDeregistering("b1"))

	// ClearDeregistering on non-existent key should not panic.
	m.ClearDeregistering("nonexistent")
}
