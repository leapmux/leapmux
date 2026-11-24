package cmd

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
)

// workerPrivateOutput carries one WorkerPrivateEvent from a Worker's
// WatchWorkerPrivateEvents stream into eventStream.
type workerPrivateOutput struct {
	workerID string
	event    *leapmuxv1.WorkerPrivateEvent
}

// workerPrivateSource streams WatchWorkerPrivateEvents for one Worker until
// ctx ends, calling onEvent for each received event.
type workerPrivateSource func(ctx context.Context, onEvent func(*leapmuxv1.WorkerPrivateEvent)) error

func workerPrivateSourceOf(c *control.Client, workerID string) workerPrivateSource {
	return func(ctx context.Context, onEvent func(*leapmuxv1.WorkerPrivateEvent)) error {
		payload, err := proto.Marshal(&leapmuxv1.WatchWorkerPrivateEventsRequest{})
		if err != nil {
			return err
		}
		forward := func(raw []byte, rpcErr *leapmuxv1.RpcError, end bool) error {
			if err := channelwire.ErrorOf(rpcErr); err != nil {
				return inBandError(err)
			}
			if len(raw) != 0 {
				var evt leapmuxv1.WorkerPrivateEvent
				if err := proto.Unmarshal(raw, &evt); err == nil && evt.GetEvent() != nil {
					onEvent(&evt)
				}
			}
			if end {
				return context.Canceled
			}
			return nil
		}
		tmpSession := &session{c: c}
		return tmpSession.receiveSemanticHistory(ctx, workerID, leapmuxv1connect.WorkerPrivateServiceWatchWorkerPrivateEventsProcedure, payload, forward)
	}
}

func (s *eventStream) startPrivateFeed(ctx context.Context, workerID string) {
	if s.privateSource == nil {
		return
	}
	src := s.privateSource(workerID)
	if src == nil {
		return
	}
	s.wg.Go(func() {
		backoff := newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
		for {
			delivered := false
			err := src(ctx, func(ev *leapmuxv1.WorkerPrivateEvent) {
				delivered = true
				select {
				case s.privateOut <- workerPrivateOutput{workerID: workerID, event: ev}:
				case <-ctx.Done():
				}
			})
			if ctx.Err() != nil || refusedForGood(err) || s.noReconnect {
				return
			}
			timer := s.clock.NewTimer(backoff.afterSession(delivered), "events-private-reconnect")
			select {
			case <-ctx.Done():
				timer.Stop("events-private-reconnect")
				return
			case <-timer.C:
			}
		}
	})
}

func (s *eventStream) onPrivateEvent(out workerPrivateOutput) error {
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
		ev := tabStreamEvent(kindTabStatus, tab)
		st := e.TabStatusChanged.GetStatus()
		if st == nil {
			ev.Status = "cleared"
			ev.Summary = "status cleared"
		} else {
			ev.Status = strings.TrimSpace(st.GetText())
			ev.Summary = ev.Status
			if st.ProgressPercent != nil {
				ev.Summary = fmt.Sprintf("%s (%d%%)", ev.Status, st.GetProgressPercent())
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
		if dest := n.GetDestination(); dest != nil {
			switch d := dest.GetTarget().(type) {
			case *leapmuxv1.NotificationDestination_WorkspaceId:
				ev.WorkspaceID = d.WorkspaceId
				if !s.inScope(d.WorkspaceId) {
					return nil
				}
			case *leapmuxv1.NotificationDestination_Tab:
				if tab, ok := s.tabs[d.Tab.GetTabId()]; ok {
					ev.WorkspaceID = tab.WorkspaceID
					ev.TabID = tab.ID
					ev.TabType = tabTypeName(tab.Type)
				} else if s.workspace != "" || s.tabID != "" {
					return nil
				} else {
					ev.TabID = d.Tab.GetTabId()
				}
			}
		}
		ev.Summary = n.GetTitle()
		if n.GetBody() != "" {
			ev.Summary = fmt.Sprintf("%s: %s", n.GetTitle(), n.GetBody())
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
		if dest := c.GetDestination(); dest != nil {
			switch d := dest.GetTarget().(type) {
			case *leapmuxv1.NotificationDestination_WorkspaceId:
				ev.WorkspaceID = d.WorkspaceId
				if !s.inScope(d.WorkspaceId) {
					return nil
				}
			case *leapmuxv1.NotificationDestination_Tab:
				if tab, ok := s.tabs[d.Tab.GetTabId()]; ok {
					ev.WorkspaceID = tab.WorkspaceID
					ev.TabID = tab.ID
					ev.TabType = tabTypeName(tab.Type)
				} else if s.workspace != "" || s.tabID != "" {
					return nil
				} else {
					ev.TabID = d.Tab.GetTabId()
				}
			}
		}
		return s.emit(ev)
	default:
		return nil
	}
}
