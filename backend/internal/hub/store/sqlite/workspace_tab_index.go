package sqlite

import (
	"context"
	"database/sql"
	"errors"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// bulkUpsertChunkRows is the per-statement row cap for bulk-upsert SQL
// constructed at runtime. SQLite's default SQLITE_MAX_VARIABLE_NUMBER
// is 999 (older builds) and 32766 (3.32+); the seven-column upsert
// uses 7 placeholders per row, so 142 rows stays safe under the
// conservative 999-param cap with one slot to spare.
const bulkUpsertChunkRows = 142

// bulkDeleteChunkRows is the per-statement key cap for bulk-delete
// SQL. Two placeholders per (user_id, tab_id) pair -> 499 keys per
// chunk under the 999-param cap (one slot to spare).
const bulkDeleteChunkRows = 499

type workspaceTabIndexStore struct {
	conn *sqliteConn
}

var _ store.WorkspaceTabIndexStore = (*workspaceTabIndexStore)(nil)

func (s *workspaceTabIndexStore) UpsertOwned(ctx context.Context, p store.UpsertOwnedTabParams) error {
	return mapErr(s.conn.q.UpsertOwnedTab(ctx, gendb.UpsertOwnedTabParams{
		UserID:      p.UserID.String(),
		WorkspaceID: p.WorkspaceID,
		TabType:     int64(p.TabType),
		TabID:       p.TabID,
		WorkerID:    p.WorkerID,
		TileID:      p.TileID,
		Position:    p.Position,
	}))
}

func (s *workspaceTabIndexStore) BulkUpsertOwned(ctx context.Context, rows []store.UpsertOwnedTabParams) error {
	return bulkUpsertTabs(ctx, s.conn.exec, "workspace_tab_owned", rows)
}

func (s *workspaceTabIndexStore) BulkDeleteOwned(ctx context.Context, keys []store.TabIndexKey) error {
	return bulkDeleteTabs(ctx, s.conn.exec, "workspace_tab_owned", keys)
}

func (s *workspaceTabIndexStore) ListOwnedByWorker(ctx context.Context, p store.ListOwnedTabsByWorkerParams) ([]store.WorkspaceTabRow, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return nil, nil
	}
	rows, err := s.conn.q.ListOwnedTabsByWorker(ctx, gendb.ListOwnedTabsByWorkerParams{
		UserID:   owner,
		WorkerID: p.WorkerID,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, ownedTabRowFromDB), nil
}

func (s *workspaceTabIndexStore) ListDistinctWorkersByWorkspace(ctx context.Context, p store.ListDistinctWorkersByWorkspaceParams) ([]string, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// See ListOwnedByWorker above.
		return nil, nil
	}
	rows, err := s.conn.q.ListDistinctWorkersByWorkspace(ctx, gendb.ListDistinctWorkersByWorkspaceParams{
		UserID:      owner,
		WorkspaceID: p.WorkspaceID,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return rows, nil
}

func (s *workspaceTabIndexStore) GetOwned(ctx context.Context, p store.GetOwnedTabParams) (*store.WorkspaceTabRow, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// See ListOwnedByWorker above; an ownership gate must refuse rather
		// than bind a blank owner.
		return nil, store.ErrNotFound
	}
	row, err := s.conn.q.GetOwnedTab(ctx, gendb.GetOwnedTabParams{
		UserID:      owner,
		WorkspaceID: p.WorkspaceID,
		TabID:       p.TabID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, mapErr(err)
	}
	out := ownedTabRowFromDB(row)
	return &out, nil
}

func (s *workspaceTabIndexStore) UpsertRendered(ctx context.Context, p store.UpsertRenderedTabParams) error {
	return mapErr(s.conn.q.UpsertRenderedTab(ctx, gendb.UpsertRenderedTabParams{
		UserID:      p.UserID.String(),
		WorkspaceID: p.WorkspaceID,
		TabType:     int64(p.TabType),
		TabID:       p.TabID,
		WorkerID:    p.WorkerID,
		TileID:      p.TileID,
		Position:    p.Position,
	}))
}

func (s *workspaceTabIndexStore) BulkUpsertRendered(ctx context.Context, rows []store.UpsertRenderedTabParams) error {
	return bulkUpsertTabs(ctx, s.conn.exec, "workspace_tab_rendered", rows)
}

func (s *workspaceTabIndexStore) BulkDeleteRendered(ctx context.Context, keys []store.TabIndexKey) error {
	return bulkDeleteTabs(ctx, s.conn.exec, "workspace_tab_rendered", keys)
}

func (s *workspaceTabIndexStore) ListRenderedByWorkspaceIDs(ctx context.Context, p store.ListRenderedTabsByWorkspaceIDsParams) ([]store.WorkspaceTabRow, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return nil, nil
	}
	if len(p.WorkspaceIDs) == 0 {
		return nil, nil
	}
	rows, err := s.conn.q.ListRenderedTabsByWorkspaceIDs(ctx, gendb.ListRenderedTabsByWorkspaceIDsParams{
		UserID:       owner,
		WorkspaceIds: p.WorkspaceIDs,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, renderedTabRowFromDB), nil
}

func (s *workspaceTabIndexStore) GetRendered(ctx context.Context, p store.GetRenderedTabParams) (*store.WorkspaceTabRow, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return nil, store.ErrNotFound
	}
	row, err := s.conn.q.GetRenderedTab(ctx, gendb.GetRenderedTabParams{
		UserID:      owner,
		WorkspaceID: p.WorkspaceID,
		TabType:     int64(p.TabType),
		TabID:       p.TabID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, mapErr(err)
	}
	out := renderedTabRowFromDB(row)
	return &out, nil
}

// bulkUpsertTabs implements BulkUpsertOwned / BulkUpsertRendered for
// SQLite. The two tables share an identical schema, conflict key, and
// update list, so the only difference between the calls is the table
// name. Each chunk of up to bulkUpsertChunkRows rows runs as a single
// INSERT ... VALUES (...), (...) ON CONFLICT DO UPDATE statement.
func bulkUpsertTabs(ctx context.Context, exec gendb.DBTX, table string, rows []store.UpsertOwnedTabParams) error {
	return sqlutil.BulkUpsertTabs(ctx, exec, table, rows, sqlutil.BulkUpsertTabsConfig{
		ConflictSuffix: " ON CONFLICT (user_id, tab_id) DO UPDATE SET workspace_id = excluded.workspace_id, tab_type = excluded.tab_type, worker_id = excluded.worker_id, tile_id = excluded.tile_id, position = excluded.position",
		ChunkRows:      bulkUpsertChunkRows,
	}, mapErr)
}

// bulkDeleteTabs implements BulkDeleteOwned / BulkDeleteRendered for
// SQLite. Each chunk runs as a single DELETE ... WHERE (user_id,
// tab_id) IN ((?, ?), ...) statement. SQLite supports row-value IN
// since 3.15 (Oct 2016) which is well below our minimum.
func bulkDeleteTabs(ctx context.Context, exec gendb.DBTX, table string, keys []store.TabIndexKey) error {
	return sqlutil.BulkDeleteTabs(ctx, exec, table, keys, bulkDeleteChunkRows, mapErr)
}

func (s *workspaceTabIndexStore) LocateAccessibleRendered(ctx context.Context, p store.LocateAccessibleRenderedTabParams) (*store.WorkspaceTabRow, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return nil, store.ErrNotFound
	}
	row, err := s.conn.q.LocateAccessibleRenderedTab(ctx, gendb.LocateAccessibleRenderedTabParams{
		UserID:  owner,
		TabID:   p.TabID,
		TabType: int64(p.TabType),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, mapErr(err)
	}
	out := renderedTabRowFromDB(row)
	return &out, nil
}

// ownedTabRowFromDB converts one generated workspace_tab_owned row to the store
// shape. renderedTabRowFromDB below is its byte-for-byte twin over
// gendb.WorkspaceTabRendered, and the pair CANNOT collapse into one generic
// helper: the two tables share a column set but sqlc generates a distinct
// struct per table, and Go permits neither field access on a union type
// parameter nor a []A -> []B slice conversion.
func ownedTabRowFromDB(r gendb.WorkspaceTabOwned) store.WorkspaceTabRow {
	return store.WorkspaceTabRow{
		UserID:      r.UserID,
		WorkspaceID: r.WorkspaceID,
		TabType:     leapmuxv1.TabType(r.TabType),
		TabID:       r.TabID,
		WorkerID:    r.WorkerID,
		TileID:      r.TileID,
		Position:    r.Position,
	}
}

// renderedTabRowFromDB converts one generated workspace_tab_rendered row to the
// store shape. See ownedTabRowFromDB for why the two are not one helper.
func renderedTabRowFromDB(r gendb.WorkspaceTabRendered) store.WorkspaceTabRow {
	return store.WorkspaceTabRow{
		UserID:      r.UserID,
		WorkspaceID: r.WorkspaceID,
		TabType:     leapmuxv1.TabType(r.TabType),
		TabID:       r.TabID,
		WorkerID:    r.WorkerID,
		TileID:      r.TileID,
		Position:    r.Position,
	}
}
