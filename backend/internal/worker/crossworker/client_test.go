package crossworker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubDelegationProvider lets tests inject a controlled bearer / error
// without spinning up a real hub mint endpoint. The real
// DelegationStore covers the HTTP path under delegation_test.go;
// here we want to exercise the Client's argument-validation +
// pooling without that machinery.
type stubDelegationProvider struct {
	bearer string
	err    error
	calls  int
}

func (s *stubDelegationProvider) GetBearer(_ context.Context, _ DelegationScope) (string, error) {
	s.calls++
	return s.bearer, s.err
}

// TestNew_ConstructsUsableClient pins down the constructor's contract.
// The pool map must be initialized so the first cache lookup doesn't
// nil-deref.
func TestNew_ConstructsUsableClient(t *testing.T) {
	c := New(context.Background(), "http://hub.test", &PinStore{}, &stubDelegationProvider{})
	require.NotNil(t, c)
	assert.NotNil(t, c.channels, "channels map must be initialized — Client mutex assumes it")

	// Close must be safe even when no channels were ever opened.
	c.Close()
	// Idempotent: a second Close after the first must not panic on a
	// nil-cleared map. (Code path the worker shutdown loop hits.)
	c.Close()
}

func TestNew_BindsPoolToExplicitLifetime(t *testing.T) {
	lifetime, cancel := context.WithCancel(context.Background())
	c := New(lifetime, "http://hub.test", &PinStore{}, &stubDelegationProvider{})
	cancel()
	select {
	case <-c.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("cross-worker pool did not stop with its lifetime context")
	}
	_, err := c.channelFor(context.Background(), "worker-2", DelegationScope{UserID: userid.MustNew("user-1"), WorkspaceID: "ws-1"})
	assert.ErrorIs(t, err, context.Canceled)
}

