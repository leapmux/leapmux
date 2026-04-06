package dynamodb

import (
	"context"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

type workerNotificationStore struct{ s *dynamoStore }

var _ store.WorkerNotificationStore = (*workerNotificationStore)(nil)

func (st *workerNotificationStore) table() string { return st.s.table(tableWorkerNotifications) }

func itemToWorkerNotification(item map[string]ddbtypes.AttributeValue) store.WorkerNotification {
	return store.WorkerNotification{
		ID:          getS(item, "id"),
		WorkerID:    getS(item, "worker_id"),
		Type:        leapmuxv1.NotificationType(getN(item, "type")),
		Payload:     getS(item, "payload"),
		Status:      leapmuxv1.NotificationStatus(getSAsInt64(item, "status")),
		Attempts:    getN(item, "attempts"),
		MaxAttempts: getN(item, "max_attempts"),
		CreatedAt:   getTime(item, "created_at"),
		DeliveredAt: getTimePtr(item, "delivered_at"),
	}
}

func (st *workerNotificationStore) Create(ctx context.Context, p store.CreateWorkerNotificationParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(st.table()),
		ConditionExpression: aws.String("attribute_not_exists(id)"),
		Item: map[string]ddbtypes.AttributeValue{
			"id":           attrS(p.ID),
			"worker_id":    attrS(p.WorkerID),
			"type":         attrN(int64(p.Type)),
			"payload":      attrS(p.Payload),
			"status":       attrS(strconv.FormatInt(int64(leapmuxv1.NotificationStatus_NOTIFICATION_STATUS_PENDING), 10)),
			"attempts":     attrN(0),
			"max_attempts": attrN(3),
			"created_at":   attrS(timeToStr(now)),
		},
	}, "id")
	return mapErr(err)
}

func (st *workerNotificationStore) ListPendingByWorker(ctx context.Context, workerID string) ([]store.WorkerNotification, error) {
	pendingStatus := strconv.FormatInt(int64(leapmuxv1.NotificationStatus_NOTIFICATION_STATUS_PENDING), 10)
	var notifications []store.WorkerNotification
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiWorkerIDStatus),
		KeyConditionExpression: aws.String("worker_id = :wid AND #st = :pending"),
		ExpressionAttributeNames: map[string]string{
			"#st": "status",
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":wid":     attrS(workerID),
			":pending": attrS(pendingStatus),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		notifications = append(notifications, itemToWorkerNotification(item))
		return true
	})
	if err != nil {
		return nil, err
	}
	return ptrconv.NonNil(notifications), nil
}

func (st *workerNotificationStore) MarkDelivered(ctx context.Context, id string) error {
	now := timeToStr(time.Now().UTC())
	deliveredStatus := strconv.FormatInt(int64(leapmuxv1.NotificationStatus_NOTIFICATION_STATUS_DELIVERED), 10)
	_, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:        aws.String(st.table()),
		Key:              map[string]ddbtypes.AttributeValue{"id": attrS(id)},
		UpdateExpression: aws.String("SET #st = :status, delivered_at = :now"),
		ExpressionAttributeNames: map[string]string{
			"#st": "status",
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":status": attrS(deliveredStatus),
			":now":    attrS(now),
		},
	})
	return mapErr(err)
}

func (st *workerNotificationStore) MarkFailed(ctx context.Context, id string) error {
	failedStatus := strconv.FormatInt(int64(leapmuxv1.NotificationStatus_NOTIFICATION_STATUS_FAILED), 10)
	_, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:        aws.String(st.table()),
		Key:              map[string]ddbtypes.AttributeValue{"id": attrS(id)},
		UpdateExpression: aws.String("SET #st = :status"),
		ExpressionAttributeNames: map[string]string{
			"#st": "status",
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":status": attrS(failedStatus),
		},
	})
	return mapErr(err)
}

func (st *workerNotificationStore) IncrementAttempts(ctx context.Context, id string) error {
	_, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:        aws.String(st.table()),
		Key:              map[string]ddbtypes.AttributeValue{"id": attrS(id)},
		UpdateExpression: aws.String("SET attempts = attempts + :one"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":one": attrN(1),
		},
	})
	return mapErr(err)
}
