package workermgr

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// local equivalents of workermgrtest — this package cannot import that one
// without a cycle (workermgrtest imports workermgr).

type testRecorder struct {
	mu   sync.Mutex
	msgs []*leapmuxv1.ConnectResponse
	err  error
}

func (r *testRecorder) Write(msg *leapmuxv1.ConnectResponse) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.msgs = append(r.msgs, msg)
	return nil
}

func (r *testRecorder) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.msgs)
}

func (r *testRecorder) Messages() []*leapmuxv1.ConnectResponse {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*leapmuxv1.ConnectResponse, len(r.msgs))
	copy(out, r.msgs)
	return out
}

// testConnOwner is the account every fixture built by newTestConn belongs to.
// Tests about the per-account cap name their own owners via newOwnedTestConn;
// the rest share this one, which keeps them all in a single bucket so a test
// that DOES set a cap on a shared fixture cannot accidentally read as unlimited.
const testConnOwner = "u1"

func newTestConn(t *testing.T, workerID string, write func(*leapmuxv1.ConnectResponse) error, greeting *leapmuxv1.ConnectResponse) (*Conn, *SendPump) {
	t.Helper()
	return newOwnedTestConn(t, workerID, testConnOwner, write, greeting)
}

func newOwnedTestConn(t *testing.T, workerID, owner string, write func(*leapmuxv1.ConnectResponse) error, greeting *leapmuxv1.ConnectResponse) (*Conn, *SendPump) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if write == nil {
		write = func(*leapmuxv1.ConnectResponse) error { return nil }
	}
	conn, pump := NewConn(ctx, cancel, workerID, owner, sendq.NewMaxBytesPoolForTest(), write, greeting)
	t.Cleanup(conn.Fence)
	return conn, pump
}

func newAutoDrainedConn(t *testing.T, workerID string, greeting *leapmuxv1.ConnectResponse) (*Conn, *testRecorder) {
	t.Helper()
	rec := &testRecorder{}
	conn, pump := newTestConn(t, workerID, rec.Write, greeting)
	go func() {
		for {
			select {
			case <-pump.Ready():
				_ = pump.Drain()
			case <-conn.Done():
				return
			}
		}
	}()
	return conn, rec
}

func TestWaitForRegistrationChange_Notified(t *testing.T) {
	m := New(DenyAllReach())

	done := make(chan error, 1)
	go func() {
		done <- m.WaitForRegistrationChange(context.Background(), "token-1", 5*time.Second)
	}()

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
	m.NotifyRegistrationChange("nonexistent-token")
}

func TestMarkDeregistering(t *testing.T) {
	m := New(DenyAllReach())

	assert.False(t, m.IsDeregistering("b1"))

	m.MarkDeregistering("b1")
	assert.True(t, m.IsDeregistering("b1"))

	assert.False(t, m.IsDeregistering("b2"))
}

func TestRegister_ReturnsReplacedFlag(t *testing.T) {
	m := New(DenyAllReach())

	conn1, _ := newTestConn(t, "w1", nil, nil)
	conn2, _ := newTestConn(t, "w1", nil, nil)
	conn3, _ := newTestConn(t, "w2", nil, nil)

	replaced, err := m.Register(conn1)
	require.NoError(t, err)
	assert.False(t, replaced)
	replaced, err = m.Register(conn2)
	require.NoError(t, err)
	assert.True(t, replaced)
	replaced, err = m.Register(conn3)
	require.NoError(t, err)
	assert.False(t, replaced)

	assert.Equal(t, conn2, m.ConnForTrustedPath("w1"))
	assert.False(t, m.Unregister("w1", conn1))
	assert.True(t, m.Unregister("w1", conn2))
}

func TestRegister_FencesReplacedConnection(t *testing.T) {
	m := New(DenyAllReach())
	oldSends := 0
	cancelled := false
	oldConn, _ := newTestConn(t, "w1", func(*leapmuxv1.ConnectResponse) error {
		oldSends++
		return nil
	}, nil)
	// Swap cancel so we can observe fencing.
	oldCancel := oldConn.cancel
	oldConn.cancel = func() {
		cancelled = true
		oldCancel()
	}
	_, _ = m.Register(oldConn)

	newConn, _ := newTestConn(t, "w1", nil, nil)
	replaced2, err2 := m.Register(newConn)
	require.NoError(t, err2)
	assert.True(t, replaced2)
	assert.ErrorIs(t, oldConn.Send(&leapmuxv1.ConnectResponse{}), ErrConnectionClosed)
	assert.Zero(t, oldSends)
	assert.True(t, cancelled)
}

