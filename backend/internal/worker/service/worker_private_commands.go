package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// registerWorkerPrivateCommandHandlers registers the notification, reveal,
// and status commands. Their events go to the caller's subscribers.
func registerWorkerPrivateCommandHandlers(d ownerOnlyRegistrar, svc *Service) {
	registerOwnerGuarded(d, leapmuxv1connect.WorkerPrivateServiceNotifyProcedure, dispatchPlain, svc.handleNotify)
	registerOwnerGuarded(d, leapmuxv1connect.WorkerPrivateServiceListNotificationsProcedure, dispatchPlain, svc.handleListNotifications)
	registerOwnerGuarded(d, leapmuxv1connect.WorkerPrivateServiceClearNotificationProcedure, dispatchPlain, svc.handleClearNotification)
	registerOwnerGuarded(d, leapmuxv1connect.WorkerPrivateServiceRevealTabProcedure, dispatchPlain, svc.handleRevealTab)
	registerOwnerGuarded(d, leapmuxv1connect.WorkerPrivateServiceSetTabStatusProcedure, dispatchPlain, svc.handleSetTabStatus)
	registerOwnerGuarded(d, leapmuxv1connect.WorkerPrivateServiceListTabStatusesProcedure, dispatchPlain, svc.handleListTabStatuses)
}

// handleRevealTab asks every frontend that the caller has open to show one tab
// that this Worker hosts. It stores nothing: which tab a client shows stays
// client-local, as the active tab of a tile is.
//
// It refuses a tab that this Worker does not host, so a mistyped id fails
// instead of answering success for a command that no frontend can act on.
func (svc *Service) handleRevealTab(ctx context.Context, caller channel.Caller, r *leapmuxv1.RevealTabRequest, sender channel.ResponseWriter) {
	tabID := r.GetTabId()
	if tabID == "" {
		sendInvalidArgument(sender, "tab_id is required")
		return
	}
	key, err := svc.hostedTabKey(ctx, caller.UserID, tabID)
	if err != nil {
		sendHostedTabError(sender, tabID, err)
		return
	}
	svc.PrivateEvents.PublishTabRevealCommand(caller.UserID, key.tabType, tabID)
	sendProtoResponse(sender, &leapmuxv1.RevealTabResponse{})
}

// handleSetTabStatus changes one status key of a hosted tab and publishes its
// visible status to the caller's frontends.
//
// The request is checked before the tab, so an invalid status fails the same
// way whether or not the tab exists.
func (svc *Service) handleSetTabStatus(ctx context.Context, caller channel.Caller, r *leapmuxv1.SetTabStatusRequest, sender channel.ResponseWriter) {
	tabID := r.GetTabId()
	if tabID == "" {
		sendInvalidArgument(sender, "tab_id is required")
		return
	}
	statusKey, err := validTabStatusKey(r.Key)
	if err != nil {
		sendInvalidArgument(sender, err.Error())
		return
	}
	// A status message that is present, even an empty one, asks for a SET,
	// and an empty one then fails for its missing text. Only an absent
	// message clears, as the proto states.
	var status *leapmuxv1.TabStatus
	if r.Status != nil {
		validated, err := validTabStatus(r.GetStatus())
		if err != nil {
			sendInvalidArgument(sender, err.Error())
			return
		}
		status = validated
	}
	closeEpoch := svc.TabStatuses.currentCloseEpoch()
	key, err := svc.hostedTabKey(ctx, caller.UserID, tabID)
	if err != nil {
		sendHostedTabError(sender, tabID, err)
		return
	}
	if status == nil {
		svc.TabStatuses.clear(key, caller.UserID, statusKey)
		sendProtoResponse(sender, &leapmuxv1.SetTabStatusResponse{Key: statusKey})
		return
	}
	if svc.beforeTabStatusSetFn != nil {
		svc.beforeTabStatusSetFn()
	}
	epochChanged, err := svc.TabStatuses.set(key, caller.UserID, statusKey, status, closeEpoch)
	if err != nil {
		sendFailedPrecondition(sender, err.Error())
		return
	}
	// A tab close that commits after set calls forgetClosed after set releases
	// TabStatusStore.mu, so forgetClosed clears this status itself. Only a close
	// whose forgetClosed ran between closeEpoch and set can leave this status
	// stranded, and that close incremented closeEpoch. Skip the second DB read
	// whenever no close occurred in that window.
	if epochChanged {
		switch open, err := svc.isHostedTabOpen(ctx, caller.UserID, key); {
		case err == nil && !open:
			svc.TabStatuses.forgetClosed(key)
			sendHostedTabError(sender, tabID, errTabNotHosted)
			return
		case err != nil:
			// The status stays: the set already reached the frontends, and the
			// tab's own close clears it if the tab is gone.
			slog.Warn("failed to read the tab again after its status was set", "tab_id", tabID, "error", err)
		}
	}
	sendProtoResponse(sender, &leapmuxv1.SetTabStatusResponse{Status: status, Key: statusKey})
}

