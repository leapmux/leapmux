package dynamodb

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/leapmux/leapmux/internal/hub/store"
)

type workspaceLayoutStore struct{ s *dynamoStore }

var _ store.WorkspaceLayoutStore = (*workspaceLayoutStore)(nil)

func (st *workspaceLayoutStore) table() string { return st.s.table(tableWorkspaceLayouts) }

func itemToWorkspaceLayout(item map[string]ddbtypes.AttributeValue) (*store.WorkspaceLayout, error) {
	workspaceID, err := mustGetS(item, attrWorkspaceID)
	if err != nil {
		return nil, err
	}
	layoutJSON, err := mustGetS(item, attrLayoutJSON)
	if err != nil {
		return nil, err
	}
	updatedAt, err := mustGetTime(item, attrUpdatedAt)
	if err != nil {
		return nil, err
	}
	return &store.WorkspaceLayout{
		WorkspaceID: workspaceID,
		LayoutJSON:  layoutJSON,
		UpdatedAt:   updatedAt,
	}, nil
}

func (st *workspaceLayoutStore) Get(ctx context.Context, workspaceID string) (*store.WorkspaceLayout, error) {
	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table()),
		Key:       map[string]ddbtypes.AttributeValue{attrWorkspaceID: attrS(workspaceID)},
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if out.Item == nil {
		return nil, store.ErrNotFound
	}
	l, err := itemToWorkspaceLayout(out.Item)
	if err != nil {
		return nil, err
	}
	return l, nil
}

func (st *workspaceLayoutStore) Upsert(ctx context.Context, p store.UpsertWorkspaceLayoutParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.table()),
		Item: map[string]ddbtypes.AttributeValue{
			attrWorkspaceID: attrS(p.WorkspaceID),
			attrLayoutJSON:  attrS(p.LayoutJSON),
			attrUpdatedAt:   attrS(timeToStr(now)),
		},
	}, attrWorkspaceID)
	return mapErr(err)
}

func (st *workspaceLayoutStore) Delete(ctx context.Context, workspaceID string) error {
	_, err := st.s.deleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table()),
		Key:       map[string]ddbtypes.AttributeValue{attrWorkspaceID: attrS(workspaceID)},
	})
	return mapErr(err)
}
