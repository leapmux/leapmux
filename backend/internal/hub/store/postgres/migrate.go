package postgres

import (
	"database/sql"
	"embed"
	"io/fs"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/pressly/goose/v3"
)

//go:embed db/migrations/*.sql
var migrations embed.FS

func newMigrator(db *sql.DB) (store.Migrator, error) {
	sub, _ := fs.Sub(migrations, "db/migrations")
	return sqlutil.NewGooseMigrator(goose.DialectPostgres, db, sub)
}
