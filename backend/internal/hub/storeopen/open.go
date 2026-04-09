// Package storeopen provides a shared factory for opening a hub store
// based on the hub configuration. It consolidates the store-opening
// logic previously duplicated in the hub server and admin CLI.
package storeopen

import (
	"context"
	"fmt"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	mysqlstore "github.com/leapmux/leapmux/internal/hub/store/mysql"
	pgstore "github.com/leapmux/leapmux/internal/hub/store/postgres"
	sqlitestore "github.com/leapmux/leapmux/internal/hub/store/sqlite"
)

// Open creates a Store based on the storage configuration. It runs
// migrations automatically. When no storage type is configured (or
// "sqlite" is specified), it falls back to SQLite using the default
// database path.
func Open(ctx context.Context, cfg *config.Config) (store.Store, error) {
	switch cfg.Storage.Type {
	case "", config.StorageTypeSQLite:
		return sqlitestore.Open(cfg.SQLiteDBPath(), cfg.SQLiteDBConfig())
	case config.StorageTypePostgres:
		return pgstore.Open(ctx, cfg.Storage.Postgres)
	case config.StorageTypeMySQL:
		return mysqlstore.Open(cfg.Storage.MySQL)
	case config.StorageTypeCockroachDB:
		return pgstore.Open(ctx, cfg.Storage.CockroachDB)
	case config.StorageTypeYugabyteDB:
		return pgstore.Open(ctx, cfg.Storage.YugabyteDB)
	case config.StorageTypeTiDB:
		return mysqlstore.Open(cfg.Storage.TiDB)
	default:
		return nil, fmt.Errorf("unsupported storage type: %s", cfg.Storage.Type)
	}
}
