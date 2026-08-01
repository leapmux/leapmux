package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate runs all pending database migrations.
//
// Goes through goose's provider API rather than its package-level one. The
// package-level API (SetBaseFS / SetDialect / Up) keeps the base FS and the
// dialect in package globals, so two Migrate calls in flight at once race on
// them -- which -race reports the moment more than one worker DB is opened
// concurrently, as the service tests now do routinely. A provider carries
// both per instance, leaving nothing global to race on. It is also the API
// the hub's stores already migrate through (sqlutil.GooseMigrator), so the
// two halves of the codebase no longer disagree about how to run a migration.
//
// Takes the caller's ctx (as sqlutil.GooseMigrator.Migrate does) rather than
// making its own: both entry points hold a cancellable one, and a migration
// that rewrites a large table on a busy worker DB would otherwise be
// uninterruptible -- Ctrl-C would be ignored until it finished.
func Migrate(ctx context.Context, db *sql.DB) error {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectSQLite3, db, sub)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	return nil
}
