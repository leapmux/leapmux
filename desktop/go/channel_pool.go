package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	tunnelpkg "github.com/leapmux/leapmux/tunnel"
)

type openChannelFunc func(
	context.Context,
	string,
	string,
	*tunnelpkg.OpenChannelOptions,
) (*managedChannel, error)

// channelHandle is the slice of *tunnelpkg.Channel this pool drives a cached
// channel through: whether it is still usable, when its lifetime ends, and how to
// tear it down. *tunnelpkg.Channel satisfies it directly, with no adapter; tests
// substitute a fake, because opening a real channel needs a hub, a WebSocket, and a
// Noise handshake.
//
// It is ONE interface rather than a set of func fields copied off the channel
// because those copies are a second source of truth that cannot be wrong out loud:
// a managedChannel holding a `closed` answering for one channel and a `done`
// belonging to another is well-typed and silently reports a dead channel as live,
// and every construction site -- test doubles included -- has to remember all of
// them (one field was already defended with a nil check the other two never got).
// A single field cannot disagree with itself.
type channelHandle interface {
	// Closed reports whether the channel is no longer usable -- closed, or its
	// lifetime cancelled out from under it.
	Closed() bool
	// Close tears the channel down. It is idempotent.
	Close()
	// CloseDone closes after the transport has been released.
	CloseDone() <-chan struct{}
	// Context is the channel's lifetime. Its Done channel fires when the channel
	// dies, which is what evicts it from the cache.
	Context() context.Context
}

// managedChannel is one cached E2EE channel.
type managedChannel struct {
	// channel is the concrete channel the DEFAULT dial seam needs:
	// tunnelpkg.DialTunnelContext takes a *tunnelpkg.Channel and nothing narrower.
	// It is nil under test, where the fake handle is paired with an overridden
	// TunnelManager.dial that never looks at it.
	channel *tunnelpkg.Channel
	// handle is what the pool itself uses, and is always set.
	handle channelHandle
}

func (m *managedChannel) closed() bool          { return m.handle.Closed() }
func (m *managedChannel) close()                { m.handle.Close() }
func (m *managedChannel) done() <-chan struct{} { return m.handle.Context().Done() }

func (m *managedChannel) closeAndWait() {
	m.close()
	<-m.handle.CloseDone()
}

func wrapChannel(ch *tunnelpkg.Channel) *managedChannel {
	if ch == nil {
		// Never store a typed-nil *Channel in the channelHandle interface:
		// runChannelOpen's `ch == nil` check only sees the non-nil *managedChannel.
		return nil
	}
	return &managedChannel{channel: ch, handle: ch}
}

type channelKey struct {
	hubURL         string
	workerID       string
	expectedUserID string
	expectedKeys   string // the composite hex identifying the expected Worker key set
}

// channelExpectation is the Worker identity one admitted tunnel proves on
// every channel open: the user the Hub must authenticate the channel as, and
// the exact Worker key set the Hub must announce. Two opens share a cached
// channel only when their expectations are equal.
type channelExpectation struct {
	userID string
	pin    tunnelpkg.KeyPinStore
	// keysHex is the composite encoding NewExpectedWorkerKeyPin decodes. It is
	// the input for the per-open pin and, once normalized to lowercase, for
	// the cache identity. Empty means no expectation:
	// the pool is internal and a production create never admits one (see
	// TunnelConfig.validateAndNormalize).
	keysHex string
}

// buildPin derives the per-open KeyPin from the expectation. A nil pin for an
// empty expectation means the open trusts whatever the Hub announces -- the
// pre-expectation behaviour, reachable only from internal callers.
func (e channelExpectation) buildPin() (tunnelpkg.KeyPinStore, error) {
	if e.pin != nil {
		return e.pin, nil
	}
	if e.keysHex == "" {
		return nil, nil
	}
	return tunnelpkg.NewExpectedWorkerKeyPin(e.keysHex)
}

type channelOpen struct {
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	key     channelKey
	waiters int // guarded by channelPool.mu
	channel *managedChannel
	err     error
}

// channelEpoch owns the ID, lifetime, and unreleased resources of one epoch.
// Reset replaces it as a unit so a tunnel cannot reuse a retired transport.
type channelEpoch struct {
	// The opaque ID is unique across resets and sidecar processes. ListTunnels
	// reports it, the reuse cache includes it, and each tunnel retains it.
	id     string
	ctx    context.Context
	cancel context.CancelFunc

	// An evicted channel and an open whose last waiter left still own
	// resources. Only the current epoch can add or remove owned channels.
	channels map[*managedChannel]struct{}
	cache    map[channelKey]*managedChannel
	inflight map[channelKey]*channelOpen
	chanOpts *tunnelpkg.OpenChannelOptions
	opening  sync.WaitGroup
}

