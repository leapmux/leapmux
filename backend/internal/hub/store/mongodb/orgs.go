package mongodb

import (
	"context"
	"regexp"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

func orgToDoc(p store.CreateOrgParams, now time.Time) bson.D {
	return bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "name", Value: p.Name},
		{Key: "is_personal", Value: p.IsPersonal},
		{Key: "created_at", Value: now},
		{Key: "deleted_at", Value: nil},
	}
}

func docToOrg(m bson.M) store.Org {
	return store.Org{
		ID:         getS(m, "_id"),
		Name:       getS(m, "name"),
		IsPersonal: getBool(m, "is_personal"),
		CreatedAt:  getTime(m, "created_at"),
		DeletedAt:  getTimePtr(m, "deleted_at"),
	}
}

func (st *orgStore) Create(ctx context.Context, p store.CreateOrgParams) error {
	now := truncateMS(time.Now().UTC())
	doc := orgToDoc(p, now)
	_, err := st.s.collection(colOrgs).InsertOne(ctx, doc)
	if err != nil {
		return mapErr(err)
	}
	st.s.trackInsert(colOrgs, p.ID)
	return nil
}

func (st *orgStore) GetByID(ctx context.Context, id string) (*store.Org, error) {
	filter := bson.D{
		{Key: "_id", Value: id},
		{Key: "deleted_at", Value: nil},
	}
	var m bson.M
	err := st.s.collection(colOrgs).FindOne(ctx, filter).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	o := docToOrg(m)
	return &o, nil
}

func (st *orgStore) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.Org, error) {
	filter := bson.D{{Key: "_id", Value: id}}
	var m bson.M
	err := st.s.collection(colOrgs).FindOne(ctx, filter).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	o := docToOrg(m)
	return &o, nil
}

func (st *orgStore) GetByName(ctx context.Context, name string) (*store.Org, error) {
	filter := bson.D{
		{Key: "name", Value: name},
		{Key: "deleted_at", Value: nil},
	}
	var m bson.M
	err := st.s.collection(colOrgs).FindOne(ctx, filter).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	o := docToOrg(m)
	return &o, nil
}

func (st *orgStore) HasAny(ctx context.Context) (bool, error) {
	count, err := st.s.collection(colOrgs).CountDocuments(ctx, notDeleted(), options.Count().SetLimit(1))
	if err != nil {
		return false, mapErr(err)
	}
	return count > 0, nil
}

func (st *orgStore) ListAll(ctx context.Context, p store.ListAllOrgsParams) ([]store.Org, error) {
	filter := notDeleted()
	if p.Cursor != "" {
		cursorTime, _, err := store.ParseCursorTime(p.Cursor)
		if err != nil {
			return nil, err
		}
		filter = append(filter, bson.E{Key: "created_at", Value: bson.D{{Key: "$lt", Value: cursorTime}}})
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(p.Limit)

	cursor, err := st.s.collection(colOrgs).Find(ctx, filter, opts)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var orgs []store.Org
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		orgs = append(orgs, docToOrg(m))
	}
	return ptrconv.NonNil(orgs), mapErr(cursor.Err())
}

func (st *orgStore) Search(ctx context.Context, p store.SearchOrgsParams) ([]store.Org, error) {
	filter := notDeleted()

	if p.Query != nil && *p.Query != "" {
		regex := bson.D{{Key: "$regex", Value: "^" + regexp.QuoteMeta(*p.Query)}, {Key: "$options", Value: "i"}}
		filter = append(filter, bson.E{Key: "name", Value: regex})
	}

	if p.Cursor != "" {
		cursorTime, _, err := store.ParseCursorTime(p.Cursor)
		if err != nil {
			return nil, err
		}
		filter = append(filter, bson.E{Key: "created_at", Value: bson.D{{Key: "$lt", Value: cursorTime}}})
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(p.Limit)

	cursor, err := st.s.collection(colOrgs).Find(ctx, filter, opts)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var orgs []store.Org
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		orgs = append(orgs, docToOrg(m))
	}
	return ptrconv.NonNil(orgs), mapErr(cursor.Err())
}

func (st *orgStore) UpdateName(ctx context.Context, p store.UpdateOrgNameParams) error {
	filter := bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "is_personal", Value: false},
		{Key: "deleted_at", Value: nil},
	}
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "name", Value: p.Name},
		}},
	}
	_, err := st.s.collection(colOrgs).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *orgStore) SoftDelete(ctx context.Context, id string) error {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "_id", Value: id},
		{Key: "deleted_at", Value: nil},
	}
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "deleted_at", Value: now},
		}},
	}
	_, err := st.s.collection(colOrgs).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *orgStore) SoftDeleteNonPersonal(ctx context.Context, id string) error {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "_id", Value: id},
		{Key: "is_personal", Value: false},
		{Key: "deleted_at", Value: nil},
	}
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "deleted_at", Value: now},
		}},
	}
	_, err := st.s.collection(colOrgs).UpdateOne(ctx, filter, update)
	return mapErr(err)
}
