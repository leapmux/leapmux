package main

import (
	"context"
	"fmt"
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
	epoch          string
}

// channelExpectation is the Worker identity one admitted tunnel proves on
// every channel open: the user the Hub must authenticate the channel as, and
// the exact Worker key set the Hub must announce. Two opens share a cached
// channel only when their expectations are equal.
type channelExpectation struct {
	userID string
	// keysHex is the composite encoding NewExpectedWorkerKeyPin decodes. It is
	// both the cache identity of the key set and the input the per-open pin is
	// derived from, so the two can never disagree. Empty means no expectation:
	// the pool is internal and a production create never admits one (see
	// TunnelConfig.validateAndNormalize).
	keysHex string
}

// buildPin derives the per-open KeyPin from the expectation. A nil pin for an
// empty expectation means the open trusts whatever the Hub announces -- the
// pre-expectation behaviour, reachable only from internal callers.
func (e channelExpectation) buildPin() (tunnelpkg.KeyPinStore, error) {
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
	channel *managedChannel
	err     error
}

// channelEpoch is the single invalidation epoch for cached E2EE channels: the
// opaque id, the channel lifetime context derived channels/opens bind to, and
// its cancel all move together. reset replaces the whole value so the three can
// never drift (e.g. rotating the id without rotating the lifetime context,
// which would let a tunnel reuse a torn-down transport).
type channelEpoch struct {
	// id is a nonempty opaque identifier, regenerated at construction and at
	// every reset (so it is unique across sidecar process replacement). It is
	// the single invalidation authority: the cache revision, the channel_epoch
	// ListTunnels reports, and the admitted epoch a tunnel retains and
	// re-compares in live.
	id     string
	ctx    context.Context
	cancel context.CancelFunc
}

// channelPool caches and revalidates E2EE channels keyed by (hubURL, workerID,
// expected user, expected keys, epoch). Extracted from TunnelManager so the
// open/cache/evict/epoch state machine is unit-testable without a listener
// harness. See https://github.com/leapmux/leapmux/issues/283.
type channelPool struct {
	mu       sync.Mutex
	channels map[channelKey]*managedChannel
	inflight map[channelKey]*channelOpen
	chanOpts *tunnelpkg.OpenChannelOptions
	epoch    channelEpoch
	openCh   openChannelFunc
}

func newChannelPool(openCh openChannelFunc) *channelPool {
	return &channelPool{
		channels: make(map[channelKey]*managedChannel),
		inflight: make(map[channelKey]*channelOpen),
		epoch:    newChannelEpoch(),
		openCh:   openCh,
	}
}

func newChannelEpoch() channelEpoch {
	ctx, cancel := context.WithCancel(context.Background())
	return channelEpoch{id: uuid.New().String(), ctx: ctx, cancel: cancel}
}

// setOptions sets transport options for opening E2EE channels and resets the
// pool: an options change is a connection-identity change.
func (p *channelPool) setOptions(opts *tunnelpkg.OpenChannelOptions) {
	p.mu.Lock()
	if opts == nil {
		p.chanOpts = nil
	} else {
		copied := *opts
		p.chanOpts = &copied
	}
	p.mu.Unlock()
	p.reset()
}

