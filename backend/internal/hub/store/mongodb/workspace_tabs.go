package mongodb

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

func workspaceTabID(workspaceID string, tabType leapmuxv1.TabType, tabID string) string {
	return compoundID(workspaceID, fmt.Sprintf("%d", tabType), tabID)
}

func docToWorkspaceTab(m bson.M) store.WorkspaceTab {
	return store.WorkspaceTab{
		WorkspaceID: getS(m, "workspace_id"),
		WorkerID:    getS(m, "worker_id"),
		TabType:     leapmuxv1.TabType(getInt32(m, "tab_type")),
		TabID:       getS(m, "tab_id"),
		Position:    getS(m, "position"),
		TileID:      getS(m, "tile_id"),
	}
}

func (st *workspaceTabStore) Upsert(ctx context.Context, p store.UpsertWorkspaceTabParams) error {
	id := workspaceTabID(p.WorkspaceID, p.TabType, p.TabID)
	doc := bson.D{
		{Key: "_id", Value: id},
		{Key: "workspace_id", Value: p.WorkspaceID},
		{Key: "worker_id", Value: p.WorkerID},
		{Key: "tab_type", Value: int32(p.TabType)},
		{Key: "tab_id", Value: p.TabID},
		{Key: "position", Value: p.Position},
		{Key: "tile_id", Value: p.TileID},
	}
	filter := bson.D{{Key: "_id", Value: id}}
	opts := options.Replace().SetUpsert(true)
	_, err := st.s.collection(colWorkspaceTabs).ReplaceOne(ctx, filter, doc, opts)
	return mapErr(err)
}

func (st *workspaceTabStore) BulkUpsert(ctx context.Context, params []store.UpsertWorkspaceTabParams) error {
	if len(params) == 0 {
		return nil
	}
	models := make([]mongo.WriteModel, len(params))
	for i, p := range params {
		id := workspaceTabID(p.WorkspaceID, p.TabType, p.TabID)
		doc := bson.D{
			{Key: "_id", Value: id},
			{Key: "workspace_id", Value: p.WorkspaceID},
			{Key: "worker_id", Value: p.WorkerID},
			{Key: "tab_type", Value: int32(p.TabType)},
			{Key: "tab_id", Value: p.TabID},
			{Key: "position", Value: p.Position},
			{Key: "tile_id", Value: p.TileID},
		}
		filter := bson.D{{Key: "_id", Value: id}}
		models[i] = mongo.NewReplaceOneModel().
			SetFilter(filter).
			SetReplacement(doc).
			SetUpsert(true)
	}
	_, err := st.s.collection(colWorkspaceTabs).BulkWrite(ctx, models)
	return mapErr(err)
}

func (st *workspaceTabStore) Delete(ctx context.Context, p store.DeleteWorkspaceTabParams) error {
	id := workspaceTabID(p.WorkspaceID, p.TabType, p.TabID)
	filter := bson.D{{Key: "_id", Value: id}}
	_, err := st.s.collection(colWorkspaceTabs).DeleteOne(ctx, filter)
	return mapErr(err)
}

func (st *workspaceTabStore) DeleteByWorker(ctx context.Context, workerID string) error {
	filter := bson.D{{Key: "worker_id", Value: workerID}}
	_, err := st.s.collection(colWorkspaceTabs).DeleteMany(ctx, filter)
	return mapErr(err)
}

func (st *workspaceTabStore) DeleteByWorkspace(ctx context.Context, workspaceID string) error {
	filter := bson.D{{Key: "workspace_id", Value: workspaceID}}
	_, err := st.s.collection(colWorkspaceTabs).DeleteMany(ctx, filter)
	return mapErr(err)
}

func (st *workspaceTabStore) DeleteWorkerTabsForWorkspace(ctx context.Context, p store.DeleteWorkerTabsForWorkspaceParams) error {
	filter := bson.D{
		{Key: "worker_id", Value: p.WorkerID},
		{Key: "workspace_id", Value: p.WorkspaceID},
	}
	_, err := st.s.collection(colWorkspaceTabs).DeleteMany(ctx, filter)
	return mapErr(err)
}

func (st *workspaceTabStore) ListByWorkspace(ctx context.Context, workspaceID string) ([]store.WorkspaceTab, error) {
	filter := bson.D{{Key: "workspace_id", Value: workspaceID}}
	cursor, err := st.s.collection(colWorkspaceTabs).Find(ctx, filter)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var results []store.WorkspaceTab
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		results = append(results, docToWorkspaceTab(m))
	}
	if err := cursor.Err(); err != nil {
		return nil, mapErr(err)
	}
	return ptrconv.NonNil(results), nil
}

func (st *workspaceTabStore) ListByWorker(ctx context.Context, workerID string) ([]store.WorkspaceTab, error) {
	filter := bson.D{{Key: "worker_id", Value: workerID}}
	cursor, err := st.s.collection(colWorkspaceTabs).Find(ctx, filter)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var results []store.WorkspaceTab
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		results = append(results, docToWorkspaceTab(m))
	}
	if err := cursor.Err(); err != nil {
		return nil, mapErr(err)
	}
	return ptrconv.NonNil(results), nil
}

func (st *workspaceTabStore) ListDistinctWorkersByWorkspace(ctx context.Context, workspaceID string) ([]string, error) {
	filter := bson.D{{Key: "workspace_id", Value: workspaceID}}
	dr := st.s.collection(colWorkspaceTabs).Distinct(ctx, "worker_id", filter)

	var result []string
	if err := dr.Decode(&result); err != nil {
		return nil, mapErr(err)
	}
	return ptrconv.NonNil(result), nil
}

func (st *workspaceTabStore) GetMaxPosition(ctx context.Context, workspaceID string) (string, error) {
	filter := bson.D{{Key: "workspace_id", Value: workspaceID}}
	opts := options.FindOne().SetSort(bson.D{{Key: "position", Value: -1}})
	var m bson.M
	err := st.s.collection(colWorkspaceTabs).FindOne(ctx, filter, opts).Decode(&m)
	if err != nil {
		if errors.Is(mapErr(err), store.ErrNotFound) {
			return "", nil
		}
		return "", mapErr(err)
	}
	return getS(m, "position"), nil
}
