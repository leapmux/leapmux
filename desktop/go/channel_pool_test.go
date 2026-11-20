package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	tunnelpkg "github.com/leapmux/leapmux/tunnel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
)

func TestChannelPool_CanceledLeaderDoesNotCancelChannelForFollower(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newChannelPool(nil)
		defer func() { p.rotate().close() }()
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
			_, err := p.getOrOpen(leaderCtx, p.currentEpoch(), hubURL, workerID, channelExpectation{})
			leaderResult <- err
		}()
		<-started
		lifetimeCtx := <-openLifetime

		followerResult := make(chan error, 1)
		go func() {
			got, err := p.getOrOpen(context.Background(), p.currentEpoch(), hubURL, workerID, channelExpectation{})
			if err == nil {
				assert.Same(t, channel, got)
			}
			followerResult <- err
		}()
		synctest.Wait()
		cancelLeader()

		require.ErrorIs(t, <-leaderResult, context.Canceled)
		assert.NoError(t, lifetimeCtx.Err(), "cached channel lifetime inherited the canceled leader")
		close(release)
		require.NoError(t, <-followerResult)
	})
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
	defer func() { p.rotate().close() }()

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
	_, err := p.getOrOpen(context.Background(), p.currentEpoch(), "http://hub", "worker", channelExpectation{})
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
	defer func() { p.rotate().close() }()
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

	first, err := p.getOrOpen(context.Background(), p.currentEpoch(), "http://first-hub", "worker", channelExpectation{})
	require.NoError(t, err)
	second, err := p.getOrOpen(context.Background(), p.currentEpoch(), "http://second-hub", "worker", channelExpectation{})
	require.NoError(t, err)

	assert.Same(t, channels[0], first)
	assert.Same(t, channels[1], second)

	p.setOptions(&tunnelpkg.OpenChannelOptions{BearerToken: "new-options"}).close()
	third, err := p.getOrOpen(context.Background(), p.currentEpoch(), "http://second-hub", "worker", channelExpectation{})
	require.NoError(t, err)
	assert.Same(t, channels[2], third)
	assert.Equal(t, 3, openCalls)
}

func TestChannelPool_RemovesClosedCachedChannel(t *testing.T) {
	p := newChannelPool(nil)
	defer func() { p.rotate().close() }()
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

	opened, err := p.getOrOpen(context.Background(), p.currentEpoch(), hubURL, workerID, channelExpectation{})
	require.NoError(t, err)
	opened.close()
	require.Eventually(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.epoch.cache) == 0
	}, time.Second, time.Millisecond)

	reopened, err := p.getOrOpen(context.Background(), p.currentEpoch(), hubURL, workerID, channelExpectation{})
	require.NoError(t, err)
	assert.Same(t, second, reopened)
	assert.Equal(t, 2, openCalls)
}

// A channel whose LIFETIME was cancelled without an explicit Close is dead too, and
// the pool must treat it as such.
func TestChannelPool_LiveReResolvesWhenLifetimeCancelled(t *testing.T) {
	p := newChannelPool(nil)
	defer func() { p.rotate().close() }()

	lifetimeCtx, cancelLifetime := context.WithCancel(context.Background())
	dying := &managedChannel{transport: &fakeChannel{ctx: lifetimeCtx, cancel: cancelLifetime, closeDone: make(chan struct{})}}
	fresh, _ := newTestManagedChannel()
	channels := []*managedChannel{dying, fresh}
	openCalls := 0
	p.openCh = func(_ context.Context, _, _ string, _ *tunnelpkg.OpenChannelOptions) (*managedChannel, error) {
		ch := channels[openCalls]
		openCalls++
		return ch, nil
	}
	const hubURL, workerID = "http://hub", "worker"

	opened, err := p.getOrOpen(context.Background(), p.currentEpoch(), hubURL, workerID, channelExpectation{})
	require.NoError(t, err)
	require.Same(t, dying, opened)

	// The transport dies: the lifetime ends, but nothing calls Close.
	cancelLifetime()
	require.Eventually(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.epoch.cache) == 0
	}, time.Second, time.Millisecond, "a channel whose lifetime ended must be evicted from the cache")

	live, err := p.live(context.Background(), dying, p.currentEpoch(), hubURL, workerID, channelExpectation{})
	require.NoError(t, err)
	assert.Same(t, fresh, live, "a tunnel holding a lifetime-cancelled channel must re-resolve a live one")
	assert.Equal(t, 2, openCalls)
}

