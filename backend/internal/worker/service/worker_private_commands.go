package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// The limits of a notification. The Worker refuses a longer text rather than
// cutting it, so a script learns the limit instead of showing a text that it
// did not write. The browser truncates what it draws and needs no copy of
// either limit.
const (
	// notificationTitleMaxBytes caps the title, which a toast shows on one
	// line and a desktop notification shows as its heading.
	notificationTitleMaxBytes = 256
	// notificationBodyMaxBytes caps the body, which a toast shows below the
	// title: a few lines, such as the last lines of a build log.
	notificationBodyMaxBytes = 4096
)

// registerWorkerPrivateCommandHandlers registers the three WorkerPrivateService
// RPCs that the Control CLI uses to reach the user's open frontends: Notify,
// RevealTab and SetTabStatus. SetQuakePanel is their older sibling, and it is
// registered with the terminal handlers, because its CLI verb is
// `terminal quake`.
//
// Each one publishes on the private-event bus to the CALLER's subscribers
// only, the way every publisher of that bus does.
func registerWorkerPrivateCommandHandlers(d ownerOnlyRegistrar, svc *Service) {
	registerOwnerGuarded(d, leapmuxv1connect.WorkerPrivateServiceNotifyProcedure, dispatchPlain, svc.handleNotify)
	registerOwnerGuarded(d, leapmuxv1connect.WorkerPrivateServiceRevealTabProcedure, dispatchPlain, svc.handleRevealTab)
	registerOwnerGuarded(d, leapmuxv1connect.WorkerPrivateServiceSetTabStatusProcedure, dispatchPlain, svc.handleSetTabStatus)
}

// handleNotify relays a notification to every frontend that the caller has
// open. It stores nothing, as SetQuakePanel stores nothing.
//
// The source tab comes from the caller, never from the request: the local IPC
// router stamps the tab of the control token that made the call, and a call
// that a sibling Worker relays carries the tab that the sibling stamped the
// same way (see channel.Caller.SourceTabID). A process cannot claim that
// another tab sent its notification.
func (svc *Service) handleNotify(_ context.Context, caller channel.Caller, r *leapmuxv1.NotifyRequest, sender channel.ResponseWriter) {
	title := strings.TrimSpace(r.GetTitle())
	body := strings.TrimSpace(r.GetBody())
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
	svc.PrivateEvents.PublishUserNotification(caller.UserID, title, body, caller.SourceTabID)
	sendProtoResponse(sender, &leapmuxv1.NotifyResponse{Title: title, Body: body})
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

// handleSetTabStatus sets or clears the status of one tab that this Worker
// hosts, and tells every frontend that the caller has open.
//
// The request is checked before the tab, so an invalid status fails the same
// way whether or not the tab exists.
func (svc *Service) handleSetTabStatus(ctx context.Context, caller channel.Caller, r *leapmuxv1.SetTabStatusRequest, sender channel.ResponseWriter) {
	tabID := r.GetTabId()
	if tabID == "" {
		sendInvalidArgument(sender, "tab_id is required")
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
	key, err := svc.hostedTabKey(ctx, caller.UserID, tabID)
	if err != nil {
		sendHostedTabError(sender, tabID, err)
		return
	}
	if status == nil {
		svc.TabStatuses.clear(key, caller.UserID)
		sendProtoResponse(sender, &leapmuxv1.SetTabStatusResponse{})
		return
	}
	svc.TabStatuses.set(key, caller.UserID, status)
	if svc.afterTabStatusSetFn != nil {
		svc.afterTabStatusSetFn()
	}
	// The tab can close between the lookup above and the set. Its close then
	// cleared a store that did not hold this status yet, and the status would
	// stay until the Worker stops, replayed to every frontend that subscribes.
	// Reading the tab again after the set closes that window: a close that
	// commits before this read is seen here, and one that commits after it
	// clears the status itself (applyTabCloseEffects runs after the commit).
	switch _, err := svc.hostedTabKey(ctx, caller.UserID, tabID); {
	case errors.Is(err, errTabNotHosted):
		svc.TabStatuses.forgetClosed(key)
		sendHostedTabError(sender, tabID, err)
		return
	case err != nil:
		// The status stays: the set already reached the frontends, and the
		// tab's own close clears it if the tab is gone.
		slog.Warn("failed to read the tab again after its status was set", "tab_id", tabID, "error", err)
	}
	sendProtoResponse(sender, &leapmuxv1.SetTabStatusResponse{Status: status})
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
	agentRow, err := svc.Queries.GetAgentByID(ctx, tabID)
	switch {
	case err == nil:
		if agentRow.ClosedAt.Valid {
			return ownedTabKey{}, errTabNotHosted
		}
		return newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, tabID, owner.String()), nil
	case !errors.Is(err, sql.ErrNoRows):
		return ownedTabKey{}, fmt.Errorf("read the agent: %w", err)
	}
	terminalRow, err := svc.Queries.GetTerminalQuakeAndClosed(ctx, tabID)
	switch {
	case err == nil:
		// A quake terminal has no tab: it belongs to a directory, and no
		// frontend draws it in a tab bar or a sidebar.
		if terminalRow.ClosedAt.Valid || terminalRow.IsQuake {
			return ownedTabKey{}, errTabNotHosted
		}
		return newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_TERMINAL, tabID, owner.String()), nil
	case !errors.Is(err, sql.ErrNoRows):
		return ownedTabKey{}, fmt.Errorf("read the terminal: %w", err)
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
