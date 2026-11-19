package cmd

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/readseq"
	"github.com/leapmux/leapmux/internal/cli/control/streamevents"
)

// activityEventKind is what one activityEvent reports.
type activityEventKind int

const (
	// activityLevel is the published state that the Worker reported after it
	// acknowledged a subscription: a state that the agent held at that moment.
	activityLevel activityEventKind = iota + 1
	// activityChange is a change of the published state.
	activityChange
	// activityTurnEnd is the end of one of the agent's turns. A provider can
	// end a turn without a change of the published state.
	activityTurnEnd
	// activityStatusChange is a change of the status of the agent's process.
	activityStatusChange
	// activityClosed reports that the Worker holds no open agent with the id.
	activityClosed
	// activityFailed ends the feed, because it cannot observe the agents.
	activityFailed
)

// activityEvent is one observation of an activity feed.
type activityEvent struct {
	kind    activityEventKind
	agentID string
	// state is the published activity of an activityLevel or an
	// activityChange.
	state leapmuxv1.AgentActivityState
	// err is the cause of an activityFailed.
	err error
}

// activityFeedSource is what an activity feed reads from one Worker.
type activityFeedSource struct {
	// openTransport opens a WatchEvents transport to the Worker. The function
	// that it returns releases the transport.
	openTransport func(ctx context.Context) (streamevents.Transport, func(), error)
	// readActivity reads the published activity of each agent. An agent that
	// the result leaves out is closed.
	readActivity func(ctx context.Context, agentIDs []string) (map[string]leapmuxv1.AgentActivityState, error)
	// clock supplies the backoff timers between reconnects.
	clock quartz.Clock
}

// workerActivitySource reads the activity of agents on workerID through c.
func workerActivitySource(c *control.Client, workerID string) activityFeedSource {
	return activityFeedSource{
		openTransport: func(ctx context.Context) (streamevents.Transport, func(), error) {
			t, err := newWorkerWatchTransport(ctx, c, workerID)
			if err != nil {
				return nil, nil, err
			}
			return t.transport, t.close, nil
		},
		readActivity: func(ctx context.Context, agentIDs []string) (map[string]leapmuxv1.AgentActivityState, error) {
			ctx, cancel := rpcDeadline(ctx)
			defer cancel()
			var resp leapmuxv1.ListAgentsResponse
			if err := callInnerRPC(ctx, c, workerID, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure,
				&leapmuxv1.ListAgentsRequest{TabIds: agentIDs}, &resp); err != nil {
				return nil, err
			}
			states := make(map[string]leapmuxv1.AgentActivityState, len(resp.GetAgents()))
			for _, a := range resp.GetAgents() {
				// The published state, not the exact one: the changes that
				// the watch delivers are published states, so a level of the
				// other kind could disagree with every change after it for
				// as long as a settle waits out its debounce.
				states[a.GetId()] = a.GetPublishedActivityState()
			}
			return states, nil
		},
		clock: quartz.NewReal(),
	}
}

// The backoff between two subscriptions of an activity feed. It matches the
// reconnect backoff of `agent messages --follow`.
const (
	activityReconnectInitial = 250 * time.Millisecond
	activityReconnectMax     = 8 * time.Second
)

// activityReconnectTimerTag identifies the reconnect timer to a test clock.
const activityReconnectTimerTag = "activity-feed-reconnect"