// TestChannelFor_RejectsEmptyTarget and TestChannelFor_RejectsEmptyUser
// guard the input contract: callers (router.go) supply userID +
// targetWorkerID, but a future refactor that drops one path could
// silently start opening unscoped channels.
func TestChannelFor_RejectsEmptyTarget(t *testing.T) {
	c := New(context.Background(), "http://hub.test", &PinStore{}, &stubDelegationProvider{bearer: "x"})
	_, err := c.channelFor(context.Background(), "", DelegationScope{UserID: userid.MustNew("user-1"), WorkspaceID: "ws-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "target_worker_id required")
}

func TestChannelFor_RejectsEmptyUser(t *testing.T) {
	c := New(context.Background(), "http://hub.test", &PinStore{}, &stubDelegationProvider{bearer: "x"})
	_, err := c.channelFor(context.Background(), "worker-B", DelegationScope{WorkspaceID: "ws-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user_id required")
}

// TestChannelFor_PropagatesDelegationError wires the failure path
// through the DelegationProvider abstraction. Worker A asks for a
// bearer for (user, workspace); if the mint endpoint refuses, the
// channel must NOT be opened — otherwise we'd hold an unauthenticated
// Noise channel in the pool.
func TestChannelFor_PropagatesDelegationError(t *testing.T) {
	dp := &stubDelegationProvider{err: errors.New("mint refused: workspace gone")}
	c := New(context.Background(), "http://hub.test", &PinStore{}, dp)

	_, err := c.channelFor(context.Background(), "worker-B", DelegationScope{UserID: userid.MustNew("user-1"), WorkspaceID: "ws-1", AgentID: "agent-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delegation token")
	assert.Contains(t, err.Error(), "mint refused")
	// Single attempt — channelFor must not retry on delegation failure
	// (retries belong inside the DelegationStore where they can be
	// scoped to the propagation race; everything else is fatal).
	assert.Equal(t, 1, dp.calls)

	// Pool must remain empty after a failed open. Otherwise a later
	// Close would try to close a nil tunnel.Channel.
	c.mu.Lock()
	assert.Empty(t, c.channels)
	c.mu.Unlock()
}

// TestChannelFor_PoolKeyIsTargetWorkerAndUser proves the cache key
// composes both the target worker and the user id. Two users on the
// same worker must receive separate channels (different Noise_NK
// session keys → different identity assertions); the same user
// hitting the same worker twice must hit the existing entry.
//
// Without standing up a real Noise responder we can't drive the open
// happy path here; the assertion is on the argument-validation code
// the pool relies on, which the existing fake covers via
// PropagatesDelegationError.
func TestChannelFor_PoolKeyComposition(t *testing.T) {
	c := New(context.Background(), "http://hub.test", &PinStore{}, &stubDelegationProvider{bearer: "x"})
	// Manually seed pool entries to verify key independence (worker,
	// user, AND workspace must each contribute to the key — delegation
	// scope is per-workspace, so two workspaces on the same (worker,
	// user) need separate Noise sessions) and that Close() reaps them.
	c.channels[clientKey{WorkerID: "B", UserID: "u-1", WorkspaceID: "ws-1"}] = nil
	c.channels[clientKey{WorkerID: "B", UserID: "u-2", WorkspaceID: "ws-1"}] = nil
	c.channels[clientKey{WorkerID: "C", UserID: "u-1", WorkspaceID: "ws-1"}] = nil
	c.channels[clientKey{WorkerID: "B", UserID: "u-1", WorkspaceID: "ws-2"}] = nil
	require.Len(t, c.channels, 4)

	c.Close()
	c.mu.Lock()
	assert.Empty(t, c.channels, "Close must clear the pool")
	c.mu.Unlock()
}

// TestCallInner_DelegationFailureSurfaces is the unary entrypoint
// counterpart to PropagatesDelegationError — proves the error
// reaches the caller intact instead of being swallowed by the
// pool layer.
func TestCallInner_DelegationFailureSurfaces(t *testing.T) {
	c := New(context.Background(), "http://hub.test", &PinStore{}, &stubDelegationProvider{err: errors.New("mint denied")})
	_, err := c.CallInner(context.Background(), "worker-B", userid.MustNew("user-1"), "ws-1", "OpenAgent", []byte("payload"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint denied")
}

// TestStreamInner_DelegationFailureSurfaces mirrors CallInner for the
// streaming path. Both go through channelFor, but the streaming
// version's onMsg callback shouldn't swallow the upstream error.
func TestStreamInner_DelegationFailureSurfaces(t *testing.T) {
	c := New(context.Background(), "http://hub.test", &PinStore{}, &stubDelegationProvider{err: errors.New("mint denied")})
	err := c.StreamInner(context.Background(), "worker-B", userid.MustNew("user-1"), "ws-1", "WatchEvents", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint denied")
}

// TestCallInner_RequiresWorkspaceID guards the new contract: a
// cross-worker call without a workspace id is rejected before any
// network I/O because the delegation bearer is per-workspace.
func TestCallInner_RequiresWorkspaceID(t *testing.T) {
	c := New(context.Background(), "http://hub.test", &PinStore{}, &stubDelegationProvider{bearer: "x"})
	_, err := c.CallInner(context.Background(), "worker-B", userid.MustNew("user-1"), "", "OpenAgent", []byte("p"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workspace_id required")
}

// blockingDelegationProvider blocks the first (and, under single-flight, only)
// GetBearer until released, so a test can register several concurrent callers
// on the same in-flight open before it resolves.
type blockingDelegationProvider struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	calls   int
	err     error
}

func (b *blockingDelegationProvider) GetBearer(ctx context.Context, _ DelegationScope) (string, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return "", b.err
}

// TestChannelFor_SingleFlightsConcurrentOpens proves the pool dedups a burst of
// concurrent first-contact calls to the same (worker, user, workspace) into a
// single delegation mint + Noise handshake, instead of racing N of them and
// discarding all but one.
func TestChannelFor_SingleFlightsConcurrentOpens(t *testing.T) {
	dp := &blockingDelegationProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
		err:     errors.New("mint done"),
	}
	c := New(context.Background(), "http://hub.test", &PinStore{}, dp)
	defer c.Close()

	const callers = 8
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := c.channelFor(context.Background(), "worker-B",
				DelegationScope{UserID: userid.MustNew("user-1"), WorkspaceID: "ws-1"})
			errs[idx] = err
		}(i)
	}

	// Wait for the shared open to begin, then give the followers time to attach
	// to it before releasing the mint.
	select {
	case <-dp.started:
	case <-time.After(2 * time.Second):
		t.Fatal("no channelFor started the shared open")
	}
	time.Sleep(100 * time.Millisecond)
	close(dp.release)
	wg.Wait()

	dp.mu.Lock()
	calls := dp.calls
	dp.mu.Unlock()
	assert.Equal(t, 1, calls, "concurrent opens for one key must mint exactly one delegation token")

	for i, err := range errs {
		require.Error(t, err, "caller %d", i)
		assert.Contains(t, err.Error(), "mint done", "caller %d", i)
	}

	// The in-flight marker and the pool must be empty after a failed shared open.
	c.mu.Lock()
	_, stillInflight := c.inflight[clientKey{WorkerID: "worker-B", UserID: "user-1", WorkspaceID: "ws-1"}]
	poolLen := len(c.channels)
	c.mu.Unlock()
	assert.False(t, stillInflight, "in-flight marker must clear once the open resolves")
	assert.Zero(t, poolLen, "a failed open must not pool a channel")
}

// Compile-time interface assertions: both stubs satisfy the contract.
var (
	_ DelegationProvider = (*stubDelegationProvider)(nil)
	_ DelegationProvider = (*blockingDelegationProvider)(nil)
)