// live is the tunnel self-heal path: a tunnel must keep working after its
// captured E2EE channel dies (worker/hub reconnect), re-resolving a fresh
// channel instead of rejecting every connection against the dead one.
func TestChannelPool_LiveReResolvesWhenClosed(t *testing.T) {
	p := newChannelPool(nil)
	defer func() { p.rotate().close() }()
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
	opened, err := p.getOrOpen(context.Background(), p.currentEpoch(), hubURL, workerID, channelExpectation{})
	require.NoError(t, err)
	require.Same(t, first, opened)

	// While first is alive, live returns it without re-opening.
	live, err := p.live(context.Background(), first, p.currentEpoch(), hubURL, workerID, channelExpectation{})
	require.NoError(t, err)
	require.Same(t, first, live)
	assert.Equal(t, 1, openCalls)

	// Close first (evicts it from the cache); live must re-resolve.
	first.close()
	require.Eventually(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.epoch.cache) == 0
	}, time.Second, time.Millisecond)
	live, err = p.live(context.Background(), first, p.currentEpoch(), hubURL, workerID, channelExpectation{})
	require.NoError(t, err)
	require.Same(t, second, live)
	assert.Equal(t, 2, openCalls)

	// A nil captured channel (test injection / no captured channel) is returned
	// as-is so injected dial seams that ignore the channel keep working.
	live, err = p.live(context.Background(), nil, p.currentEpoch(), hubURL, workerID, channelExpectation{})
	require.NoError(t, err)
	assert.Nil(t, live)
}

