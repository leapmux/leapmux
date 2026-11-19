package cmd

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/streamevents"
)

// workerFeedSource is what the notify watch of one Worker reads.
type workerFeedSource struct {
	// openTransport opens a WatchEvents transport to the Worker. The
	// function that it returns releases the transport.
	openTransport func(ctx context.Context) (streamevents.Transport, func(), error)
	// readAgents reads the row of each agent that the Worker holds open. An
	// agent that the result leaves out is not open on the Worker.
	readAgents func(ctx context.Context, agentIDs []string) (map[string]*leapmuxv1.AgentInfo, error)
	// clock supplies the timers of the watch.
	clock quartz.Clock
}

// workerFeedSourceOf reads the Worker workerID through c.
func workerFeedSourceOf(c *control.Client, workerID string) workerFeedSource {
	return workerFeedSource{
		openTransport: func(ctx context.Context) (streamevents.Transport, func(), error) {
			t, err := newWorkerWatchTransport(ctx, c, workerID)
			if err != nil {
				// The code of a channel that does not open: the Worker is
				// offline, unless the hub refused the caller.
				return nil, nil, &codedRPCError{Code: "channel_open_failed", Cause: err}
			}
			return t.transport, t.close, nil
		},
		readAgents: func(ctx context.Context, agentIDs []string) (map[string]*leapmuxv1.AgentInfo, error) {
			ctx, cancel := rpcDeadline(ctx)
			defer cancel()
			var resp leapmuxv1.ListAgentsResponse
			if err := callInnerRPC(ctx, c, workerID, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure,
				&leapmuxv1.ListAgentsRequest{TabIds: agentIDs}, &resp); err != nil {
				return nil, err
			}
			out := make(map[string]*leapmuxv1.AgentInfo, len(resp.GetAgents()))
			for _, a := range resp.GetAgents() {
				out[a.GetId()] = a
			}
			return out, nil
		},
		clock: quartz.NewReal(),
	}
}

// watchInterest is the tabs of one Worker that a watch covers, each list
// sorted.
type watchInterest struct {
	agents, terminals []string
}

func (w watchInterest) empty() bool { return len(w.agents) == 0 && len(w.terminals) == 0 }

func (w watchInterest) equal(o watchInterest) bool {
	return slices.Equal(w.agents, o.agents) && slices.Equal(w.terminals, o.terminals)
}

// tabIDs lists every tab of the interest, sorted.
func (w watchInterest) tabIDs() []string {
	return slices.Sorted(slices.Values(slices.Concat(w.agents, w.terminals)))
}

// request is the NOTIFY watch of every tab of the interest. A notify watch
// carries the notification class of each tab, with no content and no replay.
func (w watchInterest) request() *leapmuxv1.WatchEventsRequest {
	req := &leapmuxv1.WatchEventsRequest{}
	for _, id := range w.agents {
		req.Agents = append(req.Agents, &leapmuxv1.WatchAgentEntry{AgentId: id, Mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY})
	}
	for _, id := range w.terminals {
		req.Terminals = append(req.Terminals, &leapmuxv1.WatchTerminalEntry{TerminalId: id, Mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY})
	}
	return req
}

// feedOutputKind is what one feedOutput reports.
type feedOutputKind int

const (
	// feedAgentEvent and feedTerminalEvent carry one event of the watch.
	feedAgentEvent feedOutputKind = iota + 1
	feedTerminalEvent
	// feedLevels carries the rows of agents that the Worker reported after
	// it acknowledged the watch.
	feedLevels
	// feedDown reports that the watch cannot deliver now. It retries.
	feedDown
	// feedRefused reports that the Worker refused the watch for good. The
	// feed ends.
	feedRefused
	// feedRejected reports a tab that the Worker did not watch.
	feedRejected
)

// feedOutput is one output of a workerFeed, in the order of the watch.
type feedOutput struct {
	feed *workerFeed
	kind feedOutputKind
	// session is the number of the subscription, from 1, of a feedLevels.
	session  int
	agent    *leapmuxv1.AgentEvent
	terminal *leapmuxv1.TerminalEvent
	// levels maps each agent that the Worker holds open to its row, and
	// levelsErr is why the read failed.
	levels    map[string]*leapmuxv1.AgentInfo
	levelsErr error
	// first marks the first levels of a session: the Worker acknowledged
	// the watch, and the feed delivers.
	first bool
	// err is why the watch is down or refused.
	err              error
	rejection        *leapmuxv1.WatchRejection
	rejectedTerminal bool
}