// startActivityFeed watches the published activity of agentIDs on one Worker
// until ctx ends, and sends what it observes to out.
//
// A NOTIFY watch carries each change but never a level: CatchUpStart is
// content-class, so only a FULL watch receives it. The feed therefore reads
// the level after the Worker acknowledges each subscription, and holds the
// changes that arrive during that read until it sent the level. The Worker
// publishes each later change after the read began, so the stream delivers
// it after the read began too. The last event of an agent is therefore its
// current published state.
//
// A stream that ends subscribes again after a capped backoff, and the level
// of the new subscription covers the changes that the gap lost. When the
// first subscription fails, the feed sends activityFailed and ends: the
// Worker refused it, or cannot be reached at all.
func startActivityFeed(ctx context.Context, source activityFeedSource, agentIDs []string, out chan<- activityEvent) {
	f := &activityFeed{
		source:   source,
		agentIDs: append([]string(nil), agentIDs...),
		out:      out,
	}
	f.seq = readseq.New(readseq.Hooks[activityInput, activityEvent, activityLevelRead]{
		Handle:   f.handle,
		NextRead: f.nextRead,
		Apply:    f.applyLevel,
		Send:     f.send,
	})
	go f.sequence(ctx)
	go f.connect(ctx)
}

// activityFeed is the state of one startActivityFeed.
type activityFeed struct {
	source   activityFeedSource
	agentIDs []string
	// seq holds the changes behind each level read. connect pushes the
	// stream's events and acknowledgements into it in stream order, and the
	// failures of the connection.
	seq *readseq.Sequencer[activityInput, activityEvent, activityLevelRead]
	out chan<- activityEvent
}

// activityInput is one input of an activity feed: an event, or an
// acknowledgement.
type activityInput struct {
	event activityEvent
	ack   *leapmuxv1.WatchUpdateAck
}

// activityLevelRead is the result of one level read.
type activityLevelRead struct {
	states map[string]leapmuxv1.AgentActivityState
	err    error
}

// sequence orders what the feed sends, until ctx ends or the feed ends.
func (f *activityFeed) sequence(ctx context.Context) {
	defer f.seq.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case in := <-f.seq.Inputs():
			if !f.handle(ctx, in) {
				return
			}
		case read := <-f.seq.Results():
			if !f.seq.Complete(ctx, read) {
				return
			}
		}
	}
}

// handle takes one input in stream order. An acknowledgement asks for a
// level read. It returns false when the feed ends.
func (f *activityFeed) handle(ctx context.Context, in activityInput) bool {
	if in.ack == nil {
		return f.forward(ctx, in.event)
	}
	for _, rejection := range in.ack.GetRejectedAgents() {
		ev, ok := activityRejection(rejection)
		if ok && !f.forward(ctx, ev) {
			return false
		}
	}
	return f.seq.RequestRead(ctx)
}

// forward sends an event now, or holds it behind the level read in flight.
// A failure is never held: it ends the feed, and no order matters after it.
// forward returns false when the feed ends.
func (f *activityFeed) forward(ctx context.Context, ev activityEvent) bool {
	if ev.kind == activityFailed {
		f.send(ctx, ev)
		return false
	}
	return f.seq.Forward(ctx, ev)
}

// activityRejection is the event that one rejected agent of an
// acknowledgement becomes. ok is false for a rejection that the subscription
// retries by itself.
func activityRejection(rejection *leapmuxv1.WatchRejection) (activityEvent, bool) {
	id := rejection.GetEntityId()
	switch rejection.GetReason() {
	case leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED:
		return activityEvent{}, false
	case leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND:
		return activityEvent{kind: activityClosed, agentID: id}, true
	case leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_FORBIDDEN:
		return activityEvent{kind: activityFailed, agentID: id, err: cli.Newf(cli.CodePermissionDenied,
			"the credential may not watch agent %s", id)}, true
	default:
		// The protocol reads an unknown reason as durable.
		return activityEvent{kind: activityFailed, agentID: id, err: cli.Newf(cli.CodeInternal,
			"the Worker refused to watch agent %s (%s)", id, rejection.GetReason())}, true
	}
}

// nextRead returns the read of every agent's published state. Each
// acknowledgement asks for it.
func (f *activityFeed) nextRead(context.Context) (readseq.Read[activityLevelRead], bool) {
	return f.readLevel, true
}

