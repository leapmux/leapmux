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

type registrationStore struct{ s *dynamoStore }

var _ store.RegistrationStore = (*registrationStore)(nil)

func (st *registrationStore) table() string { return st.s.table(tableRegistrations) }

func itemToWorkerRegistration(item map[string]ddbtypes.AttributeValue) *store.WorkerRegistration {
	return &store.WorkerRegistration{
		ID:              getS(item, "id"),
		Version:         getS(item, "version"),
		PublicKey:       getBytes(item, "public_key"),
		MlkemPublicKey:  getBytes(item, "mlkem_public_key"),
		SlhdsaPublicKey: getBytes(item, "slhdsa_public_key"),
		Status:          leapmuxv1.RegistrationStatus(getSAsInt64(item, "status")),
		WorkerID:        ptrconv.StringToPtr(getS(item, "worker_id")),
		ApprovedBy:      ptrconv.StringToPtr(getS(item, "approved_by")),
		ExpiresAt:       getTime(item, "expires_at"),
		CreatedAt:       getTime(item, "created_at"),
	}
}

func (st *registrationStore) Create(ctx context.Context, p store.CreateRegistrationParams) error {
	now := time.Now().UTC()
	pendingStatus := strconv.FormatInt(int64(leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING), 10)
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(st.table()),
		ConditionExpression: aws.String("attribute_not_exists(id)"),
		Item: map[string]ddbtypes.AttributeValue{
			"id":                attrS(p.ID),
			"version":           attrS(p.Version),
			"public_key":        attrB(p.PublicKey),
			"mlkem_public_key":  attrB(p.MlkemPublicKey),
			"slhdsa_public_key": attrB(p.SlhdsaPublicKey),
			"status":            attrS(pendingStatus),
			"expires_at":        attrS(timeToStr(p.ExpiresAt)),
			"created_at":        attrS(timeToStr(now)),
		},
	}, "id")
	return mapErr(err)
}

func (st *registrationStore) GetByID(ctx context.Context, id string) (*store.WorkerRegistration, error) {
	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table()),
		Key:       map[string]ddbtypes.AttributeValue{"id": attrS(id)},
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if out.Item == nil {
		return nil, store.ErrNotFound
	}
	return itemToWorkerRegistration(out.Item), nil
}

func (st *registrationStore) Approve(ctx context.Context, p store.ApproveRegistrationParams) error {
	approvedStatus := strconv.FormatInt(int64(leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED), 10)
	updateExpr := "SET #st = :status"
	exprValues := map[string]ddbtypes.AttributeValue{
		":status": attrS(approvedStatus),
	}
	exprNames := map[string]string{
		"#st": "status",
	}

	if p.WorkerID != nil {
		updateExpr += ", worker_id = :wid"
		exprValues[":wid"] = attrS(*p.WorkerID)
	}
	if p.ApprovedBy != nil {
		updateExpr += ", approved_by = :ab"
		exprValues[":ab"] = attrS(*p.ApprovedBy)
	}

	_, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(st.table()),
		Key:                       map[string]ddbtypes.AttributeValue{"id": attrS(p.ID)},
		UpdateExpression:          aws.String(updateExpr),
		ExpressionAttributeNames:  exprNames,
		ExpressionAttributeValues: exprValues,
	})
	return mapErr(err)
}

func (st *registrationStore) ExpirePending(ctx context.Context) error {
	now := timeToStr(time.Now().UTC())
	pendingStatus := strconv.FormatInt(int64(leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING), 10)
	expiredStatus := strconv.FormatInt(int64(leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_EXPIRED), 10)

	// Collect all expired pending registration IDs first, then update them
	// in a separate phase to avoid interleaving reads and writes.
	var ids []string
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiStatus),
		KeyConditionExpression: aws.String("#st = :pending"),
		FilterExpression:       aws.String("expires_at < :now"),
		ProjectionExpression:   aws.String("id"),
		ExpressionAttributeNames: map[string]string{
			"#st": "status",
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":pending": attrS(pendingStatus),
			":now":     attrS(now),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		ids = append(ids, getS(item, "id"))
		return len(ids) < store.CleanupBatchLimit
	})
	if err != nil {
		return err
	}

	var firstErr error
	for _, id := range ids {
		_, err = st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
			TableName:        aws.String(st.table()),
			Key:              map[string]ddbtypes.AttributeValue{"id": attrS(id)},
			UpdateExpression: aws.String("SET #st = :expired"),
			ExpressionAttributeNames: map[string]string{
				"#st": "status",
			},
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":expired": attrS(expiredStatus),
			},
		})
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
