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
// closure and parameter style let every dialect delegate here rather than
// hand-rolling the UPDATE with its own placeholder convention (`?` vs `$n`).
func SetDeletedAt(ctx context.Context, exec func(context.Context, string, ...any) error, style ParameterStyle, entity store.TestEntity, id string, deletedAt time.Time) error {
	return SetEntityColumnValue(ctx, exec, style, entity, "deleted_at", id, deletedAt)
}

// SetCreatedAt backdates the created_at timestamp for a record. See SetDeletedAt
// for the exec/style contract.
func SetCreatedAt(ctx context.Context, exec func(context.Context, string, ...any) error, style ParameterStyle, entity store.TestEntity, id string, createdAt time.Time) error {
	return SetEntityColumnValue(ctx, exec, style, entity, "created_at", id, createdAt)
}

// SetLastActiveAt writes an exact last_active_at timestamp for a session row.
// Only the user_sessions table carries this column, so the table is pinned
// here rather than caller-supplied -- passing a table without the column
// would only fail at the database with an obscure "no such column" error.
// See SetDeletedAt for the exec/style contract.
func SetLastActiveAt(ctx context.Context, exec func(context.Context, string, ...any) error, style ParameterStyle, id string, lastActiveAt time.Time) error {
	return SetEntityColumnValue(ctx, exec, style, store.EntitySessions, "last_active_at", id, lastActiveAt)
}

// SetDeregisteringAt writes an exact deregistering_at timestamp for a worker
// row, pinned to the workers table as SetLastActiveAt is to user_sessions. See
// SetDeletedAt for the exec/style contract.
func SetDeregisteringAt(ctx context.Context, exec func(context.Context, string, ...any) error, style ParameterStyle, workerID string, deregisteringAt time.Time) error {
	return SetEntityColumnValue(ctx, exec, style, store.EntityWorkers, "deregistering_at", workerID, deregisteringAt)
}

// ClearWorkerDeregisteringAt restores only a live test worker. The identifiers
// are fixed, and the worker ID remains a parameter in both placeholder styles.
func ClearWorkerDeregisteringAt(ctx context.Context, exec func(context.Context, string, ...any) error, style ParameterStyle, workerID string) error {
	first, _, err := placeholders(style)
	if err != nil {
		return err
	}
	return exec(ctx, fmt.Sprintf("UPDATE workers SET deregistering_at = NULL WHERE id = %s AND deleted_at IS NULL", first), workerID)
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
func SetEntityColumnValue(ctx context.Context, exec func(context.Context, string, ...any) error, style ParameterStyle, entity store.TestEntity, column, id string, value any) error {
	if err := store.ValidateEntity(entity); err != nil {
		return err
	}
	if err := validateEntityColumn(column); err != nil {
		return err
	}
	first, second, err := placeholders(style)
	if err != nil {
		return err
	}
	// entity and column are validated against closed allowlists above, so the
	// table and column interpolated here are always known literals, never
	// caller input.
	query := fmt.Sprintf("UPDATE %s SET %s = %s WHERE id = %s", string(entity), column, first, second)
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

type ParameterStyle uint8

const (
	ParameterStyleQuestionMark ParameterStyle = iota + 1
	ParameterStyleDollar
)

func placeholders(style ParameterStyle) (string, string, error) {
	switch style {
	case ParameterStyleQuestionMark:
		return "?", "?", nil
	case ParameterStyleDollar:
		return "$1", "$2", nil
	default:
		return "", "", fmt.Errorf("unknown parameter style %d", style)
	}
}

// ListAllOwnedTabsSQL is the owner-blind workspace_tab_owned read behind
// store.TestHelper.ListAllOwnedTabs, shared by the two database/sql dialects
// (sqlite, mysql); postgres runs the same statement through pgx. The statement
// takes no parameters, so it needs no ParameterStyle.
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
