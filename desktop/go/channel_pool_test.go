package main

import (
	"context"
	"sync"
	"testing"
	"time"

	tunnelpkg "github.com/leapmux/leapmux/tunnel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelPool_CanceledLeaderDoesNotCancelChannelForFollower(t *testing.T) {
	p := newChannelPool(nil)
	defer p.reset()
	channel, _ := newTestManagedChannel()
	started := make(chan struct{})
	release := make(chan struct{})
	openLifetime := make(chan context.Context, 1)
	var startOnce sync.Once
	p.openCh = func(
		ctx context.Context,
		_, _ string,
		opts *tunnelpkg.OpenChannelOptions,
	) (*managedChannel, error) {
		startOnce.Do(func() { close(started) })
		openLifetime <- opts.LifetimeContext
		select {
		case <-release:
			return channel, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	const hubURL, workerID = "http://hub", "worker"
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderResult := make(chan error, 1)
	go func() {
		_, err := p.getOrOpen(leaderCtx, p.currentRevision(), hubURL, workerID)
		leaderResult <- err
	}()
	<-started
	lifetimeCtx := <-openLifetime

	followerResult := make(chan error, 1)
	go func() {
		got, err := p.getOrOpen(context.Background(), p.currentRevision(), hubURL, workerID)
		if err == nil {
			assert.Same(t, channel, got)
		}
		followerResult <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancelLeader()

	require.ErrorIs(t, <-leaderResult, context.Canceled)
	assert.NoError(t, lifetimeCtx.Err(), "cached channel lifetime inherited the canceled leader")
	close(release)
	require.NoError(t, <-followerResult)
}

func TestChannelPool_ChannelOpenTimesOutOnStalledHub(t *testing.T) {
	// A hub that accepts the connection but never completes the handshake must
	// not wedge the channel open forever (the open runs under the epoch context,
	// which has no deadline, and the desktop proxy clients carry no timeout).
	// channelOpenTimeout fences it so the accepted local conn fails fast instead
	// of hanging until tunnel teardown.
	prev := channelOpenTimeout
	channelOpenTimeout = 50 * time.Millisecond
	defer func() { channelOpenTimeout = prev }()

	p := newChannelPool(nil)
	defer p.reset()

	openReleased := make(chan struct{}, 1)
	p.openCh = func(
		ctx context.Context,
		_, _ string,
		_ *tunnelpkg.OpenChannelOptions,
	) (*managedChannel, error) {
		<-ctx.Done() // stalled hub: block until the open deadline fences ctx
		openReleased <- struct{}{}
		return nil, ctx.Err()
	}

	start := time.Now()
	_, err := p.getOrOpen(context.Background(), p.currentRevision(), "http://hub", "worker")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 5*time.Second, "open should fail at the deadline, not hang")

	select {
	case <-openReleased:
	case <-time.After(time.Second):
		t.Fatal("openCh was never released by the open deadline")
	}
}

func TestChannelPool_DoesNotShareChannelsAcrossConnectionIdentity(t *testing.T) {
	p := newChannelPool(nil)
	defer p.reset()
	firstChannel, _ := newTestManagedChannel()
	secondChannel, _ := newTestManagedChannel()
	thirdChannel, _ := newTestManagedChannel()
	channels := []*managedChannel{firstChannel, secondChannel, thirdChannel}
	openCalls := 0
	p.openCh = func(
		_ context.Context,
		_, _ string,
		_ *tunnelpkg.OpenChannelOptions,
	) (*managedChannel, error) {
		ch := channels[openCalls]
		openCalls++
		return ch, nil
	}

	first, err := p.getOrOpen(context.Background(), p.currentRevision(), "http://first-hub", "worker")
	require.NoError(t, err)
	second, err := p.getOrOpen(context.Background(), p.currentRevision(), "http://second-hub", "worker")
	require.NoError(t, err)

	assert.Same(t, channels[0], first)
	assert.Same(t, channels[1], second)

	p.setOptions(&tunnelpkg.OpenChannelOptions{BearerToken: "new-options"})
	third, err := p.getOrOpen(context.Background(), p.currentRevision(), "http://second-hub", "worker")
	require.NoError(t, err)
	assert.Same(t, channels[2], third)
	assert.Equal(t, 3, openCalls)
}

func TestChannelPool_RemovesClosedCachedChannel(t *testing.T) {
	p := newChannelPool(nil)
	defer p.reset()
	first, _ := newTestManagedChannel()
	second, _ := newTestManagedChannel()
	channels := []*managedChannel{first, second}
	openCalls := 0
	p.openCh = func(
		_ context.Context,
		_, _ string,
		_ *tunnelpkg.OpenChannelOptions,
	) (*managedChannel, error) {
		ch := channels[openCalls]
		openCalls++
		return ch, nil
	}
	const hubURL, workerID = "http://hub", "worker"

	opened, err := p.getOrOpen(context.Background(), p.currentRevision(), hubURL, workerID)
	require.NoError(t, err)
	opened.close()
	require.Eventually(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.channels) == 0
	}, time.Second, time.Millisecond)

	reopened, err := p.getOrOpen(context.Background(), p.currentRevision(), hubURL, workerID)
	require.NoError(t, err)
	assert.Same(t, second, reopened)
	assert.Equal(t, 2, openCalls)
}

