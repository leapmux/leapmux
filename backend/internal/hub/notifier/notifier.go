package notifier

import (
	"context"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/nilcheck"
)

// workerRegistry is the narrow surface Notifier needs from the live-worker
// map. ConnForTrustedPath performs no authorization, which is correct here:
// every worker id the notifier passes came from an authorized store row or a
// trusted server flow (deregister, reconnect flush), never from a user request.
// Holding this interface instead of *workermgr.Manager structurally prevents
// reaching Register / WaitFor* / the liveness probes.
type workerRegistry interface {
	ConnForTrustedPath(string) *workermgr.Conn
	MarkDeregistering(string)
	ClearDeregistering(string)
}

// Notifier manages sending notifications to workers with persistent
// queue fallback for reliable delivery.
type Notifier struct {
	store     store.Store
	workerMgr workerRegistry
	pending   *workermgr.PendingRequests
	cfg       *config.Config
}

// New creates a new Notifier. wMgr is taken as the narrow workerRegistry
// interface rather than *workermgr.Manager -- matching newWorkerCloseDispatcher
// -- so the narrowing constrains callers too, not just this package's body: a
// caller cannot hand the notifier Register / WaitFor* / the liveness probes,
// and a test can substitute a fake registry through the public constructor.
//
// The narrowing also destroys the caller's ability to spot a nil registry: a
// nil *workermgr.Manager converted to this interface is a NON-nil interface
// holding a nil pointer, so `wMgr != nil` would not catch it. Left unchecked,
// the first ConnForTrustedPath call panics on a nil receiver -- and it happens
// on the notification-delivery goroutine, which has no recover, so a wiring
// mistake takes the hub process down long after startup instead of at it.
// nilcheck sees through the conversion; workermgr.New guards its own dependency
// the same way, for the same reason.
func New(st store.Store, wMgr workerRegistry, pr *workermgr.PendingRequests, cfg *config.Config) *Notifier {
	if nilcheck.IsNilDependency(wMgr) {
		panic("notifier: New requires a worker registry")
	}
	return &Notifier{
		store:     st,
		workerMgr: wMgr,
		pending:   pr,
		cfg:       cfg,
	}
}

// SendOrQueue attempts to deliver a notification to a worker immediately.
// If the worker is offline or delivery fails, the notification is persisted
// to the worker_notifications queue for later delivery.
func (n *Notifier) SendOrQueue(ctx context.Context, workerID string, notificationType leapmuxv1.NotificationType, payload string, msg *leapmuxv1.ConnectResponse) error {
	conn := n.workerMgr.ConnForTrustedPath(workerID)
	if conn != nil {
		sendCtx, cancel := context.WithTimeout(ctx, n.cfg.APITimeout())
		defer cancel()

		_, err := n.pending.SendAndWait(sendCtx, conn, msg)
		if err == nil {
			return nil // Delivered and acked.
		}
		slog.Warn("failed to deliver notification, queueing", "worker_id", workerID, "type", notificationType, "error", err)
	}

	// Queue for later delivery.
	return n.store.WorkerNotifications().Create(ctx, store.CreateWorkerNotificationParams{
		ID:       id.Generate(),
		WorkerID: workerID,
		Type:     notificationType,
		Payload:  payload,
	})
}

// ProcessPendingNotifications delivers any queued notifications to a connected worker.
// Called when a worker connects or reconnects.
func (n *Notifier) ProcessPendingNotifications(ctx context.Context, workerID string) error {
	notifications, err := n.store.WorkerNotifications().ListPendingByWorker(ctx, workerID)
	if err != nil {
		return fmt.Errorf("list pending notifications: %w", err)
	}

	conn := n.workerMgr.ConnForTrustedPath(workerID)
	if conn == nil {
		return fmt.Errorf("worker not connected")
	}

	for _, notif := range notifications {
		_ = n.store.WorkerNotifications().IncrementAttempts(ctx, notif.ID)

		msg, err := n.buildNotificationMessage(notif)
		if err != nil {
			slog.Error("failed to build notification message", "notification_id", notif.ID, "error", err)
			if notif.Attempts+1 >= notif.MaxAttempts {
				_ = n.store.WorkerNotifications().MarkFailed(ctx, notif.ID)
			}
			continue
		}

		sendCtx, cancel := context.WithTimeout(ctx, n.cfg.APITimeout())
		_, sendErr := n.pending.SendAndWait(sendCtx, conn, msg)
		cancel()

		if sendErr != nil {
			slog.Warn("failed to deliver queued notification", "notification_id", notif.ID, "error", sendErr)
			if notif.Attempts+1 >= notif.MaxAttempts {
				_ = n.store.WorkerNotifications().MarkFailed(ctx, notif.ID)
			}
			continue
		}

		_ = n.store.WorkerNotifications().MarkDelivered(ctx, notif.ID)

		// For deregister notifications, mark worker as fully deleted after ack.
		if notif.Type == leapmuxv1.NotificationType_NOTIFICATION_TYPE_DEREGISTER {
			_ = n.store.Workers().MarkDeleted(ctx, workerID)
			n.workerMgr.ClearDeregistering(workerID)
			slog.Info("worker deregistration complete", "worker_id", workerID)
		}
	}

	return nil
}

// SendDeregister sends a deregistration notification to a worker.
func (n *Notifier) SendDeregister(ctx context.Context, workerID string) error {
	n.workerMgr.MarkDeregistering(workerID)

	msg := &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_Deregister{
			Deregister: &leapmuxv1.DeregisterNotification{
				Reason: "worker deregistered by owner",
			},
		},
	}

	return n.SendOrQueue(ctx, workerID, leapmuxv1.NotificationType_NOTIFICATION_TYPE_DEREGISTER, "{}", msg)
}

// buildNotificationMessage converts a persisted notification into a ConnectResponse.
func (n *Notifier) buildNotificationMessage(notif store.WorkerNotification) (*leapmuxv1.ConnectResponse, error) {
	switch notif.Type {
	case leapmuxv1.NotificationType_NOTIFICATION_TYPE_DEREGISTER:
		return &leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_Deregister{
				Deregister: &leapmuxv1.DeregisterNotification{
					Reason: "worker deregistered by owner",
				},
			},
		}, nil

	default:
		return nil, fmt.Errorf("unknown notification type: %s", notif.Type)
	}
}
