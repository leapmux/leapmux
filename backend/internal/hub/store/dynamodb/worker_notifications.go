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

func itemToWorkerNotification(item map[string]ddbtypes.AttributeValue) (store.WorkerNotification, error) {
	id, err := mustGetS(item, attrID)
	if err != nil {
		return store.WorkerNotification{}, err
	}
	workerID, err := mustGetS(item, attrWorkerID)
	if err != nil {
		return store.WorkerNotification{}, err
	}
	typ, err := mustGetN(item, attrType)
	if err != nil {
		return store.WorkerNotification{}, err
	}
	payload, err := mustGetS(item, attrPayload)
	if err != nil {
		return store.WorkerNotification{}, err
	}
	status, err := mustGetSAsInt64(item, attrStatus)
	if err != nil {
		return store.WorkerNotification{}, err
	}
	attempts, err := mustGetN(item, attrAttempts)
	if err != nil {
		return store.WorkerNotification{}, err
	}
	maxAttempts, err := mustGetN(item, attrMaxAttempts)
	if err != nil {
		return store.WorkerNotification{}, err
	}
	createdAt, err := mustGetTime(item, attrCreatedAt)
	if err != nil {
		return store.WorkerNotification{}, err
	}
	return store.WorkerNotification{
		ID:          id,
		WorkerID:    workerID,
		Type:        leapmuxv1.NotificationType(typ),
		Payload:     payload,
		Status:      leapmuxv1.NotificationStatus(status),
		Attempts:    attempts,
		MaxAttempts: maxAttempts,
		CreatedAt:   createdAt,
		DeliveredAt: getTimePtr(item, attrDeliveredAt),
	}, nil
}

func (st *workerNotificationStore) Create(ctx context.Context, p store.CreateWorkerNotificationParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(st.table()),
		ConditionExpression: aws.String("attribute_not_exists(id)"),
		Item: map[string]ddbtypes.AttributeValue{
			attrID:          attrS(p.ID),
			attrWorkerID:    attrS(p.WorkerID),
			attrType:        attrN(int64(p.Type)),
			attrPayload:     attrS(p.Payload),
			attrStatus:      attrS(strconv.FormatInt(int64(leapmuxv1.NotificationStatus_NOTIFICATION_STATUS_PENDING), 10)),
			attrAttempts:    attrN(0),
			attrMaxAttempts: attrN(3),
			attrCreatedAt:   attrS(timeToStr(now)),
		},
	}, attrID)
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
			"#st": attrStatus,
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":wid":     attrS(workerID),
			":pending": attrS(pendingStatus),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		n, err := itemToWorkerNotification(item)
		if err != nil {
			return false
		}
		notifications = append(notifications, n)
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
		Key:              map[string]ddbtypes.AttributeValue{attrID: attrS(id)},
		UpdateExpression: aws.String("SET #st = :status, delivered_at = :now"),
		ExpressionAttributeNames: map[string]string{
			"#st": attrStatus,
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
		Key:              map[string]ddbtypes.AttributeValue{attrID: attrS(id)},
		UpdateExpression: aws.String("SET #st = :status"),
		ExpressionAttributeNames: map[string]string{
			"#st": attrStatus,
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
		Key:              map[string]ddbtypes.AttributeValue{attrID: attrS(id)},
		UpdateExpression: aws.String("SET attempts = attempts + :one"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":one": attrN(1),
		},
	})
	return mapErr(err)
}
