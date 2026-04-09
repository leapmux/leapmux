package mongodb

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

func (st *workspaceAccessStore) Grant(ctx context.Context, p store.GrantWorkspaceAccessParams) error {
	now := truncateMS(time.Now().UTC())
	id := compoundID(p.WorkspaceID, p.UserID)
	doc := bson.D{
		{Key: "_id", Value: id},
		{Key: "workspace_id", Value: p.WorkspaceID},
		{Key: "user_id", Value: p.UserID},
		{Key: "created_at", Value: now},
	}
	_, err := st.s.collection(colWorkspaceAccess).InsertOne(ctx, doc)
	if err != nil {
		if errors.Is(mapErr(err), store.ErrConflict) {
			return nil
		}
		return mapErr(err)
	}
	st.s.trackInsert(colWorkspaceAccess, id)
	return nil
}

func (st *workspaceAccessStore) BulkGrant(ctx context.Context, params []store.GrantWorkspaceAccessParams) error {
	if len(params) == 0 {
		return nil
	}
	now := truncateMS(time.Now().UTC())
	models := make([]mongo.WriteModel, 0, len(params))
	for _, p := range params {
		id := compoundID(p.WorkspaceID, p.UserID)
		doc := bson.D{
			{Key: "_id", Value: id},
			{Key: "workspace_id", Value: p.WorkspaceID},
			{Key: "user_id", Value: p.UserID},
			{Key: "created_at", Value: now},
		}
		filter := bson.D{{Key: "_id", Value: id}}
		models = append(models, mongo.NewReplaceOneModel().
			SetFilter(filter).
			SetReplacement(doc).
			SetUpsert(true))
	}
	_, err := st.s.collection(colWorkspaceAccess).BulkWrite(ctx, models)
	return mapErr(err)
}

func (st *workspaceAccessStore) Revoke(ctx context.Context, p store.RevokeWorkspaceAccessParams) error {
	id := compoundID(p.WorkspaceID, p.UserID)
	filter := bson.D{{Key: "_id", Value: id}}
	st.s.trackBeforeDelete(ctx, colWorkspaceAccess, filter)
	_, err := st.s.collection(colWorkspaceAccess).DeleteOne(ctx, filter)
	return mapErr(err)
}

func (st *workspaceAccessStore) ListByWorkspaceID(ctx context.Context, workspaceID string) ([]store.WorkspaceAccess, error) {
	filter := bson.D{{Key: "workspace_id", Value: workspaceID}}
	cursor, err := st.s.collection(colWorkspaceAccess).Find(ctx, filter)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var results []store.WorkspaceAccess
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		results = append(results, store.WorkspaceAccess{
			WorkspaceID: getS(m, "workspace_id"),
			UserID:      getS(m, "user_id"),
			CreatedAt:   getTime(m, "created_at"),
		})
	}
	if err := cursor.Err(); err != nil {
		return nil, mapErr(err)
	}
	return ptrconv.NonNil(results), nil
}

func (st *workspaceAccessStore) HasAccess(ctx context.Context, p store.HasWorkspaceAccessParams) (bool, error) {
	id := compoundID(p.WorkspaceID, p.UserID)
	filter := bson.D{{Key: "_id", Value: id}}
	err := st.s.collection(colWorkspaceAccess).FindOne(ctx, filter).Err()
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return false, nil
		}
		return false, mapErr(err)
	}
	return true, nil
}

func (st *workspaceAccessStore) Clear(ctx context.Context, workspaceID string) error {
	filter := bson.D{{Key: "workspace_id", Value: workspaceID}}
	_, err := st.s.collection(colWorkspaceAccess).DeleteMany(ctx, filter)
	return mapErr(err)
}
