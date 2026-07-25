package main

import (
	"context"
	"fmt"
	"sync"

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
	hubURL   string
	workerID string
	revision uint64
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
// revision, the channel lifetime context derived channels/opens bind to, and
// its cancel all move together. reset replaces the whole value so the three can
// never drift (e.g. bumping the revision without rotating the lifetime context,
// which would let a tunnel reuse a torn-down transport).
type channelEpoch struct {
	revision uint64
	ctx      context.Context
	cancel   context.CancelFunc
}

// channelPool caches and revalidates E2EE channels keyed by (hubURL, workerID,
// revision). Extracted from TunnelManager so the open/cache/evict/epoch state
// machine is unit-testable without a listener harness. See
// https://github.com/leapmux/leapmux/issues/283.
type channelPool struct {
	mu       sync.Mutex
	channels map[channelKey]*managedChannel
	inflight map[channelKey]*channelOpen
	chanOpts *tunnelpkg.OpenChannelOptions
	epoch    channelEpoch
	openCh   openChannelFunc
}

func newChannelPool(openCh openChannelFunc) *channelPool {
	channelCtx, cancelChannels := context.WithCancel(context.Background())
	return &channelPool{
		channels: make(map[channelKey]*managedChannel),
		inflight: make(map[channelKey]*channelOpen),
		epoch:    channelEpoch{revision: 1, ctx: channelCtx, cancel: cancelChannels},
		openCh:   openCh,
	}
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

// reset is the single invalidation epoch for cached E2EE channels: it bumps the
// revision, rotates channel lifetimeCtx (so every channel/open derived from the
// old lifetime unwinds), and drops the cached maps. Both a transport change
// (setOptions) and a full reset (CloseAll) route through here so one revision
// comparison fences every caller.
func (p *channelPool) reset() {
	p.mu.Lock()
	// Rotate the whole epoch atomically: cancel the old lifetime (so every
	// channel/open derived from it unwinds), then replace revision+ctx+cancel
	// together.
	p.epoch.cancel()
	nextCtx, nextCancel := context.WithCancel(context.Background())
	p.epoch = channelEpoch{revision: p.epoch.revision + 1, ctx: nextCtx, cancel: nextCancel}
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

func (p *channelPool) currentRevision() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.epoch.revision
}

// getOrOpen and runChannelOpen are single-flighted by {hubURL, workerID,
// revision}. This skeleton is structurally duplicated in
// crossworker.Client.channelFor/runChannelOpen. The divergences (epoch
// invalidation + eager eviction here, delegation ExpectedUserID pinning + lazy
// eviction there) are load-bearing; a shared generic opener was rejected — see
// https://github.com/leapmux/leapmux/issues/281.
func (p *channelPool) getOrOpen(
	operationCtx context.Context,
	revision uint64,
	hubURL, workerID string,
) (*managedChannel, error) {
	p.mu.Lock()
	if revision != p.epoch.revision {
		p.mu.Unlock()
		return nil, fmt.Errorf("tunnel manager reset while opening channel")
	}
	key := channelKey{
		hubURL:   hubURL,
		workerID: workerID,
		revision: p.epoch.revision,
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
	if err == nil && open.key.revision != p.epoch.revision {
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
func (p *channelPool) live(ctx context.Context, ch *managedChannel, hubURL, workerID string) (*managedChannel, error) {
	if ch == nil {
		return nil, nil
	}
	if !ch.closed() {
		return ch, nil
	}
	opened, err := p.getOrOpen(ctx, p.currentRevision(), hubURL, workerID)
	if err != nil {
		return nil, err
	}
	return opened, nil
}
