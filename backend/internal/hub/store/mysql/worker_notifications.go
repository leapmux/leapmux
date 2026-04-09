package mysql

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

// workerNotificationStore implements store.WorkerNotificationStore backed by MySQL.
type workerNotificationStore struct{ q *gendb.Queries }

var _ store.WorkerNotificationStore = (*workerNotificationStore)(nil)

func (s *workerNotificationStore) Create(ctx context.Context, p store.CreateWorkerNotificationParams) error {
	return mapErr(s.q.CreateWorkerNotification(ctx, gendb.CreateWorkerNotificationParams{
		ID:       p.ID,
		WorkerID: p.WorkerID,
		Type:     p.Type,
		Payload:  p.Payload,
	}))
}

func (s *workerNotificationStore) ListPendingByWorker(ctx context.Context, workerID string) ([]store.WorkerNotification, error) {
	rows, err := s.q.ListPendingNotificationsByWorker(ctx, workerID)
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, fromDBWorkerNotification), nil
}

func (s *workerNotificationStore) MarkDelivered(ctx context.Context, id string) error {
	return mapErr(s.q.MarkNotificationDelivered(ctx, id))
}

func (s *workerNotificationStore) MarkFailed(ctx context.Context, id string) error {
	return mapErr(s.q.MarkNotificationFailed(ctx, id))
}

func (s *workerNotificationStore) IncrementAttempts(ctx context.Context, id string) error {
	return mapErr(s.q.IncrementNotificationAttempts(ctx, id))
}

func fromDBWorkerNotification(n gendb.WorkerNotification) store.WorkerNotification {
	return store.WorkerNotification{
		ID:          n.ID,
		WorkerID:    n.WorkerID,
		Type:        n.Type,
		Payload:     n.Payload,
		Status:      n.Status,
		Attempts:    int64(n.Attempts),
		MaxAttempts: int64(n.MaxAttempts),
		CreatedAt:   n.CreatedAt,
		DeliveredAt: sqlutil.NullTimeToPtr(n.DeliveredAt),
	}
}
