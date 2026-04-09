package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

func workspaceToDoc(p store.CreateWorkspaceParams, now time.Time) bson.D {
	return bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "org_id", Value: p.OrgID},
		{Key: "owner_user_id", Value: p.OwnerUserID},
		{Key: "title", Value: p.Title},
		{Key: "is_deleted", Value: false},
		{Key: "created_at", Value: now},
	}
}

func docToWorkspace(m bson.M) store.Workspace {
	return store.Workspace{
		ID:          getS(m, "_id"),
		OrgID:       getS(m, "org_id"),
		OwnerUserID: getS(m, "owner_user_id"),
		Title:       getS(m, "title"),
		IsDeleted:   getBool(m, "is_deleted"),
		CreatedAt:   getTime(m, "created_at"),
		DeletedAt:   getTimePtr(m, "deleted_at"),
	}
}

func (st *workspaceStore) Create(ctx context.Context, p store.CreateWorkspaceParams) error {
	now := truncateMS(time.Now().UTC())
	doc := workspaceToDoc(p, now)
	_, err := st.s.collection(colWorkspaces).InsertOne(ctx, doc)
	if err != nil {
		return mapErr(err)
	}
	st.s.trackInsert(colWorkspaces, p.ID)
	return nil
}

func (st *workspaceStore) GetByID(ctx context.Context, id string) (*store.Workspace, error) {
	filter := bson.D{
		{Key: "_id", Value: id},
		{Key: "is_deleted", Value: false},
	}
	var m bson.M
	err := st.s.collection(colWorkspaces).FindOne(ctx, filter).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	w := docToWorkspace(m)
	return &w, nil
}

func (st *workspaceStore) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.Workspace, error) {
	filter := bson.D{{Key: "_id", Value: id}}
	var m bson.M
	err := st.s.collection(colWorkspaces).FindOne(ctx, filter).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	w := docToWorkspace(m)
	return &w, nil
}

func (st *workspaceStore) ListAccessible(ctx context.Context, p store.ListAccessibleWorkspacesParams) ([]store.Workspace, error) {
	// Step 1: Find all workspace IDs this user has been granted access to.
	accessFilter := bson.D{
		{Key: "user_id", Value: p.UserID},
	}
	cursor, err := st.s.collection(colWorkspaceAccess).Find(ctx, accessFilter)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var accessedIDs []string
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		accessedIDs = append(accessedIDs, getS(m, "workspace_id"))
	}
	if err := cursor.Err(); err != nil {
		return nil, mapErr(err)
	}

	// Step 2: Query workspaces where user is owner OR has access.
	orClauses := bson.A{
		bson.D{{Key: "owner_user_id", Value: p.UserID}},
	}
	if len(accessedIDs) > 0 {
		orClauses = append(orClauses, bson.D{
			{Key: "_id", Value: bson.D{{Key: "$in", Value: accessedIDs}}},
		})
	}

	filter := bson.D{
		{Key: "org_id", Value: p.OrgID},
		{Key: "is_deleted", Value: false},
		{Key: "$or", Value: orClauses},
	}

	cursor, err = st.s.collection(colWorkspaces).Find(ctx, filter)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var results []store.Workspace
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		results = append(results, docToWorkspace(m))
	}
	if err := cursor.Err(); err != nil {
		return nil, mapErr(err)
	}
	return ptrconv.NonNil(results), nil
}

func (st *workspaceStore) Rename(ctx context.Context, p store.RenameWorkspaceParams) (int64, error) {
	filter := bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "owner_user_id", Value: p.OwnerUserID},
	}
	st.s.trackBeforeUpdate(ctx, colWorkspaces, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "title", Value: p.Title},
		}},
	}
	res, err := st.s.collection(colWorkspaces).UpdateOne(ctx, filter, update)
	if err != nil {
		return 0, mapErr(err)
	}
	return res.MatchedCount, nil
}

func (st *workspaceStore) SoftDelete(ctx context.Context, p store.SoftDeleteWorkspaceParams) (int64, error) {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "owner_user_id", Value: p.OwnerUserID},
	}
	st.s.trackBeforeUpdate(ctx, colWorkspaces, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "is_deleted", Value: true},
			{Key: "deleted_at", Value: now},
		}},
	}
	res, err := st.s.collection(colWorkspaces).UpdateOne(ctx, filter, update)
	if err != nil {
		return 0, mapErr(err)
	}
	return res.MatchedCount, nil
}

func (st *workspaceStore) SoftDeleteAllByUser(ctx context.Context, ownerUserID string) error {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "owner_user_id", Value: ownerUserID},
		{Key: "is_deleted", Value: false},
	}
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "is_deleted", Value: true},
			{Key: "deleted_at", Value: now},
		}},
	}
	_, err := st.s.collection(colWorkspaces).UpdateMany(ctx, filter, update)
	return mapErr(err)
}
