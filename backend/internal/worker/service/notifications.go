package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"connectrpc.com/connect/v2"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

const (
	notificationTitleMaxBytes = 256
	notificationBodyMaxBytes  = 4096
	recentNotificationLimit   = 100
)

// recentNotifications holds the records that the CLI can list and clear.
// Its live events publish under the same lock as their record changes.
type recentNotifications struct {
	mu      sync.Mutex
	byOwner map[string][]*leapmuxv1.UserNotification
}

func (s *Service) publishNotification(owner userid.UserID, notice *leapmuxv1.UserNotification) {
	history := &s.notifications
	history.mu.Lock()
	defer history.mu.Unlock()
	if history.byOwner == nil {
		history.byOwner = make(map[string][]*leapmuxv1.UserNotification)
	}
	ownerID := owner.String()
	records := history.byOwner[ownerID]
	if len(records) == recentNotificationLimit {
		s.publishNotificationClear(owner, records[0])
		records = slices.Delete(records, 0, 1)
	}
	history.byOwner[ownerID] = append(records, notice)
	s.PrivateEvents.publish(owner, &leapmuxv1.WorkerPrivateEvent{Event: &leapmuxv1.WorkerPrivateEvent_UserNotification{UserNotification: notice}})
}

func (s *Service) publishNotificationClear(owner userid.UserID, notice *leapmuxv1.UserNotification) {
	s.PrivateEvents.publish(owner, &leapmuxv1.WorkerPrivateEvent{
		Event: &leapmuxv1.WorkerPrivateEvent_UserNotificationCleared{
			UserNotificationCleared: &leapmuxv1.UserNotificationCleared{Id: notice.GetId(), Destination: notice.GetDestination()},
		},
	})
}

func (s *Service) handleNotify(ctx context.Context, caller channel.Caller, request *leapmuxv1.NotifyRequest, sender channel.ResponseWriter) {
	title := strings.TrimSpace(request.GetTitle())
	body := strings.TrimSpace(request.GetBody())
	if title == "" {
		sendInvalidArgument(sender, "title is required")
		return
	}
	if len(title) > notificationTitleMaxBytes {
		sendInvalidArgument(sender, fmt.Sprintf("title is %d bytes long; the limit is %d bytes", len(title), notificationTitleMaxBytes))
		return
	}
	if len(body) > notificationBodyMaxBytes {
		sendInvalidArgument(sender, fmt.Sprintf("body is %d bytes long; the limit is %d bytes", len(body), notificationBodyMaxBytes))
		return
	}
	destination, valid := s.verifiedNotificationDestination(ctx, caller.UserID, request.GetDestination(), sender)
	if !valid {
		return
	}
	// The router or authenticated sibling Worker supplies the source.
	notice := &leapmuxv1.UserNotification{
		Id: id.Generate(), Title: title, Body: body, SourceTabId: caller.SourceTabID, Destination: destination,
	}
	s.publishNotification(caller.UserID, notice)
	sendProtoResponse(sender, &leapmuxv1.NotifyResponse{
		Id: notice.GetId(), Title: title, Body: body, Destination: destination,
	})
}

func (s *Service) verifiedNotificationDestination(ctx context.Context, owner userid.UserID, destination *leapmuxv1.NotificationDestination, sender channel.ResponseWriter) (*leapmuxv1.NotificationDestination, bool) {
	if destination == nil {
		return nil, true
	}
	switch target := destination.GetTarget().(type) {
	case *leapmuxv1.NotificationDestination_Tab:
		tabID := target.Tab.GetTabId()
		if tabID == "" || target.Tab.GetTabType() == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
			sendInvalidArgument(sender, "a notification's tab destination requires a tab ID and type")
			return nil, false
		}
		key, err := s.hostedTabKey(ctx, owner, tabID)
		if err == nil && target.Tab.GetTabType() != key.tabType {
			err = errTabNotHosted
		}
		if err != nil {
			sendHostedTabError(sender, tabID, err)
			return nil, false
		}
		return &leapmuxv1.NotificationDestination{
			Target: &leapmuxv1.NotificationDestination_Tab{Tab: &leapmuxv1.TabIdent{TabId: tabID, TabType: key.tabType}},
		}, true
	case *leapmuxv1.NotificationDestination_WorkspaceId:
		if target.WorkspaceId == "" {
			sendInvalidArgument(sender, "a notification's workspace destination requires a workspace ID")
			return nil, false
		}
		if s.CheckNotificationWorkspace == nil {
			sendUnavailable(sender, "the Worker cannot verify the workspace destination")
			return nil, false
		}
		if err := s.CheckNotificationWorkspace(ctx, target.WorkspaceId); err != nil {
			if connect.CodeOf(err) == connect.CodeNotFound {
				sendNotFoundError(sender, "the Worker owner has no such workspace")
			} else {
				sendUnavailable(sender, "the Worker could not verify the workspace destination")
			}
			return nil, false
		}
		return &leapmuxv1.NotificationDestination{
			Target: &leapmuxv1.NotificationDestination_WorkspaceId{WorkspaceId: target.WorkspaceId},
		}, true
	default:
		sendInvalidArgument(sender, "the notification destination is empty")
		return nil, false
	}
}

func (s *Service) handleListNotifications(_ context.Context, caller channel.Caller, _ *leapmuxv1.ListNotificationsRequest, sender channel.ResponseWriter) {
	history := &s.notifications
	history.mu.Lock()
	records := slices.Clone(history.byOwner[caller.UserID.String()])
	history.mu.Unlock()
	var visible []*leapmuxv1.UserNotification
	for i := len(records) - 1; i >= 0; i-- {
		if notificationDestinationVisible(caller, records[i].GetDestination()) {
			visible = append(visible, records[i])
		}
	}
	sendProtoResponse(sender, &leapmuxv1.ListNotificationsResponse{Notifications: visible})
}

func (s *Service) handleClearNotification(_ context.Context, caller channel.Caller, request *leapmuxv1.ClearNotificationRequest, sender channel.ResponseWriter) {
	noticeID := request.GetId()
	if noticeID == "" {
		sendInvalidArgument(sender, "a notification ID is required")
		return
	}
	cleared := s.clearNotification(caller.UserID, noticeID)
	sendProtoResponse(sender, &leapmuxv1.ClearNotificationResponse{Cleared: cleared})
}

func (s *Service) clearNotification(owner userid.UserID, noticeID string) bool {
	history := &s.notifications
	history.mu.Lock()
	defer history.mu.Unlock()
	ownerID := owner.String()
	records := history.byOwner[ownerID]
	index := slices.IndexFunc(records, func(notice *leapmuxv1.UserNotification) bool {
		return notice.GetId() == noticeID
	})
	if index < 0 {
		return false
	}
	notice := records[index]
	records = slices.Delete(records, index, index+1)
	if len(records) == 0 {
		delete(history.byOwner, ownerID)
	} else {
		history.byOwner[ownerID] = records
	}
	s.publishNotificationClear(owner, notice)
	return true
}

func notificationDestinationVisible(caller channel.Caller, destination *leapmuxv1.NotificationDestination) bool {
	if tab := destination.GetTab(); tab != nil {
		return callerReadsTabView(caller, tab.GetTabType())
	}
	return caller.Allows(leapmuxv1.Scope_SCOPE_WORKSPACE_READ)
}
