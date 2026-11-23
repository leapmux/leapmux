package sqlutil

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// DBQuery is the read-side counterpart of DBExec: the subset of
// database/sql.DB the shared test-helper reads need.
type DBQuery interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// SetDeletedAt backdates the deleted_at timestamp for a record. The exec
// closure and dialect let every backend delegate here rather than hand-rolling
// the UPDATE with its own placeholder convention (`?` vs `$n`).
func SetDeletedAt(ctx context.Context, exec func(context.Context, string, ...any) error, dialect Dialect, entity store.TestEntity, id string, deletedAt time.Time) error {
	return SetEntityColumnValue(ctx, exec, dialect, entity, "deleted_at", id, deletedAt)
}

// SetCreatedAt backdates the created_at timestamp for a record. See SetDeletedAt
// for the exec/dialect contract.
func SetCreatedAt(ctx context.Context, exec func(context.Context, string, ...any) error, dialect Dialect, entity store.TestEntity, id string, createdAt time.Time) error {
	return SetEntityColumnValue(ctx, exec, dialect, entity, "created_at", id, createdAt)
}

// SetLastActiveAt writes an exact last_active_at timestamp for a session row.
// Only the user_sessions table carries this column, so the table is pinned
// here rather than caller-supplied -- passing a table without the column
// would only fail at the database with an obscure "no such column" error.
// See SetDeletedAt for the exec/dialect contract.
func SetLastActiveAt(ctx context.Context, exec func(context.Context, string, ...any) error, dialect Dialect, id string, lastActiveAt time.Time) error {
	return SetEntityColumnValue(ctx, exec, dialect, store.EntitySessions, "last_active_at", id, lastActiveAt)
}

// SetDeregisteringAt writes an exact deregistering_at timestamp for a worker
// row, pinned to the workers table as SetLastActiveAt is to user_sessions. See
// SetDeletedAt for the exec/dialect contract.
func SetDeregisteringAt(ctx context.Context, exec func(context.Context, string, ...any) error, dialect Dialect, workerID string, deregisteringAt time.Time) error {
	return SetEntityColumnValue(ctx, exec, dialect, store.EntityWorkers, "deregistering_at", workerID, deregisteringAt)
}

// ClearWorkerDeregisteringAt restores only a live test worker. The identifiers
// are fixed, and the worker ID remains a parameter in every dialect.
func ClearWorkerDeregisteringAt(ctx context.Context, exec func(context.Context, string, ...any) error, dialect Dialect, workerID string) error {
	if err := validateDialect(dialect); err != nil {
		return err
	}
	return exec(ctx, fmt.Sprintf("UPDATE workers SET deregistering_at = NULL WHERE id = %s AND deleted_at IS NULL", paramPlaceholder(dialect, 1)), workerID)
}

// CopyWorkspaceTitleCiphertext copies the raw encrypted workspaces.title bytes
// from srcWorkspaceID to dstWorkspaceID to verify AAD tampering detection.
// Uses two separate queries and closes *sql.Rows before calling exec so
// SQLite's SetMaxOpenConns(1) cannot deadlock and MySQL 8 avoids Error 1093
// on self-referencing UPDATE subqueries.
func CopyWorkspaceTitleCiphertext(ctx context.Context, db DBQuery, exec func(context.Context, string, ...any) error, dialect Dialect, srcWorkspaceID, dstWorkspaceID string) error {
	if err := validateDialect(dialect); err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT title FROM workspaces WHERE id = %s", paramPlaceholder(dialect, 1)), srcWorkspaceID)
	if err != nil {
		return err
	}
	var raw []byte
	found := false
	if rows.Next() {
		if scanErr := rows.Scan(&raw); scanErr != nil {
			_ = rows.Close()
			return scanErr
		}
		found = true
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("CopyWorkspaceTitleCiphertext: source workspace %q not found", srcWorkspaceID)
	}
	return exec(ctx, fmt.Sprintf("UPDATE workspaces SET title = %s WHERE id = %s", paramPlaceholder(dialect, 1), paramPlaceholder(dialect, 2)), raw, dstWorkspaceID)
}

// SetSettingSecretRaw overwrites hub_settings.secret with raw ciphertext bytes
// for a single key, used by tests simulating partial-column key skew.
func SetSettingSecretRaw(ctx context.Context, exec func(context.Context, string, ...any) error, dialect Dialect, key string, raw []byte) error {
	if err := validateDialect(dialect); err != nil {
		return err
	}
	return exec(ctx, fmt.Sprintf("UPDATE hub_settings SET secret = %s WHERE %s = %s", paramPlaceholder(dialect, 1), quoteIdent(dialect, "key"), paramPlaceholder(dialect, 2)), raw, key)
}

