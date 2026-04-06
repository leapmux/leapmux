// Package storeopen provides a shared factory for opening a hub store
// based on the hub configuration. It consolidates the store-opening
// logic previously duplicated in the hub server and admin CLI.
package storeopen

import (
	"context"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	dynamostore "github.com/leapmux/leapmux/internal/hub/store/dynamodb"
	mongostore "github.com/leapmux/leapmux/internal/hub/store/mongodb"
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
	case config.StorageTypeMongoDB:
		return mongostore.New(ctx, cfg.Storage.MongoDB)
	case config.StorageTypeDynamoDB:
		return openDynamoDB(ctx, cfg)
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

func openDynamoDB(ctx context.Context, cfg *config.Config) (store.Store, error) {
	dc := cfg.Storage.DynamoDB
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(dc.Region),
	)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	var ddbOpts []func(*dynamodb.Options)
	if dc.Endpoint != "" {
		ddbOpts = append(ddbOpts, func(o *dynamodb.Options) {
			o.BaseEndpoint = &dc.Endpoint
		})
	}
	client := dynamodb.NewFromConfig(awsCfg, ddbOpts...)
	return dynamostore.New(ctx, dynamostore.Options{
		Client:              client,
		Prefix:              dc.TablePrefix,
		CreateTables:        dc.CreateTables,
		PointInTimeRecovery: dc.PointInTimeRecovery,
	})
}