// A channel whose LIFETIME was cancelled without an explicit Close is dead too, and
// the pool must treat it as such.
func TestChannelPool_LiveReResolvesWhenLifetimeCancelled(t *testing.T) {
	p := newChannelPool(nil)
	defer p.reset()

	lifetimeCtx, cancelLifetime := context.WithCancel(context.Background())
	dying := &managedChannel{handle: &fakeChannel{ctx: lifetimeCtx, cancel: cancelLifetime}}
	fresh, _ := newTestManagedChannel()
	channels := []*managedChannel{dying, fresh}
	openCalls := 0
	p.openCh = func(_ context.Context, _, _ string, _ *tunnelpkg.OpenChannelOptions) (*managedChannel, error) {
		ch := channels[openCalls]
		openCalls++
		return ch, nil
	}
	const hubURL, workerID = "http://hub", "worker"

	opened, err := p.getOrOpen(context.Background(), p.currentRevision(), hubURL, workerID)
	require.NoError(t, err)
	require.Same(t, dying, opened)

	// The transport dies: the lifetime ends, but nothing calls Close.
	cancelLifetime()
	require.Eventually(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.channels) == 0
	}, time.Second, time.Millisecond, "a channel whose lifetime ended must be evicted from the cache")

	live, err := p.live(context.Background(), dying, hubURL, workerID)
	require.NoError(t, err)
	assert.Same(t, fresh, live, "a tunnel holding a lifetime-cancelled channel must re-resolve a live one")
	assert.Equal(t, 2, openCalls)
}

// live is the tunnel self-heal path: a tunnel must keep working after its
// captured E2EE channel dies (worker/hub reconnect), re-resolving a fresh
// channel instead of rejecting every connection against the dead one.
func TestChannelPool_LiveReResolvesWhenClosed(t *testing.T) {
	p := newChannelPool(nil)
	defer p.reset()
	first, _ := newTestManagedChannel()
	second, _ := newTestManagedChannel()
	channels := []*managedChannel{first, second}
	openCalls := 0
	p.openCh = func(_ context.Context, _, _ string, _ *tunnelpkg.OpenChannelOptions) (*managedChannel, error) {
		ch := channels[openCalls]
		openCalls++
		return ch, nil
	}
	const hubURL, workerID = "http://hub", "worker"

	// Seed the cache with `first`.
	opened, err := p.getOrOpen(context.Background(), p.currentRevision(), hubURL, workerID)
	require.NoError(t, err)
	require.Same(t, first, opened)

	// While first is alive, live returns it without re-opening.
	live, err := p.live(context.Background(), first, hubURL, workerID)
	require.NoError(t, err)
	require.Same(t, first, live)
	assert.Equal(t, 1, openCalls)

	// Close first (evicts it from the cache); live must re-resolve.
	first.close()
	require.Eventually(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.channels) == 0
	}, time.Second, time.Millisecond)
	live, err = p.live(context.Background(), first, hubURL, workerID)
	require.NoError(t, err)
	require.Same(t, second, live)
	assert.Equal(t, 2, openCalls)

	// A nil captured channel (test injection / no captured channel) is returned
	// as-is so injected dial seams that ignore the channel keep working.
	live, err = p.live(context.Background(), nil, hubURL, workerID)
	require.NoError(t, err)
	assert.Nil(t, live)
}

// A caller holding a revision captured before reset must fail closed rather than
// open into the new epoch (or race an open started under a torn-down lifetime).
func TestChannelPool_StaleRevisionRejected(t *testing.T) {
	p := newChannelPool(nil)
	defer p.reset()
	stale := p.currentRevision()
	p.reset()
	_, err := p.getOrOpen(context.Background(), stale, "http://hub", "worker")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reset while opening channel")
}

// reset is the single invalidation epoch: bump revision, cancel the old lifetime
// (so every derived channel/open unwinds), close cached channels, and cancel
// in-flight opens so they cannot register into the new maps.
func TestChannelPool_ResetCancelsInflightAndInvalidatesLifetime(t *testing.T) {
	p := newChannelPool(nil)
	defer p.reset()

	started := make(chan struct{})
	openLifetime := make(chan context.Context, 1)
	var startOnce sync.Once
	p.openCh = func(
		ctx context.Context,
		_, _ string,
		opts *tunnelpkg.OpenChannelOptions,
	) (*managedChannel, error) {
		startOnce.Do(func() { close(started) })
		openLifetime <- opts.LifetimeContext
		<-ctx.Done()
		return nil, ctx.Err()
	}

	const hubURL, workerID = "http://hub", "worker"
	result := make(chan error, 1)
	go func() {
		_, err := p.getOrOpen(context.Background(), p.currentRevision(), hubURL, workerID)
		result <- err
	}()
	<-started
	lifetimeCtx := <-openLifetime
	oldRev := p.currentRevision()

	p.reset()

	require.Error(t, <-result)
	assert.Error(t, lifetimeCtx.Err(), "reset must cancel the old channel lifetime context")
	assert.NotEqual(t, oldRev, p.currentRevision())

	fresh, closed := newTestManagedChannel()
	p.openCh = func(_ context.Context, _, _ string, _ *tunnelpkg.OpenChannelOptions) (*managedChannel, error) {
		return fresh, nil
	}
	opened, err := p.getOrOpen(context.Background(), p.currentRevision(), hubURL, workerID)
	require.NoError(t, err)
	require.Same(t, fresh, opened)
	assert.False(t, closed.Load())
}

