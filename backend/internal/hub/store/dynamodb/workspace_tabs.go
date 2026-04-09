package dynamodb

import (
	"context"
	"fmt"
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

func itemToWorkspaceTab(item map[string]ddbtypes.AttributeValue) (store.WorkspaceTab, error) {
	sk, err := mustGetS(item, attrTabTypeSK)
	if err != nil {
		return store.WorkspaceTab{}, err
	}
	tabTypeStr, tabID := parseTabSK(sk)
	tabTypeInt, err := strconv.ParseInt(tabTypeStr, 10, 32)
	if err != nil {
		return store.WorkspaceTab{}, fmt.Errorf("attribute %q: %w", attrTabTypeSK, err)
	}
	workspaceID, err := mustGetS(item, attrWorkspaceID)
	if err != nil {
		return store.WorkspaceTab{}, err
	}
	workerID, err := mustGetS(item, attrWorkerID)
	if err != nil {
		return store.WorkspaceTab{}, err
	}
	position, err := mustGetS(item, attrPosition)
	if err != nil {
		return store.WorkspaceTab{}, err
	}
	tileID, err := mustGetS(item, attrTileID)
	if err != nil {
		return store.WorkspaceTab{}, err
	}
	return store.WorkspaceTab{
		WorkspaceID: workspaceID,
		WorkerID:    workerID,
		TabType:     leapmuxv1.TabType(tabTypeInt),
		TabID:       tabID,
		Position:    position,
		TileID:      tileID,
	}, nil
}

func (st *workspaceTabStore) Upsert(ctx context.Context, p store.UpsertWorkspaceTabParams) error {
	sk := tabSK(strconv.FormatInt(int64(p.TabType), 10), p.TabID)
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.table()),
		Item: map[string]ddbtypes.AttributeValue{
			attrWorkspaceID: attrS(p.WorkspaceID),
			attrTabTypeSK:   attrS(sk),
			attrWorkerID:    attrS(p.WorkerID),
			attrPosition:    attrS(p.Position),
			attrTileID:      attrS(p.TileID),
		},
	}, attrWorkspaceID, attrTabTypeSK)
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
					attrWorkspaceID: attrS(p.WorkspaceID),
					attrTabTypeSK:   attrS(sk),
					attrWorkerID:    attrS(p.WorkerID),
					attrPosition:    attrS(p.Position),
					attrTileID:      attrS(p.TileID),
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
			attrWorkspaceID: attrS(p.WorkspaceID),
			attrTabTypeSK:   attrS(sk),
		},
	})
	return mapErr(err)
}

func (st *workspaceTabStore) DeleteByWorker(ctx context.Context, workerID string) error {
	return deleteAllByGSI(ctx, st.s, st.table(), gsiWorkerID, attrWorkerID, workerID, attrWorkspaceID, attrTabTypeSK)
}

func (st *workspaceTabStore) DeleteByWorkspace(ctx context.Context, workspaceID string) error {
	return deleteAllByPK(ctx, st.s, st.table(), attrWorkspaceID, workspaceID, attrTabTypeSK)
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
		sk := getS(item, attrTabTypeSK)
		keys = append(keys, map[string]ddbtypes.AttributeValue{
			attrWorkspaceID: attrS(p.WorkspaceID),
			attrTabTypeSK:   attrS(sk),
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
		t, err := itemToWorkspaceTab(item)
		if err != nil {
			return false
		}
		tabs = append(tabs, t)
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
		t, err := itemToWorkspaceTab(item)
		if err != nil {
			return false
		}
		tabs = append(tabs, t)
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
		ProjectionExpression:   aws.String(attrWorkerID),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":wsid": attrS(workspaceID),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		workerSet[getS(item, attrWorkerID)] = true
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
			"#p": attrPosition,
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":wsid": attrS(workspaceID),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		pos := getS(item, attrPosition)
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
