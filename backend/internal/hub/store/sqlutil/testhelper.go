package sqlutil

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// SetDeletedAt backdates the deleted_at timestamp for a record using
// "?" placeholders. Works with SQLite and MySQL drivers.
func SetDeletedAt(ctx context.Context, db *sql.DB, entity store.TestEntity, id string, deletedAt time.Time) error {
	if err := store.ValidateEntity(entity); err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET deleted_at = ? WHERE id = ?", entity)
	_, err := db.ExecContext(ctx, query, deletedAt, id)
	return err
}

// SetCreatedAt backdates the created_at timestamp for a record using
// "?" placeholders. Works with SQLite and MySQL drivers.
func SetCreatedAt(ctx context.Context, db *sql.DB, entity store.TestEntity, id string, createdAt time.Time) error {
	if err := store.ValidateEntity(entity); err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET created_at = ? WHERE id = ?", entity)
	_, err := db.ExecContext(ctx, query, createdAt, id)
	return err
}
