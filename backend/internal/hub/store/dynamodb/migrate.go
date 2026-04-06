package dynamodb

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"golang.org/x/sync/errgroup"

	"github.com/leapmux/leapmux/internal/hub/store"
)

var _ store.Migrator = (*dynamoMigrator)(nil)

// dynamoMigrator manages programmatic DynamoDB table creation/migration.
type dynamoMigrator struct {
	client              *dynamodb.Client
	prefix              string
	createTables        bool
	pointInTimeRecovery bool
}

// migrations defines all schema migration steps.
var migrations = []store.NoSQLMigration[*dynamoMigrator]{
	{Version: 1, Up: func(ctx context.Context, m *dynamoMigrator) error {
		if err := createAllTables(ctx, m); err != nil {
			return err
		}
		// Enable TTL on tables that use expiration (in parallel).
		ttlTables := map[string]string{
			tableSessions:            "ttl",
			tableOAuthStates:         "ttl",
			tablePendingOAuthSignups: "ttl",
		}
		eg, egCtx := errgroup.WithContext(ctx)
		for table, attr := range ttlTables {
			eg.Go(func() error {
				if err := enableTTL(egCtx, m.client, m.prefix+table, attr); err != nil {
					return fmt.Errorf("enable TTL on %s: %w", table, err)
				}
				return nil
			})
		}
		if err := eg.Wait(); err != nil {
			return err
		}
		return nil
	}},
}

func newMigrator(opts Options) *dynamoMigrator {
	return &dynamoMigrator{
		client:              opts.Client,
		prefix:              opts.Prefix,
		createTables:        opts.CreateTables,
		pointInTimeRecovery: opts.PointInTimeRecovery,
	}
}

func (m *dynamoMigrator) metaTable() string {
	return m.prefix + tableMeta
}

func (m *dynamoMigrator) CurrentVersion(ctx context.Context) (int64, error) {
	// Ensure the meta table exists first.
	if err := ensureMetaTable(ctx, m.client, m.prefix); err != nil {
		return 0, err
	}

	out, err := m.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(m.metaTable()),
		Key: map[string]ddbtypes.AttributeValue{
			"key": attrS(metaKeySchemaVersion),
		},
	})
	if err != nil {
		return 0, mapErr(err)
	}
	if out.Item == nil {
		return 0, nil
	}

	return getN(out.Item, "value"), nil
}

func (m *dynamoMigrator) LatestVersion() int64 {
	return store.LatestNoSQLVersion(migrations)
}

func (m *dynamoMigrator) Migrate(ctx context.Context) error {
	return store.MigrateToLatest(ctx, m)
}

func (m *dynamoMigrator) MigrateTo(ctx context.Context, version int64) error {
	current, err := m.CurrentVersion(ctx)
	if err != nil {
		return fmt.Errorf("get current version: %w", err)
	}
	return store.RunNoSQLMigrations(ctx, current, version, migrations, m, m.setVersion)
}

func (m *dynamoMigrator) setVersion(ctx context.Context, version int64) error {
	_, err := m.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(m.metaTable()),
		Item: map[string]ddbtypes.AttributeValue{
			"key":   attrS(metaKeySchemaVersion),
			"value": attrN(version),
		},
	})
	return mapErr(err)
}

// ensureMetaTable creates the meta table if it does not exist.
func ensureMetaTable(ctx context.Context, client *dynamodb.Client, prefix string) error {
	metaTableName := prefix + tableMeta
	_, err := client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(metaTableName),
	})
	if err == nil {
		return nil // table exists
	}

	var notFoundErr *ddbtypes.ResourceNotFoundException
	if !errors.As(err, &notFoundErr) {
		return mapErr(err)
	}

	// Create the meta table.
	td := tableDef{name: tableMeta, pk: "key"}
	_, err = client.CreateTable(ctx, buildCreateTableInput(prefix, td))
	if err != nil {
		// Ignore "already exists" race.
		var existsErr *ddbtypes.ResourceInUseException
		if errors.As(err, &existsErr) {
			return nil
		}
		return mapErr(err)
	}

	return waitForTable(ctx, client, metaTableName)
}

// createAllTables is migration version 1: creates every table.
// Tables are created and waited on concurrently to minimise startup time.
func createAllTables(ctx context.Context, m *dynamoMigrator) error {
	if !m.createTables {
		return nil
	}

	eg, egCtx := errgroup.WithContext(ctx)
	for _, td := range allTables() {
		if td.name == tableMeta {
			continue // meta table already created by ensureMetaTable
		}
		td := td // capture loop variable
		tableName := m.prefix + td.name
		eg.Go(func() error {
			_, err := m.client.CreateTable(egCtx, buildCreateTableInput(m.prefix, td))
			if err != nil {
				var existsErr *ddbtypes.ResourceInUseException
				if errors.As(err, &existsErr) {
					return nil
				}
				return fmt.Errorf("create table %s: %w", tableName, err)
			}
			if err := waitForTable(egCtx, m.client, tableName); err != nil {
				return fmt.Errorf("wait for table %s: %w", tableName, err)
			}
			if m.pointInTimeRecovery {
				if err := enablePITR(egCtx, m.client, tableName); err != nil {
					return fmt.Errorf("enable PITR on %s: %w", tableName, err)
				}
			}
			return nil
		})
	}
	return eg.Wait()
}

// enablePITR enables point-in-time recovery on a table.
func enablePITR(ctx context.Context, client *dynamodb.Client, tableName string) error {
	_, err := client.UpdateContinuousBackups(ctx, &dynamodb.UpdateContinuousBackupsInput{
		TableName: aws.String(tableName),
		PointInTimeRecoverySpecification: &ddbtypes.PointInTimeRecoverySpecification{
			PointInTimeRecoveryEnabled: aws.Bool(true),
		},
	})
	return err
}

// enableTTL enables time-to-live on the given attribute of a table.
func enableTTL(ctx context.Context, client *dynamodb.Client, tableName, attributeName string) error {
	_, err := client.UpdateTimeToLive(ctx, &dynamodb.UpdateTimeToLiveInput{
		TableName: aws.String(tableName),
		TimeToLiveSpecification: &ddbtypes.TimeToLiveSpecification{
			Enabled:       aws.Bool(true),
			AttributeName: aws.String(attributeName),
		},
	})
	return err
}

// waitForTable waits for a table to become ACTIVE.
func waitForTable(ctx context.Context, client *dynamodb.Client, tableName string) error {
	waiter := dynamodb.NewTableExistsWaiter(client)
	return waiter.Wait(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(tableName),
	}, tableWaitTimeout)
}
