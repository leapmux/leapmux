package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/coder/quartz"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
)

// privateReconnectTimerTag identifies the private-event reconnect timer to a
// test clock.
const privateReconnectTimerTag = "events-private-reconnect"

// errPrivateStreamEnded is the reason reported when a Worker ends a
// WatchWorkerPrivateEvents stream without an error.
var errPrivateStreamEnded = cli.Newf(cli.CodeUnavailable, "the worker ended the private event stream")

// errPrivateStreamFellBehind is reported when a private event consumer stalls
// behind incoming frames on a shared Worker channel. Failing only the private
// stream lets WatchEvents and ListAgents keep flowing on the shared channel
// while privateFeed reconnects and reports a gap.
var errPrivateStreamFellBehind = cli.Newf(cli.CodeUnavailable, "the private event stream fell behind")

// privateOutputKind classifies one delivery from a Worker's private event feed.
// privateEvent is zero so direct unit tests constructing workerPrivateOutput
// with only workerID and event keep working.
type privateOutputKind int

const (
	privateEvent privateOutputKind = iota
	privateDown
	privateRefused
)

// workerPrivateOutput carries one delivery from a Worker's
// WatchWorkerPrivateEvents stream into eventStream.
type workerPrivateOutput struct {
	workerID string
	// feed identifies the privateFeed instance so outputs from a stopped feed
	// are ignored. Nil is accepted for direct unit tests.
	feed     *privateFeed
	kind     privateOutputKind
	event    *leapmuxv1.WorkerPrivateEvent
	err      error
	retrying bool
}

// workerPrivateSource streams WatchWorkerPrivateEvents for one Worker until
// ctx ends, calling onEvent for each received event.
type workerPrivateSource func(ctx context.Context, onEvent func(*leapmuxv1.WorkerPrivateEvent)) error

func workerPrivateSourceOf(c control.Peer, workerID string, channels *sharedWorkerChannels) workerPrivateSource {
	return func(ctx context.Context, onEvent func(*leapmuxv1.WorkerPrivateEvent)) error {
		payload, err := proto.Marshal(&leapmuxv1.WatchWorkerPrivateEventsRequest{})
		if err != nil {
			return err
		}
		decode := func(raw []byte) error {
			if len(raw) == 0 {
				return nil
			}
			var evt leapmuxv1.WorkerPrivateEvent
			if err := proto.Unmarshal(raw, &evt); err != nil {
				return cli.Wrap(cli.CodeInternal, err, "the Worker sent an invalid private event frame")
			}
			if evt.GetEvent() != nil {
				onEvent(&evt)
			}
			return nil
		}
		if channels != nil {
			ch, release, err := channels.acquire(ctx, workerID)
			if err != nil {
				return err
			}
			defer release()
			return readPrivateEventsOnChannel(ctx, ch, payload, decode)
		}
		return streamWorkerInner(ctx, c, workerID,
			leapmuxv1connect.WorkerPrivateServiceWatchWorkerPrivateEventsProcedure,
			payload,
			decode,
		)
	}
}

func readPrivateEventsOnChannel(ctx context.Context, ch semanticWatchChannel, payload []byte, decode func([]byte) error) error {
	return readEncryptedStream(ctx, ch,
		leapmuxv1connect.WorkerPrivateServiceWatchWorkerPrivateEventsProcedure,
		payload,
		errPrivateStreamFellBehind,
		func(raw []byte, rpcError *leapmuxv1.RpcError, end bool) error {
			if err := semanticFrameError(rpcError); err != nil {
				return err
			}
			if len(raw) != 0 {
				if err := decode(raw); err != nil {
					return err
				}
			}
			if end {
				return io.EOF
			}
			return nil
		},
	)
}

// privateFeed manages the WatchWorkerPrivateEvents reconnect loop for one
// Worker and supports waking from backoff when WorkersChanged arrives.
type privateFeed struct {
	workerID    string
	src         workerPrivateSource
	clock       quartz.Clock
	noReconnect bool
	out         chan<- workerPrivateOutput
	wake        chan struct{}
}

func newPrivateFeed(workerID string, src workerPrivateSource, clock quartz.Clock, noReconnect bool, out chan<- workerPrivateOutput) *privateFeed {
	return &privateFeed{
		workerID:    workerID,
		src:         src,
		clock:       clock,
		noReconnect: noReconnect,
		out:         out,
		wake:        make(chan struct{}, 1),
	}
}

// wakeUp ends the backoff wait between two private-event sessions.
func (pf *privateFeed) wakeUp() {
	select {
	case pf.wake <- struct{}{}:
	default:
	}
}

