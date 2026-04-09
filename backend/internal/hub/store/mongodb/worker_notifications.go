package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

func notifToDoc(p store.CreateWorkerNotificationParams, now time.Time) bson.D {
	return bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "worker_id", Value: p.WorkerID},
		{Key: "type", Value: int32(p.Type)},
		{Key: "payload", Value: p.Payload},
		{Key: "status", Value: int32(leapmuxv1.NotificationStatus_NOTIFICATION_STATUS_PENDING)},
		{Key: "attempts", Value: int64(0)},
		{Key: "max_attempts", Value: int64(3)},
		{Key: "created_at", Value: now},
	}
}

func docToNotif(m bson.M) store.WorkerNotification {
	return store.WorkerNotification{
		ID:          getS(m, "_id"),
		WorkerID:    getS(m, "worker_id"),
		Type:        leapmuxv1.NotificationType(getInt32(m, "type")),
		Payload:     getS(m, "payload"),
		Status:      leapmuxv1.NotificationStatus(getInt32(m, "status")),
		Attempts:    getInt64(m, "attempts"),
		MaxAttempts: getInt64(m, "max_attempts"),
		CreatedAt:   getTime(m, "created_at"),
		DeliveredAt: getTimePtr(m, "delivered_at"),
	}
}

func (st *workerNotificationStore) Create(ctx context.Context, p store.CreateWorkerNotificationParams) error {
	now := truncateMS(time.Now().UTC())
	doc := notifToDoc(p, now)
	_, err := st.s.collection(colWorkerNotifications).InsertOne(ctx, doc)
	if err != nil {
		return mapErr(err)
	}
	st.s.trackInsert(colWorkerNotifications, p.ID)
	return nil
}

func (st *workerNotificationStore) ListPendingByWorker(ctx context.Context, workerID string) ([]store.WorkerNotification, error) {
	filter := bson.D{
		{Key: "worker_id", Value: workerID},
		{Key: "status", Value: int32(leapmuxv1.NotificationStatus_NOTIFICATION_STATUS_PENDING)},
	}
	cursor, err := st.s.collection(colWorkerNotifications).Find(ctx, filter)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var notifications []store.WorkerNotification
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		notifications = append(notifications, docToNotif(m))
	}
	return ptrconv.NonNil(notifications), mapErr(cursor.Err())
}

func (st *workerNotificationStore) MarkDelivered(ctx context.Context, id string) error {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{{Key: "_id", Value: id}}
	st.s.trackBeforeUpdate(ctx, colWorkerNotifications, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "status", Value: int32(leapmuxv1.NotificationStatus_NOTIFICATION_STATUS_DELIVERED)},
			{Key: "delivered_at", Value: now},
		}},
	}
	_, err := st.s.collection(colWorkerNotifications).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *workerNotificationStore) MarkFailed(ctx context.Context, id string) error {
	filter := bson.D{{Key: "_id", Value: id}}
	st.s.trackBeforeUpdate(ctx, colWorkerNotifications, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "status", Value: int32(leapmuxv1.NotificationStatus_NOTIFICATION_STATUS_FAILED)},
		}},
	}
	_, err := st.s.collection(colWorkerNotifications).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *workerNotificationStore) IncrementAttempts(ctx context.Context, id string) error {
	filter := bson.D{{Key: "_id", Value: id}}
	st.s.trackBeforeUpdate(ctx, colWorkerNotifications, filter)
	update := bson.D{
		{Key: "$inc", Value: bson.D{
			{Key: "attempts", Value: int64(1)},
		}},
	}
	_, err := st.s.collection(colWorkerNotifications).UpdateOne(ctx, filter, update)
	return mapErr(err)
}
