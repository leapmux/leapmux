package dynamodb

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

type workerAccessGrantStore struct{ s *dynamoStore }

var _ store.WorkerAccessGrantStore = (*workerAccessGrantStore)(nil)

func (st *workerAccessGrantStore) table() string { return st.s.table(tableWorkerGrants) }

func itemToWorkerAccessGrant(item map[string]ddbtypes.AttributeValue) (store.WorkerAccessGrant, error) {
	workerID, err := mustGetS(item, attrWorkerID)
	if err != nil {
		return store.WorkerAccessGrant{}, err
	}
	userID, err := mustGetS(item, attrUserID)
	if err != nil {
		return store.WorkerAccessGrant{}, err
	}
	grantedBy, err := mustGetS(item, attrGrantedBy)
	if err != nil {
		return store.WorkerAccessGrant{}, err
	}
	createdAt, err := mustGetTime(item, attrCreatedAt)
	if err != nil {
		return store.WorkerAccessGrant{}, err
	}
	return store.WorkerAccessGrant{
		WorkerID:  workerID,
		UserID:    userID,
		GrantedBy: grantedBy,
		CreatedAt: createdAt,
	}, nil
}

func (st *workerAccessGrantStore) Grant(ctx context.Context, p store.GrantWorkerAccessParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.table()),
		Item: map[string]ddbtypes.AttributeValue{
			attrWorkerID:  attrS(p.WorkerID),
			attrUserID:    attrS(p.UserID),
			attrGrantedBy: attrS(p.GrantedBy),
			attrCreatedAt: attrS(timeToStr(now)),
		},
	}, attrWorkerID, attrUserID)
	return mapErr(err)
}

func (st *workerAccessGrantStore) Revoke(ctx context.Context, p store.RevokeWorkerAccessParams) error {
	_, err := st.s.deleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table()),
		Key: map[string]ddbtypes.AttributeValue{
			attrWorkerID: attrS(p.WorkerID),
			attrUserID:   attrS(p.UserID),
		},
	})
	return mapErr(err)
}

func (st *workerAccessGrantStore) List(ctx context.Context, workerID string) ([]store.WorkerAccessGrant, error) {
	var grants []store.WorkerAccessGrant
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		KeyConditionExpression: aws.String("worker_id = :wid"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":wid": attrS(workerID),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		g, err := itemToWorkerAccessGrant(item)
		if err != nil {
			return false
		}
		grants = append(grants, g)
		return true
	})
	if err != nil {
		return nil, err
	}
	return ptrconv.NonNil(grants), nil
}

func (st *workerAccessGrantStore) HasAccess(ctx context.Context, p store.HasWorkerAccessParams) (bool, error) {
	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table()),
		Key: map[string]ddbtypes.AttributeValue{
			attrWorkerID: attrS(p.WorkerID),
			attrUserID:   attrS(p.UserID),
		},
		ProjectionExpression: aws.String(attrWorkerID),
	})
	if err != nil {
		return false, mapErr(err)
	}
	return out.Item != nil, nil
}

func (st *workerAccessGrantStore) DeleteByWorker(ctx context.Context, workerID string) error {
	return deleteAllByPK(ctx, st.s, st.table(), attrWorkerID, workerID, attrUserID)
}

func (st *workerAccessGrantStore) DeleteByUser(ctx context.Context, userID string) error {
	return deleteAllByGSI(ctx, st.s, st.table(), gsiUserID, attrUserID, userID, attrWorkerID, attrUserID)
}

func (st *workerAccessGrantStore) DeleteByUserInOrg(ctx context.Context, p store.DeleteWorkerAccessGrantsByUserInOrgParams) error {
	// 1. Collect all grant worker IDs for this user.
	var workerIDs []string
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiUserID),
		KeyConditionExpression: aws.String("user_id = :uid"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":uid": attrS(p.UserID),
		},
		ProjectionExpression: aws.String(attrWorkerID),
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		workerIDs = append(workerIDs, getS(item, attrWorkerID))
		return true
	})
	if err != nil {
		return err
	}
	if len(workerIDs) == 0 {
		return nil
	}

	// 2. Batch-fetch registered_by for all workers.
	workerKeys := make([]map[string]ddbtypes.AttributeValue, len(workerIDs))
	for i, wid := range workerIDs {
		workerKeys[i] = map[string]ddbtypes.AttributeValue{attrID: attrS(wid)}
	}
	workerItems, err := st.s.batchGetItemsProjected(ctx, st.s.table(tableWorkers), workerKeys, attrID+", "+attrRegisteredBy)
	if err != nil {
		return err
	}

	// Build a map of workerID → registeredBy, and collect unique registeredBy IDs.
	workerOwner := make(map[string]string, len(workerItems))
	var ownerIDs []string
	seenOwners := make(map[string]bool)
	for _, item := range workerItems {
		wid := getS(item, attrID)
		rb := getS(item, attrRegisteredBy)
		workerOwner[wid] = rb
		if !seenOwners[rb] {
			seenOwners[rb] = true
			ownerIDs = append(ownerIDs, rb)
		}
	}

	// 3. Batch-check which owners are members of the org.
	memberKeys := make([]map[string]ddbtypes.AttributeValue, len(ownerIDs))
	for i, uid := range ownerIDs {
		memberKeys[i] = map[string]ddbtypes.AttributeValue{
			attrOrgID:  attrS(p.OrgID),
			attrUserID: attrS(uid),
		}
	}
	memberItems, err := st.s.batchGetItemsProjected(ctx, st.s.table(tableOrgMembers), memberKeys, attrUserID)
	if err != nil {
		return err
	}
	orgMembers := make(map[string]bool, len(memberItems))
	for _, item := range memberItems {
		orgMembers[getS(item, attrUserID)] = true
	}

	// 4. Delete grants whose worker owner is in the org.
	var deleteKeys []map[string]ddbtypes.AttributeValue
	for _, wid := range workerIDs {
		if orgMembers[workerOwner[wid]] {
			deleteKeys = append(deleteKeys, map[string]ddbtypes.AttributeValue{
				attrWorkerID: attrS(wid),
				attrUserID:   attrS(p.UserID),
			})
		}
	}
	return st.s.batchDelete(ctx, st.table(), deleteKeys)
}
