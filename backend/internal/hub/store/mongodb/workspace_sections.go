package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

func workspaceSectionToDoc(p store.CreateWorkspaceSectionParams, now time.Time) bson.D {
	return bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "user_id", Value: p.UserID},
		{Key: "name", Value: p.Name},
		{Key: "position", Value: p.Position},
		{Key: "section_type", Value: int32(p.SectionType)},
		{Key: "sidebar", Value: int32(p.Sidebar)},
		{Key: "created_at", Value: now},
	}
}

func docToWorkspaceSection(m bson.M) store.WorkspaceSection {
	return store.WorkspaceSection{
		ID:          getS(m, "_id"),
		UserID:      getS(m, "user_id"),
		Name:        getS(m, "name"),
		Position:    getS(m, "position"),
		SectionType: leapmuxv1.SectionType(getInt32(m, "section_type")),
		Sidebar:     leapmuxv1.Sidebar(getInt32(m, "sidebar")),
		CreatedAt:   getTime(m, "created_at"),
	}
}

func (st *workspaceSectionStore) Create(ctx context.Context, p store.CreateWorkspaceSectionParams) error {
	now := truncateMS(time.Now().UTC())
	doc := workspaceSectionToDoc(p, now)
	_, err := st.s.collection(colWorkspaceSections).InsertOne(ctx, doc)
	if err != nil {
		return mapErr(err)
	}
	st.s.trackInsert(colWorkspaceSections, p.ID)
	return nil
}

func (st *workspaceSectionStore) GetByID(ctx context.Context, id string) (*store.WorkspaceSection, error) {
	filter := bson.D{{Key: "_id", Value: id}}
	var m bson.M
	err := st.s.collection(colWorkspaceSections).FindOne(ctx, filter).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	s := docToWorkspaceSection(m)
	return &s, nil
}

func (st *workspaceSectionStore) ListByUserID(ctx context.Context, userID string) ([]store.WorkspaceSection, error) {
	filter := bson.D{{Key: "user_id", Value: userID}}
	cursor, err := st.s.collection(colWorkspaceSections).Find(ctx, filter)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var results []store.WorkspaceSection
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		results = append(results, docToWorkspaceSection(m))
	}
	if err := cursor.Err(); err != nil {
		return nil, mapErr(err)
	}
	return ptrconv.NonNil(results), nil
}

func (st *workspaceSectionStore) Rename(ctx context.Context, p store.RenameWorkspaceSectionParams) (int64, error) {
	filter := bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "user_id", Value: p.UserID},
		{Key: "section_type", Value: int32(leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM)},
	}
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "name", Value: p.Name},
		}},
	}
	res, err := st.s.collection(colWorkspaceSections).UpdateOne(ctx, filter, update)
	if err != nil {
		return 0, mapErr(err)
	}
	return res.MatchedCount, nil
}

func (st *workspaceSectionStore) UpdatePosition(ctx context.Context, p store.UpdateWorkspaceSectionPositionParams) error {
	filter := bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "user_id", Value: p.UserID},
	}
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "position", Value: p.Position},
		}},
	}
	_, err := st.s.collection(colWorkspaceSections).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *workspaceSectionStore) UpdateSidebarPosition(ctx context.Context, p store.UpdateWorkspaceSectionSidebarPositionParams) error {
	filter := bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "user_id", Value: p.UserID},
	}
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "sidebar", Value: int32(p.Sidebar)},
			{Key: "position", Value: p.Position},
		}},
	}
	_, err := st.s.collection(colWorkspaceSections).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *workspaceSectionStore) Delete(ctx context.Context, p store.DeleteWorkspaceSectionParams) (int64, error) {
	filter := bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "user_id", Value: p.UserID},
		{Key: "section_type", Value: int32(leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM)},
	}
	res, err := st.s.collection(colWorkspaceSections).DeleteOne(ctx, filter)
	if err != nil {
		return 0, mapErr(err)
	}
	return res.DeletedCount, nil
}

func (st *workspaceSectionStore) HasDefaultForUser(ctx context.Context, userID string) (bool, error) {
	filter := bson.D{
		{Key: "user_id", Value: userID},
		{Key: "section_type", Value: bson.D{{Key: "$ne", Value: int32(leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM)}}},
	}
	opts := options.Count().SetLimit(1)
	count, err := st.s.collection(colWorkspaceSections).CountDocuments(ctx, filter, opts)
	if err != nil {
		return false, mapErr(err)
	}
	return count > 0, nil
}