func TestConnFenceRejectsLaterSend(t *testing.T) {
	sent := 0
	conn, _ := newTestConn(t, "w1", func(*leapmuxv1.ConnectResponse) error {
		sent++
		return nil
	}, nil)
	conn.Fence()

	err := conn.Send(&leapmuxv1.ConnectResponse{})

	assert.ErrorIs(t, err, ErrConnectionClosed)
	assert.Zero(t, sent)
}

func TestFenceReturnsWhileAWriteIsParked(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	conn, pump := newTestConn(t, "w1", func(*leapmuxv1.ConnectResponse) error {
		close(started)
		<-release
		return nil
	}, nil)
	require.NoError(t, conn.SendControl(&leapmuxv1.ConnectResponse{}))
	go func() { _ = pump.Drain() }()
	<-started

	fenceDone := make(chan struct{})
	go func() {
		conn.Fence()
		close(fenceDone)
	}()
	select {
	case <-fenceDone:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Fence blocked behind a parked drain write")
	}
}

func TestUnregisterReturnsWhileAWriteIsParked(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	conn, pump := newTestConn(t, "worker", func(*leapmuxv1.ConnectResponse) error {
		close(started)
		<-release
		return nil
	}, nil)
	mgr := New(DenyAllReach())
	_, _ = mgr.Register(conn)
	require.NoError(t, conn.SendControl(&leapmuxv1.ConnectResponse{}))
	go func() { _ = pump.Drain() }()
	<-started

	unregistered := make(chan bool, 1)
	go func() { unregistered <- mgr.Unregister("worker", conn) }()

	select {
	case removed := <-unregistered:
		assert.True(t, removed)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Unregister blocked behind a parked drain write")
	}
	assert.False(t, mgr.OnlineForTrustedPath("worker"))

	// Unregister no longer Fences: the handler drains then Fences so leftover
	// frames are not discarded. Send may still enqueue until Fence.
	require.NoError(t, conn.Send(&leapmuxv1.ConnectResponse{}))
	conn.Fence()
	assert.ErrorIs(t, conn.Send(&leapmuxv1.ConnectResponse{}), ErrConnectionClosed)
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

	m.ClearDeregistering("nonexistent")
}

// Register enqueues a Conn's Greeting BEFORE publishing it. With a single
// handler drain that makes it mechanically the first frame written.
func TestRegisterSendsGreetingBeforePublishing(t *testing.T) {
	m := New(DenyAllReach())

	rec := &testRecorder{}
	greeting := &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_WorkerIdentity{
			WorkerIdentity: &leapmuxv1.WorkerIdentity{RegisteredBy: "alice"},
		},
	}
	conn, pump := newTestConn(t, "w1", rec.Write, greeting)

	replaced, err := m.Register(conn)
	require.NoError(t, err)
	assert.False(t, replaced)
	assert.Nil(t, conn.Greeting, "Register clears Greeting after enqueue")
	assert.Same(t, conn, m.ConnForTrustedPath("w1"), "conn must be published after Register")
	assert.Zero(t, rec.Len(), "greeting is queued, not written, until Drain")

	require.NoError(t, conn.SendControl(&leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_Heartbeat{Heartbeat: &leapmuxv1.Heartbeat{}},
	}))
	require.NoError(t, pump.Drain())

	msgs := rec.Messages()
	require.Len(t, msgs, 2)
	assert.NotNil(t, msgs[0].GetWorkerIdentity(), "greeting must be frame 0")
	assert.NotNil(t, msgs[1].GetHeartbeat(), "post-Register enqueue drains after the greeting")
}

// A greeting that cannot be enqueued (fenced queue) must not be published.
func TestRegisterRefusesGreetingOnAFencedConn(t *testing.T) {
	m := New(DenyAllReach())
	conn, _ := newTestConn(t, "w1", nil, &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_WorkerIdentity{
			WorkerIdentity: &leapmuxv1.WorkerIdentity{RegisteredBy: "alice"},
		},
	})
	conn.Fence()

	replaced, err := m.Register(conn)
	require.ErrorIs(t, err, ErrConnectionClosed)
	assert.False(t, replaced)
	assert.Nil(t, m.ConnForTrustedPath("w1"), "a conn whose greeting cannot be enqueued must not be published")
}