// A caller holding a revision captured before reset must fail closed rather than
// open into the new epoch (or race an open started under a torn-down lifetime).
func TestChannelPool_StaleEpochRejected(t *testing.T) {
	p := newChannelPool(nil)
	defer func() { p.rotate().close() }()
	stale := p.currentEpoch()
	p.rotate().close()
	_, err := p.getOrOpen(context.Background(), stale, "http://hub", "worker", channelExpectation{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reset while opening channel")
}

// reset is the single invalidation epoch: bump revision, cancel the old lifetime
// (so every derived channel/open unwinds), close cached channels, and cancel
// in-flight opens so they cannot register into the new maps.
func TestChannelPool_ResetCancelsInflightAndInvalidatesLifetime(t *testing.T) {
	p := newChannelPool(nil)
	defer func() { p.rotate().close() }()

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
		_, err := p.getOrOpen(context.Background(), p.currentEpoch(), hubURL, workerID, channelExpectation{})
		result <- err
	}()
	<-started
	lifetimeCtx := <-openLifetime
	oldRev := p.currentEpoch()

	p.rotate().close()

	require.Error(t, <-result)
	assert.Error(t, lifetimeCtx.Err(), "reset must cancel the old channel lifetime context")
	assert.NotEqual(t, oldRev, p.currentEpoch())

	fresh, closed := newTestManagedChannel()
	p.openCh = func(_ context.Context, _, _ string, _ *tunnelpkg.OpenChannelOptions) (*managedChannel, error) {
		return fresh, nil
	}
	opened, err := p.getOrOpen(context.Background(), p.currentEpoch(), hubURL, workerID, channelExpectation{})
	require.NoError(t, err)
	require.Same(t, fresh, opened)
	assert.False(t, closed.Load())
}

func TestChannelPool_ResetClosesCachedChannels(t *testing.T) {
	p := newChannelPool(nil)
	defer func() { p.rotate().close() }()
	cached, closed := newTestManagedChannel()
	var lifetime context.Context
	p.openCh = func(_ context.Context, _, _ string, opts *tunnelpkg.OpenChannelOptions) (*managedChannel, error) {
		lifetime = opts.LifetimeContext
		return cached, nil
	}

	_, err := p.getOrOpen(context.Background(), p.currentEpoch(), "http://hub", "worker", channelExpectation{})
	require.NoError(t, err)
	require.NoError(t, lifetime.Err())

	p.rotate().close()

	assert.True(t, closed.Load(), "reset must close every cached channel")
	assert.Error(t, lifetime.Err(), "reset must cancel the lifetime the channel was opened under")
	p.mu.Lock()
	defer p.mu.Unlock()
	assert.Empty(t, p.epoch.cache)
	assert.Empty(t, p.epoch.inflight)
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
	defer func() { p.rotate().close() }()
	p.openCh = func(
		_ context.Context,
		_, _ string,
		_ *tunnelpkg.OpenChannelOptions,
	) (*managedChannel, error) {
		return wrapChannel(nil), nil
	}

	_, err := p.getOrOpen(context.Background(), p.currentEpoch(), "http://hub", "worker", channelExpectation{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open channel returned no channel")
}

// An open that completes successfully after reset must not publish into the new
// epoch: the revision fence after dial is distinct from cancelling the open ctx.
func TestChannelPool_SuccessfulOpenAfterResetIsRejected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newChannelPool(nil)
		defer func() { p.rotate().close() }()
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
			_, err := p.getOrOpen(context.Background(), p.currentEpoch(), "http://hub", "worker", channelExpectation{})
			result <- err
		}()
		<-started
		reset := make(chan struct{})
		go func() {
			defer close(reset)
			p.rotate().close()
		}()
		assertTunnelCleanupPending(t, reset, "reset must join the old open")
		close(release)
		<-reset

		err := <-result
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reset while opening channel")
		assert.True(t, closed.Load(), "late channel must be closed, not cached under the new epoch")
		p.mu.Lock()
		defer p.mu.Unlock()
		assert.Empty(t, p.epoch.cache)
	})
}

func TestChannelPool_WaiterRejectsChannelClosedBeforeWake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newChannelPool(nil)
		defer func() { p.rotate().close() }()
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
			_, err := p.getOrOpen(context.Background(), p.currentEpoch(), hubURL, workerID, channelExpectation{})
			followerErr <- err
		}()
		<-started
		// Second waiter joins the same inflight open.
		go func() {
			_, err := p.getOrOpen(context.Background(), p.currentEpoch(), hubURL, workerID, channelExpectation{})
			followerErr <- err
		}()
		synctest.Wait()

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
	})
}

// Two opens share a cached channel only when their expectations are equal: a
// channel admitted for one (user, key set) pair must never serve another.
func TestChannelPool_KeyedByExpectation(t *testing.T) {
	p := newChannelPool(nil)
	defer func() { p.rotate().close() }()
	openCalls := 0
	p.openCh = func(_ context.Context, _, _ string, _ *tunnelpkg.OpenChannelOptions) (*managedChannel, error) {
		openCalls++
		ch, _ := newTestManagedChannel()
		return ch, nil
	}

	const hubURL, workerID = "http://hub", "worker"
	expectA := channelExpectation{userID: "user-a", keysHex: testExpectedWorkerKeysHex}

	first, err := p.getOrOpen(context.Background(), p.currentEpoch(), hubURL, workerID, expectA)
	require.NoError(t, err)
	shared, err := p.getOrOpen(context.Background(), p.currentEpoch(), hubURL, workerID,
		channelExpectation{userID: "user-a", keysHex: strings.ToUpper(testExpectedWorkerKeysHex)})
	require.NoError(t, err)
	assert.Same(t, first, shared, "identical key bytes with different hex case share one channel")
	assert.Equal(t, 1, openCalls)

	otherUser, err := p.getOrOpen(context.Background(), p.currentEpoch(), hubURL, workerID,
		channelExpectation{userID: "user-b", keysHex: testExpectedWorkerKeysHex})
	require.NoError(t, err)
	assert.NotSame(t, first, otherUser, "a different expected user is a different identity")
	assert.Equal(t, 2, openCalls)

	otherKeys, err := p.getOrOpen(context.Background(), p.currentEpoch(), hubURL, workerID,
		channelExpectation{userID: "user-a", keysHex: testOtherWorkerKeysHex})
	require.NoError(t, err)
	assert.NotSame(t, first, otherKeys, "a different expected key set is a different identity")
	assert.Equal(t, 3, openCalls)
}

