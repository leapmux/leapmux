package cmd

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/streamevents"
)

// screenFeedItem is one output of a screen feed. Exactly one field is active:
//   - event: a live or replayed terminal event in stream order
//   - rejected: a durable WatchRejection for the watched terminal
//   - overflow: live output exceeded the pre-replay buffer cap
//   - err: a stream or transport error that ended the feed
type screenFeedItem struct {
	event    *leapmuxv1.TerminalEvent
	err      error
	rejected *leapmuxv1.WatchRejection
	overflow bool
}

// screenFeedSource is what the content watch of one terminal reads from its
// Worker.
type screenFeedSource struct {
	// openTransport opens a WatchEvents transport to the Worker. The function
	// that it returns releases the transport.
	openTransport func(ctx context.Context) (streamevents.Transport, func(), error)
	// clock supplies the backoff timers between reconnects.
	clock quartz.Clock
}

// workerScreenFeedSource watches through c the terminals of the Worker
// workerID. A transport that does not open reports the Worker offline, as
// the notify watch of `events` does.
func workerScreenFeedSource(c control.Peer, workerID string) screenFeedSource {
	notify := workerFeedSourceOf(c, workerID)
	return screenFeedSource{openTransport: notify.openTransport, clock: notify.clock}
}

// screenFeedReconnectTimerTag identifies the reconnect timer of a screen feed
// to a test clock.
const screenFeedReconnectTimerTag = "screen-feed-reconnect"

// startScreenFeed watches the content of the terminal terminalID until ctx
// ends, and sends what the Worker delivers to out, in stream order. wg counts
// its goroutine.
//
// A content watch carries the bytes of the terminal: the bytes after
// afterOffset, which the caller already holds, and then each chunk of output.
// It also replays the status of the terminal. A stream that ends subscribes
// again from the offset of the last chunk that it delivered, after a capped
// backoff, so the Worker replays the bytes that the gap lost, or the whole
// state when its buffer no longer holds them. When the first subscription
// fails, the feed sends a screenFeedItem with err set and ends.
func startScreenFeed(ctx context.Context, wg *sync.WaitGroup, source screenFeedSource, terminalID string, position terminalScreenPosition, out chan<- screenFeedItem) {
	f := &screenFeed{source: source, terminalID: terminalID, position: position, screenGeometry: true, out: out}
	wg.Go(func() { f.connect(ctx) })
}

// startTerminalOutputFeed watches the raw byte stream of terminalID without
// requesting ScreenTimeline or ScreenGeometry frames, ordering catch-up before
// live output via TerminalData.ReplayComplete.
func startTerminalOutputFeed(ctx context.Context, wg *sync.WaitGroup, source screenFeedSource, terminalID string, afterOffset int64, out chan<- screenFeedItem) {
	f := &screenFeed{
		source:     source,
		terminalID: terminalID,
		position:   terminalScreenPosition{offset: afterOffset},
		out:        out,
	}
	wg.Go(func() { f.connect(ctx) })
}

// screenFeed is the state of one startScreenFeed or startTerminalOutputFeed.
type screenFeed struct {
	source     screenFeedSource
	terminalID string
	// position records what the feed handed to the consumer, including geometry
	// when screenGeometry is true.
	position       terminalScreenPosition
	screenGeometry bool
	out            chan<- screenFeedItem
}

// connect keeps one subscription open until ctx ends.
func (f *screenFeed) connect(ctx context.Context) {
	backoff := newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
	// subscribed turns true once a subscription succeeded. From then on a
	// failed subscription is a gap that the next one covers, unless the
	// Worker refused the watch for good.
	subscribed := false
	for {
		delivered, err := f.subscribeOnce(ctx, func() { subscribed = true })
		if ctx.Err() != nil {
			return
		}
		if (err != nil && !subscribed) || refusedForGood(err) {
			f.send(ctx, screenFeedItem{err: err})
			return
		}
		timer := f.source.clock.NewTimer(backoff.afterSession(delivered), screenFeedReconnectTimerTag)
		select {
		case <-ctx.Done():
			timer.Stop(screenFeedReconnectTimerTag)
			return
		case <-timer.C:
		}
	}
}

// subscribeOnce opens one subscription and returns when its stream ends:
// with whether the stream carried anything, and why it ended. onSubscribed
// runs once the Worker accepted the interest.
func (f *screenFeed) subscribeOnce(ctx context.Context, onSubscribed func()) (bool, error) {
	transport, release, err := f.source.openTransport(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	streamCtx, stop := context.WithCancel(ctx)
	frames := make(chan screenFeedItem)
	var forward terminalReplayForwarder
	if f.screenGeometry {
		forward = &screenReplayForwarder{feed: f, position: f.position}
	} else {
		forward = &outputReplayForwarder{feed: f, offset: f.position.offset}
	}
	var forwarder sync.WaitGroup
	forwarder.Go(func() {
		for {
			select {
			case <-streamCtx.Done():
				return
			case frame := <-frames:
				if !forward.receive(streamCtx, frame) {
					stop()
					return
				}
			}
		}
	})

	offer := func(frame screenFeedItem) {
		select {
		case frames <- frame:
		case <-streamCtx.Done():
		}
	}
	var delivered atomic.Bool
	onTerminal := func(te *leapmuxv1.TerminalEvent) {
		if te.GetTerminalId() != f.terminalID {
			return
		}
		delivered.Store(true)
		offer(screenFeedItem{event: te})
	}
	onAck := func(ack *leapmuxv1.WatchUpdateAck) {
		delivered.Store(true)
		for _, rejection := range ack.GetRejectedTerminals() {
			if rejection.GetReason() == leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED {
				continue
			}
			offer(screenFeedItem{rejected: rejection})
		}
	}
	// The forwarder owns the replay and live cursors. A callback can arrive
	// before replay or block during delivery. Neither case advances the next
	// subscription.
	sub := streamevents.NewSubscription(transport, streamevents.NewAgentCursor(), streamevents.NewTerminalCursor(), nil, onTerminal, nil,
		streamevents.WithUpdateAck(onAck), streamevents.WithClock(f.source.clock), streamevents.WithUntrimmedTerminalData())
	defer func() {
		stop()
		forwarder.Wait()
		sub.Cancel()
		f.position = forward.nextPosition()
	}()
	if err := sub.Update(streamCtx, f.interest()); err != nil {
		return false, err
	}
	onSubscribed()
	select {
	case <-streamCtx.Done():
		return delivered.Load(), nil
	case <-sub.Done():
		err := sub.EndErr()
		if err == nil {
			err = errWatchEnded
		}
		return delivered.Load(), err
	}
}

// interest is the content watch of the terminal, from the offset that the
// feed delivered to the consumer.
func (f *screenFeed) interest() *leapmuxv1.WatchEventsRequest {
	entry := &leapmuxv1.WatchTerminalEntry{
		TerminalId:  f.terminalID,
		AfterOffset: f.position.offset,
		Mode:        leapmuxv1.WatchMode_WATCH_MODE_FULL,
	}
	if f.screenGeometry {
		entry.ScreenGeometry = true
		entry.ScreenEpoch = f.position.epoch
		entry.AfterGeometryRevision = f.position.revision
	}
	return &leapmuxv1.WatchEventsRequest{Terminals: []*leapmuxv1.WatchTerminalEntry{entry}}
}

// send delivers item to the wait. It returns false when ctx ended first.
func (f *screenFeed) send(ctx context.Context, item screenFeedItem) bool {
	select {
	case f.out <- item:
		return true
	case <-ctx.Done():
		return false
	}
}