// A conn with no greeting registers exactly as before -- the field is optional.
func TestRegisterWithoutGreetingPublishes(t *testing.T) {
	m := New(DenyAllReach())
	conn, _ := newTestConn(t, "w1", nil, nil)
	replaced, err := m.Register(conn)
	require.NoError(t, err)
	assert.False(t, replaced)
	assert.NotNil(t, m.ConnForTrustedPath("w1"))
}

type fakeReachAuthorizer struct {
	err   error
	asked []string
}

func (f *fakeReachAuthorizer) AuthorizeWorkerReach(_ context.Context, _ *auth.UserInfo, workerID string) error {
	f.asked = append(f.asked, workerID)
	return f.err
}

func TestNew_RequiresReachAuthorizer(t *testing.T) {
	assert.Panics(t, func() { New(nil) },
		"a registry with no gate must not be constructible")

	var typedNil *fakeReachAuthorizer
	assert.Panics(t, func() { New(typedNil) },
		"a typed-nil authorizer is still no gate")
}

func TestDenyAllReach_Denies(t *testing.T) {
	m := New(DenyAllReach())
	conn, _ := newTestConn(t, "w1", nil, nil)
	_, err := m.Register(conn)
	require.NoError(t, err)

	got, err := m.ConnForUser(context.Background(), &auth.UserInfo{}, "w1")
	require.ErrorIs(t, err, ErrReachDenied,
		"a deny-all registry must refuse user-directed reach")
	assert.Nil(t, got, "no connection may be returned by a deny-all registry")
}

func TestManager_ConnForUser_NilUserDenies(t *testing.T) {
	allow := &fakeReachAuthorizer{}
	m := New(allow)
	conn, _ := newTestConn(t, "w1", nil, nil)
	_, err := m.Register(conn)
	require.NoError(t, err)

	got, err := m.ConnForUser(context.Background(), nil, "w1")
	require.ErrorIs(t, err, ErrReachDenied, "a nil principal must be refused")
	assert.Nil(t, got)
	assert.Empty(t, allow.asked, "the authorizer must never be handed a nil principal")
}

func TestManager_ConnForUser_DeregisteringWorkerIsUnreachable(t *testing.T) {
	m := New(&fakeReachAuthorizer{})
	conn, _ := newTestConn(t, "w1", nil, nil)
	_, err := m.Register(conn)
	require.NoError(t, err)

	got, err := m.ConnForUser(context.Background(), &auth.UserInfo{}, "w1")
	require.NoError(t, err)
	require.Same(t, conn, got, "control: an ordinary worker is reachable")

	m.MarkDeregistering("w1")

	got, err = m.ConnForUser(context.Background(), &auth.UserInfo{}, "w1")
	require.NoError(t, err, "a worker being torn down is unreachable, not an error")
	assert.Nil(t, got, "a deregistering worker must not be handed to its user")
	assert.Same(t, conn, m.ConnForTrustedPath("w1"),
		"the trusted path stays open: it is how the deregister notification is delivered")

	m.ClearDeregistering("w1")
	got, err = m.ConnForUser(context.Background(), &auth.UserInfo{}, "w1")
	require.NoError(t, err)
	assert.Same(t, conn, got, "clearing the flag restores user-directed reach")
}

func TestErrReachDenied_IsPermissionDenied(t *testing.T) {
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(ErrReachDenied),
		"a permanent deny must not reach the client as an unknown fault")
}

func TestManager_ConnForUser_DeniedDoesNotLeakLiveness(t *testing.T) {
	denied := &fakeReachAuthorizer{err: errors.New("not your worker")}
	m := New(denied)
	online, _ := newTestConn(t, "online", nil, nil)
	_, err := m.Register(online)
	require.NoError(t, err)

	for _, workerID := range []string{"online", "never-registered"} {
		got, err := m.ConnForUser(context.Background(), &auth.UserInfo{}, workerID)
		require.Error(t, err, "a denied reach must error")
		assert.Nil(t, got)
	}
	assert.Equal(t, []string{"online", "never-registered"}, denied.asked,
		"the authorizer runs for both, so neither answer depends on connectedness")
}

func TestManager_ConnForUser_AuthorizedReturnsConn(t *testing.T) {
	allow := &fakeReachAuthorizer{}
	m := New(allow)
	conn, _ := newTestConn(t, "w1", nil, nil)
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