// reset is the single invalidation epoch for cached E2EE channels: it rotates
// the epoch (a fresh opaque id, a fresh channel lifetime context), and drops
// the cached maps. Both a transport change (setOptions) and a full reset
// (CloseAll) route through here so one epoch comparison fences every caller.
func (p *channelPool) reset() {
	p.mu.Lock()
	// Rotate the whole epoch atomically: cancel the old lifetime (so every
	// channel/open derived from it unwinds), then replace id+ctx+cancel
	// together.
	p.epoch.cancel()
	p.epoch = newChannelEpoch()
	channels := p.channels
	p.channels = make(map[channelKey]*managedChannel)
	inflight := p.inflight
	p.inflight = make(map[channelKey]*channelOpen)
	p.mu.Unlock()

	for _, open := range inflight {
		open.cancel()
	}
	for _, ch := range channels {
		ch.close()
	}
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
	if epoch != p.epoch.id {
		p.mu.Unlock()
		return nil, fmt.Errorf("tunnel manager reset while opening channel")
	}
	key := channelKey{
		hubURL:         hubURL,
		workerID:       workerID,
		expectedUserID: expect.userID,
		expectedKeys:   expect.keysHex,
		epoch:          p.epoch.id,
	}
	if ch := p.channels[key]; ch != nil {
		if !ch.closed() {
			p.mu.Unlock()
			return ch, nil
		}
		delete(p.channels, key)
	}
	open := p.inflight[key]
	if open == nil {
		openCtx, cancelOpen := context.WithCancel(p.epoch.ctx)
		open = &channelOpen{
			ctx:    openCtx,
			cancel: cancelOpen,
			done:   make(chan struct{}),
			key:    key,
		}
		p.inflight[key] = open
		opts := tunnelpkg.OpenChannelOptions{LifetimeContext: p.epoch.ctx}
		if p.chanOpts != nil {
			opts = *p.chanOpts
			opts.LifetimeContext = p.epoch.ctx
		}
		// Per-open identity: the channel must authenticate as the expected
		// user and the Hub must announce exactly the expected Worker keys
		// (https://github.com/leapmux/leapmux/issues/660). An empty
		// expectation (internal callers) opens with neither, which is the
		// pre-expectation behaviour.
		opts.ExpectedUserID = expect.userID
		opts.KeyPin = pin
		go p.runChannelOpen(open, hubURL, workerID, opts)
	}
	p.mu.Unlock()

	select {
	case <-operationCtx.Done():
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
	hubURL, workerID string,
	opts tunnelpkg.OpenChannelOptions,
) {
	// Bound the handshake/dial so a stalled hub cannot wedge the open forever.
	// opts.LifetimeContext (the epoch context) still owns the opened channel, so
	// this deadline only fences the open itself, not the channel's lifetime.
	openCtx, cancelOpen := context.WithTimeout(open.ctx, channelOpenTimeout)
	ch, err := p.openCh(openCtx, hubURL, workerID, &opts)
	cancelOpen()
	open.cancel()
	if err == nil && ch == nil {
		err = fmt.Errorf("open channel returned no channel")
	}

	p.mu.Lock()
	if err == nil && open.key.epoch != p.epoch.id {
		err = fmt.Errorf("tunnel manager reset while opening channel")
	}
	if current := p.inflight[open.key]; current == open {
		delete(p.inflight, open.key)
	}
	if err == nil {
		p.channels[open.key] = ch
		open.channel = ch
	}
	open.err = err
	close(open.done)
	p.mu.Unlock()

	if err != nil {
		if ch != nil {
			ch.close()
		}
		return
	}
	// Evict the channel from the cache when its lifetime ends. Unconditional: done()
	// is derived from the handle, so unlike the func-field copy it replaced there is
	// no nil channel here to receive from forever.
	go p.removeClosedChannel(open.key, ch)
}

func (p *channelPool) removeClosedChannel(key channelKey, ch *managedChannel) {
	<-ch.done()
	p.mu.Lock()
	if p.channels[key] == ch {
		delete(p.channels, key)
	}
	p.mu.Unlock()
}

// live returns a live E2EE channel for a tunnel to dial through. It returns the
// captured channel unchanged while it is still alive, and re-resolves (opening a
// fresh one via getOrOpen) when that channel has died -- so a tunnel survives a
// worker/hub reconnect instead of becoming a zombie that rejects every connection
// after its captured channel closed (removeClosedChannel only evicts the cache for
// future opens; it never notified the running tunnel). A nil ch (test injection /
// no captured channel) is returned as-is so injected dial seams that ignore the
// channel keep working.
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
