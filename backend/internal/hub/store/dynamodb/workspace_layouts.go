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

func (st *workspaceLayoutStore) Get(ctx context.Context, workspaceID string) (*store.WorkspaceLayout, error) {
	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table()),
		Key:       map[string]ddbtypes.AttributeValue{"workspace_id": attrS(workspaceID)},
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if out.Item == nil {
		return nil, store.ErrNotFound
	}
	return &store.WorkspaceLayout{
		WorkspaceID: getS(out.Item, "workspace_id"),
		LayoutJSON:  getS(out.Item, "layout_json"),
		UpdatedAt:   getTime(out.Item, "updated_at"),
	}, nil
}

func (st *workspaceLayoutStore) Upsert(ctx context.Context, p store.UpsertWorkspaceLayoutParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.table()),
		Item: map[string]ddbtypes.AttributeValue{
			"workspace_id": attrS(p.WorkspaceID),
			"layout_json":  attrS(p.LayoutJSON),
			"updated_at":   attrS(timeToStr(now)),
		},
	}, "workspace_id")
	return mapErr(err)
}

func (st *workspaceLayoutStore) Delete(ctx context.Context, workspaceID string) error {
	_, err := st.s.deleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table()),
		Key:       map[string]ddbtypes.AttributeValue{"workspace_id": attrS(workspaceID)},
	})
	return mapErr(err)
}
