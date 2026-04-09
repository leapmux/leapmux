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

type workspaceAccessStore struct{ s *dynamoStore }

var _ store.WorkspaceAccessStore = (*workspaceAccessStore)(nil)

func (st *workspaceAccessStore) table() string { return st.s.table(tableWorkspaceAccess) }

func itemToWorkspaceAccess(item map[string]ddbtypes.AttributeValue) (store.WorkspaceAccess, error) {
	workspaceID, err := mustGetS(item, attrWorkspaceID)
	if err != nil {
		return store.WorkspaceAccess{}, err
	}
	userID, err := mustGetS(item, attrUserID)
	if err != nil {
		return store.WorkspaceAccess{}, err
	}
	createdAt, err := mustGetTime(item, attrCreatedAt)
	if err != nil {
		return store.WorkspaceAccess{}, err
	}
	return store.WorkspaceAccess{
		WorkspaceID: workspaceID,
		UserID:      userID,
		CreatedAt:   createdAt,
	}, nil
}

func (st *workspaceAccessStore) Grant(ctx context.Context, p store.GrantWorkspaceAccessParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.table()),
		Item: map[string]ddbtypes.AttributeValue{
			attrWorkspaceID: attrS(p.WorkspaceID),
			attrUserID:      attrS(p.UserID),
			attrCreatedAt:   attrS(timeToStr(now)),
		},
	}, attrWorkspaceID, attrUserID)
	return mapErr(err)
}

func (st *workspaceAccessStore) BulkGrant(ctx context.Context, params []store.GrantWorkspaceAccessParams) error {
	if len(params) == 0 {
		return nil
	}
	now := time.Now().UTC()
	requests := make([]ddbtypes.WriteRequest, len(params))
	for i, p := range params {
		requests[i] = ddbtypes.WriteRequest{
			PutRequest: &ddbtypes.PutRequest{
				Item: map[string]ddbtypes.AttributeValue{
					attrWorkspaceID: attrS(p.WorkspaceID),
					attrUserID:      attrS(p.UserID),
					attrCreatedAt:   attrS(timeToStr(now)),
				},
			},
		}
	}
	return st.s.batchWrite(ctx, st.table(), requests)
}

func (st *workspaceAccessStore) Revoke(ctx context.Context, p store.RevokeWorkspaceAccessParams) error {
	_, err := st.s.deleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table()),
		Key: map[string]ddbtypes.AttributeValue{
			attrWorkspaceID: attrS(p.WorkspaceID),
			attrUserID:      attrS(p.UserID),
		},
	})
	return mapErr(err)
}

func (st *workspaceAccessStore) ListByWorkspaceID(ctx context.Context, workspaceID string) ([]store.WorkspaceAccess, error) {
	var result []store.WorkspaceAccess
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		KeyConditionExpression: aws.String("workspace_id = :wsid"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":wsid": attrS(workspaceID),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		a, err := itemToWorkspaceAccess(item)
		if err != nil {
			return false
		}
		result = append(result, a)
		return true
	})
	if err != nil {
		return nil, err
	}
	return ptrconv.NonNil(result), nil
}

func (st *workspaceAccessStore) HasAccess(ctx context.Context, p store.HasWorkspaceAccessParams) (bool, error) {
	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table()),
		Key: map[string]ddbtypes.AttributeValue{
			attrWorkspaceID: attrS(p.WorkspaceID),
			attrUserID:      attrS(p.UserID),
		},
		ProjectionExpression: aws.String(attrWorkspaceID),
	})
	if err != nil {
		return false, mapErr(err)
	}
	return out.Item != nil, nil
}

func (st *workspaceAccessStore) Clear(ctx context.Context, workspaceID string) error {
	return deleteAllByPK(ctx, st.s, st.table(), attrWorkspaceID, workspaceID, attrUserID)
}
