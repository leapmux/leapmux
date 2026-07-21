package sqlutil

import (
	"context"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
)

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

// SetEntityColumnValue backdates one fixed column (deleted_at/created_at/
// last_active_at -- a literal, never caller input) on a validated entity
// table. BOTH the entity and the column are checked against closed allowlists
// so this cannot become an arbitrary-SQL escape hatch -- a future typed wrapper
// that passes an unvalidated column fails loudly here rather than interpolating
// caller input into the UPDATE. value is `any` (mirroring SetTimestampColumn)
// so SQLite can pass a pre-formatted strftime string that byte-matches
// production rows while the driver-serialized dialects pass a time.Time; the
// exported typed wrappers above keep every call site's value strongly typed.
func SetEntityColumnValue(ctx context.Context, exec func(context.Context, string, ...any) error, style ParameterStyle, entity store.TestEntity, column, id string, value any) error {
	if err := store.ValidateEntity(entity); err != nil {
		return err
	}
	if err := validateEntityColumn(column); err != nil {
		return err
	}
	return execColumnUpdate(ctx, exec, style, string(entity), column, id, value)
}

// execColumnUpdate is the shared UPDATE tail of SetEntityColumnValue and
// SetTimestampColumn: resolve the parameter placeholders, format the
// single-column UPDATE, and execute it. Both callers validate (table, column)
// against their own closed allowlist BEFORE calling; table and column here are
// therefore always known literals, never caller input.
func execColumnUpdate(ctx context.Context, exec func(context.Context, string, ...any) error, style ParameterStyle, table, column, id string, value any) error {
	first, second, err := placeholders(style)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET %s = %s WHERE id = %s", table, column, first, second)
	return exec(ctx, query, value, id)
}

// allowedEntityColumns is the closed set of literal column names
// SetEntityColumnValue may interpolate. Mirroring the entity allowlist on the
// column side makes the SQL-injection surface mechanically impossible to
// widen: a column outside this set is rejected before the sprintf, so the
// interpolation only ever receives a known literal.
var allowedEntityColumns = map[string]bool{
	"deleted_at":     true,
	"created_at":     true,
	"last_active_at": true,
}

func validateEntityColumn(column string) error {
	if !allowedEntityColumns[column] {
		return fmt.Errorf("unknown entity column %q", column)
	}
	return nil
}

// TimestampColumn is a closed set of timestamp columns exposed through the
// store test helper. Keeping the SQL identifiers here prevents callers from
// turning a backdating helper into an arbitrary SQL escape hatch.
type TimestampColumn uint8

const (
	TimestampColumnRevocationEventRevokedAt TimestampColumn = iota + 1
)

type ParameterStyle uint8

const (
	ParameterStyleQuestionMark ParameterStyle = iota + 1
	ParameterStyleDollar
)

// SetTimestampColumn writes an exact timestamp to one approved test column.
func SetTimestampColumn(
	ctx context.Context,
	exec func(context.Context, string, ...any) error,
	style ParameterStyle,
	column TimestampColumn,
	id string,
	at any,
) error {
	table, name, err := timestampColumnNames(column)
	if err != nil {
		return err
	}
	return execColumnUpdate(ctx, exec, style, table, name, id, at)
}

func timestampColumnNames(column TimestampColumn) (string, string, error) {
	switch column {
	case TimestampColumnRevocationEventRevokedAt:
		return "revocation_events", "revoked_at", nil
	default:
		return "", "", fmt.Errorf("unknown timestamp column %d", column)
	}
}

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
