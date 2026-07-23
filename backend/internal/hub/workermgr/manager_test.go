package workermgr

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestWaitForRegistrationChange_Notified(t *testing.T) {
	m := New(DenyAllReach())

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
	m := New(DenyAllReach())

	err := m.WaitForRegistrationChange(context.Background(), "token-2", 10*time.Millisecond)
	require.Error(t, err)
	assert.Equal(t, "wait for registration change timed out", err.Error())
}

func TestWaitForRegistrationChange_ContextCancel(t *testing.T) {
	m := New(DenyAllReach())

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
	m := New(DenyAllReach())
	// Should not panic.
	m.NotifyRegistrationChange("nonexistent-token")
}

func TestMarkDeregistering(t *testing.T) {
	m := New(DenyAllReach())

	assert.False(t, m.IsDeregistering("b1"))

	m.MarkDeregistering("b1")
	assert.True(t, m.IsDeregistering("b1"))

	// Other workers should not be affected.
	assert.False(t, m.IsDeregistering("b2"))
}

func TestRegister_ReturnsReplacedFlag(t *testing.T) {
	m := New(DenyAllReach())

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
	assert.Equal(t, conn2, m.ConnForTrustedPath("w1"))
	// Unregister with old conn1 should return false (already replaced).
	assert.False(t, m.Unregister("w1", conn1))
	// Unregister with current conn2 should return true.
	assert.True(t, m.Unregister("w1", conn2))
}

func TestRegister_FencesReplacedConnection(t *testing.T) {
	m := New(DenyAllReach())
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
	mgr := New(DenyAllReach())
	_, _ = mgr.Register(conn)
	go func() { _ = conn.Send(&leapmuxv1.ConnectResponse{}) }()
	<-started
	unregistered := make(chan bool, 1)
	go func() { unregistered <- mgr.Unregister("worker", conn) }()

	testutil.AssertEventually(t, func() bool { return !mgr.OnlineForTrustedPath("worker") })
	select {
	case <-unregistered:
		t.Fatal("Unregister returned before the in-flight send completed")
	default:
	}
	close(release)
	assert.True(t, <-unregistered)
}

func TestManager_ConnForTrustedPath_NotRegistered(t *testing.T) {
	m := New(DenyAllReach())

	conn := m.ConnForTrustedPath("nonexistent-worker")
	assert.Nil(t, conn, "ConnForTrustedPath on unregistered worker should return nil")
}

func TestManager_OnlineForTrustedPath_NotRegistered(t *testing.T) {
	m := New(DenyAllReach())

	assert.False(t, m.OnlineForTrustedPath("nonexistent-worker"), "OnlineForTrustedPath should return false for unknown worker")
}

