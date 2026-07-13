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
	return setEntityTimestamp(ctx, exec, style, entity, "deleted_at", id, deletedAt)
}

// SetCreatedAt backdates the created_at timestamp for a record. See SetDeletedAt
// for the exec/style contract.
func SetCreatedAt(ctx context.Context, exec func(context.Context, string, ...any) error, style ParameterStyle, entity store.TestEntity, id string, createdAt time.Time) error {
	return setEntityTimestamp(ctx, exec, style, entity, "created_at", id, createdAt)
}

// setEntityTimestamp backdates one fixed column (deleted_at/created_at -- a
// literal, never caller input) on a validated entity table. The entity is
// checked against the allowlist so this cannot become an arbitrary-SQL escape
// hatch.
func setEntityTimestamp(ctx context.Context, exec func(context.Context, string, ...any) error, style ParameterStyle, entity store.TestEntity, column, id string, at time.Time) error {
	if err := store.ValidateEntity(entity); err != nil {
		return err
	}
	first, second, err := placeholders(style)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET %s = %s WHERE id = %s", entity, column, first, second)
	return exec(ctx, query, at, id)
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
	first, second, err := placeholders(style)
	if err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET %s = %s WHERE id = %s", table, name, first, second)
	return exec(ctx, query, at, id)
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
