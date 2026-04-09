package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
)

type testableMongoStore struct {
	*mongoStore
}

var _ store.TestableStore = (*testableMongoStore)(nil)

func NewTestable(ctx context.Context, opts config.MongoDBConfig) (store.TestableStore, error) {
	st, err := New(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &testableMongoStore{mongoStore: st.(*mongoStore)}, nil
}

func (s *testableMongoStore) TestHelper() store.TestHelper {
	return &mongoTestHelper{db: s.db}
}

type mongoTestHelper struct {
	db *mongo.Database
}

func (h *mongoTestHelper) SetDeletedAt(ctx context.Context, entity store.TestEntity, id string, deletedAt time.Time) error {
	if err := store.ValidateEntity(entity); err != nil {
		return err
	}
	_, err := h.db.Collection(string(entity)).UpdateOne(ctx,
		bson.D{{Key: "_id", Value: id}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "deleted_at", Value: deletedAt}}}},
	)
	return err
}

func (h *mongoTestHelper) SetCreatedAt(ctx context.Context, entity store.TestEntity, id string, createdAt time.Time) error {
	if err := store.ValidateEntity(entity); err != nil {
		return err
	}
	_, err := h.db.Collection(string(entity)).UpdateOne(ctx,
		bson.D{{Key: "_id", Value: id}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "created_at", Value: createdAt}}}},
	)
	return err
}

func (h *mongoTestHelper) TruncateAll(ctx context.Context) error {
	for _, name := range allCollectionNames() {
		_, err := h.db.Collection(name).DeleteMany(ctx, bson.D{})
		if err != nil {
			return err
		}
	}
	return nil
}
