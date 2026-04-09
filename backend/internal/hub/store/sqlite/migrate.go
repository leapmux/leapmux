package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/pressly/goose/v3"
)

//go:embed db/migrations/*.sql
var migrations embed.FS

func newMigrator(sqlDB *sql.DB) (store.Migrator, error) {
	sub, _ := fs.Sub(migrations, "db/migrations")
	return sqlutil.NewGooseMigrator(goose.DialectSQLite3, sqlDB, sub)
}

// MigrateDB runs all pending database migrations.
func MigrateDB(db *sql.DB) error {
	m, err := newMigrator(db)
	if err != nil {
		return err
	}
	return m.Migrate(context.Background())
}