// readLevel reads the published state of every agent.
func (f *activityFeed) readLevel(ctx context.Context) activityLevelRead {
	states, err := f.source.readActivity(ctx, f.agentIDs)
	return activityLevelRead{states: states, err: err}
}

// applyLevel sends the level of each agent that a read returned. An agent
// that the result leaves out is closed. A read that failed ends the feed.
func (f *activityFeed) applyLevel(ctx context.Context, read activityLevelRead) bool {
	if read.err != nil {
		f.send(ctx, activityEvent{kind: activityFailed, err: fmt.Errorf("read the state of the agents: %w", read.err)})
		return false
	}
	for _, id := range f.agentIDs {
		ev := activityEvent{kind: activityClosed, agentID: id}
		if state, open := read.states[id]; open {
			ev = activityEvent{kind: activityLevel, agentID: id, state: state}
		}
		if !f.send(ctx, ev) {
			return false
		}
	}
	return true
}

// send delivers ev to the consumer. It returns false when ctx ended first.
func (f *activityFeed) send(ctx context.Context, ev activityEvent) bool {
	select {
	case f.out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// interest is the NOTIFY watch of every agent of the feed.
func (f *activityFeed) interest() *leapmuxv1.WatchEventsRequest {
	req := &leapmuxv1.WatchEventsRequest{}
	for _, id := range f.agentIDs {
		req.Agents = append(req.Agents, &leapmuxv1.WatchAgentEntry{AgentId: id, Mode: leapmuxv1.WatchMode_WATCH_MODE_NOTIFY})
	}
	return req
}

// connect keeps one subscription open until ctx ends.
func (f *activityFeed) connect(ctx context.Context) {
	backoff := newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
	// subscribed turns true once a subscription succeeded. From then on a
	// failed subscription is a gap that the next one covers, not a reason to
	// end the feed.
	subscribed := false
	for {
		var delivered atomic.Bool
		err := f.subscribeOnce(ctx, &delivered, func() { subscribed = true })
		if ctx.Err() != nil {
			return
		}
		if err != nil && !subscribed {
			f.seq.Push(ctx, activityInput{event: activityEvent{kind: activityFailed, err: err}})
			return
		}
		timer := f.source.clock.NewTimer(backoff.afterSession(delivered.Load()), activityReconnectTimerTag)
		select {
		case <-ctx.Done():
			timer.Stop(activityReconnectTimerTag)
			return
		case <-timer.C:
		}
	}
}

// subscribeOnce opens one subscription and returns when its stream ends.
// onSubscribed runs once the Worker accepted the interest. delivered records
// whether the stream carried anything.
func (f *activityFeed) subscribeOnce(ctx context.Context, delivered *atomic.Bool, onSubscribed func()) error {
	transport, release, err := f.source.openTransport(ctx)
	if err != nil {
		return err
	}
	defer release()
	onAgent := func(ae *leapmuxv1.AgentEvent) {
		ev := activityEvent{agentID: ae.GetAgentId()}
		switch {
		case ae.GetActivityChanged() != nil:
			ev.kind, ev.state = activityChange, ae.GetActivityChanged().GetState()
		case ae.GetTurnEnd() != nil:
			ev.kind = activityTurnEnd
		case ae.GetStatusChange() != nil:
			ev.kind = activityStatusChange
		default:
			return
		}
		delivered.Store(true)
		f.seq.Push(ctx, activityInput{event: ev})
	}
	onAck := func(ack *leapmuxv1.WatchUpdateAck) {
		delivered.Store(true)
		f.seq.Push(ctx, activityInput{ack: ack})
	}
	sub := streamevents.NewSubscription(transport, streamevents.NewAgentCursor(), streamevents.NewTerminalCursor(),
		onAgent, nil, nil, streamevents.WithUpdateAck(onAck))
	defer sub.Cancel()
	if err := sub.Update(ctx, f.interest()); err != nil {
		return err
	}
	onSubscribed()
	select {
	case <-ctx.Done():
	case <-sub.Done():
	}
	return nil
}