// The timers of a workerFeed, by their tags for a test clock.
const (
	feedReconnectTimerTag = "events-feed-reconnect"
	feedAckTimerTag       = "events-feed-ack"
)

// watchAckTimeout is how long a Worker can take to acknowledge a watch
// before the stream reports it unavailable. The watch goes on waiting, and
// a late acknowledgement makes it live again.
const watchAckTimeout = defaultInnerRPCTimeout

// errNoWatchAck is why a watch whose acknowledgement did not arrive is down.
var errNoWatchAck = cli.Newf(cli.CodeUnavailable, "the worker did not acknowledge the watch within %s", watchAckTimeout)

// errWatchEnded is why a watch is down when its stream ended without a
// reason.
var errWatchEnded = cli.Newf(cli.CodeUnavailable, "the worker ended the watch")

// workerFeed is the notify watch of the agents and the terminals of one
// Worker. Its interest changes as tabs join and leave the scope, on the same
// stream. A stream that ends subscribes again after a capped backoff, or at
// once when wakeUp runs.
//
// Two goroutines run it, as startActivityFeed does: connect owns the
// subscription, and sequence orders what the feed sends. After each
// acknowledgement, sequence reads the rows of the agents that the session
// did not read yet, and holds the events that arrive during the read until
// it sent the rows: an event that the Worker published after the read began
// is newer than the read, so it must follow it.
type workerFeed struct {
	workerID string
	source   workerFeedSource
	out      chan<- feedOutput

	mu       sync.Mutex
	interest watchInterest

	// revised carries a token when the interest changed, and wake when the
	// Worker may be reachable again. Both hold one token at most.
	revised chan struct{}
	wake    chan struct{}
	// inputs carries the stream's events and acknowledgements in stream
	// order, and the start and the end of each session.
	inputs chan feedInput
	// levels carries the result of each read of the agents.
	levels chan feedLevelRead
}

func newWorkerFeed(workerID string, source workerFeedSource, out chan<- feedOutput) *workerFeed {
	return &workerFeed{
		workerID: workerID,
		source:   source,
		out:      out,
		revised:  make(chan struct{}, 1),
		wake:     make(chan struct{}, 1),
		inputs:   make(chan feedInput, 16),
		levels:   make(chan feedLevelRead, 1),
	}
}

// start runs the feed until ctx ends. wg counts its goroutines.
func (f *workerFeed) start(ctx context.Context, wg *sync.WaitGroup) {
	wg.Go(func() { f.sequence(ctx) })
	wg.Go(func() { f.connect(ctx) })
}

// setInterest replaces the tabs that the watch covers.
func (f *workerFeed) setInterest(interest watchInterest) {
	f.mu.Lock()
	f.interest = interest
	f.mu.Unlock()
	select {
	case f.revised <- struct{}{}:
	default:
	}
}

func (f *workerFeed) currentInterest() watchInterest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.interest
}

