package store

import (
	"context"
	"fmt"
)

// MigrateToLatest is a convenience that calls MigrateTo with LatestVersion.
// Use it to deduplicate identical Migrate() methods across backends.
func MigrateToLatest(ctx context.Context, m Migrator) error {
	return m.MigrateTo(ctx, m.LatestVersion())
}

// NoSQLMigration describes a single forward-only schema migration step
// for NoSQL backends (DynamoDB, MongoDB).
type NoSQLMigration[R any] struct {
	Version     int64
	Description string
	Up          func(ctx context.Context, runner R) error
}

// LatestNoSQLVersion returns the highest version in the migration list,
// or 0 if the list is empty.
func LatestNoSQLVersion[R any](migrations []NoSQLMigration[R]) int64 {
	if len(migrations) == 0 {
		return 0
	}
	return migrations[len(migrations)-1].Version
}

// RunNoSQLMigrations applies forward-only migrations from the current
// version up to the target version. It calls setVersion after each
// successful step to record progress.
func RunNoSQLMigrations[R any](
	ctx context.Context,
	current, target int64,
	migrations []NoSQLMigration[R],
	runner R,
	setVersion func(ctx context.Context, version int64) error,
) error {
	if target < current {
		return ErrRollbackNotSupported
	}

	for _, mig := range migrations {
		if mig.Version <= current {
			continue
		}
		if mig.Version > target {
			break
		}
		if err := mig.Up(ctx, runner); err != nil {
			desc := mig.Description
			if desc == "" {
				return fmt.Errorf("migration %d: %w", mig.Version, err)
			}
			return fmt.Errorf("migration v%d (%s): %w", mig.Version, desc, err)
		}
		if err := setVersion(ctx, mig.Version); err != nil {
			return fmt.Errorf("set version %d: %w", mig.Version, err)
		}
	}
	return nil
}