// channelPool caches and revalidates E2EE channels keyed by (hubURL, workerID,
// expected user, expected keys, epoch). Extracted from TunnelManager so the
// open/cache/evict/epoch state machine is unit-testable without a listener
// harness. See https://github.com/leapmux/leapmux/issues/283.
type channelPool struct {
	mu     sync.Mutex
	epoch  *channelEpoch
	openCh openChannelFunc
}

func newChannelPool(openCh openChannelFunc) *channelPool {
	return &channelPool{
		epoch:  newChannelEpoch(nil),
		openCh: openCh,
	}
}

func newChannelEpoch(opts *tunnelpkg.OpenChannelOptions) *channelEpoch {
	ctx, cancel := context.WithCancel(context.Background())
	var copiedOpts *tunnelpkg.OpenChannelOptions
	if opts != nil {
		copied := *opts
		copiedOpts = &copied
	}
	return &channelEpoch{
		id:       uuid.New().String(),
		ctx:      ctx,
		cancel:   cancel,
		channels: make(map[*managedChannel]struct{}),
		cache:    make(map[channelKey]*managedChannel),
		inflight: make(map[channelKey]*channelOpen),
		chanOpts: copiedOpts,
	}
}

// close waits for the retired epoch's transports and any unfinished opens,
// including their rollback. The caller holds neither pool nor manager lock.
func (epoch *channelEpoch) close() {
	var closing sync.WaitGroup
	for ch := range epoch.channels {
		closing.Go(ch.closeAndWait)
	}
	epoch.opening.Wait()
	closing.Wait()
}

// setOptions changes the transport and returns the detached resources for
// cleanup after releasing the manager lock.
func (p *channelPool) setOptions(opts *tunnelpkg.OpenChannelOptions) *channelEpoch {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rotateLocked(opts)
}

func (p *channelPool) rotate() *channelEpoch {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rotateLocked(p.epoch.chanOpts)
}

func (p *channelPool) rotateLocked(opts *tunnelpkg.OpenChannelOptions) *channelEpoch {
	// Cancel the retiring lifetime and detach every resource it still owns.
	retired := p.epoch
	retired.cancel()
	p.epoch = newChannelEpoch(opts)
	return retired
}

// currentEpoch returns the pool's current invalidation epoch id. Tests and
// CreateTunnel capture it before awaiting an open so a concurrent reset can
// fence registration, and a tunnel retains it as the epoch it was admitted
// under.
func (p *channelPool) currentEpoch() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.epoch.id
}

// getOrOpen and runChannelOpen are single-flighted by {hubURL, workerID,
// expected user, expected keys, epoch}. This skeleton is structurally
// duplicated in crossworker.Client.channelFor/runChannelOpen. The divergences
// (epoch invalidation + eager eviction here, delegation ExpectedUserID pinning
// + lazy eviction there) are load-bearing; a shared generic opener was
// rejected — see https://github.com/leapmux/leapmux/issues/281.
func (p *channelPool) getOrOpen(
	operationCtx context.Context,
	epoch string,
	hubURL, workerID string,
	expect channelExpectation,
) (*managedChannel, error) {
	// The pin is derived from the expectation; build it before the lock so a
	// malformed expectation fails without occupying an in-flight slot.
	pin, err := expect.buildPin()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if err := operationCtx.Err(); err != nil {
		p.mu.Unlock()
		return nil, err
	}
	activeEpoch := p.epoch
	if epoch != activeEpoch.id {
		p.mu.Unlock()
		return nil, fmt.Errorf("tunnel manager reset while opening channel")
	}
	key := channelKey{
		hubURL:         hubURL,
		workerID:       workerID,
		expectedUserID: expect.userID,
		expectedKeys:   strings.ToLower(expect.keysHex),
	}
	if ch := activeEpoch.cache[key]; ch != nil {
		if !ch.closed() {
			p.mu.Unlock()
			return ch, nil
		}
		delete(activeEpoch.cache, key)
	}
	open := activeEpoch.inflight[key]
	if open == nil {
		openCtx, cancelOpen := context.WithCancel(activeEpoch.ctx)
		open = &channelOpen{
			ctx:    openCtx,
			cancel: cancelOpen,
			done:   make(chan struct{}),
			key:    key,
		}
		activeEpoch.inflight[key] = open
		activeEpoch.opening.Add(1)
		opts := tunnelpkg.OpenChannelOptions{}
		if activeEpoch.chanOpts != nil {
			opts = *activeEpoch.chanOpts
		}
		opts.LifetimeContext = activeEpoch.ctx
		// Per-open identity: the channel must authenticate as the expected
		// user and the Hub must announce exactly the expected Worker keys
		// (https://github.com/leapmux/leapmux/issues/660). An empty
		// expectation (internal callers) opens with neither, which is the
		// pre-expectation behaviour.
		opts.ExpectedUserID = expect.userID
		opts.KeyPin = pin
		go p.runChannelOpen(open, activeEpoch, hubURL, workerID, opts)
	}
	open.waiters++
	p.mu.Unlock()

	select {
	case <-operationCtx.Done():
		p.mu.Lock()
		open.waiters--
		if open.waiters == 0 && activeEpoch.inflight[key] == open {
			// A later caller can start again, while reset keeps owning
			// this open until the transport and rollback finish.
			delete(activeEpoch.inflight, key)
			open.cancel()
		}
		p.mu.Unlock()
		return nil, operationCtx.Err()
	case <-open.done:
		if err := operationCtx.Err(); err != nil {
			return nil, err
		}
		if open.err != nil {
			return nil, open.err
		}
		// Channel may have died (or reset closed it) between publish and this
		// waiter waking — same Closed() gate the cache-hit path applies.
		if open.channel == nil || open.channel.closed() {
			return nil, fmt.Errorf("channel closed while opening")
		}
		return open.channel, nil
	}
}