func TestChannelPool_SharesOpeningAcrossHexCase(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newChannelPool(nil)
		defer func() { p.rotate().close() }()
		release := make(chan struct{})
		var opens atomic.Int32
		p.openCh = func(_ context.Context, _, _ string, _ *tunnelpkg.OpenChannelOptions) (*managedChannel, error) {
			opens.Add(1)
			<-release
			ch, _ := newTestManagedChannel()
			return ch, nil
		}
		get := func(keysHex string, result chan<- *managedChannel) {
			ch, err := p.getOrOpen(context.Background(), p.currentEpoch(), "http://hub", "worker",
				channelExpectation{userID: "user-a", keysHex: keysHex})
			assert.NoError(t, err)
			result <- ch
		}
		first, second := make(chan *managedChannel, 1), make(chan *managedChannel, 1)
		go get(testExpectedWorkerKeysHex, first)
		synctest.Wait()
		go get(strings.ToUpper(testExpectedWorkerKeysHex), second)
		synctest.Wait()
		assert.EqualValues(t, 1, opens.Load(), "identical key bytes must join the same open")
		close(release)
		assert.Same(t, <-first, <-second)
	})
}

// The per-open identity rides on the open options: ExpectedUserID and the
// pin derived from the expectation, so OpenChannel cross-checks the user and
// verifies the announced keys against exactly the expected set.
func TestChannelPool_PassesExpectationOnEveryOpen(t *testing.T) {
	p := newChannelPool(nil)
	defer func() { p.rotate().close() }()
	var got *tunnelpkg.OpenChannelOptions
	p.openCh = func(_ context.Context, _, _ string, opts *tunnelpkg.OpenChannelOptions) (*managedChannel, error) {
		got = opts
		ch, _ := newTestManagedChannel()
		return ch, nil
	}

	_, err := p.getOrOpen(context.Background(), p.currentEpoch(), "http://hub", "worker",
		channelExpectation{userID: "user-a", keysHex: testExpectedWorkerKeysHex})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "user-a", got.ExpectedUserID)
	require.NotNil(t, got.KeyPin, "a keyed expectation must pin the announced keys")

	// The pin accepts the expected set and refuses another.
	commit, err := got.KeyPin.Resolve(context.Background(), "worker",
		make([]byte, contracts.X25519PublicKeySize),
		make([]byte, contracts.MlkemPublicKeySize),
		make([]byte, contracts.SlhdsaPublicKeySize))
	require.Error(t, err, "a pin built from the expectation refuses other keys")
	assert.Nil(t, commit)
}