// CopySettingSecretCiphertext copies the raw persisted hub_settings.secret
// ciphertext from srcKey to dstKey so tests can verify key-scoped AAD
// rejection without synthesizing ciphertext in Go.
func CopySettingSecretCiphertext(ctx context.Context, db DBQuery, exec func(context.Context, string, ...any) error, dialect Dialect, srcKey, dstKey string) error {
	if err := validateDialect(dialect); err != nil {
		return err
	}
	keyCol := quoteIdent(dialect, "key")
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT secret FROM hub_settings WHERE %s = %s", keyCol, paramPlaceholder(dialect, 1)), srcKey)
	if err != nil {
		return err
	}
	var raw []byte
	found := false
	if rows.Next() {
		if scanErr := rows.Scan(&raw); scanErr != nil {
			_ = rows.Close()
			return scanErr
		}
		found = true
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("CopySettingSecretCiphertext: source setting %q not found", srcKey)
	}
	return SetSettingSecretRaw(ctx, exec, dialect, dstKey, raw)
}

// SetEntityColumnValue backdates one fixed column (deleted_at/created_at/
// last_active_at/deregistering_at -- a literal, never caller input) on a validated entity
// table. BOTH the entity and the column are checked against closed allowlists
// so this cannot become an arbitrary-SQL escape hatch -- a future typed wrapper
// that passes an unvalidated column fails loudly here rather than interpolating
// caller input into the UPDATE. value is `any` so SQLite can pass a
// pre-formatted strftime string that byte-matches
// production rows while the driver-serialized dialects pass a time.Time; the
// exported typed wrappers above keep every call site's value strongly typed.
func SetEntityColumnValue(ctx context.Context, exec func(context.Context, string, ...any) error, dialect Dialect, entity store.TestEntity, column, id string, value any) error {
	if err := store.ValidateEntity(entity); err != nil {
		return err
	}
	if err := validateEntityColumn(column); err != nil {
		return err
	}
	if err := validateDialect(dialect); err != nil {
		return err
	}
	// entity and column are validated against closed allowlists above, so the
	// table and column interpolated here are always known literals, never
	// caller input.
	query := fmt.Sprintf("UPDATE %s SET %s = %s WHERE id = %s", string(entity), column, paramPlaceholder(dialect, 1), paramPlaceholder(dialect, 2))
	return exec(ctx, query, value, id)
}

// allowedEntityColumns is the closed set of literal column names
// SetEntityColumnValue may interpolate. Mirroring the entity allowlist on the
// column side makes the SQL-injection surface mechanically impossible to
// widen: a column outside this set is rejected before the sprintf, so the
// interpolation only ever receives a known literal.
var allowedEntityColumns = map[string]bool{
	"deleted_at":       true,
	"created_at":       true,
	"last_active_at":   true,
	"deregistering_at": true,
}

func validateEntityColumn(column string) error {
	if !allowedEntityColumns[column] {
		return fmt.Errorf("unknown entity column %q", column)
	}
	return nil
}

// ListAllOwnedTabsSQL is the owner-blind workspace_tab_owned read behind
// store.TestHelper.ListAllOwnedTabs, shared by the two database/sql dialects
// (sqlite, mysql); postgres runs the same statement through pgx. The statement
// takes no parameters, so it needs no Dialect.
//
// Test-only: see the TestHelper.ListAllOwnedTabs doc for why no production read
// of this table may omit user_id.
const ListAllOwnedTabsSQL = `SELECT user_id, workspace_id, tab_type, tab_id, worker_id, tile_id, position, rendered
FROM workspace_tab_owned ORDER BY user_id, tab_id`

// ListAllOwnedTabs runs ListAllOwnedTabsSQL against a database/sql handle and
// converts each row to store.WorkspaceTabRow. tab_type is scanned as int64
// because both sqlite (INTEGER) and mysql (INT) widen into it losslessly.
func ListAllOwnedTabs(ctx context.Context, db DBQuery) ([]store.WorkspaceTabRow, error) {
	rows, err := db.QueryContext(ctx, ListAllOwnedTabsSQL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []store.WorkspaceTabRow
	for rows.Next() {
		var r store.WorkspaceTabRow
		var tabType int64
		if err := rows.Scan(&r.UserID, &r.WorkspaceID, &tabType, &r.TabID, &r.WorkerID, &r.TileID, &r.Position, &r.Rendered); err != nil {
			return nil, err
		}
		r.TabType = leapmuxv1.TabType(tabType)
		out = append(out, r)
	}
	return out, rows.Err()
}