// wakeUp ends the wait between two sessions, so a Worker that came online
// is watched at once.
func (f *workerFeed) wakeUp() {
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// feedInputKind is what one feedInput reports.
type feedInputKind int

const (
	inputSessionStarted feedInputKind = iota + 1
	inputAgent
	inputTerminal
	inputAck
	inputDown
	inputRefused
)

// feedInput is one item of workerFeed.inputs. session is the number of the
// subscription that it belongs to.
type feedInput struct {
	kind     feedInputKind
	session  int
	agent    *leapmuxv1.AgentEvent
	terminal *leapmuxv1.TerminalEvent
	ack      *leapmuxv1.WatchUpdateAck
	err      error
}

// feedLevelRead is the result of one read of the agents.
type feedLevelRead struct {
	// epoch identifies the read. A session that started or ended since the
	// read began makes it stale.
	epoch  int
	first  bool
	levels map[string]*leapmuxv1.AgentInfo
	err    error
}

// input hands one item to sequence. It returns false when ctx ended first.
func (f *workerFeed) input(ctx context.Context, in feedInput) bool {
	select {
	case f.inputs <- in:
		return true
	case <-ctx.Done():
		return false
	}
}

// connect keeps one subscription open until ctx ends or the Worker refuses
// the watch for good.
func (f *workerFeed) connect(ctx context.Context) {
	backoff := newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
	for session := 1; ; session++ {
		if !f.input(ctx, feedInput{kind: inputSessionStarted, session: session}) {
			return
		}
		delivered, err := f.runSession(ctx, session)
		if ctx.Err() != nil {
			return
		}
		// A wake that arrived while the session ran is no news for the wait
		// after it. The stream wakes only a feed that it knows is down, and
		// it learns that from the input below, so a wake after this point
		// counts.
		select {
		case <-f.wake:
		default:
		}
		if refusedForGood(err) {
			f.input(ctx, feedInput{kind: inputRefused, session: session, err: err})
			return
		}
		if !f.input(ctx, feedInput{kind: inputDown, session: session, err: err}) {
			return
		}
		timer := f.source.clock.NewTimer(backoff.afterSession(delivered), feedReconnectTimerTag)
		select {
		case <-ctx.Done():
			timer.Stop(feedReconnectTimerTag)
			return
		case <-timer.C:
		case <-f.wake:
			timer.Stop(feedReconnectTimerTag)
		}
	}
}

// runSession opens one subscription, revises its interest on each change,
// and returns when its stream ends: with why it ended, and whether it
// carried anything.
func (f *workerFeed) runSession(ctx context.Context, session int) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	transport, release, err := f.source.openTransport(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	var delivered atomic.Bool
	onAgent := func(ae *leapmuxv1.AgentEvent) {
		delivered.Store(true)
		f.input(ctx, feedInput{kind: inputAgent, session: session, agent: ae})
	}
	onTerminal := func(te *leapmuxv1.TerminalEvent) {
		delivered.Store(true)
		f.input(ctx, feedInput{kind: inputTerminal, session: session, terminal: te})
	}
	onAck := func(ack *leapmuxv1.WatchUpdateAck) {
		delivered.Store(true)
		f.input(ctx, feedInput{kind: inputAck, session: session, ack: ack})
	}
	sub := streamevents.NewSubscription(transport, streamevents.NewAgentCursor(), streamevents.NewTerminalCursor(),
		onAgent, onTerminal, nil, streamevents.WithUpdateAck(onAck), streamevents.WithTerminalNotifications(),
		streamevents.WithClock(f.source.clock))
	defer sub.Cancel()
	// The opening request states the interest in force, so a revision that
	// came before it needs no revision of its own. A revision after this
	// point leaves a token for the loop below.
	select {
	case <-f.revised:
	default:
	}
	if err := sub.Update(ctx, f.currentInterest().request()); err != nil {
		return false, err
	}
	for {
		select {
		case <-ctx.Done():
			return delivered.Load(), nil
		case <-sub.Done():
			err := sub.EndErr()
			if err == nil {
				err = errWatchEnded
			}
			return delivered.Load(), err
		case <-f.revised:
			if err := sub.Update(ctx, f.currentInterest().request()); err != nil {
				if errors.Is(err, streamevents.ErrSubscriptionClosed) {
					return delivered.Load(), nil
				}
				// The next session states the interest anew.
				return delivered.Load(), err
			}
		}
	}
}

// feedSequencer is the state of workerFeed.sequence. Only its goroutine
// reads and writes it.
type feedSequencer struct {
	f *workerFeed
	// session is the number of the current subscription.
	session int
	// firstOwed reports that the session owes its first levels.
	firstOwed bool
	// epoch identifies the reads that the current session may still use.
	epoch int
	// reading reports a read in flight, and readAgain an acknowledgement
	// that arrived during it.
	reading, readAgain bool
	// held holds the events that arrived during the read in flight.
	held []feedOutput
	// read holds the agents that a read of this session covered.
	read map[string]bool
	// ackTimer ends the wait for the first acknowledgement of the session.
	ackTimer *quartz.Timer
}

// sequence orders what the feed sends, until ctx ends or the Worker refused
// the watch.
func (f *workerFeed) sequence(ctx context.Context) {
	q := &feedSequencer{f: f, read: map[string]bool{}}
	defer q.stopAckTimer()
	for {
		var ackDeadline <-chan time.Time
		if q.ackTimer != nil {
			ackDeadline = q.ackTimer.C
		}
		select {
		case <-ctx.Done():
			return
		case in := <-f.inputs:
			if !q.input(ctx, in) {
				return
			}
		case read := <-f.levels:
			if !q.levelsRead(ctx, read) {
				return
			}
		case <-ackDeadline:
			q.ackTimer = nil
			if !q.send(ctx, feedOutput{kind: feedDown, err: errNoWatchAck}) {
				return
			}
		}
	}
}

// input takes one item in stream order. It returns false when the feed ends.
func (q *feedSequencer) input(ctx context.Context, in feedInput) bool {
	if in.session < q.session {
		// A late item of a session that ended.
		return true
	}
	switch in.kind {
	case inputSessionStarted:
		if !q.flushHeld(ctx) {
			return false
		}
		q.session, q.firstOwed = in.session, true
		q.epoch++
		q.reading, q.readAgain = false, false
		clear(q.read)
		q.startAckTimer()
		return true
	case inputAgent:
		return q.forward(ctx, feedOutput{kind: feedAgentEvent, agent: in.agent})
	case inputTerminal:
		return q.forward(ctx, feedOutput{kind: feedTerminalEvent, terminal: in.terminal})
	case inputAck:
		for _, rejection := range in.ack.GetRejectedAgents() {
			if !q.send(ctx, feedOutput{kind: feedRejected, rejection: rejection}) {
				return false
			}
		}
		for _, rejection := range in.ack.GetRejectedTerminals() {
			if !q.send(ctx, feedOutput{kind: feedRejected, rejection: rejection, rejectedTerminal: true}) {
				return false
			}
		}
		if q.reading {
			q.readAgain = true
			return true
		}
		return q.startRead(ctx)
	case inputDown:
		// The session ended, so a read in flight can no longer make it live,
		// and nothing that it holds waits for a read any more.
		q.stopAckTimer()
		q.epoch++
		q.firstOwed, q.reading, q.readAgain = false, false, false
		return q.flushHeld(ctx) && q.send(ctx, feedOutput{kind: feedDown, err: in.err})
	case inputRefused:
		q.stopAckTimer()
		q.flushHeld(ctx)
		q.send(ctx, feedOutput{kind: feedRefused, err: in.err})
		return false
	}
	return true
}

// forward sends an event now, or holds it behind the read in flight.
func (q *feedSequencer) forward(ctx context.Context, out feedOutput) bool {
	if q.reading {
		q.held = append(q.held, out)
		return true
	}
	return q.send(ctx, out)
}

// startRead reads the agents of the interest that this session did not read
// yet. A first acknowledgement of a session with no such agent makes the
// session live at once.
func (q *feedSequencer) startRead(ctx context.Context) bool {
	interest := q.f.currentInterest()
	for id := range q.read {
		if !slices.Contains(interest.agents, id) {
			// An agent that left the interest is read again if it returns.
			delete(q.read, id)
		}
	}
	var agents []string
	for _, id := range interest.agents {
		if !q.read[id] {
			agents = append(agents, id)
			q.read[id] = true
		}
	}
	first := q.firstOwed
	if len(agents) == 0 {
		if !first {
			return true
		}
		q.firstOwed = false
		q.stopAckTimer()
		return q.send(ctx, feedOutput{kind: feedLevels, session: q.session, first: true})
	}
	q.reading = true
	epoch := q.epoch
	go func() {
		levels, err := q.f.source.readAgents(ctx, agents)
		select {
		case q.f.levels <- feedLevelRead{epoch: epoch, first: first, levels: levels, err: err}:
		case <-ctx.Done():
		}
	}()
	return true
}

// levelsRead sends the result of a read, then the events that it held.
func (q *feedSequencer) levelsRead(ctx context.Context, read feedLevelRead) bool {
	if read.epoch != q.epoch {
		return true
	}
	// Input that is already queued arrived before the feed acts on this
	// result, so it goes first: an acknowledgement among it asks for the read
	// that follows this one, and the end of the session makes this one stale.
	for drained := false; !drained; {
		select {
		case in := <-q.f.inputs:
			if !q.input(ctx, in) {
				return false
			}
		default:
			drained = true
		}
	}
	if read.epoch != q.epoch {
		return true
	}
	if read.first {
		q.firstOwed = false
		q.stopAckTimer()
	}
	if !q.send(ctx, feedOutput{kind: feedLevels, session: q.session, first: read.first, levels: read.levels, levelsErr: read.err}) {
		return false
	}
	q.reading = false
	if !q.flushHeld(ctx) {
		return false
	}
	if q.readAgain {
		q.readAgain = false
		return q.startRead(ctx)
	}
	return true
}

// flushHeld sends every held event, in order.
func (q *feedSequencer) flushHeld(ctx context.Context) bool {
	held := q.held
	q.held = nil
	for _, out := range held {
		if !q.send(ctx, out) {
			return false
		}
	}
	return true
}

func (q *feedSequencer) startAckTimer() {
	q.stopAckTimer()
	q.ackTimer = q.f.source.clock.NewTimer(watchAckTimeout, feedAckTimerTag)
}

func (q *feedSequencer) stopAckTimer() {
	if q.ackTimer != nil {
		q.ackTimer.Stop(feedAckTimerTag)
		q.ackTimer = nil
	}
}

// send delivers out to the stream. It returns false when ctx ended first.
func (q *feedSequencer) send(ctx context.Context, out feedOutput) bool {
	out.feed = q.f
	select {
	case q.f.out <- out:
		return true
	case <-ctx.Done():
		return false
	}
}
