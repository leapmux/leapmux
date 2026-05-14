package sqlite

import (
	"context"
	"database/sql"
	"errors"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

// bulkUpsertChunkRows is the per-statement row cap for bulk-upsert SQL
// constructed at runtime. SQLite's default SQLITE_MAX_VARIABLE_NUMBER
// is 999 (older builds) and 32766 (3.32+); the seven-column upsert
// uses 7 placeholders per row, so 142 rows stays safe under the
// conservative 999-param cap with one slot to spare.
const bulkUpsertChunkRows = 142

// bulkDeleteChunkRows is the per-statement key cap for bulk-delete
// SQL. Two placeholders per (org_id, tab_id) pair -> 499 keys per
// chunk under the 999-param cap (one slot to spare).
const bulkDeleteChunkRows = 499

type workspaceTabIndexStore struct {
	conn *sqliteConn
}

var _ store.WorkspaceTabIndexStore = (*workspaceTabIndexStore)(nil)

func (s *workspaceTabIndexStore) UpsertOwned(ctx context.Context, p store.UpsertOwnedTabParams) error {
	return mapErr(s.conn.q.UpsertOwnedTab(ctx, gendb.UpsertOwnedTabParams{
		OrgID:       p.OrgID,
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

func (s *workspaceTabIndexStore) DeleteOwned(ctx context.Context, orgID, tabID string) error {
	return mapErr(s.conn.q.DeleteOwnedTab(ctx, gendb.DeleteOwnedTabParams{OrgID: orgID, TabID: tabID}))
}

func (s *workspaceTabIndexStore) BulkDeleteOwned(ctx context.Context, keys []store.TabIndexKey) error {
	return bulkDeleteTabs(ctx, s.conn.exec, "workspace_tab_owned", keys)
}

func (s *workspaceTabIndexStore) DeleteOwnedByOrg(ctx context.Context, orgID string) error {
	return mapErr(s.conn.q.DeleteOwnedTabsByOrg(ctx, orgID))
}

func (s *workspaceTabIndexStore) ListOwnedByWorkspace(ctx context.Context, workspaceID string) ([]store.WorkspaceTabRow, error) {
	rows, err := s.conn.q.ListOwnedTabsByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.WorkspaceTabRow, len(rows))
	for i, r := range rows {
		out[i] = store.WorkspaceTabRow{
			OrgID:       r.OrgID,
			WorkspaceID: r.WorkspaceID,
			TabType:     leapmuxv1.TabType(r.TabType),
			TabID:       r.TabID,
			WorkerID:    r.WorkerID,
			TileID:      r.TileID,
			Position:    r.Position,
		}
	}
	return out, nil
}

func (s *workspaceTabIndexStore) ListOwnedByWorker(ctx context.Context, workerID string) ([]store.WorkspaceTabRow, error) {
	rows, err := s.conn.q.ListOwnedTabsByWorker(ctx, workerID)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.WorkspaceTabRow, len(rows))
	for i, r := range rows {
		out[i] = store.WorkspaceTabRow{
			OrgID:       r.OrgID,
			WorkspaceID: r.WorkspaceID,
			TabType:     leapmuxv1.TabType(r.TabType),
			TabID:       r.TabID,
			WorkerID:    r.WorkerID,
			TileID:      r.TileID,
			Position:    r.Position,
		}
	}
	return out, nil
}

func (s *workspaceTabIndexStore) ListDistinctWorkersByWorkspace(ctx context.Context, workspaceID string) ([]string, error) {
	rows, err := s.conn.q.ListDistinctWorkersByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, mapErr(err)
	}
	return rows, nil
}

func (s *workspaceTabIndexStore) GetOwned(ctx context.Context, p store.GetOwnedTabParams) (*store.WorkspaceTabRow, error) {
	row, err := s.conn.q.GetOwnedTab(ctx, gendb.GetOwnedTabParams{
		WorkspaceID: p.WorkspaceID,
		TabID:       p.TabID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, mapErr(err)
	}
	return &store.WorkspaceTabRow{
		OrgID:       row.OrgID,
		WorkspaceID: row.WorkspaceID,
		TabType:     leapmuxv1.TabType(row.TabType),
		TabID:       row.TabID,
		WorkerID:    row.WorkerID,
		TileID:      row.TileID,
		Position:    row.Position,
	}, nil
}

func (s *workspaceTabIndexStore) UpsertRendered(ctx context.Context, p store.UpsertRenderedTabParams) error {
	return mapErr(s.conn.q.UpsertRenderedTab(ctx, gendb.UpsertRenderedTabParams{
		OrgID:       p.OrgID,
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

func (s *workspaceTabIndexStore) DeleteRendered(ctx context.Context, orgID, tabID string) error {
	return mapErr(s.conn.q.DeleteRenderedTab(ctx, gendb.DeleteRenderedTabParams{OrgID: orgID, TabID: tabID}))
}

func (s *workspaceTabIndexStore) BulkDeleteRendered(ctx context.Context, keys []store.TabIndexKey) error {
	return bulkDeleteTabs(ctx, s.conn.exec, "workspace_tab_rendered", keys)
}

func (s *workspaceTabIndexStore) DeleteRenderedByOrg(ctx context.Context, orgID string) error {
	return mapErr(s.conn.q.DeleteRenderedTabsByOrg(ctx, orgID))
}

func (s *workspaceTabIndexStore) ListRenderedByWorkspace(ctx context.Context, workspaceID string) ([]store.WorkspaceTabRow, error) {
	rows, err := s.conn.q.ListRenderedTabsByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, mapErr(err)
	}
	return renderedTabsFromDB(rows), nil
}

func (s *workspaceTabIndexStore) ListRenderedByWorkspaceIDs(ctx context.Context, workspaceIDs []string) ([]store.WorkspaceTabRow, error) {
	if len(workspaceIDs) == 0 {
		return nil, nil
	}
	rows, err := s.conn.q.ListRenderedTabsByWorkspaceIDs(ctx, workspaceIDs)
	if err != nil {
		return nil, mapErr(err)
	}
	return renderedTabsFromDB(rows), nil
}

func renderedTabsFromDB(rows []gendb.WorkspaceTabRendered) []store.WorkspaceTabRow {
	out := make([]store.WorkspaceTabRow, len(rows))
	for i, r := range rows {
		out[i] = store.WorkspaceTabRow{
			OrgID:       r.OrgID,
			WorkspaceID: r.WorkspaceID,
			TabType:     leapmuxv1.TabType(r.TabType),
			TabID:       r.TabID,
			WorkerID:    r.WorkerID,
			TileID:      r.TileID,
			Position:    r.Position,
		}
	}
	return out
}

func (s *workspaceTabIndexStore) GetRendered(ctx context.Context, p store.GetRenderedTabParams) (*store.WorkspaceTabRow, error) {
	row, err := s.conn.q.GetRenderedTab(ctx, gendb.GetRenderedTabParams{
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
	return &store.WorkspaceTabRow{
		OrgID:       row.OrgID,
		WorkspaceID: row.WorkspaceID,
		TabType:     leapmuxv1.TabType(row.TabType),
		TabID:       row.TabID,
		WorkerID:    row.WorkerID,
		TileID:      row.TileID,
		Position:    row.Position,
	}, nil
}

// bulkUpsertTabs implements BulkUpsertOwned / BulkUpsertRendered for
// SQLite. The two tables share an identical schema, conflict key, and
// update list, so the only difference between the calls is the table
// name. Each chunk of up to bulkUpsertChunkRows rows runs as a single
// INSERT ... VALUES (...), (...) ON CONFLICT DO UPDATE statement.
func bulkUpsertTabs(ctx context.Context, exec gendb.DBTX, table string, rows []store.UpsertOwnedTabParams) error {
	return sqlutil.BulkUpsertTabs(ctx, exec, table, rows, sqlutil.BulkUpsertTabsConfig{
		ConflictSuffix: " ON CONFLICT (org_id, tab_id) DO UPDATE SET workspace_id = excluded.workspace_id, tab_type = excluded.tab_type, worker_id = excluded.worker_id, tile_id = excluded.tile_id, position = excluded.position",
		ChunkRows:      bulkUpsertChunkRows,
	}, mapErr)
}

// bulkDeleteTabs implements BulkDeleteOwned / BulkDeleteRendered for
// SQLite. Each chunk runs as a single DELETE ... WHERE (org_id,
// tab_id) IN ((?, ?), ...) statement. SQLite supports row-value IN
// since 3.15 (Oct 2016) which is well below our minimum.
func bulkDeleteTabs(ctx context.Context, exec gendb.DBTX, table string, keys []store.TabIndexKey) error {
	return sqlutil.BulkDeleteTabs(ctx, exec, table, keys, bulkDeleteChunkRows, mapErr)
}

func (s *workspaceTabIndexStore) LocateAccessibleRendered(ctx context.Context, p store.LocateAccessibleRenderedTabParams) (*store.WorkspaceTabRow, error) {
	row, err := s.conn.q.LocateAccessibleRenderedTab(ctx, gendb.LocateAccessibleRenderedTabParams{
		UserID:  p.UserID,
		TabID:   p.TabID,
		TabType: int64(p.TabType),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, mapErr(err)
	}
	return &store.WorkspaceTabRow{
		OrgID:       row.OrgID,
		WorkspaceID: row.WorkspaceID,
		TabType:     leapmuxv1.TabType(row.TabType),
		TabID:       row.TabID,
		WorkerID:    row.WorkerID,
		TileID:      row.TileID,
		Position:    row.Position,
	}, nil
}