// A malformed expectation fails the open outright, before any channel work.
func TestChannelPool_RefusesInvalidExpectation(t *testing.T) {
	p := newChannelPool(nil)
	defer func() { p.rotate().close() }()
	var openCalls atomic.Int32
	p.openCh = func(_ context.Context, _, _ string, _ *tunnelpkg.OpenChannelOptions) (*managedChannel, error) {
		openCalls.Add(1)
		return nil, errors.New("unexpected open")
	}

	_, err := p.getOrOpen(context.Background(), p.currentEpoch(), "http://hub", "worker",
		channelExpectation{userID: "user-a", keysHex: "zz"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode the expected worker keys")
	assert.Zero(t, openCalls.Load(), "an invalid expectation must fail before the open starts")
}

// live compares the admitted epoch even on a cache HIT: a tunnel admitted
// under one epoch is never rebased onto a later one, so a reset (an identity
// change) retires its re-resolves instead of handing it a fresh channel.
func TestChannelPool_LiveRefusesRebaseOntoNewEpoch(t *testing.T) {
	p := newChannelPool(nil)
	defer func() { p.rotate().close() }()
	channel, _ := newTestManagedChannel()
	p.openCh = func(_ context.Context, _, _ string, _ *tunnelpkg.OpenChannelOptions) (*managedChannel, error) {
		ch, _ := newTestManagedChannel()
		return ch, nil
	}

	const hubURL, workerID = "http://hub", "worker"
	expect := channelExpectation{userID: "user-a", keysHex: testExpectedWorkerKeysHex}
	opened, err := p.getOrOpen(context.Background(), p.currentEpoch(), hubURL, workerID, expect)
	require.NoError(t, err)
	admitted := p.currentEpoch()

	p.rotate().close()

	// The captured channel is still open from the test's point of view (reset
	// closed the cache's handle, so mirror a still-open capture with a fresh
	// fake the tunnel holds): the epoch check must refuse it regardless.
	live, err := p.live(context.Background(), channel, admitted, hubURL, workerID, expect)
	require.Error(t, err, "a cache hit under a retired epoch must not be returned")
	assert.Nil(t, live)
	assert.Contains(t, err.Error(), "reset")

	// Even a closed channel must not re-resolve under the new epoch.
	live, err = p.live(context.Background(), opened, admitted, hubURL, workerID, expect)
	require.Error(t, err)
	assert.Nil(t, live)
	assert.Contains(t, err.Error(), "reset")
}

func TestChannelPool_ResetJoinsAnOpenAfterItsLastWaiterLeaves(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newChannelPool(nil)
		late, _ := newTestManagedChannel()
		fresh, _ := newTestManagedChannel()
		started := make(chan context.Context, 1)
		release := make(chan struct{})
		releaseOpen := sync.OnceFunc(func() { close(release) })
		defer releaseOpen()
		opens := 0
		p.openCh = func(ctx context.Context, _, _ string, _ *tunnelpkg.OpenChannelOptions) (*managedChannel, error) {
			opens++
			if opens == 1 {
				started <- ctx
				<-release
				return late, nil
			}
			return fresh, nil
		}
		epoch := p.currentEpoch()
		expect := channelExpectation{}
		firstCtx, cancelFirst := context.WithCancel(t.Context())
		firstResult := make(chan error, 1)
		go func() {
			_, err := p.getOrOpen(firstCtx, epoch, "http://hub", "worker", expect)
			firstResult <- err
		}()
		openCtx := <-started
		cancelFirst()
		require.ErrorIs(t, <-firstResult, context.Canceled)
		assert.ErrorIs(t, openCtx.Err(), context.Canceled, "the last waiter must cancel its open")

		replacement := make(chan struct{})
		var opened *managedChannel
		var openErr error
		go func() {
			defer close(replacement)
			opened, openErr = p.getOrOpen(t.Context(), epoch, "http://hub", "worker", expect)
		}()
		synctest.Wait()
		select {
		case <-replacement:
			assert.NoError(t, openErr)
			assert.Same(t, fresh, opened)
		default:
			t.Error("a new waiter joined an abandoned open")
		}

		reset := make(chan struct{})
		go func() {
			defer close(reset)
			p.rotate().close()
		}()
		assertTunnelCleanupPending(t, reset, "reset lost ownership of an abandoned open")
		releaseOpen()
		<-reset
		<-replacement
		assert.True(t, late.closed(), "reset must wait for the late transport to close")
	})
}
