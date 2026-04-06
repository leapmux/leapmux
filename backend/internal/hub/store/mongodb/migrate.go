package mongodb

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/leapmux/leapmux/internal/hub/store"
)

var _ store.Migrator = (*mongoMigrator)(nil)

// mongoMigrator manages programmatic MongoDB collection/index creation
// and schema migration.
type mongoMigrator struct {
	db *mongo.Database
}

// migrations defines all schema migration steps.
var migrations = []store.NoSQLMigration[*mongoMigrator]{
	{Version: 1, Up: func(ctx context.Context, m *mongoMigrator) error {
		return createAllCollections(ctx, m)
	}},
}

func newMigrator(db *mongo.Database) *mongoMigrator {
	return &mongoMigrator{db: db}
}

func (m *mongoMigrator) CurrentVersion(ctx context.Context) (int64, error) {
	coll := m.db.Collection(colMeta)
	var result bson.M
	err := coll.FindOne(ctx, bson.D{{Key: "_id", Value: "schema_version"}}).Decode(&result)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return 0, nil
		}
		return 0, err
	}
	return getInt64(result, "value"), nil
}

func (m *mongoMigrator) LatestVersion() int64 {
	return store.LatestNoSQLVersion(migrations)
}

func (m *mongoMigrator) Migrate(ctx context.Context) error {
	return store.MigrateToLatest(ctx, m)
}

func (m *mongoMigrator) MigrateTo(ctx context.Context, version int64) error {
	current, err := m.CurrentVersion(ctx)
	if err != nil {
		return fmt.Errorf("get current version: %w", err)
	}
	return store.RunNoSQLMigrations(ctx, current, version, migrations, m, m.setVersion)
}

func (m *mongoMigrator) setVersion(ctx context.Context, version int64) error {
	coll := m.db.Collection(colMeta)
	opts := options.Replace().SetUpsert(true)
	_, err := coll.ReplaceOne(ctx, bson.D{{Key: "_id", Value: "schema_version"}}, bson.D{
		{Key: "_id", Value: "schema_version"},
		{Key: "value", Value: version},
	}, opts)
	return err
}

// createAllCollections is migration version 1: creates every collection
// and its indexes.
func createAllCollections(ctx context.Context, m *mongoMigrator) error {
	for _, cd := range allCollections() {
		// Create collection (ignore "already exists" error).
		_ = m.db.CreateCollection(ctx, cd.name)

		if len(cd.indexes) > 0 {
			_, err := m.db.Collection(cd.name).Indexes().CreateMany(ctx, cd.indexes)
			if err != nil {
				return fmt.Errorf("create indexes for %s: %w", cd.name, err)
			}
		}
	}
	return nil
}