func TestClearDeregistering(t *testing.T) {
	m := New(DenyAllReach())

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
	m := New(DenyAllReach())

	var sentWhilePublished []bool
	conn := &Conn{
		WorkerID: "w1",
		Greeting: &leapmuxv1.ConnectResponse{},
	}
	conn.SendFn = func(*leapmuxv1.ConnectResponse) error {
		// If the conn were already published, ConnForTrustedPath would find it.
		sentWhilePublished = append(sentWhilePublished, m.ConnForTrustedPath("w1") != nil)
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
	m := New(DenyAllReach())
	boom := errors.New("stream gone")
	conn := &Conn{
		WorkerID: "w1",
		Greeting: &leapmuxv1.ConnectResponse{},
		SendFn:   func(*leapmuxv1.ConnectResponse) error { return boom },
	}

	replaced, err := m.Register(conn)
	require.ErrorIs(t, err, boom)
	assert.False(t, replaced)

	assert.Nil(t, m.ConnForTrustedPath("w1"), "a conn whose greeting failed must not be published")
}

// A conn with no greeting registers exactly as before -- the field is optional.
func TestRegisterWithoutGreetingPublishes(t *testing.T) {
	m := New(DenyAllReach())
	replaced, err := m.Register(&Conn{WorkerID: "w1"})
	require.NoError(t, err)
	assert.False(t, replaced)
	assert.NotNil(t, m.ConnForTrustedPath("w1"))
}

// fakeReachAuthorizer records what it was asked and answers a fixed verdict.
type fakeReachAuthorizer struct {
	err   error
	asked []string
}

func (f *fakeReachAuthorizer) AuthorizeWorkerReach(_ context.Context, _ *auth.UserInfo, workerID string) error {
	f.asked = append(f.asked, workerID)
	return f.err
}

// A Manager cannot be constructed without a gate.
//
// This is what makes the gate structural rather than a wiring convention:
// "ungated" is not a reachable state, so a hub composition that forgets the
// authorizer fails at construction instead of serving the registry unchecked
// (or denying everything and looking like a permissions bug). The typed-nil
// case is checked too -- a nil *T in an interface is a NON-nil interface, so
// `a == nil` alone would let it through and panic on the first reach.
func TestNew_RequiresReachAuthorizer(t *testing.T) {
	assert.Panics(t, func() { New(nil) },
		"a registry with no gate must not be constructible")

	var typedNil *fakeReachAuthorizer
	assert.Panics(t, func() { New(typedNil) },
		"a typed-nil authorizer is still no gate")
}

// DenyAllReach is a real deny, not a placeholder that happens to work.
func TestDenyAllReach_Denies(t *testing.T) {
	m := New(DenyAllReach())
	conn := &Conn{WorkerID: "w1"}
	_, err := m.Register(conn)
	require.NoError(t, err)

	got, err := m.ConnForUser(context.Background(), &auth.UserInfo{}, "w1")
	require.ErrorIs(t, err, ErrReachDenied,
		"a deny-all registry must refuse user-directed reach")
	assert.Nil(t, got, "no connection may be returned by a deny-all registry")
}

// A nil principal is a deny, not a panic.
//
// ConnForUser is the fail-closed gate, so its own degenerate input has to have
// a defined answer: every authorizer dereferences user.ID, so passing nil
// through would crash the request goroutine instead of refusing. The authorizer
// must not even be consulted.
func TestManager_ConnForUser_NilUserDenies(t *testing.T) {
	allow := &fakeReachAuthorizer{}
	m := New(allow)
	conn := &Conn{WorkerID: "w1"}
	_, err := m.Register(conn)
	require.NoError(t, err)

	got, err := m.ConnForUser(context.Background(), nil, "w1")
	require.ErrorIs(t, err, ErrReachDenied, "a nil principal must be refused")
	assert.Nil(t, got)
	assert.Empty(t, allow.asked, "the authorizer must never be handed a nil principal")
}

// A worker being deregistered is not reachable by its user, even while its
// connection is still open.
//
// Deregistration is asynchronous -- the flag is set when the notification is
// SENT and cleared only when the worker ACKS it -- and for an offline worker
// the notification sits queued until it reconnects, so the window is unbounded.
// Before this, the operator's containment action left the machine fully
// reachable for all of it. The trusted path must stay open in the same state,
// because that is how the deregister notification reaches the worker at all: a
// gate there would make the teardown unable to complete and the flag permanent.
func TestManager_ConnForUser_DeregisteringWorkerIsUnreachable(t *testing.T) {
	m := New(&fakeReachAuthorizer{})
	conn := &Conn{WorkerID: "w1"}
	_, err := m.Register(conn)
	require.NoError(t, err)

	// Control: reachable before the operator acts.
	got, err := m.ConnForUser(context.Background(), &auth.UserInfo{}, "w1")
	require.NoError(t, err)
	require.Same(t, conn, got, "control: an ordinary worker is reachable")

	m.MarkDeregistering("w1")

	got, err = m.ConnForUser(context.Background(), &auth.UserInfo{}, "w1")
	require.NoError(t, err, "a worker being torn down is unreachable, not an error")
	assert.Nil(t, got, "a deregistering worker must not be handed to its user")
	assert.Same(t, conn, m.ConnForTrustedPath("w1"),
		"the trusted path stays open: it is how the deregister notification is delivered")

	// The worker acks, the flag clears, and reach is restored -- so the gate is
	// the flag rather than the registration being torn down underneath it.
	m.ClearDeregistering("w1")
	got, err = m.ConnForUser(context.Background(), &auth.UserInfo{}, "w1")
	require.NoError(t, err)
	assert.Same(t, conn, got, "clearing the flag restores user-directed reach")
}

// The deny has to reach the client as a PERMANENT refusal.
//
// requireOnlineWorker forwards this error verbatim to the RPC boundary, next to
// the coded denials the real authorizer returns. A bare error maps to
// CodeUnknown, which a client reads as a transient fault -- so a decision that
// will never change would drive an endless retry loop against it.
func TestErrReachDenied_IsPermissionDenied(t *testing.T) {
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(ErrReachDenied),
		"a permanent deny must not reach the client as an unknown fault")
}

// A denied reach must not disclose the online/offline bit.
//
// Returning the conn (or a distinguishable nil) after a failed check would keep
// the liveness oracle open, which is the exact exposure the gate closes.
func TestManager_ConnForUser_DeniedDoesNotLeakLiveness(t *testing.T) {
	denied := &fakeReachAuthorizer{err: errors.New("not your worker")}
	m := New(denied)
	online := &Conn{WorkerID: "online"}
	_, err := m.Register(online)
	require.NoError(t, err)

	// A worker that IS connected and one that is not must be indistinguishable.
	for _, workerID := range []string{"online", "never-registered"} {
		got, err := m.ConnForUser(context.Background(), &auth.UserInfo{}, workerID)
		require.Error(t, err, "a denied reach must error")
		assert.Nil(t, got)
	}
	assert.Equal(t, []string{"online", "never-registered"}, denied.asked,
		"the authorizer runs for both, so neither answer depends on connectedness")
}

// An authorized reach returns the live connection, and a nil conn means
// "authorized but offline" -- not "denied".
func TestManager_ConnForUser_AuthorizedReturnsConn(t *testing.T) {
	allow := &fakeReachAuthorizer{}
	m := New(allow)
	conn := &Conn{WorkerID: "w1"}
	_, err := m.Register(conn)
	require.NoError(t, err)

	got, err := m.ConnForUser(context.Background(), &auth.UserInfo{}, "w1")
	require.NoError(t, err)
	assert.Equal(t, conn, got)

	offline, err := m.ConnForUser(context.Background(), &auth.UserInfo{}, "not-connected")
	require.NoError(t, err, "authorized-but-offline is not an error")
	assert.Nil(t, offline)
	assert.Equal(t, []string{"w1", "not-connected"}, allow.asked,
		"both lookups are authorized before the map is read")
}
