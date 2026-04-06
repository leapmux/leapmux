package dynamodb

import (
	"context"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

type workspaceTabStore struct{ s *dynamoStore }

var _ store.WorkspaceTabStore = (*workspaceTabStore)(nil)

func (st *workspaceTabStore) table() string { return st.s.table(tableWorkspaceTabs) }

func itemToWorkspaceTab(item map[string]ddbtypes.AttributeValue) store.WorkspaceTab {
	sk := getS(item, "tab_type#tab_id")
	tabTypeStr, tabID := parseTabSK(sk)
	tabTypeInt, _ := strconv.ParseInt(tabTypeStr, 10, 32)
	return store.WorkspaceTab{
		WorkspaceID: getS(item, "workspace_id"),
		WorkerID:    getS(item, "worker_id"),
		TabType:     leapmuxv1.TabType(tabTypeInt),
		TabID:       tabID,
		Position:    getS(item, "position"),
		TileID:      getS(item, "tile_id"),
	}
}

func (st *workspaceTabStore) Upsert(ctx context.Context, p store.UpsertWorkspaceTabParams) error {
	sk := tabSK(strconv.FormatInt(int64(p.TabType), 10), p.TabID)
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.table()),
		Item: map[string]ddbtypes.AttributeValue{
			"workspace_id":    attrS(p.WorkspaceID),
			"tab_type#tab_id": attrS(sk),
			"worker_id":       attrS(p.WorkerID),
			"position":        attrS(p.Position),
			"tile_id":         attrS(p.TileID),
		},
	}, "workspace_id", "tab_type#tab_id")
	return mapErr(err)
}

func (st *workspaceTabStore) BulkUpsert(ctx context.Context, params []store.UpsertWorkspaceTabParams) error {
	if len(params) == 0 {
		return nil
	}
	requests := make([]ddbtypes.WriteRequest, len(params))
	for i, p := range params {
		sk := tabSK(strconv.FormatInt(int64(p.TabType), 10), p.TabID)
		requests[i] = ddbtypes.WriteRequest{
			PutRequest: &ddbtypes.PutRequest{
				Item: map[string]ddbtypes.AttributeValue{
					"workspace_id":    attrS(p.WorkspaceID),
					"tab_type#tab_id": attrS(sk),
					"worker_id":       attrS(p.WorkerID),
					"position":        attrS(p.Position),
					"tile_id":         attrS(p.TileID),
				},
			},
		}
	}
	return st.s.batchWrite(ctx, st.table(), requests)
}

func (st *workspaceTabStore) Delete(ctx context.Context, p store.DeleteWorkspaceTabParams) error {
	sk := tabSK(strconv.FormatInt(int64(p.TabType), 10), p.TabID)
	_, err := st.s.deleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table()),
		Key: map[string]ddbtypes.AttributeValue{
			"workspace_id":    attrS(p.WorkspaceID),
			"tab_type#tab_id": attrS(sk),
		},
	})
	return mapErr(err)
}

func (st *workspaceTabStore) DeleteByWorker(ctx context.Context, workerID string) error {
	return deleteAllByGSI(ctx, st.s, st.table(), gsiWorkerID, "worker_id", workerID, "workspace_id", "tab_type#tab_id")
}

func (st *workspaceTabStore) DeleteByWorkspace(ctx context.Context, workspaceID string) error {
	return deleteAllByPK(ctx, st.s, st.table(), "workspace_id", workspaceID, "tab_type#tab_id")
}

func (st *workspaceTabStore) DeleteWorkerTabsForWorkspace(ctx context.Context, p store.DeleteWorkerTabsForWorkspaceParams) error {
	var keys []map[string]ddbtypes.AttributeValue
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		KeyConditionExpression: aws.String("workspace_id = :wsid"),
		FilterExpression:       aws.String("worker_id = :wid"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":wsid": attrS(p.WorkspaceID),
			":wid":  attrS(p.WorkerID),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		sk := getS(item, "tab_type#tab_id")
		keys = append(keys, map[string]ddbtypes.AttributeValue{
			"workspace_id":    attrS(p.WorkspaceID),
			"tab_type#tab_id": attrS(sk),
		})
		return true
	})
	if err != nil {
		return err
	}
	return st.s.batchDelete(ctx, st.table(), keys)
}

func (st *workspaceTabStore) ListByWorkspace(ctx context.Context, workspaceID string) ([]store.WorkspaceTab, error) {
	var tabs []store.WorkspaceTab
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		KeyConditionExpression: aws.String("workspace_id = :wsid"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":wsid": attrS(workspaceID),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		tabs = append(tabs, itemToWorkspaceTab(item))
		return true
	})
	if err != nil {
		return nil, err
	}
	return ptrconv.NonNil(tabs), nil
}

func (st *workspaceTabStore) ListByWorker(ctx context.Context, workerID string) ([]store.WorkspaceTab, error) {
	var tabs []store.WorkspaceTab
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiWorkerID),
		KeyConditionExpression: aws.String("worker_id = :wid"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":wid": attrS(workerID),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		tabs = append(tabs, itemToWorkspaceTab(item))
		return true
	})
	if err != nil {
		return nil, err
	}
	return ptrconv.NonNil(tabs), nil
}

func (st *workspaceTabStore) ListDistinctWorkersByWorkspace(ctx context.Context, workspaceID string) ([]string, error) {
	workerSet := make(map[string]bool)
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		KeyConditionExpression: aws.String("workspace_id = :wsid"),
		ProjectionExpression:   aws.String("worker_id"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":wsid": attrS(workspaceID),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		workerSet[getS(item, "worker_id")] = true
		return true
	})
	if err != nil {
		return nil, err
	}
	workers := []string{}
	for id := range workerSet {
		workers = append(workers, id)
	}
	return workers, nil
}

func (st *workspaceTabStore) GetMaxPosition(ctx context.Context, workspaceID string) (string, error) {
	maxPos := ""
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		KeyConditionExpression: aws.String("workspace_id = :wsid"),
		ProjectionExpression:   aws.String("#p"),
		ExpressionAttributeNames: map[string]string{
			"#p": "position",
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":wsid": attrS(workspaceID),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		pos := getS(item, "position")
		if pos > maxPos {
			maxPos = pos
		}
		return true
	})
	if err != nil {
		return "", err
	}
	return maxPos, nil
}