func (pf *privateFeed) run(ctx context.Context) {
	backoff := newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
	send := func(out workerPrivateOutput) bool {
		out.workerID = pf.workerID
		out.feed = pf
		select {
		case pf.out <- out:
			return true
		case <-ctx.Done():
			return false
		}
	}
	for {
		delivered := false
		err := pf.src(ctx, func(ev *leapmuxv1.WorkerPrivateEvent) {
			delivered = true
			send(workerPrivateOutput{kind: privateEvent, event: ev})
		})
		if ctx.Err() != nil {
			return
		}
		// A wake that arrived while the session ran does not apply to the
		// backoff wait after it ends.
		select {
		case <-pf.wake:
		default:
		}
		if err == nil || errors.Is(err, io.EOF) {
			err = errPrivateStreamEnded
		}
		if refusedForGood(err) {
			send(workerPrivateOutput{kind: privateRefused, err: err})
			return
		}
		if pf.noReconnect {
			send(workerPrivateOutput{kind: privateDown, err: err, retrying: false})
			return
		}
		if !send(workerPrivateOutput{kind: privateDown, err: err, retrying: true}) {
			return
		}
		timer := pf.clock.NewTimer(backoff.afterSession(delivered), privateReconnectTimerTag)
		select {
		case <-ctx.Done():
			timer.Stop(privateReconnectTimerTag)
			return
		case <-timer.C:
		case <-pf.wake:
			timer.Stop(privateReconnectTimerTag)
		}
	}
}

// tabStatusState holds the last seen status of one tab.
// WatchWorkerPrivateEvents replays active TabStatusChanged frames at the start
// of every connection without an end-of-snapshot marker, so a client cannot
// distinguish a first-connect baseline from a live update on the wire alone.
// Tracking the last seen status per tab suppresses duplicate replays across
// reconnects and across the register-then-snapshot window without dropping
// live updates on tabs that start without a status.
type tabStatusState struct {
	cleared         bool
	text            string
	tone            leapmuxv1.TabStatusTone
	hasProgress     bool
	progressPercent uint32
}

func tabStatusStateOf(st *leapmuxv1.TabStatus) tabStatusState {
	if st == nil {
		return tabStatusState{cleared: true}
	}
	return tabStatusState{
		text:            strings.TrimSpace(st.GetText()),
		tone:            st.GetTone(),
		hasProgress:     st.ProgressPercent != nil,
		progressPercent: st.GetProgressPercent(),
	}
}

func privateLostKinds() []string {
	return []string{kindTabRenamed, kindTabStatus, kindNotificationCreated, kindNotificationCleared}
}

