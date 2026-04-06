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
)

// Notifier manages sending notifications to workers with persistent
// queue fallback for reliable delivery.
type Notifier struct {
	store     store.Store
	workerMgr *workermgr.Manager
	pending   *workermgr.PendingRequests
	cfg       *config.Config
}

// New creates a new Notifier.
func New(st store.Store, wMgr *workermgr.Manager, pr *workermgr.PendingRequests, cfg *config.Config) *Notifier {
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
	conn := n.workerMgr.Get(workerID)
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

	conn := n.workerMgr.Get(workerID)
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
