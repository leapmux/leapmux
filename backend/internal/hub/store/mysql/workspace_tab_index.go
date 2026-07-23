package mysql

import (
	"context"
	"database/sql"
	"errors"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

// bulkUpsertChunkRows is the per-statement row cap for bulk-upsert
// SQL constructed at runtime. MySQL's default max_prepared_stmt_count
// allows up to 65535 placeholders per statement; the seven-column
// upsert uses 7 per row, so 4096 rows stays well under that limit
// while still avoiding pathological packet sizes (max_allowed_packet
// defaults to 64 MB, way more than 4096 rows worth of small VARCHARs).
const bulkUpsertChunkRows = 4096

// bulkDeleteChunkRows is the per-statement key cap for bulk-delete
// SQL. Two placeholders per (org_id, tab_id) pair -> 16384 keys per
// chunk under the 65535-placeholder cap.
const bulkDeleteChunkRows = 16384

type workspaceTabIndexStore struct {
	conn *mysqlConn
}

var _ store.WorkspaceTabIndexStore = (*workspaceTabIndexStore)(nil)

func (s *workspaceTabIndexStore) UpsertOwned(ctx context.Context, p store.UpsertOwnedTabParams) error {
	return mapErr(s.conn.q.UpsertOwnedTab(ctx, gendb.UpsertOwnedTabParams{
		OrgID:       p.OrgID,
		WorkspaceID: p.WorkspaceID,
		TabType:     int32(p.TabType),
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
		TabType:     int32(p.TabType),
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
		TabType:     int32(p.TabType),
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
// MySQL. The two tables share an identical schema, primary key, and
// update list; only the table name differs between calls. Each chunk
// of up to bulkUpsertChunkRows rows runs as a single INSERT ...
// VALUES (...), (...) ON DUPLICATE KEY UPDATE statement.
func bulkUpsertTabs(ctx context.Context, exec gendb.DBTX, table string, rows []store.UpsertOwnedTabParams) error {
	return sqlutil.BulkUpsertTabs(ctx, exec, table, rows, sqlutil.BulkUpsertTabsConfig{
		ConflictSuffix: " ON DUPLICATE KEY UPDATE workspace_id = VALUES(workspace_id), tab_type = VALUES(tab_type), worker_id = VALUES(worker_id), tile_id = VALUES(tile_id), position = VALUES(position)",
		ChunkRows:      bulkUpsertChunkRows,
	}, mapErr)
}

// bulkDeleteTabs implements BulkDeleteOwned / BulkDeleteRendered for
// MySQL. Each chunk runs as a single DELETE ... WHERE (org_id,
// tab_id) IN ((?, ?), ...) statement.
func bulkDeleteTabs(ctx context.Context, exec gendb.DBTX, table string, keys []store.TabIndexKey) error {
	return sqlutil.BulkDeleteTabs(ctx, exec, table, keys, bulkDeleteChunkRows, mapErr)
}

func (s *workspaceTabIndexStore) LocateAccessibleRendered(ctx context.Context, p store.LocateAccessibleRenderedTabParams) (*store.WorkspaceTabRow, error) {
	owner, ok := store.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See store.OwnerFilter.
		return nil, store.ErrNotFound
	}
	row, err := s.conn.q.LocateAccessibleRenderedTab(ctx, gendb.LocateAccessibleRenderedTabParams{
		UserID:  owner,
		TabID:   p.TabID,
		TabType: int32(p.TabType),
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