func (svc *Service) handleListTabStatuses(ctx context.Context, caller channel.Caller, r *leapmuxv1.ListTabStatusesRequest, sender channel.ResponseWriter) {
	tabID := r.GetTabId()
	if tabID == "" {
		sendInvalidArgument(sender, "tab_id is required")
		return
	}
	key, err := svc.hostedTabKey(ctx, caller.UserID, tabID)
	if err != nil {
		sendHostedTabError(sender, tabID, err)
		return
	}
	if !callerReadsTabView(caller, key.tabType) {
		sendPermissionDenied(sender, "reading status entries requires the tab kind's read permission")
		return
	}
	sendProtoResponse(sender, &leapmuxv1.ListTabStatusesResponse{Statuses: svc.TabStatuses.list(key, caller.UserID)})
}

// errTabNotHosted reports a tab that this Worker does not host for the caller:
// an unknown id, a closed tab, or a quake terminal, which has no tab.
var errTabNotHosted = errors.New("this worker hosts no such tab")

// hostedTabKey returns the key of the open tab tabID that this Worker hosts
// for owner, or errTabNotHosted. Any other error is a failure to read.
//
// The request states no tab type, so each kind is asked in turn. An AGENT or a
// TERMINAL id is unique on its own; a FILE or IMAGE id is unique only within a
// user, which is why the payload read binds the owner.
//
// A closed row is not a hosted tab: its status would survive the close that
// already ran, and a reveal would move every frontend to nothing. A child
// agent's row stays open while its tab is closed, because closing a subagent
// tab only hides it; such a tab still counts as hosted.
func (svc *Service) hostedTabKey(ctx context.Context, owner userid.UserID, tabID string) (ownedTabKey, error) {
	openAgent, err := svc.Queries.CanWatchAgentHistory(ctx, tabID)
	if err != nil {
		return ownedTabKey{}, fmt.Errorf("read the agent: %w", err)
	}
	if openAgent {
		return newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, tabID, owner.String()), nil
	}
	openTerminal, err := svc.Queries.CanWatchTerminalHistory(ctx, tabID)
	if err != nil {
		return ownedTabKey{}, fmt.Errorf("read the terminal: %w", err)
	}
	if openTerminal {
		return newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_TERMINAL, tabID, owner.String()), nil
	}
	tabType, err := svc.TabPayloads.TabTypeOf(ctx, owner.String(), tabID)
	switch {
	case err == nil:
		return newOwnedTabKey(tabType, tabID, owner.String()), nil
	case errors.Is(err, ErrTabPayloadNotFound):
		return ownedTabKey{}, errTabNotHosted
	default:
		return ownedTabKey{}, fmt.Errorf("read the tab payload: %w", err)
	}
}

func (svc *Service) isHostedTabOpen(ctx context.Context, owner userid.UserID, key ownedTabKey) (bool, error) {
	switch key.tabType {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		return svc.Queries.CanWatchAgentHistory(ctx, key.tabID)
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		return svc.Queries.CanWatchTerminalHistory(ctx, key.tabID)
	default:
		tabType, err := svc.TabPayloads.TabTypeOf(ctx, owner.String(), key.tabID)
		if errors.Is(err, ErrTabPayloadNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return tabType == key.tabType, nil
	}
}

// sendHostedTabError answers a failed hostedTabKey: NOT_FOUND for a tab that
// this Worker does not host, and INTERNAL for a failure to read.
func sendHostedTabError(sender channel.ResponseWriter, tabID string, err error) {
	if errors.Is(err, errTabNotHosted) {
		sendNotFoundError(sender, "tab not found on this worker")
		return
	}
	slog.Error("failed to look up a tab", "tab_id", tabID, "error", err)
	sendInternalError(sender, "failed to look up the tab")
}

// privateEventsSnapshot returns the events that the bootstrap of
// WatchWorkerPrivateEvents sends before any live event: one
// TabPayloadRegistered for each payload row of owner, then one
// TabStatusChanged for each of owner's tabs whose status is set.
//
// A failed payload read still sends the statuses. They live in memory, and a
// busy database must not hide them; the payloads arrive again on the next
// subscription, as before.
func (svc *Service) privateEventsSnapshot(ctx context.Context, owner userid.UserID) []*leapmuxv1.WorkerPrivateEvent {
	payloads, err := svc.TabPayloads.SnapshotForOwner(ctx, owner)
	if err != nil {
		// A client that cancels its stream during the read is not a fault.
		if ctx.Err() == nil {
			slog.Warn("failed to read the tab payloads for a private-event bootstrap", "error", err)
		}
		payloads = nil
	}
	return append(payloads, svc.TabStatuses.snapshotFor(owner)...)
}