func TestChannelPool_ResetClosesCachedChannels(t *testing.T) {
	p := newChannelPool(nil)
	defer p.reset()
	cached, closed := newTestManagedChannel()
	var lifetime context.Context
	p.openCh = func(_ context.Context, _, _ string, opts *tunnelpkg.OpenChannelOptions) (*managedChannel, error) {
		lifetime = opts.LifetimeContext
		return cached, nil
	}

	_, err := p.getOrOpen(context.Background(), p.currentRevision(), "http://hub", "worker")
	require.NoError(t, err)
	require.NoError(t, lifetime.Err())

	p.reset()

	assert.True(t, closed.Load(), "reset must close every cached channel")
	assert.Error(t, lifetime.Err(), "reset must cancel the lifetime the channel was opened under")
	p.mu.Lock()
	defer p.mu.Unlock()
	assert.Empty(t, p.channels)
	assert.Empty(t, p.inflight)
}

func TestWrapChannel_NilReturnsNil(t *testing.T) {
	// A typed-nil *tunnelpkg.Channel must not become a non-nil managedChannel
	// whose handle is a typed-nil interface (Closed/Context would panic).
	assert.Nil(t, wrapChannel(nil))
}

// Dial seams that return (nil, nil) — e.g. wrapChannel(nil) — must fail closed
// rather than cache a nil managedChannel that panics on Closed/Context.
func TestChannelPool_NilOpenChannelIsError(t *testing.T) {
	p := newChannelPool(nil)
	defer p.reset()
	p.openCh = func(
		_ context.Context,
		_, _ string,
		_ *tunnelpkg.OpenChannelOptions,
	) (*managedChannel, error) {
		return wrapChannel(nil), nil
	}

	_, err := p.getOrOpen(context.Background(), p.currentRevision(), "http://hub", "worker")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open channel returned no channel")
}

// An open that completes successfully after reset must not publish into the new
// epoch: the revision fence after dial is distinct from cancelling the open ctx.
func TestChannelPool_SuccessfulOpenAfterResetIsRejected(t *testing.T) {
	p := newChannelPool(nil)
	defer p.reset()
	channel, closed := newTestManagedChannel()
	started := make(chan struct{})
	release := make(chan struct{})
	p.openCh = func(
		_ context.Context,
		_, _ string,
		_ *tunnelpkg.OpenChannelOptions,
	) (*managedChannel, error) {
		close(started)
		<-release
		// Ignore cancelled open ctx — late success after reset.
		return channel, nil
	}

	result := make(chan error, 1)
	go func() {
		_, err := p.getOrOpen(context.Background(), p.currentRevision(), "http://hub", "worker")
		result <- err
	}()
	<-started
	p.reset()
	close(release)

	err := <-result
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reset while opening channel")
	assert.True(t, closed.Load(), "late channel must be closed, not cached under the new epoch")
	p.mu.Lock()
	defer p.mu.Unlock()
	assert.Empty(t, p.channels)
}

func TestChannelPool_WaiterRejectsChannelClosedBeforeWake(t *testing.T) {
	p := newChannelPool(nil)
	defer p.reset()
	channel, closedFlag := newTestManagedChannel()
	started := make(chan struct{})
	release := make(chan struct{})
	p.openCh = func(
		ctx context.Context,
		_, _ string,
		_ *tunnelpkg.OpenChannelOptions,
	) (*managedChannel, error) {
		close(started)
		<-release
		return channel, nil
	}

	const hubURL, workerID = "http://hub", "worker"
	followerErr := make(chan error, 2)
	go func() {
		_, err := p.getOrOpen(context.Background(), p.currentRevision(), hubURL, workerID)
		followerErr <- err
	}()
	<-started
	// Second waiter joins the same inflight open.
	go func() {
		_, err := p.getOrOpen(context.Background(), p.currentRevision(), hubURL, workerID)
		followerErr <- err
	}()
	time.Sleep(20 * time.Millisecond)

	// Close the channel before the leader publishes so waiters wake to a dead handle.
	channel.close()
	assert.True(t, closedFlag.Load())
	close(release)

	err1 := <-followerErr
	err2 := <-followerErr
	require.Error(t, err1)
	require.Error(t, err2)
	assert.Contains(t, err1.Error(), "channel closed while opening")
	assert.Contains(t, err2.Error(), "channel closed while opening")
}