func (s *eventStream) startPrivateFeed(fs *feedState) {
	if s.privateSource == nil {
		return
	}
	src := s.privateSource(fs.workerID)
	if src == nil {
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	pf := newPrivateFeed(fs.workerID, src, s.clock, s.noReconnect, s.privateOut)
	fs.private = pf
	fs.privateCancel = cancel
	fs.privateStarted = s.now()
	fs.privateHealth = sourceHealth{}
	fs.privateRefused = false
	s.wg.Go(func() { pf.run(ctx) })
}

func (s *eventStream) stopPrivateFeed(fs *feedState) {
	if fs.privateCancel != nil {
		fs.privateCancel()
	}
	fs.private = nil
	fs.privateCancel = nil
	fs.privateHealth = sourceHealth{}
	fs.privateRefused = false
}

func (s *eventStream) privateFeedDown(fs *feedState, err error, retrying bool) error {
	fs.privateHealth.live = false
	if !retrying {
		fs.privateRefused = true
	}
	if fs.privateHealth.down && retrying {
		return nil
	}
	if !fs.privateHealth.down {
		fs.privateHealth.down, fs.privateHealth.since = true, fs.privateStarted
		if fs.privateHealth.everLive {
			fs.privateHealth.since = s.now()
		}
	}
	return s.emit(unavailableEvent(workerEvent(fs), err, retrying))
}

func (s *eventStream) privateFeedRefused(fs *feedState, err error) error {
	fs.privateRefused = true
	fs.privateHealth.live = false
	return s.emit(unavailableEvent(workerEvent(fs), err, false))
}

func (s *eventStream) privateFeedLive(fs *feedState) error {
	wasDown, since := fs.privateHealth.down, fs.privateHealth.since
	fs.privateHealth = sourceHealth{live: true, everLive: true}
	if !wasDown {
		return nil
	}
	return s.emit(gapEvent(workerEvent(fs), s.now(), since, privateLostKinds(), s.workerTabIDs(fs.workerID)))
}

// applyNotificationDestination copies dest onto ev and reports whether the
// notification belongs to the stream's scope. A notification without a
// destination is excluded when --workspace or --tab narrows the stream.
func (s *eventStream) applyNotificationDestination(ev *streamEvent, dest *leapmuxv1.NotificationDestination) bool {
	if dest == nil || dest.GetTarget() == nil {
		return s.workspace == "" && s.tabID == ""
	}
	switch d := dest.GetTarget().(type) {
	case *leapmuxv1.NotificationDestination_WorkspaceId:
		if s.tabID != "" || !s.inScope(d.WorkspaceId) {
			return false
		}
		ev.WorkspaceID = d.WorkspaceId
		return true
	case *leapmuxv1.NotificationDestination_Tab:
		tabID := d.Tab.GetTabId()
		if tab, ok := s.tabs[tabID]; ok {
			ev.WorkspaceID = tab.WorkspaceID
			ev.TabID = tab.ID
			ev.TabType = tabTypeName(tab.Type)
			return true
		}
		if s.workspace != "" || s.tabID != "" {
			return false
		}
		ev.TabID = tabID
		return true
	default:
		return s.workspace == "" && s.tabID == ""
	}
}

func (s *eventStream) onPrivateEvent(out workerPrivateOutput) error {
	var fs *feedState
	if out.feed != nil {
		fs = s.feeds[out.workerID]
		if fs == nil || fs.private != out.feed {
			return nil
		}
	}
	switch out.kind {
	case privateEvent:
	case privateDown:
		if fs == nil {
			return nil
		}
		return s.privateFeedDown(fs, out.err, out.retrying)
	case privateRefused:
		if fs == nil {
			return nil
		}
		return s.privateFeedRefused(fs, out.err)
	}
	if fs != nil {
		if err := s.privateFeedLive(fs); err != nil {
			return err
		}
	}
	if out.event == nil {
		return nil
	}
	switch e := out.event.GetEvent().(type) {
	case *leapmuxv1.WorkerPrivateEvent_TabRenamed:
		tab, ok := s.tabs[e.TabRenamed.GetTabId()]
		if !ok || tab.WorkerID != out.workerID {
			return nil
		}
		ev := tabStreamEvent(kindTabRenamed, tab)
		ev.Title = e.TabRenamed.GetTitle()
		ev.Summary = fmt.Sprintf("renamed to %q", e.TabRenamed.GetTitle())
		return s.emit(ev)
	case *leapmuxv1.WorkerPrivateEvent_TabStatusChanged:
		tab, ok := s.tabs[e.TabStatusChanged.GetTabId()]
		if !ok || tab.WorkerID != out.workerID {
			return nil
		}
		st := e.TabStatusChanged.GetStatus()
		next := tabStatusStateOf(st)
		if prev, known := s.tabStatuses[tab.ID]; known && prev == next {
			return nil
		}
		s.tabStatuses[tab.ID] = next
		ev := tabStreamEvent(kindTabStatus, tab)
		if st == nil {
			ev.TabStatus = &tabStatusReport{Cleared: true}
			ev.Summary = "status cleared"
		} else {
			report := &tabStatusReport{
				Text: next.text,
				Tone: enumToken(next.tone),
			}
			if st.ProgressPercent != nil {
				pct := st.GetProgressPercent()
				report.ProgressPercent = &pct
			}
			ev.TabStatus = report
			ev.Summary = next.text
			if st.ProgressPercent != nil {
				if ev.Summary == "" {
					ev.Summary = fmt.Sprintf("(%d%%)", st.GetProgressPercent())
				} else {
					ev.Summary = fmt.Sprintf("%s (%d%%)", ev.Summary, st.GetProgressPercent())
				}
			}
		}
		return s.emit(ev)
	case *leapmuxv1.WorkerPrivateEvent_UserNotification:
		n := e.UserNotification
		ev := streamEvent{
			Kind:           kindNotificationCreated,
			WorkerID:       out.workerID,
			NotificationID: n.GetId(),
			Title:          n.GetTitle(),
			Body:           n.GetBody(),
		}
		if !s.applyNotificationDestination(&ev, n.GetDestination()) {
			return nil
		}
		switch {
		case n.GetTitle() != "" && n.GetBody() != "":
			ev.Summary = fmt.Sprintf("%s: %s", n.GetTitle(), n.GetBody())
		case n.GetTitle() != "":
			ev.Summary = n.GetTitle()
		default:
			ev.Summary = n.GetBody()
		}
		return s.emit(ev)
	case *leapmuxv1.WorkerPrivateEvent_UserNotificationCleared:
		c := e.UserNotificationCleared
		ev := streamEvent{
			Kind:           kindNotificationCleared,
			WorkerID:       out.workerID,
			NotificationID: c.GetId(),
			Summary:        fmt.Sprintf("notification %s cleared", cli.ShortID(c.GetId())),
		}
		if !s.applyNotificationDestination(&ev, c.GetDestination()) {
			return nil
		}
		return s.emit(ev)
	default:
		return nil
	}
}
