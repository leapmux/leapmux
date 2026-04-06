package mongodb

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

func (st *workspaceSectionItemStore) Set(ctx context.Context, p store.SetWorkspaceSectionItemParams) error {
	id := compoundID(p.UserID, p.WorkspaceID)
	doc := bson.D{
		{Key: "_id", Value: id},
		{Key: "user_id", Value: p.UserID},
		{Key: "workspace_id", Value: p.WorkspaceID},
		{Key: "section_id", Value: p.SectionID},
		{Key: "position", Value: p.Position},
	}
	filter := bson.D{{Key: "_id", Value: id}}
	opts := options.Replace().SetUpsert(true)
	_, err := st.s.collection(colWorkspaceSectionItems).ReplaceOne(ctx, filter, doc, opts)
	return mapErr(err)
}

func (st *workspaceSectionItemStore) Get(ctx context.Context, p store.GetWorkspaceSectionItemParams) (*store.WorkspaceSectionItem, error) {
	id := compoundID(p.UserID, p.WorkspaceID)
	filter := bson.D{{Key: "_id", Value: id}}
	var m bson.M
	err := st.s.collection(colWorkspaceSectionItems).FindOne(ctx, filter).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	item := store.WorkspaceSectionItem{
		UserID:      getS(m, "user_id"),
		WorkspaceID: getS(m, "workspace_id"),
		SectionID:   getS(m, "section_id"),
		Position:    getS(m, "position"),
	}
	return &item, nil
}

func (st *workspaceSectionItemStore) ListByUser(ctx context.Context, userID string) ([]store.WorkspaceSectionItem, error) {
	filter := bson.D{{Key: "user_id", Value: userID}}
	cursor, err := st.s.collection(colWorkspaceSectionItems).Find(ctx, filter)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var results []store.WorkspaceSectionItem
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		results = append(results, store.WorkspaceSectionItem{
			UserID:      getS(m, "user_id"),
			WorkspaceID: getS(m, "workspace_id"),
			SectionID:   getS(m, "section_id"),
			Position:    getS(m, "position"),
		})
	}
	if err := cursor.Err(); err != nil {
		return nil, mapErr(err)
	}
	return ptrconv.NonNil(results), nil
}

func (st *workspaceSectionItemStore) Delete(ctx context.Context, p store.DeleteWorkspaceSectionItemParams) error {
	id := compoundID(p.UserID, p.WorkspaceID)
	filter := bson.D{{Key: "_id", Value: id}}
	_, err := st.s.collection(colWorkspaceSectionItems).DeleteOne(ctx, filter)
	return mapErr(err)
}

func (st *workspaceSectionItemStore) DeleteBySection(ctx context.Context, sectionID string) error {
	filter := bson.D{{Key: "section_id", Value: sectionID}}
	_, err := st.s.collection(colWorkspaceSectionItems).DeleteMany(ctx, filter)
	return mapErr(err)
}

func (st *workspaceSectionItemStore) MoveToSection(ctx context.Context, p store.MoveWorkspaceSectionItemsToSectionParams) error {
	filter := bson.D{{Key: "section_id", Value: p.FromSectionID}}
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "section_id", Value: p.ToSectionID},
		}},
	}
	_, err := st.s.collection(colWorkspaceSectionItems).UpdateMany(ctx, filter, update)
	return mapErr(err)
}

func (st *workspaceSectionItemStore) HasItemsBySection(ctx context.Context, sectionID string) (bool, error) {
	filter := bson.D{{Key: "section_id", Value: sectionID}}
	count, err := st.s.collection(colWorkspaceSectionItems).CountDocuments(ctx, filter, options.Count().SetLimit(1))
	if err != nil {
		return false, mapErr(err)
	}
	return count > 0, nil
}

func (st *workspaceSectionItemStore) IsInArchivedSection(ctx context.Context, p store.IsWorkspaceInArchivedSectionParams) (bool, error) {
	// Step 1: Find the section item for this user+workspace.
	id := compoundID(p.UserID, p.WorkspaceID)
	filter := bson.D{{Key: "_id", Value: id}}
	var m bson.M
	err := st.s.collection(colWorkspaceSectionItems).FindOne(ctx, filter).Decode(&m)
	if err != nil {
		if errors.Is(mapErr(err), store.ErrNotFound) {
			return false, nil
		}
		return false, mapErr(err)
	}

	// Step 2: Look up the section and check if it is archived.
	sectionID := getS(m, "section_id")
	sectionFilter := bson.D{{Key: "_id", Value: sectionID}}
	var sm bson.M
	err = st.s.collection(colWorkspaceSections).FindOne(ctx, sectionFilter).Decode(&sm)
	if err != nil {
		if errors.Is(mapErr(err), store.ErrNotFound) {
			return false, nil
		}
		return false, mapErr(err)
	}

	// SectionType 2 = archived.
	return getInt32(sm, "section_type") == int32(leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED), nil
}
