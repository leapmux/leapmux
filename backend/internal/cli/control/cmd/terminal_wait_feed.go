package cmd

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/streamevents"
)

// screenFeedKind is what one screenFeedItem reports.
type screenFeedKind int

const (
	// screenFeedEvent carries one event of the terminal, in stream order.
	screenFeedEvent screenFeedKind = iota + 1
	// screenFeedRejected reports that the Worker refused to watch the
	// terminal for good. err states why.
	screenFeedRejected
	// screenFeedFailed ends the feed, because it cannot watch the terminal.
	// err states why.
	screenFeedFailed
)

// screenFeedItem is one output of a screen feed.
type screenFeedItem struct {
	kind  screenFeedKind
	event *leapmuxv1.TerminalEvent
	err   error
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
func workerScreenFeedSource(c *control.Client, workerID string) screenFeedSource {
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
// fails, the feed sends screenFeedFailed and ends.
func startScreenFeed(ctx context.Context, wg *sync.WaitGroup, source screenFeedSource, terminalID string, afterOffset int64, out chan<- screenFeedItem) {
	f := &screenFeed{source: source, terminalID: terminalID, afterOffset: afterOffset, out: out}
	wg.Go(func() { f.connect(ctx) })
}

// screenFeed is the state of one startScreenFeed.
type screenFeed struct {
	source     screenFeedSource
	terminalID string
	// afterOffset belongs to the feed goroutine. It records the last byte
	// handed to the waiter, including the initial screen.
	afterOffset int64
	out         chan<- screenFeedItem
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
			f.send(ctx, screenFeedItem{kind: screenFeedFailed, err: err})
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
	afterOffset := f.afterOffset
	var forwarder sync.WaitGroup
	forwarder.Go(func() {
		for {
			select {
			case <-streamCtx.Done():
				return
			case frame := <-frames:
				if !f.send(streamCtx, frame) {
					return
				}
				if data := frame.event.GetData(); data != nil {
					afterOffset = data.GetEndOffset()
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
		offer(screenFeedItem{kind: screenFeedEvent, event: te})
	}
	onAck := func(ack *leapmuxv1.WatchUpdateAck) {
		delivered.Store(true)
		for _, rejection := range ack.GetRejectedTerminals() {
			if err := terminalRejection(rejection); err != nil {
				offer(screenFeedItem{kind: screenFeedRejected, err: err})
			}
		}
	}
	// Incoming frames advance this stream's own cursor. The feed resumes from
	// afterOffset, which the forwarder advances only when the waiter receives
	// a frame. A blocked callback from this stream cannot advance the next.
	cursor := streamevents.NewTerminalCursor()
	cursor.Track(f.terminalID, f.afterOffset)
	sub := streamevents.NewSubscription(transport, streamevents.NewAgentCursor(), cursor, nil, onTerminal, nil,
		streamevents.WithUpdateAck(onAck), streamevents.WithClock(f.source.clock))
	defer func() {
		stop()
		forwarder.Wait()
		sub.Cancel()
		f.afterOffset = afterOffset
	}()
	if err := sub.Update(streamCtx, f.interest()); err != nil {
		return false, err
	}
	onSubscribed()
	select {
	case <-ctx.Done():
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
// feed delivered to the waiter.
func (f *screenFeed) interest() *leapmuxv1.WatchEventsRequest {
	return &leapmuxv1.WatchEventsRequest{Terminals: []*leapmuxv1.WatchTerminalEntry{{
		TerminalId:  f.terminalID,
		AfterOffset: f.afterOffset,
		Mode:        leapmuxv1.WatchMode_WATCH_MODE_FULL,
	}}}
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

// terminalRejection is the error that one rejected terminal of an
// acknowledgement becomes. It is nil for a rejection that the subscription
// retries by itself.
//
// A terminal that the Worker no longer holds closed during the wait, so the
// wait ended without its condition: the error exits 6, as `agent send --wait`
// does for an agent that closed.
func terminalRejection(rejection *leapmuxv1.WatchRejection) error {
	id := rejection.GetEntityId()
	switch rejection.GetReason() {
	case leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED:
		return nil
	case leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND:
		return cli.Newf(cli.CodeNotFound, "terminal %s closed during the wait", id).
			WithExitCode(cli.ExitWaitEnded)
	case leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_FORBIDDEN:
		return cli.Newf(cli.CodePermissionDenied, "the credential may not watch terminal %s", id)
	default:
		// The protocol reads an unknown reason as durable.
		return cli.Newf(cli.CodeInternal, "the Worker refused to watch terminal %s (%s)", id, rejection.GetReason())
	}
}
