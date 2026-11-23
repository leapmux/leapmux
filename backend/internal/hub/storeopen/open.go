// Package storeopen provides a shared factory for opening a hub store
// based on the hub configuration. It consolidates the store-opening
// logic previously duplicated in the hub server and admin CLI.
package storeopen

import (
	"context"
	"fmt"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	mysqlstore "github.com/leapmux/leapmux/internal/hub/store/mysql"
	pgstore "github.com/leapmux/leapmux/internal/hub/store/postgres"
	sqlitestore "github.com/leapmux/leapmux/internal/hub/store/sqlite"
)

// Open creates a Store based on the storage configuration. It runs
// migrations automatically and seeds built-in rows. When no storage type is
// configured (or "sqlite" is specified), it falls back to SQLite using the
// default database path.
func Open(ctx context.Context, cfg *config.Config, ks *keystore.Keystore) (store.Store, error) {
	switch cfg.Storage.Type {
	case "", config.StorageTypeSQLite:
		return sqlitestore.Open(cfg.SQLiteDBPath(), cfg.SQLiteDBConfig(), ks)
	case config.StorageTypePostgres:
		return pgstore.Open(ctx, cfg.Storage.Postgres, ks)
	case config.StorageTypeMySQL:
		return mysqlstore.Open(cfg.Storage.MySQL, ks)
	case config.StorageTypeCockroachDB:
		return pgstore.Open(ctx, cfg.Storage.CockroachDB, ks)
	case config.StorageTypeYugabyteDB:
		return pgstore.Open(ctx, cfg.Storage.YugabyteDB, ks)
	case config.StorageTypeTiDB:
		return mysqlstore.Open(cfg.Storage.TiDB, ks)
	default:
		return nil, fmt.Errorf("unsupported storage type: %s", cfg.Storage.Type)
	}
}

// OpenWithoutMigrate opens a Store without running schema migrations or
// seeding built-in rows. Recovery and inspection commands that target an
// existing database use this entry point so they never mutate rows as a side
// effect of opening the store.
func OpenWithoutMigrate(ctx context.Context, cfg *config.Config, ks *keystore.Keystore) (store.Store, error) {
	switch cfg.Storage.Type {
	case "", config.StorageTypeSQLite:
		return sqlitestore.OpenWithoutMigrate(cfg.SQLiteDBPath(), cfg.SQLiteDBConfig(), ks)
	case config.StorageTypePostgres:
		return pgstore.OpenWithoutMigrate(ctx, cfg.Storage.Postgres, ks)
	case config.StorageTypeMySQL:
		return mysqlstore.OpenWithoutMigrate(cfg.Storage.MySQL, ks)
	case config.StorageTypeCockroachDB:
		return pgstore.OpenWithoutMigrate(ctx, cfg.Storage.CockroachDB, ks)
	case config.StorageTypeYugabyteDB:
		return pgstore.OpenWithoutMigrate(ctx, cfg.Storage.YugabyteDB, ks)
	case config.StorageTypeTiDB:
		return mysqlstore.OpenWithoutMigrate(cfg.Storage.TiDB, ks)
	default:
		return nil, fmt.Errorf("unsupported storage type: %s", cfg.Storage.Type)
	}
}
