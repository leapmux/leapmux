package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/leapmux/leapmux/internal/hub/store"
)

func (st *workspaceLayoutStore) Get(ctx context.Context, workspaceID string) (*store.WorkspaceLayout, error) {
	filter := bson.D{{Key: "_id", Value: workspaceID}}
	var m bson.M
	err := st.s.collection(colWorkspaceLayouts).FindOne(ctx, filter).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	l := store.WorkspaceLayout{
		WorkspaceID: getS(m, "_id"),
		LayoutJSON:  getS(m, "layout_json"),
		UpdatedAt:   getTime(m, "updated_at"),
	}
	return &l, nil
}

func (st *workspaceLayoutStore) Upsert(ctx context.Context, p store.UpsertWorkspaceLayoutParams) error {
	now := truncateMS(time.Now().UTC())
	doc := bson.D{
		{Key: "_id", Value: p.WorkspaceID},
		{Key: "layout_json", Value: p.LayoutJSON},
		{Key: "updated_at", Value: now},
	}
	filter := bson.D{{Key: "_id", Value: p.WorkspaceID}}
	opts := options.Replace().SetUpsert(true)
	_, err := st.s.collection(colWorkspaceLayouts).ReplaceOne(ctx, filter, doc, opts)
	return mapErr(err)
}

func (st *workspaceLayoutStore) Delete(ctx context.Context, workspaceID string) error {
	filter := bson.D{{Key: "_id", Value: workspaceID}}
	_, err := st.s.collection(colWorkspaceLayouts).DeleteOne(ctx, filter)
	return mapErr(err)
}