func (p *channelPool) runChannelOpen(
	open *channelOpen,
	epoch *channelEpoch,
	hubURL, workerID string,
	opts tunnelpkg.OpenChannelOptions,
) {
	// The timeout limits the handshake and dial. The epoch context in opts
	// still owns the lifetime of a published channel.
	openCtx, cancelOpen := context.WithTimeout(open.ctx, channelOpenTimeout)
	ch, err := p.openCh(openCtx, hubURL, workerID, &opts)
	if err == nil && ch == nil {
		err = fmt.Errorf("open channel returned no channel")
	}

	p.mu.Lock()
	if err == nil {
		switch {
		case epoch != p.epoch:
			err = fmt.Errorf("tunnel manager reset while opening channel")
		case epoch.inflight[open.key] != open:
			err = context.Canceled
		default:
			err = openCtx.Err()
		}
	}
	if epoch.inflight[open.key] == open {
		delete(epoch.inflight, open.key)
	}
	if err == nil {
		epoch.cache[open.key] = ch
		epoch.channels[ch] = struct{}{}
		open.channel = ch
	}
	p.mu.Unlock()
	cancelOpen()
	open.cancel()

	if err != nil && ch != nil {
		ch.closeAndWait()
	}
	open.err = err
	close(open.done)
	// A successful open transfers ownership to channels under the lock.
	// A failed open holds its reservation until close and rollback finish.
	epoch.opening.Done()
	if err == nil {
		p.watchChannelClose(epoch, open.key, ch)
	}
}

func (p *channelPool) watchChannelClose(epoch *channelEpoch, key channelKey, ch *managedChannel) {
	<-ch.done()
	p.mu.Lock()
	if epoch.cache[key] == ch {
		delete(epoch.cache, key)
	}
	p.mu.Unlock()

	<-ch.handle.CloseDone()
	p.mu.Lock()
	if epoch == p.epoch {
		delete(epoch.channels, ch)
	}
	p.mu.Unlock()
}

// live returns a live E2EE channel for a tunnel to dial through. It returns the
// captured channel unchanged while it is still alive, and re-resolves (opening a
// fresh one via getOrOpen) when that channel has died -- so a tunnel survives a
// worker/hub reconnect instead of becoming a zombie that rejects every connection
// after its captured channel closed. Cache eviction does not notify the tunnel.
// The test dial seam can supply a nil channel, which live returns unchanged.
//
// The admitted epoch is compared even on a cache hit, and is what gets passed
// into getOrOpen: a tunnel is admitted under one epoch and never rebased onto a
// later one, so a reset (an identity change) retires its re-resolves instead of
// handing it a channel opened under different transport options or trust.
func (p *channelPool) live(ctx context.Context, ch *managedChannel, admittedEpoch string, hubURL, workerID string, expect channelExpectation) (*managedChannel, error) {
	if ch == nil {
		return nil, nil
	}
	if admittedEpoch != p.currentEpoch() {
		return nil, fmt.Errorf("tunnel manager reset while the tunnel was open")
	}
	if !ch.closed() {
		return ch, nil
	}
	opened, err := p.getOrOpen(ctx, admittedEpoch, hubURL, workerID, expect)
	if err != nil {
		return nil, err
	}
	return opened, nil
}
