package sqlutil

import (
	"context"
	"database/sql"
	"strings"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// DBExec is the subset of database/sql.DB / *sql.Tx that the generic
// bulk helpers need. Both sqlite/mysql's `gendb.DBTX` interfaces
// satisfy it; the typed alias keeps callers from importing the
// per-dialect generated package just for the exec parameter type.
type DBExec interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// BulkUpsertTabsConfig holds the dialect-specific knobs for
// BulkUpsertTabs. The static suffix is the conflict / duplicate-key
// clause appended after the VALUES list; chunkRows caps the per-
// statement bind count.
type BulkUpsertTabsConfig struct {
	// ConflictSuffix is the ` ON CONFLICT ... DO UPDATE ...` clause
	// (sqlite) or ` ON DUPLICATE KEY UPDATE ...` clause (mysql)
	// appended after the VALUES list. Must start with a leading
	// space — the caller is responsible for the table-name fit.
	ConflictSuffix string
	// ChunkRows caps the per-statement row count so each chunk stays
	// under the dialect's bound-parameter limit (999 for sqlite, much
	// larger for mysql).
	ChunkRows int
}

// BulkUpsertTabs runs `INSERT INTO <table> ... VALUES (...) ...
// <ConflictSuffix>` against `exec`, chunking the input to stay under
// the dialect's bound-parameter limit. Each chunk uses 7 placeholders
// per row (org_id, workspace_id, tab_type, tab_id, worker_id, tile_id,
// position) and casts TabType through int64, which both sqlite and
// mysql accept for an integer column.
//
// mapErr lets the caller convert dialect-specific errors into the
// store-package errors.
func BulkUpsertTabs(ctx context.Context, exec DBExec, table string, rows []store.UpsertOwnedTabParams, cfg BulkUpsertTabsConfig, mapErr func(error) error) error {
	if len(rows) == 0 {
		return nil
	}
	for start := 0; start < len(rows); start += cfg.ChunkRows {
		end := start + cfg.ChunkRows
		if end > len(rows) {
			end = len(rows)
		}
		chunk := rows[start:end]
		var sb strings.Builder
		sb.Grow(160 + len(chunk)*24)
		sb.WriteString("INSERT INTO ")
		sb.WriteString(table)
		sb.WriteString(" (org_id, workspace_id, tab_type, tab_id, worker_id, tile_id, position) VALUES ")
		args := make([]any, 0, len(chunk)*7)
		for i, r := range chunk {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString("(?, ?, ?, ?, ?, ?, ?)")
			args = append(args, r.OrgID, r.WorkspaceID, int64(r.TabType), r.TabID, r.WorkerID, r.TileID, r.Position)
		}
		sb.WriteString(cfg.ConflictSuffix)
		if _, err := exec.ExecContext(ctx, sb.String(), args...); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

// BulkDeleteTabs runs `DELETE FROM <table> WHERE (org_id, tab_id) IN
// (...)` against `exec`, chunking the keys to stay under the dialect's
// bound-parameter limit. Both sqlite and mysql support the row-value
// IN syntax on currently-supported versions.
func BulkDeleteTabs(ctx context.Context, exec DBExec, table string, keys []store.TabIndexKey, chunkRows int, mapErr func(error) error) error {
	if len(keys) == 0 {
		return nil
	}
	for start := 0; start < len(keys); start += chunkRows {
		end := start + chunkRows
		if end > len(keys) {
			end = len(keys)
		}
		chunk := keys[start:end]
		var sb strings.Builder
		sb.Grow(80 + len(chunk)*8)
		sb.WriteString("DELETE FROM ")
		sb.WriteString(table)
		sb.WriteString(" WHERE (org_id, tab_id) IN (")
		args := make([]any, 0, len(chunk)*2)
		for i, k := range chunk {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString("(?, ?)")
			args = append(args, k.OrgID, k.TabID)
		}
		sb.WriteString(")")
		if _, err := exec.ExecContext(ctx, sb.String(), args...); err != nil {
			return mapErr(err)
		}
	}
	return nil
}
