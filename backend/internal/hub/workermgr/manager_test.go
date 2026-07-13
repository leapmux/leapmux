package workermgr

import (
	"context"
	"errors"
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
	replaced, err := m.Register(conn1)
	require.NoError(t, err)
	assert.False(t, replaced)
	// Second registration for w1: replaces conn1.
	replaced, err = m.Register(conn2)
	require.NoError(t, err)
	assert.True(t, replaced)
	// First registration for w2: not a replacement.
	replaced, err = m.Register(conn3)
	require.NoError(t, err)
	assert.False(t, replaced)

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
	_, _ = m.Register(oldConn)

	replaced2, err2 := m.Register(&Conn{WorkerID: "w1"})
	require.NoError(t, err2)
	assert.True(t, replaced2)
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
	_, _ = mgr.Register(conn)
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

// Register must send a Conn's Greeting BEFORE publishing it.
//
// The ordering is the greeting's entire purpose: the Hub greets a worker with its
// own identity, which the worker needs before the first ChannelOpen creates a
// session (every machine-scoped handler gates on it). Until Register publishes the
// conn, nothing else can look it up to send on -- so a greeting sent here is
// mechanically first. This pins that the send happens on the pre-publication side.
func TestRegisterSendsGreetingBeforePublishing(t *testing.T) {
	m := New()

	var sentWhilePublished []bool
	conn := &Conn{
		WorkerID: "w1",
		Greeting: &leapmuxv1.ConnectResponse{},
	}
	conn.SendFn = func(*leapmuxv1.ConnectResponse) error {
		// If the conn were already published, Get would find it.
		sentWhilePublished = append(sentWhilePublished, m.Get("w1") != nil)
		return nil
	}

	replaced, err := m.Register(conn)
	require.NoError(t, err)
	assert.False(t, replaced)

	require.Len(t, sentWhilePublished, 1, "the greeting must be sent exactly once")
	assert.False(t, sentWhilePublished[0],
		"the greeting must be sent BEFORE the conn is published, or a ChannelOpen can precede it")
}

// A conn whose greeting cannot be delivered must NOT be published: a stream that
// cannot carry its greeting cannot carry a channel either, and publishing it would
// advertise the worker as reachable on a connection already known to be broken.
func TestRegisterDoesNotPublishOnGreetingFailure(t *testing.T) {
	m := New()
	boom := errors.New("stream gone")
	conn := &Conn{
		WorkerID: "w1",
		Greeting: &leapmuxv1.ConnectResponse{},
		SendFn:   func(*leapmuxv1.ConnectResponse) error { return boom },
	}

	replaced, err := m.Register(conn)
	require.ErrorIs(t, err, boom)
	assert.False(t, replaced)

	assert.Nil(t, m.Get("w1"), "a conn whose greeting failed must not be published")
}

// A conn with no greeting registers exactly as before -- the field is optional.
func TestRegisterWithoutGreetingPublishes(t *testing.T) {
	m := New()
	replaced, err := m.Register(&Conn{WorkerID: "w1"})
	require.NoError(t, err)
	assert.False(t, replaced)
	assert.NotNil(t, m.Get("w1"))
}
