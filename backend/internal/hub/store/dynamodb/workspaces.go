package dynamodb

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/leapmux/leapmux/internal/hub/store"
)

type workspaceStore struct{ s *dynamoStore }

var _ store.WorkspaceStore = (*workspaceStore)(nil)

func (st *workspaceStore) table() string { return st.s.table(tableWorkspaces) }

func workspaceToItem(p store.CreateWorkspaceParams, now time.Time) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		attrID:          attrS(p.ID),
		attrOrgID:       attrS(p.OrgID),
		attrOwnerUserID: attrS(p.OwnerUserID),
		attrTitle:       attrS(p.Title),
		attrIsDeleted:   attrBool(false),
		attrCreatedAt:   attrS(timeToStr(now)),
	}
}

func itemToWorkspace(item map[string]ddbtypes.AttributeValue) (*store.Workspace, error) {
	id, err := mustGetS(item, attrID)
	if err != nil {
		return nil, err
	}
	orgID, err := mustGetS(item, attrOrgID)
	if err != nil {
		return nil, err
	}
	ownerUserID, err := mustGetS(item, attrOwnerUserID)
	if err != nil {
		return nil, err
	}
	title, err := mustGetS(item, attrTitle)
	if err != nil {
		return nil, err
	}
	isDeleted, err := mustGetBool(item, attrIsDeleted)
	if err != nil {
		return nil, err
	}
	createdAt, err := mustGetTime(item, attrCreatedAt)
	if err != nil {
		return nil, err
	}
	return &store.Workspace{
		ID:          id,
		OrgID:       orgID,
		OwnerUserID: ownerUserID,
		Title:       title,
		IsDeleted:   isDeleted,
		CreatedAt:   createdAt,
		DeletedAt:   getTimePtr(item, attrDeletedAt),
	}, nil
}

func (st *workspaceStore) Create(ctx context.Context, p store.CreateWorkspaceParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(st.table()),
		Item:                workspaceToItem(p, now),
		ConditionExpression: aws.String("attribute_not_exists(id)"),
	}, attrID)
	return mapErr(err)
}

func (st *workspaceStore) GetByID(ctx context.Context, id string) (*store.Workspace, error) {
	w, err := st.GetByIDIncludeDeleted(ctx, id)
	if err != nil {
		return nil, err
	}
	if w.IsDeleted {
		return nil, store.ErrNotFound
	}
	return w, nil
}

func (st *workspaceStore) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.Workspace, error) {
	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table()),
		Key:       map[string]ddbtypes.AttributeValue{attrID: attrS(id)},
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if out.Item == nil {
		return nil, store.ErrNotFound
	}
	w, err := itemToWorkspace(out.Item)
	if err != nil {
		return nil, err
	}
	return w, nil
}

func (st *workspaceStore) ListAccessible(ctx context.Context, p store.ListAccessibleWorkspacesParams) ([]store.Workspace, error) {
	workspaceMap := make(map[string]*store.Workspace)

	// 1. Workspaces owned by user in the org.
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiOrgOwner),
		KeyConditionExpression: aws.String("org_id = :orgID AND owner_user_id = :uid"),
		FilterExpression:       aws.String("is_deleted = :false"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":orgID": attrS(p.OrgID),
			":uid":   attrS(p.UserID),
			":false": attrBool(false),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		w, err := itemToWorkspace(item)
		if err != nil {
			return false
		}
		workspaceMap[w.ID] = w
		return true
	})
	if err != nil {
		return nil, err
	}

	// 2. Workspaces shared with user (via workspace_access).
	var sharedWSIDs []string
	err = st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.s.table(tableWorkspaceAccess)),
		IndexName:              aws.String(gsiUserID),
		KeyConditionExpression: aws.String("user_id = :uid"),
		ProjectionExpression:   aws.String(attrWorkspaceID),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":uid": attrS(p.UserID),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		wsID := getS(item, attrWorkspaceID)
		if _, exists := workspaceMap[wsID]; !exists {
			sharedWSIDs = append(sharedWSIDs, wsID)
		}
		return true
	})
	if err != nil {
		return nil, err
	}

	// Batch-fetch shared workspaces.
	if len(sharedWSIDs) > 0 {
		keys := make([]map[string]ddbtypes.AttributeValue, len(sharedWSIDs))
		for i, id := range sharedWSIDs {
			keys[i] = map[string]ddbtypes.AttributeValue{attrID: attrS(id)}
		}
		items, err := st.s.batchGetItems(ctx, st.table(), keys)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			w, err := itemToWorkspace(item)
			if err != nil {
				return nil, err
			}
			if !w.IsDeleted && w.OrgID == p.OrgID {
				workspaceMap[w.ID] = w
			}
		}
	}

	result := []store.Workspace{}
	for _, w := range workspaceMap {
		result = append(result, *w)
	}
	return result, nil
}

func (st *workspaceStore) Rename(ctx context.Context, p store.RenameWorkspaceParams) (int64, error) {
	out, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{attrID: attrS(p.ID)},
		UpdateExpression:    aws.String("SET title = :t"),
		ConditionExpression: aws.String("attribute_exists(id) AND owner_user_id = :uid AND is_deleted = :false"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":t":     attrS(p.Title),
			":uid":   attrS(p.OwnerUserID),
			":false": attrBool(false),
		},
		ReturnValues: ddbtypes.ReturnValueAllNew,
	})
	if err != nil {
		if isConditionFailed(err) {
			return 0, nil
		}
		return 0, mapErr(err)
	}
	if out.Attributes == nil {
		return 0, nil
	}
	return 1, nil
}

func (st *workspaceStore) SoftDelete(ctx context.Context, p store.SoftDeleteWorkspaceParams) (int64, error) {
	now := timeToStr(time.Now().UTC())
	out, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{attrID: attrS(p.ID)},
		UpdateExpression:    aws.String("SET is_deleted = :true, deleted_at = :now, deleted = :del"),
		ConditionExpression: aws.String("attribute_exists(id) AND owner_user_id = :uid AND is_deleted = :false"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":true":  attrBool(true),
			":now":   attrS(now),
			":del":   attrS(deletedTrue),
			":uid":   attrS(p.OwnerUserID),
			":false": attrBool(false),
		},
		ReturnValues: ddbtypes.ReturnValueAllNew,
	})
	if err != nil {
		if isConditionFailed(err) {
			return 0, nil
		}
		return 0, mapErr(err)
	}
	if out.Attributes == nil {
		return 0, nil
	}
	return 1, nil
}

func (st *workspaceStore) SoftDeleteAllByUser(ctx context.Context, ownerUserID string) error {
	now := timeToStr(time.Now().UTC())
	// Query the owner_user_id GSI instead of scanning the full table.
	var updateErr error
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiOwnerUserID),
		KeyConditionExpression: aws.String("owner_user_id = :uid"),
		FilterExpression:       aws.String("is_deleted = :false"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":uid":   attrS(ownerUserID),
			":false": attrBool(false),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		id := getS(item, attrID)
		if _, updateErr = st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
			TableName:        aws.String(st.table()),
			Key:              map[string]ddbtypes.AttributeValue{attrID: attrS(id)},
			UpdateExpression: aws.String("SET is_deleted = :true, deleted_at = :now, deleted = :del"),
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":true": attrBool(true),
				":now":  attrS(now),
				":del":  attrS(deletedTrue),
			},
		}); updateErr != nil {
			return false
		}
		return true
	})
	if err != nil {
		return err
	}
	if updateErr != nil {
		return mapErr(updateErr)
	}
	return nil
}
